package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"blog-engine/internal/logging"

	_ "modernc.org/sqlite"
)

// Hooks used by failure-path tests. Production always uses the real
// implementations; these variables exist because the corresponding failures
// depend on filesystem permissions, broken drivers, or timing.
type rowScanner interface {
	Scan(dest ...any) error
}

var (
	sqlOpen                      = sql.Open
	migrateFn                    = migrate
	verifyPragmasFn              = verifyPragmas
	chmodPath                    = os.Chmod
	isWindows                    = runtime.GOOS == "windows"
	pragmaRow                    = func(db *sql.DB, query string) rowScanner { return db.QueryRow(query) }
	rowsNext                     = (*sql.Rows).Next
	rowsScan                     = (*sql.Rows).Scan
	rowsErr                      = (*sql.Rows).Err
	rowsAffected                 = func(result sql.Result) (int64, error) { return result.RowsAffected() }
	ensureColumnFn               = ensureColumn
	execMigration                = func(db *sql.DB, statement string) (sql.Result, error) { return db.Exec(statement) }
	backfillUsernamesFn          = backfillUsernames
	selectUsersWithoutUsernameFn = selectUsersWithoutUsername
	selectTakenUsernamesFn       = selectTakenUsernames
	beginTx                      = func(db *sql.DB) (*sql.Tx, error) { return db.Begin() }
	execTx                       = func(tx *sql.Tx, query string, args ...any) (sql.Result, error) { return tx.Exec(query, args...) }
	commitTx                     = func(tx *sql.Tx) error { return tx.Commit() }
)

