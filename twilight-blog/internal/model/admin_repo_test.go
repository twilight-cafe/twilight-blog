package model

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"blog-engine/internal/database"
)

// newTestAdminRepo returns both repositories over one fresh database, because
// most assertions here are about how the administrator row and the visitor
// directory see each other.
func newTestAdminRepo(t *testing.T) (*SQLiteAdminRepository, *SQLiteUserRepository, *sql.DB) {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return NewSQLiteAdminRepository(db), NewSQLiteUserRepository(db), db
}

// seedAdmin inserts the administrator through the repository, which is the path
// the installer and the migration both take.
func seedAdmin(t *testing.T, repo *SQLiteAdminRepository, username, nickname string) *User {
	t.Helper()

	admin := &User{
		// The empty address is the sentinel every administrator row carries.
		Email:        "",
		Username:     username,
		Nickname:     nickname,
		PasswordHash: "hash",
		CreatedAt:    time.Now().Unix(),
		UpdatedAt:    time.Now().Unix(),
	}
	if err := repo.Create(context.Background(), admin); err != nil {
		t.Fatalf("create administrator %q: %v", username, err)
	}
	return admin
}

// TestUserColumnsCarryIsAdmin is the direct guard for the silent failure that
// would lock the site out: userColumns is the single SELECT list shared by every
// read path, so leaving is_admin out of it compiles, passes every other test,
// and makes model.User.IsAdmin false forever. The administrator then signs in
// with role=user and can never reach /api/admin again.
func TestUserColumnsCarryIsAdmin(t *testing.T) {
	adminRepo, userRepo, _ := newTestAdminRepo(t)
	ctx := context.Background()

	admin := seedAdmin(t, adminRepo, "admin", "站长")

	// Every read path that goes through userColumns must report the flag.
	byID, err := userRepo.GetByID(ctx, admin.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !byID.IsAdmin {
		t.Fatalf("GetByID() returned IsAdmin = false for the administrator row")
	}

	byUsername, err := userRepo.FindByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("FindByUsername() error = %v", err)
	}
	if !byUsername.IsAdmin {
		t.Fatal("FindByUsername() returned IsAdmin = false for the administrator row")
	}

	byEmail, err := userRepo.FindByEmail(ctx, "")
	if err != nil {
		t.Fatalf("FindByEmail(\"\") should find the administrator row by its sentinel address: %v", err)
	}
	if !byEmail.IsAdmin {
		t.Fatal("FindByEmail() returned IsAdmin = false for the administrator row")
	}

	// The boolean round-trip itself: a stored 1 must read back as true and a
	// stored 0 as false, through both Create and a plain UPDATE.
	if err := userRepo.UpdateNickname(ctx, admin.ID, "站长", time.Now().Unix()); err != nil {
		t.Fatalf("UpdateNickname() error = %v", err)
	}
	if got, err := userRepo.GetByID(ctx, admin.ID); err != nil || !got.IsAdmin {
		t.Fatalf("IsAdmin did not survive an unrelated UPDATE (err=%v, IsAdmin=%t)", err, got != nil && got.IsAdmin)
	}

	// A visitor must not be mistaken for the administrator.
	visitor := &User{Email: "visitor@example.com", Username: "visitor", Nickname: "V", PasswordHash: "h", CreatedAt: 1, UpdatedAt: 1}
	if err := userRepo.Create(ctx, visitor); err != nil {
		t.Fatalf("create visitor: %v", err)
	}
	stored, err := userRepo.GetByID(ctx, visitor.ID)
	if err != nil {
		t.Fatalf("GetByID(visitor) error = %v", err)
	}
	if stored.IsAdmin {
		t.Fatal("an ordinary account was read back as the administrator")
	}
}

