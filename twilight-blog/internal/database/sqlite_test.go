package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOpenEnablesForeignKeys is the regression test for the previously silent
// loss of referential integrity: the schema declares ON DELETE CASCADE
// everywhere, but SQLite leaves foreign_keys off by default, so without the DSN
// pragmas those constraints were inert.
func TestOpenEnablesForeignKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys;`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := db.QueryRow(`PRAGMA busy_timeout;`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("PRAGMA busy_timeout = %d, want 5000", busyTimeout)
	}
}

// TestForeignKeysStillEnabledOnNewConnection guards the reason the pragmas live
// in the DSN: session state is lost whenever database/sql opens a fresh
// connection, whereas DSN pragmas are reapplied by the driver.
func TestForeignKeysStillEnabledOnNewConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reconnect.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	// Force the pooled connection to be replaced.
	db.SetMaxIdleConns(0)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`SELECT 1;`); err != nil {
		t.Fatalf("warm connection: %v", err)
	}

	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys;`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys after reconnect: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d after reconnect, want 1", foreignKeys)
	}
}

func TestCascadeDeleteIsEnforced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO tags (id, name) VALUES (1, 'go');`); err != nil {
		t.Fatalf("insert tag: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO articles (id, title, content, created_at, updated_at)
VALUES (1, 't', 'c', 1, 1);
INSERT INTO article_tags (article_id, tag_id) VALUES (1, 1);
`); err != nil {
		t.Fatalf("insert article and link: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM articles WHERE id = 1;`); err != nil {
		t.Fatalf("delete article: %v", err)
	}

	var links int
	if err := db.QueryRow(`SELECT COUNT(*) FROM article_tags;`).Scan(&links); err != nil {
		t.Fatalf("count article_tags: %v", err)
	}
	if links != 0 {
		t.Fatalf("article_tags rows = %d after cascade, want 0", links)
	}
}

func TestMigrateAddsNicknameColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "columns.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	for _, table := range []string{"comments", "users"} {
		if !hasColumn(t, db, table, "nickname") {
			t.Fatalf("%s.nickname was not created", table)
		}
	}
	if !hasColumn(t, db, "users", "username") {
		t.Fatal("users.username was not created; sign-in has no identifier column")
	}
	if !hasColumn(t, db, "users", "is_admin") {
		t.Fatal("users.is_admin was not created; the administrator has nowhere to live")
	}
}

// TestOnlyOneAdministratorCanExist pins invariant I1: "at most one administrator"
// is enforced by the database, not by an application-level check.
//
// The distinction matters. A pre-check ("is there already an admin?") reads the
// table and then writes it, so two concurrent callers both see "no admin" and
// both insert. A partial unique index cannot be raced: whichever INSERT lands
// second hits the constraint. This test drives the index directly rather than
// through any caller, so removing the index fails it even if every Go-side guard
// were still in place.
func TestOnlyOneAdministratorCanExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "single-admin.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	var definition string
	err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_users_single_admin'`).Scan(&definition)
	if err != nil {
		t.Fatalf("idx_users_single_admin is missing: %v", err)
	}
	if !strings.Contains(definition, "WHERE is_admin = 1") {
		t.Fatalf("the single-admin index is not partial on is_admin = 1: %s", definition)
	}

	if _, err := db.Exec(`
INSERT INTO users (email, username, nickname, password_hash, is_admin, created_at, updated_at)
VALUES ('', 'admin', 'Admin', 'h', 1, 1, 1);
`); err != nil {
		t.Fatalf("insert the first administrator: %v", err)
	}

	// The second administrator row must be refused, whatever name it picks.
	if _, err := db.Exec(`
INSERT INTO users (email, username, nickname, password_hash, is_admin, created_at, updated_at)
VALUES ('', 'root', 'Root', 'h', 1, 2, 2);
`); err == nil {
		t.Fatal("a second administrator row was accepted; I1 is not enforced by the database")
	}

	// Any number of ordinary accounts still coexist, which is what makes the
	// index partial rather than a plain UNIQUE that would forbid is_admin = 0
	// after the first visitor is created.
	if _, err := db.Exec(`
INSERT INTO users (email, username, nickname, password_hash, is_admin, created_at, updated_at)
VALUES ('a@example.com', 'alice', 'Alice', 'h', 0, 3, 3),
       ('b@example.com', 'bob', 'Bob', 'h', 0, 4, 4);
`); err != nil {
		t.Fatalf("ordinary accounts were rejected: %v", err)
	}
}

