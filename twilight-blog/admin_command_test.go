package main

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/database"
	"blog-engine/internal/logging"
	"blog-engine/internal/model"
	"blog-engine/internal/session"
)

// TestMain 负责两件事，都是为了让这条命令可测：
//
//  1. 日志文件。runAdminSetPassword 会调 logging.Configure("")，它在**当前工作
//     目录**下打开 log.txt 并在进程生命周期内一直持有那个句柄（刻意的设计——服务
//     要持续写日志）。Windows 不允许删除仍被打开的文件，所以如果工作目录是
//     t.TempDir()，它的自动清理会失败，并把一个**断言全过**的用例标成失败。
//     因此这里把所有测试实例建在一个自己管理的根目录下，并且**不在** m.Run 期间
//     回收它——进程退出时操作系统会一并释放。
//
//  2. 一个可用的当前工作目录。settingsPath / databasePath 是包级**相对**常量，
//     切换工作目录就等于把它们指向某个测试实例。
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "twilight-blog-cli-tests")
	if err != nil {
		panic("create the CLI test root: " + err.Error())
	}
	cliTestRoot = root

	// 把日志文件钉在测试根目录里，而不是让第一次写日志的那个用例决定它去哪。
	// log.txt 一旦打开就跟着进程走到结束（logging 包的设计），所以谁先写谁决定
	// 位置——那意味着一个随机用例的目录里会多出一个谁也删不掉的 log.txt。
	// 在这里先打开，位置就固定了，与用例的执行顺序无关。
	pinLogFile(root)

	code := m.Run()

	// 日志句柄此时仍被持有，删除可能失败；失败不是错误，临时目录会被系统回收。
	_ = os.RemoveAll(root)
	os.Exit(code)
}

// pinLogFile 以 root 为工作目录打开一次日志文件，之后所有日志都写到那里。
func pinLogFile(root string) {
	previousWD, err := os.Getwd()
	if err != nil {
		panic("getwd: " + err.Error())
	}
	if err := os.Chdir(root); err != nil {
		panic("chdir to the CLI test root: " + err.Error())
	}
	defer func() { _ = os.Chdir(previousWD) }()

	if err := logging.Configure(""); err != nil {
		panic("configure logging: " + err.Error())
	}
}

// cliTestRoot 见 TestMain。
var cliTestRoot string

// useTempInstance 为本次调用新建一个隔离的工作目录并切过去。
//
// 它**不用** t.TempDir()：理由见 TestMain（日志文件句柄会挡住 Windows 的删除）。
func useTempInstance(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp(cliTestRoot, "instance-")
	if err != nil {
		t.Fatalf("create the instance directory: %v", err)
	}

	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir to the temp instance: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousWD) })

	return dir
}

// feedStdin 替换三个标准流，把 input 当作 stdin 喂进去。
//
// admin_command.go 里的 stdin/stdout/stderr 之所以是变量，正是为了让这里能注入：
// 密码输入的分支（两次不一致、空密码、强度不足、CRLF）不注入就完全测不到。
// 它返回两个 builder 供断言读取输出。
func feedStdin(t *testing.T, input string) (out, errOut *strings.Builder) {
	t.Helper()

	out, errOut = &strings.Builder{}, &strings.Builder{}
	in := strings.NewReader(input)

	prevIn, prevOut, prevErr := stdin, stdout, stderr
	stdin = func() io.Reader { return in }
	stdout = func() io.Writer { return out }
	stderr = func() io.Writer { return errOut }
	t.Cleanup(func() { stdin, stdout, stderr = prevIn, prevOut, prevErr })

	return out, errOut
}

// withTempInstance 是上面两者的组合，也是大多数用例想要的形态。
func withTempInstance(t *testing.T, input string) (dir string, out, errOut *strings.Builder) {
	t.Helper()
	dir = useTempInstance(t)
	out, errOut = feedStdin(t, input)
	return dir, out, errOut
}