// Open 打开数据库、执行迁移，并校验关键 pragma。
//
// 任何一步失败都会关闭连接并返回错误，由 main 直接终止进程：
// 数据库不可用不是一种可以降级的状态。
func Open(path string) (*sql.DB, error) {
	logging.Debugf("[database] opening sqlite database: %s", path)
	db, err := sqlOpen("sqlite", buildDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// 只保留一条连接：这既把写操作串行化（SQLite 本身就是单写者），
	// 也让下面那些「按连接生效」的 pragma 有了确定的作用范围。
	// 代价是进程内不存在真正的并发读——WAL 在这里的价值主要体现在崩溃安全、
	// 以及「跨进程读写不互相阻塞」，而不是本进程内的读并发。
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	logging.Debugf("[database] running migrations for %s", path)
	if err := migrateFn(db); err != nil {
		db.Close()
		return nil, err
	}

	// 宁可启动失败，也不要静默地跑在一个没有引用完整性的库上：
	// schema 里每一处 ON DELETE CASCADE 都依赖这个开关真的生效，
	// 一旦失效，删文章不会清掉 article_tags，会悄悄留下孤儿行。
	if err := verifyPragmasFn(db); err != nil {
		db.Close()
		return nil, err
	}

	// 收紧数据库文件的权限。放在 migrate 之后：那时 WAL 与 SHM 已经创建出来了。
	restrictDatabasePermissions(path)

	logging.Debugf("[database] migration completed for %s", path)
	return db, nil
}

// Snapshot 把数据库压平成一份独立文件，供备份使用。
//
// # 为什么不能直接拷贝 blog.db
//
// 库运行在 WAL 模式下，「已提交」的事务可能还留在 blog.db-wal 里，只拷主文件会
// 丢掉最近写入的数据——而丢多少取决于别人恰好写到了哪一步。这类归档最危险的地方
// 是它**看起来是完整的**：能打开、有表、有数据，只是少了最近几条。
//
// VACUUM INTO 由 SQLite 自己完成这件事：它在单个读事务里读一遍库、把结果写进一个
// 新文件，所以结果是一致快照，同时自动压掉空闲页（归档更小）。对生产库只读、
// 不阻塞写入，因此备份可以边服务边跑。
//
// # 两个实现细节
//
//   - 用的是一个**不跑迁移**的连接：迁移会写 schema，而备份只该读。
//     （pragma 仍走 buildDSN，为的是在 Windows 上正确地转义路径。）
//   - 目标文件必须先不存在：VACUUM INTO 不接受已存在的目标。这不是限制而是好事，
//     它保证了「这份快照是本次生成的」。
func Snapshot(srcPath, dstPath string) error {
	if _, err := os.Stat(srcPath); err != nil {
		return fmt.Errorf("open the database: %w", err)
	}
	if _, err := os.Stat(dstPath); err == nil {
		return fmt.Errorf("the snapshot target %s already exists", dstPath)
	}

	db, err := sqlOpen("sqlite", buildDSN(srcPath))
	if err != nil {
		return fmt.Errorf("open the database: %w", err)
	}
	defer db.Close()

	logging.Debugf("[database] snapshotting %s into %s", srcPath, dstPath)
	if _, err := db.Exec(`VACUUM INTO ?`, dstPath); err != nil {
		return fmt.Errorf("snapshot the database: %w", err)
	}

	// VACUUM INTO 用 SQLite 自己的默认模式（0644）创建文件，而快照里装着全部账号
	// 的 bcrypt 哈希、会话 token 摘要与回收站内容——权限必须和源库一样紧。
	// 失败只告警：与 restrictDatabasePermissions 同一个理由（有些文件系统根本没有
	// POSIX 权限概念，为此让备份失败会误伤合法部署）。
	if err := chmodPath(dstPath, 0o600); err != nil {
		logging.Warnf("[database] tightening permissions on the snapshot %s failed: %v", dstPath, err)
	}
	return nil
}

// restrictDatabasePermissions 把 SQLite 数据库文件收成 0600，目录收成 0700。
//
// # 为什么必须显式做
//
// SQLite 用 `open(path, O_RDWR|O_CREAT, 0644)` 创建库文件——0644 是它写死的模式
// （见 SQLite 源码文档里那段示例注释），再被进程 umask 掩一次。于是在常见的
// umask 022 下，**blog.db 是 0644，同机任何用户都能读**。而这个库里装着：
//
//   - 管理员与访客的 bcrypt 密码哈希（cost 10，可离线爆破）；
//   - 全部会话 token 与 CSRF token 的 SHA-256；
//   - 每一个账号的邮箱地址；
//   - 回收站里的文章/评论原文（它们之所以被删，往往正是因为不该再被看到）。
//
// Windows 上这不是问题（工作目录通常在自己的用户配置目录下，且默认 ACL 已限制
// 其他用户），而在**多用户的 Linux 主机**上这是一条实打实的本地信息泄露。
//
// # 为什么每次启动都要做，而不是只做一次
//
// WAL 与 SHM 由 SQLite 按需创建，而且 **WAL 会在最后一个连接正常关闭时被删掉、
// 下次连接时重建**——重建的那一次权限又回到 0644。所以这不是一次性的迁移动作，
// 而是每次启动都要重做的收尾。三个文件里任何一个是 0644，整个库的内容就能被读出来。
//
// 失败只记 Warn 而不阻断启动：在没有 POSIX 权限概念的文件系统（FAT/exFAT、
// 某些网络挂载、Windows）上 chmod 本来就无意义，为此拒绝启动会误伤合法部署。
// 代价是这里的保证是「尽力而为」，因此 systemd 单元里另外设了 UMask=0077
// 作为更底层的兜底（见 deploy/twilight-blog.service）。
func restrictDatabasePermissions(path string) {
	// 目录也要收：0600 的文件放进 0755 的目录里仍然安全，但目录可列举本身
	// 就暴露了「这里有个数据库、它多大、什么时候写的」。0700 一并解决。
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := chmodPath(dir, 0o700); err != nil {
			logging.Warnf("[database] tightening permissions on the data directory %s failed: %v", dir, err)
		}
	}

	// -wal 与 -shm 只在 WAL 模式下存在；不存在时 Chmod 返回 ENOENT，直接忽略。
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		target := path + suffix
		if _, err := os.Stat(target); err != nil {
			continue
		}
		if err := chmodPath(target, 0o600); err != nil {
			logging.Warnf("[database] tightening permissions on %s failed: "+
				"the database may be readable by other local users: %v", target, err)
		}
	}
}

