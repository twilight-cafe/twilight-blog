package model

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
)

// adminPasswordHash 是一份真实的 bcrypt 哈希，对应传入的明文。
//
// 用它而不是随便一个占位串：这条迁移的全部意义就是「旧密码在新版本里仍然能登录」，
// 所以测试必须能真的比对一次。
func adminPasswordHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return string(hash)
}

// legacyConfig 构造一个「凭据还留在 settings.json 里」的配置对象，
// 也就是升级前的状态。它不落盘：迁移只读内存里的字段，文件读写另有测试覆盖。
func legacyConfig(hash, username, nickname string) *config.Config {
	return &config.Config{
		PasswordHash:  hash,
		AdminUsername: username,
		AdminNickname: nickname,
	}
}

// openMigrationTestDB 打开一个空的测试库（表已建好、没有任何账号）。
func openMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "migrate.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestLegacySettingsHashIsMigratedOnce covers the one migration this refactor
// cannot skip, and the property that makes it safe to run on every start.
//
// 不迁移的后果是一个真实的接管窗口：密码哈希此前只存在于 settings.json，
// 升级后若直接把它从文件里删掉而不搬进库，第一个启动瞬间库里没有管理员行 →
// 「已安装」为假 → 未鉴权的 /install 重新开放。
//
// 幂等那一半同样重要：老配置里可能留着一个**已经轮换掉**的哈希（用户早就通过
// CLI 或 /install 换过密码），覆盖会让旧密码在无人察觉的情况下复活。
func TestLegacySettingsHashIsMigratedOnce(t *testing.T) {
	db := openMigrationTestDB(t)
	ctx := context.Background()
	repo := NewSQLiteAdminRepository(db)

	const plaintext = "MigratedPass123"
	hash := adminPasswordHash(t, plaintext)

	// 第一次：库里没有管理员行，凭据必须被搬进来。
	if err := MigrateAdminCredentials(ctx, db, legacyConfig(hash, "root", "站长")); err != nil {
		t.Fatalf("MigrateAdminCredentials() error = %v", err)
	}

	admin, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get() after migration error = %v", err)
	}
	if !admin.IsAdmin {
		t.Fatal("the migrated row is not marked as the administrator")
	}
	if admin.Username != "root" || admin.Nickname != "站长" {
		t.Fatalf("migrated identity = (%q, %q), want (root, 站长)", admin.Username, admin.Nickname)
	}
	if admin.Email != "" {
		t.Fatalf("migrated email = %q, want the empty sentinel", admin.Email)
	}
	// 迁移的全部意义：旧密码在新版本里仍然能登录。
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(plaintext)); err != nil {
		t.Fatalf("the migrated hash does not match the old password: %v", err)
	}

	// 第二次：库里已经有管理员行，**绝不覆盖**。
	// 这里换一个哈希和一个名字，模拟「settings.json 是一份陈旧备份」。
	stale := adminPasswordHash(t, "RotatedPass456")
	if err := MigrateAdminCredentials(ctx, db, legacyConfig(stale, "stale-name", "Stale")); err != nil {
		t.Fatalf("second MigrateAdminCredentials() error = %v", err)
	}

	after, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get() after the second migration error = %v", err)
	}
	if after.ID != admin.ID {
		t.Fatalf("the second migration created another administrator (id %d → %d)", admin.ID, after.ID)
	}
	if after.PasswordHash != admin.PasswordHash {
		t.Fatal("a stale settings.json backup resurrected the rotated password")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), []byte("RotatedPass456")); err == nil {
		t.Fatal("the stale hash was installed over the current one")
	}
	if after.Username != "root" || after.Nickname != "站长" {
		t.Fatalf("the second migration overwrote the identity: (%q, %q)", after.Username, after.Nickname)
	}

	// 库里只能有一行管理员，无论迁移跑多少次。
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&count); err != nil {
		t.Fatalf("count administrators: %v", err)
	}
	if count != 1 {
		t.Fatalf("administrator rows = %d after two migrations, want 1", count)
	}
}

// TestMigrateWithoutCredentialsLeavesTheInstallOpen covers the "not installed"
// case: a settings.json that never held a password must not produce a
// password-less administrator row.
//
// 那种行会把 /install 永久关掉（因为这个端点靠「有没有管理员行」判断），
// 却谁也没法登录——比「没装过」糟糕得多，因为用户再也回不到安装向导。
func TestMigrateWithoutCredentialsLeavesTheInstallOpen(t *testing.T) {
	db := openMigrationTestDB(t)
	ctx := context.Background()

	if err := MigrateAdminCredentials(ctx, db, legacyConfig("", "", "")); err != nil {
		t.Fatalf("MigrateAdminCredentials() with no credentials error = %v", err)
	}

	exists, err := NewSQLiteAdminRepository(db).Exists(ctx)
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if exists {
		t.Fatal("a password-less administrator row was created; /install would be closed forever")
	}
}

// TestMigrateDefaultsTheIdentity covers the fallbacks: an old configuration may
// carry a hash but no names at all.
func TestMigrateDefaultsTheIdentity(t *testing.T) {
	db := openMigrationTestDB(t)
	ctx := context.Background()

	if err := MigrateAdminCredentials(ctx, db, legacyConfig(adminPasswordHash(t, "MigratedPass123"), "", "")); err != nil {
		t.Fatalf("MigrateAdminCredentials() error = %v", err)
	}

	admin, err := NewSQLiteAdminRepository(db).Get(ctx)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if admin.Username != config.DefaultAdminName {
		t.Fatalf("username = %q, want the default %q", admin.Username, config.DefaultAdminName)
	}
	// 昵称缺失时回退到登录名，而不是默认值——否则「改了登录名却没设昵称」的站点
	// 会显示成一个用户不认识的名字。
	if admin.Nickname != admin.Username {
		t.Fatalf("nickname = %q, want the login name %q", admin.Nickname, admin.Username)
	}
	if admin.CreatedAt == 0 || admin.UpdatedAt == 0 {
		t.Fatal("the migrated row has no timestamps")
	}
	if admin.CreatedAt > time.Now().Unix()+1 {
		t.Fatalf("created_at is in the future: %d", admin.CreatedAt)
	}
}

// TestMigrateToleratesANilConfig keeps the "config was never loaded" path from
// panicking: main.go tolerates ErrNotInstalled and may end up with no config.
func TestMigrateToleratesANilConfig(t *testing.T) {
	db := openMigrationTestDB(t)

	if err := MigrateAdminCredentials(context.Background(), db, nil); err != nil {
		t.Fatalf("MigrateAdminCredentials(nil) error = %v, want nil", err)
	}
}