// TestUpgradeAddsIsAdminToAnOldDatabase covers the second half of the "add a
// column" rule: a database created before this column existed has a users table
// that CREATE TABLE IF NOT EXISTS leaves untouched, so only addedColumns can
// bring it the new field.
func TestUpgradeAddsIsAdminToAnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-is-admin.db")

	raw, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	createLegacyUsersTable(t, raw)
	if _, err := raw.Exec(`
INSERT INTO users (email, nickname, password_hash, created_at, updated_at)
VALUES ('old@example.com', 'Old', 'h', 1, 1);
`); err != nil {
		t.Fatalf("seed the legacy account: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() on a pre-is_admin database error = %v", err)
	}
	defer db.Close()

	if !hasColumn(t, db, "users", "is_admin") {
		t.Fatal("users.is_admin was not added to the existing table")
	}

	// The pre-existing row must come out as an ordinary visitor, and the
	// single-admin index must be usable straight away — if the ALTER had added
	// the column without a DEFAULT, this UPDATE would either fail or leave NULLs.
	var isAdmin int64
	if err := db.QueryRow(`SELECT is_admin FROM users WHERE email = 'old@example.com'`).Scan(&isAdmin); err != nil {
		t.Fatalf("read is_admin of the legacy row: %v", err)
	}
	if isAdmin != 0 {
		t.Fatalf("a pre-existing account became an administrator (is_admin = %d)", isAdmin)
	}
}

// TestUsernameIndexIsPartialOnPurpose pins the shape of the uniqueness
// constraint, because the shape is what makes the migration safe: a column-level
// UNIQUE could not be added to a table whose existing rows all hold an empty string.
func TestUsernameIndexIsPartialOnPurpose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "username-index.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	var definition string
	err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_users_username'`).Scan(&definition)
	if err != nil {
		t.Fatalf("idx_users_username is missing: %v", err)
	}
	if !strings.Contains(definition, "WHERE username <> ''") {
		t.Fatalf("the index is not partial, so a pre-migration table could never receive it: %s", definition)
	}

	// Two rows without a username coexist, which a plain UNIQUE would forbid.
	if _, err := db.Exec(`
INSERT INTO users (email, username, created_at, updated_at) VALUES ('a@example.com', '', 1, 1), ('b@example.com', '', 1, 1);
`); err != nil {
		t.Fatalf("two accounts without a username were rejected: %v", err)
	}

	// A real username is unique, and the comparison ignores case.
	if _, err := db.Exec(`INSERT INTO users (email, username, created_at, updated_at) VALUES ('c@example.com', 'taken', 1, 1)`); err != nil {
		t.Fatalf("insert first username: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (email, username, created_at, updated_at) VALUES ('d@example.com', 'TAKEN', 1, 1)`); err == nil {
		t.Fatal("a differently-cased duplicate username was accepted")
	}
}

// TestUpgradeBackfillsUsernames drives the real upgrade path: a database created
// by the previous version (users without a username column, because a plain
// CREATE TABLE IF NOT EXISTS never adds one) must come out of Open with every
// account able to sign in.
func TestUpgradeBackfillsUsernames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	raw, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	createLegacyUsersTable(t, raw)
	if _, err := raw.Exec(`
INSERT INTO users (email, nickname, password_hash, created_at, updated_at) VALUES
    ('Alpha@Example.com', '甲', 'h', 1, 1),
    ('alice@example.com', 'Alice', 'h', 2, 2),
    ('alice@other.example', 'Another Alice', 'h', 3, 3),
    ('x@example.com', '太短', 'h', 4, 4),
    ('y@example.com', '也短', 'h', 5, 5);
`); err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	// Open() runs the whole migration on a pool capped at one connection. If the
	// backfill iterated rows while writing, it would wait forever for the
	// connection its own query is holding — a hang with no error message, so the
	// test needs its own deadline to produce a usable diagnosis.
	type opened struct {
		db  *sql.DB
		err error
	}
	result := make(chan opened, 1)
	go func() {
		db, err := Open(path)
		result <- opened{db: db, err: err}
	}()

	var db *sql.DB
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("Open() after a legacy upgrade error = %v", got.err)
		}
		db = got.db
		defer db.Close()
	case <-time.After(60 * time.Second):
		t.Fatal("Open() never returned: the username backfill is probably iterating rows while writing on the single pooled connection")
	}

	got := map[string]string{}
	rows, err := db.Query(`SELECT email, username FROM users ORDER BY id`)
	if err != nil {
		t.Fatalf("select backfilled usernames: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var email, username string
		if err := rows.Scan(&email, &username); err != nil {
			t.Fatalf("scan backfilled username: %v", err)
		}
		// The address is stored as typed; compare case-insensitively because the
		// column is COLLATE NOCASE and the fixture deliberately mixes case.
		got[strings.ToLower(email)] = username
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate backfilled usernames: %v", err)
	}

	want := map[string]string{
		"alpha@example.com":   "alpha",
		"alice@example.com":   "alice",
		"alice@other.example": "alice-2", // the duplicate had to be displaced
		"x@example.com":       "user",    // too short to stand alone
		"y@example.com":       "user-2",
	}
	if len(got) != len(want) {
		t.Fatalf("backfilled %d rows, want %d: %v", len(got), len(want), got)
	}
	for email, username := range want {
		if got[email] != username {
			t.Errorf("username for %s = %q, want %q", email, got[email], username)
		}
	}
}