// openCommandDB 打开命令行会用的那个库，供断言直接查表。
func openCommandDB(t *testing.T, dir string) *sql.DB {
	t.Helper()

	db, err := database.Open(filepath.Join(dir, "blog.db"))
	if err != nil {
		t.Fatalf("open the command's database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestAdminSetPasswordCreatesThenUpdates covers both branches: no administrator
// yet (create) and one already present (update). 这是「忘记密码」的唯一恢复路径，
// 所以两条分支都必须工作。
func TestAdminSetPasswordCreatesThenUpdates(t *testing.T) {
	const first = "AdminPass123"
	dir, out, errOut := withTempInstance(t, first+"\n"+first+"\n")

	if code := runAdminCommand([]string{"set-password"}); code != 0 {
		t.Fatalf("set-password exit code = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "created") {
		t.Fatalf("the create branch printed %q", out.String())
	}

	db := openCommandDB(t, dir)
	repo := model.NewSQLiteAdminRepository(db)

	admin, err := repo.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() after the create error = %v", err)
	}
	if !admin.IsAdmin {
		t.Fatal("the created row is not marked as the administrator")
	}
	if admin.Username != "admin" || admin.Nickname != "admin" {
		t.Fatalf("default identity = (%q, %q), want (admin, admin)", admin.Username, admin.Nickname)
	}
	if admin.Email != "" {
		t.Fatalf("email = %q, want the empty sentinel", admin.Email)
	}

	// 第二次：管理员已存在，走更新分支，而且**身份不变**。
	// 输入流要先重建（上一个已经被读完）。
	const second = "RotatedPass456"
	out2, errOut2 := feedStdin(t, second+"\n"+second+"\n")
	if code := runAdminCommand([]string{"set-password"}); code != 0 {
		t.Fatalf("second set-password exit code = %d, want 0 (stderr: %s)", code, errOut2.String())
	}
	if !strings.Contains(out2.String(), "updated") {
		t.Fatalf("the update branch printed %q", out2.String())
	}

	updated, err := repo.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() after the update error = %v", err)
	}
	if updated.ID != admin.ID {
		t.Fatalf("the update created another administrator (id %d → %d)", admin.ID, updated.ID)
	}
	if updated.PasswordHash == admin.PasswordHash {
		t.Fatal("the password hash did not change")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(updated.PasswordHash), []byte(second)); err != nil {
		t.Fatalf("the stored hash does not match the new password: %v", err)
	}
}

// TestAdminSetPasswordRevokesSessions is the guard for the step that makes the
// rotation mean something: a password change must invalidate every session that
// existed under the old password.
//
// 反证方式：删掉 session.RevokeAll 调用 → 旧 token 仍然有效，测试失败。
// 这一条尤其重要，因为这条命令的典型场景就是「怀疑密码泄露了」——若会话不失效，
// 操作者会以为已经处理好，而攻击者的 token 还活着。
func TestAdminSetPasswordRevokesSessions(t *testing.T) {
	const first = "AdminPass123"
	dir, _, errOut := withTempInstance(t, first+"\n"+first+"\n")
	if code := runAdminCommand([]string{"set-password"}); code != 0 {
		t.Fatalf("set-password exit code = %d (stderr: %s)", code, errOut.String())
	}

	db := openCommandDB(t, dir)
	repo := model.NewSQLiteAdminRepository(db)
	admin, err := repo.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	// 冒充「改密码之前就已经登录的设备」：两个管理员会话 + 一个访客会话。
	store := session.NewStore(db)
	ctx := context.Background()
	adminID := admin.ID
	oldAdminToken, _, _, err := store.Create(ctx, session.RoleAdmin, &adminID, session.AdminTTL)
	if err != nil {
		t.Fatalf("create the administrator session: %v", err)
	}
	otherAdminToken, _, _, err := store.Create(ctx, session.RoleAdmin, &adminID, session.AdminTTL)
	if err != nil {
		t.Fatalf("create the second administrator session: %v", err)
	}

	const second = "RotatedPass456"
	feedStdin(t, second+"\n"+second+"\n")
	if code := runAdminCommand([]string{"set-password"}); code != 0 {
		t.Fatalf("second set-password exit code = %d", code)
	}

	for name, token := range map[string]string{"first": oldAdminToken, "second": otherAdminToken} {
		if _, err := store.Lookup(ctx, token); err == nil {
			t.Fatalf("the %s administrator session survived set-password; "+
				"a rotated password must log every device out", name)
		}
	}
}

// TestAdminSetPasswordValidation covers every input path that must exit with the
// documented code 2 and change nothing.
func TestAdminSetPasswordValidation(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		input string
	}{
		{"mismatched repeat", []string{"set-password"}, "AdminPass123\nAdminPass124\n"},
		{"empty password", []string{"set-password"}, "\n\n"},
		{"too short", []string{"set-password"}, "Ab1\nAb1\n"},
		{"no digit", []string{"set-password"}, "onlyletters\nonlyletters\n"},
		{"no letter", []string{"set-password"}, "1234567890\n1234567890\n"},
		{"unknown argument", []string{"set-password", "--password", "x"}, "AdminPass123\nAdminPass123\n"},
		{"missing username value", []string{"set-password", "--username"}, "AdminPass123\nAdminPass123\n"},
		{"invalid username shape", []string{"set-password", "--username", "1bad"}, "AdminPass123\nAdminPass123\n"},
		{"unknown subcommand", []string{"nope"}, ""},
		{"no subcommand", []string{}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, errOut := withTempInstance(t, tc.input)

			if code := runAdminCommand(tc.args); code != exitUsageOrValidation {
				t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitUsageOrValidation, errOut.String())
			}
			if errOut.Len() == 0 {
				t.Fatal("a rejected invocation printed nothing to stderr")
			}

			// 被拒绝的调用不得留下任何副作用：不该建行、不该建库。
			if _, err := os.Stat(filepath.Join(dir, "blog.db")); err == nil {
				db, openErr := database.Open(filepath.Join(dir, "blog.db"))
				if openErr != nil {
					t.Fatalf("open the database the rejected command created: %v", openErr)
				}
				defer db.Close()
				exists, existsErr := model.NewSQLiteAdminRepository(db).Exists(context.Background())
				if existsErr != nil {
					t.Fatalf("Exists() error = %v", existsErr)
				}
				if exists {
					t.Fatal("a rejected command still created an administrator row")
				}
			}
		})
	}
}