// TestAdminRowIsInvisibleToTheVisitorList keeps the administrator out of the
// console's user management screen.
//
// Both halves are asserted on purpose: the SELECT and the COUNT are separate
// statements, and forgetting either one produces a different visible symptom
// (the administrator listed among visitors, or a total that does not match the
// rows that can actually be fetched).
func TestAdminRowIsInvisibleToTheVisitorList(t *testing.T) {
	adminRepo, userRepo, _ := newTestAdminRepo(t)
	ctx := context.Background()

	seedAdmin(t, adminRepo, "admin", "站长")
	for i, name := range []string{"alice", "bob"} {
		if err := userRepo.Create(ctx, &User{
			Email:        name + "@example.com",
			Username:     name,
			Nickname:     name,
			PasswordHash: "h",
			CreatedAt:    int64(i + 1),
			UpdatedAt:    int64(i + 1),
		}); err != nil {
			t.Fatalf("create visitor %s: %v", name, err)
		}
	}

	users, total, err := userRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if total != 2 {
		t.Fatalf("List() total = %d, want 2 (the administrator must not be counted)", total)
	}
	if len(users) != 2 {
		t.Fatalf("List() returned %d rows, want 2", len(users))
	}
	for _, user := range users {
		if user.IsAdmin {
			t.Fatalf("List() leaked the administrator row (username=%q)", user.Username)
		}
		if user.Username == "admin" {
			t.Fatalf("List() leaked the administrator row by name")
		}
	}
}

// TestAdminEmailSentinelIsUnreachable pins the choice of the empty string as the
// administrator's e-mail.
//
// users.email is NOT NULL UNIQUE, so the administrator row needs some value, and
// SQLite cannot relax a column constraint through ALTER. ” is used because no
// registration path can produce it — that is the whole reason no extra guard
// code is needed. If the sentinel were changed to a real-looking address such as
// admin@example.com, a visitor could register it first and the UNIQUE constraint
// would then block the installer; this test fails in that world.
func TestAdminEmailSentinelIsUnreachable(t *testing.T) {
	adminRepo, userRepo, db := newTestAdminRepo(t)
	ctx := context.Background()

	admin := seedAdmin(t, adminRepo, "admin", "站长")
	if admin.Email != "" {
		t.Fatalf("the administrator was created with email %q, want the empty sentinel", admin.Email)
	}

	// No legitimate address resolves to the administrator.
	for _, candidate := range []string{"admin@example.com", "ADMIN@EXAMPLE.COM", "root@localhost", "a@b.co"} {
		if _, err := userRepo.FindByEmail(ctx, candidate); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("FindByEmail(%q) resolved to the administrator (err = %v)", candidate, err)
		}
	}

	// The empty string is not a value any registration path can supply, and the
	// UNIQUE constraint means only one row can ever carry it.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = ''`).Scan(&count); err != nil {
		t.Fatalf("count sentinel addresses: %v", err)
	}
	if count != 1 {
		t.Fatalf("users with the sentinel address = %d, want exactly 1", count)
	}
	if _, err := db.Exec(`
INSERT INTO users (email, username, nickname, password_hash, is_admin, created_at, updated_at)
VALUES ('', 'second-empty', 'Second', 'h', 0, 9, 9);
`); err == nil {
		t.Fatal("a second row was allowed to take the sentinel address")
	}
}

// TestSecondAdministratorIsRefusedThroughTheRepository covers the same invariant
// as the database-level test, but at the layer callers actually use: the error
// must be ErrAdminAlreadyExists, not a raw driver error and not an email or
// username conflict (which would send the operator hunting for the wrong fix).
func TestSecondAdministratorIsRefusedThroughTheRepository(t *testing.T) {
	adminRepo, _, _ := newTestAdminRepo(t)
	ctx := context.Background()

	seedAdmin(t, adminRepo, "admin", "站长")

	err := adminRepo.Create(ctx, &User{
		Email:        "",
		Username:     "root",
		Nickname:     "Root",
		PasswordHash: "h",
		CreatedAt:    2,
		UpdatedAt:    2,
	})
	if !errors.Is(err, ErrAdminAlreadyExists) {
		t.Fatalf("second Create() error = %v, want ErrAdminAlreadyExists", err)
	}

	ok, err := adminRepo.Exists(ctx)
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if !ok {
		t.Fatal("Exists() reported no administrator after one was created")
	}
}