// TestBackfillIsIdempotent guards against a restart renaming accounts: the
// backfill only ever looks at rows whose username is empty, so a second Open
// must leave every value untouched.
func TestBackfillIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotent.db")

	raw, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	createLegacyUsersTable(t, raw)
	if _, err := raw.Exec(`
INSERT INTO users (email, nickname, password_hash, created_at, updated_at)
VALUES ('alice@example.com', 'Alice', 'h', 1, 1), ('alice@other.example', 'Other', 'h', 2, 2);
`); err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	before := usernamesByEmail(t, first)
	if err := first.Close(); err != nil {
		t.Fatalf("close first database: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer second.Close()
	after := usernamesByEmail(t, second)

	if len(before) != 2 || len(after) != 2 {
		t.Fatalf("expected two accounts, got before=%v after=%v", before, after)
	}
	for email, username := range before {
		// Non-empty is asserted here as well, so this test fails if the backfill
		// stops running entirely and not only if it renames something.
		if username == "" {
			t.Fatalf("%s came out of the first migration with no username", email)
		}
		if after[email] != username {
			t.Fatalf("a restart renamed %s from %q to %q", email, username, after[email])
		}
	}
}

// TestBackfillFillsTheColumnCompletely asserts that no account is left behind in
// the "exists but cannot sign in" state the backfill exists to prevent.
func TestBackfillFillsTheColumnCompletely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "complete.db")

	raw, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	createLegacyUsersTable(t, raw)
	if _, err := raw.Exec(`
INSERT INTO users (email, nickname, password_hash, created_at, updated_at) VALUES
    ('@example.com', '无本地部分', 'h', 1, 1),
    ('dot.dot@example.com', '点点', 'h', 2, 2),
    ('____@example.com', '下划线开头', 'h', 3, 3),
    ('a-b-c-guy@example.com', '带连字符', 'h', 4, 4);
`); err != nil {
		t.Fatalf("seed awkward legacy rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	var missing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE username IS NULL OR username = ''`).Scan(&missing); err != nil {
		t.Fatalf("count accounts without a username: %v", err)
	}
	if missing != 0 {
		t.Fatalf("%d account(s) still have no username after the backfill", missing)
	}

	// The derived names must also be usable sign-in identifiers, i.e. they have
	// to satisfy the same shape rule the service enforces. A name that starts
	// with a digit or an underscore would be rejected on the very next edit.
	rows, err := db.Query(`SELECT email, username FROM users`)
	if err != nil {
		t.Fatalf("select backfilled usernames: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var email, username string
		if err := rows.Scan(&email, &username); err != nil {
			t.Fatalf("scan backfilled username: %v", err)
		}
		head := username[0]
		if !(head >= 'a' && head <= 'z') {
			t.Errorf("backfilled username %q for %s does not start with a letter", username, email)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate backfilled usernames: %v", err)
	}
}

// createLegacyUsersTable builds the users table exactly as the previous version
// of the schema defined it — no username column. This is what makes the upgrade
// test meaningful: the column can only appear through addedColumns.
func createLegacyUsersTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    email         TEXT    NOT NULL COLLATE NOCASE UNIQUE,
    nickname      TEXT    NOT NULL DEFAULT '',
    password_hash TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);`); err != nil {
		t.Fatalf("create legacy users table: %v", err)
	}
}

func usernamesByEmail(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()

	result := map[string]string{}
	rows, err := db.Query(`SELECT email, username FROM users`)
	if err != nil {
		t.Fatalf("select usernames: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var email, username string
		if err := rows.Scan(&email, &username); err != nil {
			t.Fatalf("scan username: %v", err)
		}
		result[email] = username
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate usernames: %v", err)
	}
	return result
}

func TestPruneExpired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prune.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.Exec(`
INSERT INTO users (id, email, created_at, updated_at) VALUES (1, 'a@example.com', 1, 1);
INSERT INTO comment_tokens (user_id, token_hash, expires_at, created_at) VALUES (1, 'old', 1, 1), (1, 'new', 9999999999, 1);
INSERT INTO email_verification_codes (email, code_hash, expires_at, consumed_at, created_at)
VALUES ('a@example.com', 'h', 9999999999, 1, 1), ('a@example.com', 'h2', 1, NULL, 1);
`); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	if _, err := PruneExpired(ctx, db); err != nil {
		t.Fatalf("PruneExpired() error = %v", err)
	}

	for _, tc := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM comment_tokens`, 1},
		{`SELECT COUNT(*) FROM email_verification_codes`, 0},
	} {
		var got int
		if err := db.QueryRow(tc.query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		if got != tc.want {
			t.Fatalf("%s = %d, want %d", tc.query, got, tc.want)
		}
	}
}

// TestLegacyAdminSessionsTableIsGone guards the deliberate removal of the old
// admin session store: sessions covers both roles now, so nothing reads or
// writes admin_sessions and keeping the CREATE TABLE around only invited
// PruneExpired to fail on freshly created databases.
func TestLegacyAdminSessionsTableIsGone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-admin-sessions.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	// A fresh database must not contain the table at all, while the table that
	// replaced it must be there.
	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'admin_sessions'`).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("admin_sessions lookup error = %v, want sql.ErrNoRows (table must not exist)", err)
	}

	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`).Scan(&name); err != nil {
		t.Fatalf("sessions table is missing: %v", err)
	}
}

func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()

	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			pk           int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return false
}