// buildDSN 把连接级 pragma 拼到数据库路径上，组成 DSN。
//
// 为什么必须写在 DSN 里：foreign_keys 与 busy_timeout 都是**按连接**生效的设置。
// 打开之后用 PRAGMA 设一次是不够的——database/sql 随时可能丢弃并重建底层连接，
// 那些设置会无声消失，表现成「级联删除偶尔失效」这类极难复现的 bug。
// 写进 DSN 则由驱动在每条新连接上重新应用。
//
// 三项 pragma 各自的理由：
//   - busy_timeout(5000)：遇到锁时不立刻报 SQLITE_BUSY，而是最多等 5 秒；
//   - foreign_keys(1)：外键与级联删除的总开关，必须开，见 verifyPragmas；
//   - journal_mode(WAL)：写不阻塞读，崩溃后恢复更快。
//
// 其中 journal_mode 会持久化到数据库文件里，另两项不会，但一并写在这里
// 可以保证新库第一次打开就已经是 WAL 模式。
func buildDSN(path string) string {
	// modernc.org/sqlite 把 DSN 当 URI 解析，而 URI 里反斜杠是转义字符，
	// 所以 **Windows** 的路径分隔符必须先换成正斜杠，否则路径会被吃掉。
	//
	// 只在 Windows 上做这个替换：在 Linux 上反斜杠是**合法的文件名字符**，
	// 无条件替换会把 `blog\prod.db` 静默改写成 `blog/prod.db`——写到另一个
	// 位置（或写到不存在的目录而失败），而报错里显示的是改写后的路径，
	// 排查起来毫无线索。
	dsn := path
	if isWindows {
		dsn = strings.ReplaceAll(dsn, "\\", "/")
	}
	if !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}

	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}

	pragmas := []string{
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(1)",
		"_pragma=journal_mode(WAL)",
	}
	return dsn + separator + strings.Join(pragmas, "&")
}

// verifyPragmas 读回关键 pragma，确认它们真的生效了。
//
// 这是对「DSN 拼错 / 驱动改了行为」的兜底断言。这类设置静默失效时应用不会
// 立刻报错，而是在某次删除操作上留下孤儿数据，排查成本极高；
// 启动时花两次查询换一个明确的失败，非常划算。
func verifyPragmas(db *sql.DB) error {
	var foreignKeys int
	if err := pragmaRow(db, `PRAGMA foreign_keys;`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("check foreign_keys pragma: %w", err)
	}
	if foreignKeys != 1 {
		return errors.New("foreign key enforcement is not enabled; ON DELETE CASCADE would be silently inert")
	}

	var busyTimeout int
	if err := pragmaRow(db, `PRAGMA busy_timeout;`).Scan(&busyTimeout); err != nil {
		return fmt.Errorf("check busy_timeout pragma: %w", err)
	}
	if busyTimeout <= 0 {
		return errors.New("busy_timeout is not configured")
	}

	logging.Debugf("[database] pragmas verified foreign_keys=%d busy_timeout=%d", foreignKeys, busyTimeout)
	return nil
}

// PruneExpired 删除已经无法再使用的行：过期的会话、过期的评论 token，
// 以及过期或已被消费的邮箱验证码。
//
// 没有它这些表会无限增长——别处都不会删它们，会话只在自己被访问且已过期时
// 才顺手删掉那一行。由 router 的定时任务每小时调用一次。
//
// 注意这里**不再**清理 admin_sessions：那张旧的管理员会话表已经废弃、
// 连建表语句都移除了（见 schema.go），没有任何代码往里写数据，
// 仍然对它发 DELETE 只会在新库上直接报 "no such table"。
//
// 返回实际删除的总行数；任一语句失败即中断并返回已完成的部分，
// 因为继续删下去只会掩盖问题。
func PruneExpired(ctx context.Context, db *sql.DB) (int64, error) {
	now := time.Now().Unix()
	var removed int64

	for _, statement := range []string{
		`DELETE FROM sessions WHERE expires_at <= ?`,
		`DELETE FROM comment_tokens WHERE expires_at <= ?`,
		`DELETE FROM email_verification_codes WHERE expires_at <= ? OR consumed_at IS NOT NULL`,
	} {
		res, err := db.ExecContext(ctx, statement, now)
		if err != nil {
			return removed, fmt.Errorf("prune expired rows: %w", err)
		}
		n, err := rowsAffected(res)
		if err != nil {
			return removed, fmt.Errorf("prune rows affected: %w", err)
		}
		removed += n
	}

	if removed > 0 {
		logging.Infof("[database] pruned %d expired rows", removed)
	}
	return removed, nil
}

