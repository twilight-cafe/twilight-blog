package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"blog-engine/internal/logging"
)

// UserRepository 是访客账号的持久化契约。
//
// 本包只负责落库，不做任何密码学处理：哈希算法、强度校验、旧密码验证都在
// internal/service/user_service。email 与 username 在表上都是 COLLATE NOCASE，
// 因此「大小写不同但实为同一个值」会被唯一约束拦下，并在此处被映射成
// ErrEmailTaken / ErrUsernameTaken——**区分这两者很重要**：前者是联系方式冲突，
// 后者是登录标识冲突，给用户看的话术完全不同。
type UserRepository interface {
	// FindByUsername 按登录标识查找（不区分大小写）。登录路径用它，不用 email。
	FindByUsername(ctx context.Context, username string) (*User, error)
	// FindByEmail 按联系方式查找，供注册与找回密码判断账号是否已存在。
	FindByEmail(ctx context.Context, email string) (*User, error)
	GetByID(ctx context.Context, id int64) (*User, error)
	Create(ctx context.Context, user *User) error
	// UpdateNickname 持久化公开显示名。
	UpdateNickname(ctx context.Context, userID int64, nickname string, updatedAt int64) error
	// UpdateUsername 持久化登录标识；冲突返回 ErrUsernameTaken。
	UpdateUsername(ctx context.Context, userID int64, username string, updatedAt int64) error
	// UpdatePassword 替换 bcrypt 哈希（校验旧密码是服务层的职责）。
	UpdatePassword(ctx context.Context, userID int64, passwordHash string, updatedAt int64) error
	// UpdateProfile 供管理员一次改掉登录标识、显示名与邮箱；两个唯一约束都可能触发。
	UpdateProfile(ctx context.Context, userID int64, username, nickname, email string, updatedAt int64) error
	List(ctx context.Context, p Pagination) ([]User, int64, error)
}

type SQLiteUserRepository struct {
	db *sql.DB
}

func NewSQLiteUserRepository(db *sql.DB) *SQLiteUserRepository {
	return &SQLiteUserRepository{db: db}
}

// userColumns 是 users 表所有读取路径**共用**的列清单。
//
// 它同时服务访客与管理员的读取（FindByUsername / FindByEmail / GetByID / List /
// AdminRepository.Get），所以每一列都必须在这里出现。漏列不会报错、不会编译失败，
// 只会让那一列在 Go 侧恒为字段零值：漏掉 is_admin 的具体后果是**管理员登录后拿到
// role=user 的会话，从而再也进不去 /api/admin**——站点锁死，而所有既有测试仍然是
// 绿的。TestUserColumnsCarryIsAdmin 就是为这一条存在的。
//
// is_admin 用 COALESCE 兜底，与 username / nickname / password_hash 一致：老库在
// 补列之前根本没有这一列，兜底让「列还没补上」也不至于整条查询失败。
//
// avatar_media_id 刻意**不**用 COALESCE：它是可空的引用，NULL 是有意义的取值
// （「没有头像」），而 COALESCE(x, ...) 会把它压成一个数字。这里的兜底由
// addedColumns 的补列负责——迁移之后这一列一定存在。
const userColumns = `id, email, COALESCE(username, ''), COALESCE(nickname, ''), COALESCE(password_hash, ''), COALESCE(is_admin, 0), avatar_media_id, created_at, updated_at`

func scanUser(row scanner) (*User, error) {
	var user User
	// is_admin 扫进 int64 再转换，而不是直接扫 *bool：SQLite 的 INTEGER 到 Go 的
	// bool 依赖驱动实现，赌错的话错误只会在运行期出现。一次显式转换换掉这个不确定性。
	var isAdmin int64
	if err := row.Scan(&user.ID, &user.Email, &user.Username, &user.Nickname, &user.PasswordHash, &isAdmin, &user.AvatarMediaID, &user.CreatedAt, &user.UpdatedAt); err != nil {
		return nil, err
	}
	user.IsAdmin = isAdmin != 0
	return &user, nil
}

// FindByUsername 按登录标识查找，比较时不区分大小写（COLLATE NOCASE）。
// 不存在返回 ErrUserNotFound。
//
// 提醒：登录流程不要把「用户不存在」与「密码错误」区分着返回给前端，
// 那会变成一个账号枚举接口。
func (r *SQLiteUserRepository) FindByUsername(ctx context.Context, username string) (*User, error) {
	logging.Debugf("[user-repo] FindByUsername called")
	row := r.db.QueryRowContext(ctx, `
SELECT `+userColumns+`
FROM users
WHERE username = ? COLLATE NOCASE
`, username)

	user, err := scanUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("find user by username: %w", err)
	}
	return user, nil
}