// TestAdminUsernameSharesTheVisitorNamespace is the structural reason the whole
// refactor is worth doing: one unique index covers both identities, so neither
// direction needs a pre-check or a reserved-name list.
func TestAdminUsernameSharesTheVisitorNamespace(t *testing.T) {
	adminRepo, userRepo, _ := newTestAdminRepo(t)
	ctx := context.Background()

	admin := seedAdmin(t, adminRepo, "admin", "站长")

	// A visitor cannot take the administrator's name.
	err := userRepo.Create(ctx, &User{Email: "copycat@example.com", Username: "ADMIN", Nickname: "C", PasswordHash: "h", CreatedAt: 1, UpdatedAt: 1})
	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("a visitor was allowed to take the administrator name: %v", err)
	}

	// The administrator cannot move onto a visitor's name.
	if err := userRepo.Create(ctx, &User{Email: "alice@example.com", Username: "alice", Nickname: "A", PasswordHash: "h", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("create visitor: %v", err)
	}
	if err := adminRepo.UpdateIdentity(ctx, admin.ID, "ALICE", "站长", 5); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("UpdateIdentity() onto a visitor username error = %v, want ErrUsernameTaken", err)
	}

	// A refused rename leaves the stored name alone.
	got, err := adminRepo.Get(ctx)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Username != "admin" {
		t.Fatalf("a refused rename moved the administrator name to %q", got.Username)
	}

	// And the old name is freed by a successful rename — no reserved-name list
	// to keep in sync, the index is the only authority.
	if err := adminRepo.UpdateIdentity(ctx, admin.ID, "root", "站长", 6); err != nil {
		t.Fatalf("UpdateIdentity() to a free name error = %v", err)
	}
	if err := userRepo.Create(ctx, &User{Email: "freed@example.com", Username: "admin", Nickname: "F", PasswordHash: "h", CreatedAt: 7, UpdatedAt: 7}); err != nil {
		t.Fatalf("the name the administrator vacated was not released: %v", err)
	}
}

// TestAdminSetPasswordRequiresTheAdminRow guards the WHERE clause: the update
// must not quietly succeed against a visitor row with the same id.
func TestAdminSetPasswordRequiresTheAdminRow(t *testing.T) {
	adminRepo, userRepo, _ := newTestAdminRepo(t)
	ctx := context.Background()

	admin := seedAdmin(t, adminRepo, "admin", "站长")
	visitor := &User{Email: "visitor@example.com", Username: "visitor", Nickname: "V", PasswordHash: "old", CreatedAt: 1, UpdatedAt: 1}
	if err := userRepo.Create(ctx, visitor); err != nil {
		t.Fatalf("create visitor: %v", err)
	}

	if err := adminRepo.SetPassword(ctx, admin.ID, "new-admin-hash", 10); err != nil {
		t.Fatalf("SetPassword() error = %v", err)
	}
	got, err := adminRepo.Get(ctx)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.PasswordHash != "new-admin-hash" {
		t.Fatalf("administrator password_hash = %q", got.PasswordHash)
	}

	if err := adminRepo.SetPassword(ctx, visitor.ID, "hijacked", 11); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("SetPassword() on a visitor id error = %v, want ErrUserNotFound", err)
	}
	unchanged, err := userRepo.GetByID(ctx, visitor.ID)
	if err != nil {
		t.Fatalf("GetByID(visitor) error = %v", err)
	}
	if unchanged.PasswordHash != "old" {
		t.Fatalf("the administrator update touched a visitor row (hash = %q)", unchanged.PasswordHash)
	}
}