// TestAdminSetPasswordAcceptsAUsername covers the --username/--nickname flags and
// their four spellings, plus the fact that they are ignored once an account exists.
func TestAdminSetPasswordAcceptsAUsername(t *testing.T) {
	const password = "AdminPass123"
	dir, out, errOut := withTempInstance(t, password+"\n"+password+"\n")
	if code := runAdminCommand([]string{"set-password", "--username=root", "--nickname", "站长"}); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"root"`) {
		t.Fatalf("the command did not report the chosen login name: %q", out.String())
	}

	db := openCommandDB(t, dir)
	admin, err := model.NewSQLiteAdminRepository(db).Get(context.Background())
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if admin.Username != "root" || admin.Nickname != "站长" {
		t.Fatalf("identity = (%q, %q), want (root, 站长)", admin.Username, admin.Nickname)
	}

	// 第二次带一个不同的 --username：管理员已存在，所以它必须被**忽略**——
	// set-password 只改密码，改身份走控制台。悄悄改名会让操作者以为改了密码却
	// 顺带换了登录名，下一次登录就进不去了。
	feedStdin(t, "AnotherPass456\nAnotherPass456\n")
	if code := runAdminCommand([]string{"set-password", "--username", "somebody-else"}); code != 0 {
		t.Fatalf("second exit code = %d", code)
	}

	after, err := model.NewSQLiteAdminRepository(db).Get(context.Background())
	if err != nil {
		t.Fatalf("Get() after the second run error = %v", err)
	}
	if after.Username != "root" || after.ID != admin.ID {
		t.Fatalf("set-password changed the identity of an existing account: (%q, id %d)", after.Username, after.ID)
	}
}

// TestAdminSetPasswordMigratesLegacyCredentials covers the ordering hazard that
// would otherwise make this command write to the *wrong* account.
//
// 场景：一台老站点升级上来，凭据还在 settings.json 里、库里还没有管理员行。
// 若这条命令不先跑迁移，它会看到「没有管理员」并创建一个**新的**行（登录名取
// 默认值 admin）；随后主进程启动时迁移又会试图插入它自己那一行——撞上单管理员
// 索引失败，于是**旧密码永久失效、新密码写在一个并不是迁移来源的账号上**。
// 先迁移就没有这个分叉：命令更新的是迁移建出来的那一行。
func TestAdminSetPasswordMigratesLegacyCredentials(t *testing.T) {
	dir, _, errOut := withTempInstance(t, "")

	// 伪造一份旧版 settings.json：凭据在文件里。
	legacyHash, err := bcrypt.GenerateFromPassword([]byte("LegacyPass123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash the legacy password: %v", err)
	}
	settings := `{"password_hash":"` + string(legacyHash) + `","admin_username":"legacy-root","admin_nickname":"老站长"}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatalf("write the legacy settings.json: %v", err)
	}

	// 换成带密码的输入流。
	const newPassword = "FreshPass789"
	feedStdin(t, newPassword+"\n"+newPassword+"\n")
	if code := runAdminCommand([]string{"set-password"}); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut.String())
	}

	db := openCommandDB(t, dir)
	repo := model.NewSQLiteAdminRepository(db)

	admin, err := repo.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	// 必须是迁移建出来的那一行，而不是命令自己新建的一行。
	if admin.Username != "legacy-root" || admin.Nickname != "老站长" {
		t.Fatalf("the command did not reuse the migrated row: (%q, %q)", admin.Username, admin.Nickname)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(newPassword)); err != nil {
		t.Fatalf("the stored hash does not match the new password: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&count); err != nil {
		t.Fatalf("count administrators: %v", err)
	}
	if count != 1 {
		t.Fatalf("administrator rows = %d, want exactly 1", count)
	}
}