// migrate 把数据库带到当前 schema。
//
// 分三个阶段，因为 schema 是幂等重放而不是按版本管理的（见 schemaSQL）：
//  1. 整体执行一遍建表语句（只在表缺失时生效）；
//  2. 逐个补上 CREATE TABLE IF NOT EXISTS 无法给已有表补的那些列；
//  3. 建那些引用了新列的索引，再把新列的数据回填上。
//
// 顺序不能乱——补列的前提是表已存在，建索引与回填的前提是列已存在。
func migrate(db *sql.DB) error {
	logging.Debugf("[database] executing schema migration")

	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	for _, column := range addedColumns {
		if err := ensureColumnFn(db, column.table, column.name, column.definition); err != nil {
			return err
		}
	}

	for _, statement := range addedIndexes {
		if _, err := execMigration(db, statement); err != nil {
			return fmt.Errorf("create index after column addition: %w", err)
		}
	}

	if err := backfillUsernamesFn(db); err != nil {
		return err
	}
	return nil
}

// backfillUsernames 给还没有用户名的历史账号补上一个。
//
// 为什么必须回填：username 是登录标识，而这一列是后加的，老库里每一行都是空串。
// 不回填的话那些账号会处于「存在但用用户名登不进来」的状态——这比直接报错更难查，
// 因为数据看上去是完整的。回填之后每行都有用户名，登录路径就不需要任何特例。
//
// 取名规则是**确定性**的：取邮箱本地部分，小写，把不允许的字符换成 '-'，
// 截到 24 字符，不足 3 字符补成 "user"；重名时依次尝试 -2、-3……
// 规则可预测，用户看到自己的用户名不会觉得陌生，也便于在文档里说明。
//
// 单连接池的坑（重要）：不能一边遍历 rows 一边发 UPDATE/INSERT。连接池只有
// 一条连接（见 Open 的 SetMaxOpenConns(1)），Exec 会去等那条被未关闭的 rows
// 占住的连接，表现成**进程卡死且不报任何错**。所以这里先把结果全部读进切片、
// 关闭 rows，再开始写。
func backfillUsernames(db *sql.DB) error {
	pending, err := selectUsersWithoutUsernameFn(db)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	taken, err := selectTakenUsernamesFn(db)
	if err != nil {
		return err
	}

	tx, err := beginTx(db)
	if err != nil {
		return fmt.Errorf("begin username backfill: %w", err)
	}
	// 提交之后再回滚是空操作；这条 defer 只负责「中途 return 时别留下半个事务」。
	defer tx.Rollback()

	for _, row := range pending {
		username := uniqueUsername(deriveUsername(row.email), taken)
		taken[strings.ToLower(username)] = struct{}{}

		if _, err := execTx(tx,
			`UPDATE users SET username = ? WHERE id = ?`, username, row.id,
		); err != nil {
			return fmt.Errorf("backfill username for user %d: %w", row.id, err)
		}
	}

	if err := commitTx(tx); err != nil {
		return fmt.Errorf("commit username backfill: %w", err)
	}

	logging.Warnf("[database] backfilled usernames for %d account(s) created before the username column existed; "+
		"each address keeps working at sign-in and the owner can change the username from the account page", len(pending))
	return nil
}

type userWithoutUsername struct {
	id    int64
	email string
}