// FindByEmail 按邮箱查找，比较时不区分大小写（COLLATE NOCASE）。
// 不存在返回 ErrUserNotFound。仅用于「这个邮箱有没有账号」这类判断，
// **不用于登录**——登录标识是 username。
func (r *SQLiteUserRepository) FindByEmail(ctx context.Context, email string) (*User, error) {
	logging.Debugf("[user-repo] FindByEmail called")
	row := r.db.QueryRowContext(ctx, `
SELECT `+userColumns+`
FROM users
WHERE email = ? COLLATE NOCASE
`, email)

	user, err := scanUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("find user by email: %w", err)
	}
	return user, nil
}

func (r *SQLiteUserRepository) GetByID(ctx context.Context, id int64) (*User, error) {
	logging.Debugf("[user-repo] GetByID called")
	row := r.db.QueryRowContext(ctx, `
SELECT `+userColumns+`
FROM users
WHERE id = ?
`, id)

	user, err := scanUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("get user: %w", err)
	}
	return user, nil
}

// Create 插入账号并回填 ID。唯一约束冲突时返回 ErrEmailTaken 或
// ErrUsernameTaken，而不是把驱动的错误直接抛给上层。
//
// is_admin 一并写入：管理员行就是靠这一列（加上 Create 时的 IsAdmin=true）产生的，
// 漏写会让迁移建出来的管理员行退化成普通访客。
func (r *SQLiteUserRepository) Create(ctx context.Context, user *User) error {
	logging.Debugf("[user-repo] Create called")
	res, err := r.db.ExecContext(ctx, `
INSERT INTO users (email, username, nickname, password_hash, is_admin, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, user.Email, user.Username, user.Nickname, user.PasswordHash, boolToInt(user.IsAdmin), user.CreatedAt, user.UpdatedAt)
	if err != nil {
		if target, ok := uniqueViolationTarget(err); ok {
			return uniqueTargetError(target)
		}
		return fmt.Errorf("insert user: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("get last insert id: %w", err)
	}
	user.ID = id
	return nil
}

func (r *SQLiteUserRepository) UpdateNickname(ctx context.Context, userID int64, nickname string, updatedAt int64) error {
	logging.Debugf("[user-repo] UpdateNickname called")
	res, err := r.db.ExecContext(ctx, `
UPDATE users
SET nickname = ?, updated_at = ?
WHERE id = ?
`, nickname, updatedAt, userID)
	if err != nil {
		return fmt.Errorf("update user nickname: %w", err)
	}
	return requireAffected(res, ErrUserNotFound)
}

func (r *SQLiteUserRepository) UpdateUsername(ctx context.Context, userID int64, username string, updatedAt int64) error {
	logging.Debugf("[user-repo] UpdateUsername called")
	res, err := r.db.ExecContext(ctx, `
UPDATE users
SET username = ?, updated_at = ?
WHERE id = ?
`, username, updatedAt, userID)
	if err != nil {
		if target, ok := uniqueViolationTarget(err); ok {
			return uniqueTargetError(target)
		}
		return fmt.Errorf("update user username: %w", err)
	}
	return requireAffected(res, ErrUserNotFound)
}

func (r *SQLiteUserRepository) UpdatePassword(ctx context.Context, userID int64, passwordHash string, updatedAt int64) error {
	logging.Debugf("[user-repo] UpdatePassword called")
	res, err := r.db.ExecContext(ctx, `
UPDATE users
SET password_hash = ?, updated_at = ?
WHERE id = ?
`, passwordHash, updatedAt, userID)
	if err != nil {
		return fmt.Errorf("update user password: %w", err)
	}
	return requireAffected(res, ErrUserNotFound)
}

// UpdateProfile 更新**访客账号**的登录名、展示名与邮箱。
//
// `AND is_admin = 0` 不是可选的，删掉它会打开一条真实的接管链：
//
//	管理员行的 email 平时是空串哨兵，因此公开的 POST /api/auth/reset-password
//	够不到它（normalizeAndValidateEmail 拒绝空值）。一旦这个 UPDATE 允许写管理员行，
//	就等于把站长账号改成一个「任何能收那个邮箱的人都能重置密码」的账号——
//	而 ResetPassword 只按 email 查行，从不检查 is_admin。
//
// 目标行不存在返回 ErrUserNotFound；存在但不是访客账号返回 ErrNotAVisitorAccount。
// 两者分开是因为处置方式完全不同：前者是 id 写错了，后者是对象选错了。
func (r *SQLiteUserRepository) UpdateProfile(ctx context.Context, userID int64, username, nickname, email string, updatedAt int64) error {
	logging.Debugf("[user-repo] UpdateProfile called")
	res, err := r.db.ExecContext(ctx, `
UPDATE users
SET username = ?, nickname = ?, email = ?, updated_at = ?
WHERE id = ? AND is_admin = 0
`, username, nickname, email, updatedAt, userID)
	if err != nil {
		if target, ok := uniqueViolationTarget(err); ok {
			return uniqueTargetError(target)
		}
		return fmt.Errorf("update user profile: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update user profile rows affected: %w", err)
	}
	if affected > 0 {
		return nil
	}

	// 一行都没改到。区分两种情况，让操作员知道该改 id 还是该换入口。
	var isAdmin int
	switch scanErr := r.db.QueryRowContext(ctx, `SELECT is_admin FROM users WHERE id = ?`, userID).Scan(&isAdmin); {
	case errors.Is(scanErr, sql.ErrNoRows):
		return ErrUserNotFound
	case scanErr != nil:
		return fmt.Errorf("classify update profile target: %w", scanErr)
	case isAdmin != 0:
		logging.Warnf("[user-repo] refused to edit the administrator row through the visitor profile path user_id=%d", userID)
		return ErrNotAVisitorAccount
	default:
		// 行存在、是访客，却「影响 0 行」：SQLite 的 changes() 统计的是 WHERE
		// 匹配到的行数（而不是值真正发生变化的行数），所以这里理论上不可达。
		// 保守地按不存在处理，而不是静默成功。
		return ErrUserNotFound
	}
}

// List 返回一页**访客**账号，按创建时间倒序。
// total 与当页在同一个读事务里取出（见 query.go 的 readTx），两者不会不一致。
//
// WHERE is_admin = 0 是**必需**的，而且 COUNT 与 SELECT **两处都要加**：
// 管理员也住在 users 表里（is_admin = 1），漏掉任何一处都会出问题——
// 只漏 SELECT 会让管理员混进后台的「用户管理」列表，
// 只漏 COUNT 会让分页总数比实际能取到的条目多。
func (r *SQLiteUserRepository) List(ctx context.Context, p Pagination) ([]User, int64, error) {
	logging.Debugf("[user-repo] List called")

	var total int64
	users := make([]User, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 0`).Scan(&total); err != nil {
			return fmt.Errorf("count users: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT `+userColumns+`
FROM users
WHERE is_admin = 0
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			user, err := scanUser(rows)
			if err != nil {
				return err
			}
			users = append(users, *user)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate users: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

func requireAffected(res sql.Result, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return notFound
	}
	return nil
}

// uniqueViolationTarget 判断 err 是否为 UNIQUE 约束冲突，并指出是**哪一列**冲突。
//
// modernc 驱动把它暴露成普通 error，错误文本里带着被违反的索引或列名，
// 例如 "UNIQUE constraint failed: users.email"。所以这里匹配文本而不是依赖
// 驱动私有的错误码——升级驱动时需留意这段匹配是否仍然成立。
//
// 之所以要区分列：email 冲突要提示「邮箱已注册」，username 冲突要提示
// 「用户名已被占用」，把两者合并会让用户完全不知道该改哪一项。
// 第三个可能是单管理员索引：它意味着「已经有一个管理员了」，与上面两者都不同，
// 单独辨认出来才能给出正确的判断（同样没人该把这条错当成邮箱冲突）。
//
// 单管理员索引必须排在 email 之前判断：它建在 users(is_admin) 上，而驱动报的是
// **列名**（"UNIQUE constraint failed: users.is_admin"）而不是索引名。管理员行的
// email 恒为空串哨兵，所以现实中「插入第二个管理员」会**同时**违反这两条约束，
// 驱动只会报告先被检查到的那一条——而这个顺序是实现的细节，不能依赖。把
// is_admin 放在最前面，两种顺序下都能得到正确的 ErrAdminAlreadyExists。
func uniqueViolationTarget(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	text := strings.ToLower(err.Error())
	if !strings.Contains(text, "unique constraint failed") {
		return "", false
	}
	switch {
	case strings.Contains(text, "users.is_admin"), strings.Contains(text, "idx_users_single_admin"):
		return "admin", true
	case strings.Contains(text, "users.username"), strings.Contains(text, "idx_users_username"):
		return "username", true
	case strings.Contains(text, "users.email"):
		return "email", true
	default:
		return "", true
	}
}

// uniqueTargetError 把 uniqueViolationTarget 的结果映射成哨兵错误。
// 认不出具体列时保守地按邮箱冲突处理——那是本表更常见的一项。
func uniqueTargetError(target string) error {
	switch target {
	case "username":
		return ErrUsernameTaken
	case "admin":
		return ErrAdminAlreadyExists
	default:
		return ErrEmailTaken
	}
}

// boolToInt 把 Go 的 bool 写成 SQLite 的 0/1。modernc 驱动其实也接受 bool 参数，
// 但显式转换让存进去的值在 sqlite3 CLI 里是 0/1 而不是 'true'/'false' 这种
// 依赖驱动的表示——这一列会被 SQL 的 WHERE is_admin = 1 直接比较。
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