// TestReadPasswordTwiceHandlesCRLF pins the Windows detail: both a terminal and a
// pipe deliver CRLF, and leaving the CR in place makes two visually identical
// passwords compare as different.
func TestReadPasswordTwiceHandlesCRLF(t *testing.T) {
	var prompt strings.Builder

	got, err := readPasswordTwice(strings.NewReader("CRLFPass123\r\nCRLFPass123\r\n"), &prompt)
	if err != nil {
		t.Fatalf("readPasswordTwice() error = %v", err)
	}
	if got != "CRLFPass123" {
		t.Fatalf("readPasswordTwice() = %q, want CRLFPass123", got)
	}

	// 只有一次输入（EOF 之后没有第二行）必须报错，而不是把空串当成第二次输入
	// 然后误判为「不一致」——错误信息要指向真正的问题。
	if _, err := readPasswordTwice(strings.NewReader("OnlyOnce123\n"), &prompt); err == nil {
		t.Fatal("a single password line was accepted")
	}
}

// TestAdminSetPasswordNeverTakesThePasswordFromArgv pins the deliberate absence
// of a password flag: argv leaks into shell history and the process list.
func TestAdminSetPasswordNeverTakesThePasswordFromArgv(t *testing.T) {
	dir, _, errOut := withTempInstance(t, "")

	code := runAdminCommand([]string{"set-password", "--password", "AdminPass123"})
	if code != exitUsageOrValidation {
		t.Fatalf("exit code = %d, want %d", code, exitUsageOrValidation)
	}
	if !strings.Contains(errOut.String(), "unknown argument") {
		t.Fatalf("a --password flag was not rejected: %s", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "blog.db")); err == nil {
		t.Fatal("a rejected invocation created a database")
	}
}