// selectUsersWithoutUsername 读出所有还没有用户名的账号，读完即关闭 rows。
func selectUsersWithoutUsername(db *sql.DB) ([]userWithoutUsername, error) {
	rows, err := db.Query(`SELECT id, email FROM users WHERE username IS NULL OR username = '' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("find users without username: %w", err)
	}
	defer rows.Close()

	var result []userWithoutUsername
	for rowsNext(rows) {
		var row userWithoutUsername
		if err := rowsScan(rows, &row.id, &row.email); err != nil {
			return nil, fmt.Errorf("scan user without username: %w", err)
		}
		result = append(result, row)
	}
	if err := rowsErr(rows); err != nil {
		return nil, fmt.Errorf("iterate users without username: %w", err)
	}
	return result, nil
}

// selectTakenUsernames 读出已被占用的用户名，键是小写形式（唯一索引也是 NOCASE）。
func selectTakenUsernames(db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.Query(`SELECT username FROM users WHERE username IS NOT NULL AND username <> ''`)
	if err != nil {
		return nil, fmt.Errorf("load existing usernames: %w", err)
	}
	defer rows.Close()

	taken := make(map[string]struct{})
	for rowsNext(rows) {
		var username string
		if err := rowsScan(rows, &username); err != nil {
			return nil, fmt.Errorf("scan existing username: %w", err)
		}
		taken[strings.ToLower(username)] = struct{}{}
	}
	if err := rowsErr(rows); err != nil {
		return nil, fmt.Errorf("iterate existing usernames: %w", err)
	}
	return taken, nil
}

// maxBackfilledUsernameLength 与 service 层的用户名长度上限保持一致。
// 这里刻意重复一个常量而不去 import service：database 是最底层，
// 让它依赖业务包会把依赖方向倒过来。
const maxBackfilledUsernameLength = 24

// deriveUsername 从邮箱推出一个候选用户名。
//
// 结果必须能通过 service 层的用户名规则（以字母开头、只含字母数字下划线连字符、
// 长度 3–24）。这一点很重要但容易漏：回填出来的名字如果形状不合法，用户下次
// 在「我的资料」里保存任何改动都会撞上校验错误——一个由系统自己造成的死结。
func deriveUsername(email string) string {
	local := email
	if at := strings.IndexByte(local, '@'); at >= 0 {
		local = local[:at]
	}

	var builder strings.Builder
	for _, r := range strings.ToLower(local) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			builder.WriteRune(r)
		case r == '-' || r == '.' || r == '+':
			// 常见的邮箱分隔符映射成 '-'，让 foo.bar@x 变成 foo-bar 而不是 foobar。
			builder.WriteByte('-')
		}
	}

	// 去掉首尾的分隔符：用户名不能以 '-' 或 '_' 开头。
	candidate := strings.Trim(builder.String(), "-_")
	if candidate == "" {
		return "user"
	}
	// 数字或下划线开头时加前缀，而不是把这一段丢掉——"1a2b@x.com" 应该得到
	// user-1a2b，一个用户还能认出来的名字。
	if head := candidate[0]; head < 'a' || head > 'z' {
		candidate = "user-" + candidate
	}

	if len(candidate) > maxBackfilledUsernameLength {
		candidate = strings.Trim(candidate[:maxBackfilledUsernameLength], "-_")
	}
	if len(candidate) < 3 {
		return "user"
	}
	return candidate
}

// uniqueUsername 在 taken 里为候选名找一个还没被占用的版本。
// 依次尝试 name、name-2、name-3……全部失败时退化成带时间戳的名字，
// 保证函数总能返回一个可用值（否则回填会在重名上卡住）。
func uniqueUsername(candidate string, taken map[string]struct{}) string {
	if _, ok := taken[strings.ToLower(candidate)]; !ok {
		return candidate
	}
	for suffix := 2; suffix < 1000; suffix++ {
		attempt := fmt.Sprintf("%s-%d", candidate, suffix)
		if len(attempt) > maxBackfilledUsernameLength {
			attempt = attempt[:maxBackfilledUsernameLength]
		}
		if _, ok := taken[strings.ToLower(attempt)]; !ok {
			return attempt
		}
	}
	return fmt.Sprintf("user-%d", time.Now().UnixNano())
}

// ensureColumn 在表缺少该列时补上它，已存在则什么都不做（因而可重复执行）。
//
// 用 PRAGMA table_info 主动探测，而不是「直接 ALTER 再忽略 duplicate column 错误」，
// 是为了让日志能准确区分「本次新增了列」与「本来就有」。
func ensureColumn(db *sql.DB, table, column, definition string) error {
	logging.Debugf("[database] checking column %s.%s", table, column)
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("check column %s.%s: %w", table, column, err)
	}
	defer rows.Close()

	found := false
	for rowsNext(rows) {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rowsScan(rows, &cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("scan column %s.%s: %w", table, column, err)
		}
		if name == column {
			found = true
			break
		}
	}
	if err := rowsErr(rows); err != nil {
		return fmt.Errorf("iterate columns %s: %w", table, err)
	}
	if found {
		return nil
	}

	logging.Debugf("[database] adding column %s.%s %s", table, column, definition)
	if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}
