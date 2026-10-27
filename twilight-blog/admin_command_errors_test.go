package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/session"
)

func TestAdminSetPasswordFailureBranches(t *testing.T) {
	openDB := func(t *testing.T) *sql.DB {
		t.Helper()
		db, err := database.Open(filepath.Join(t.TempDir(), "admin.db"))
		if err != nil {
			t.Fatalf("open test database: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}

	cases := []struct {
		name  string
		patch func(t *testing.T)
	}{
		{name: "logging", patch: func(t *testing.T) {
			overrideCommandHook(t, &configureAdminLogging, func(string) error { return errors.New("no log") })
		}},
		{name: "load settings", patch: func(t *testing.T) {
			overrideCommandHook(t, &loadAdminSettings, func(string) (*config.Config, error) {
				return nil, errors.New("bad json")
			})
		}},
		{name: "open database", patch: func(t *testing.T) {
			overrideCommandHook(t, &openAdminDatabase, func(string) (*sql.DB, error) {
				return nil, errors.New("disk full")
			})
		}},
		{name: "migration", patch: func(t *testing.T) {
			db := openDB(t)
			overrideCommandHook(t, &openAdminDatabase, func(string) (*sql.DB, error) { return db, nil })
			overrideCommandHook(t, &migrateAdminCreds, func(context.Context, *sql.DB, *config.Config) error {
				return errors.New("migration failed")
			})
		}},
		{name: "save settings", patch: func(t *testing.T) {
			db := openDB(t)
			overrideCommandHook(t, &openAdminDatabase, func(string) (*sql.DB, error) { return db, nil })
			overrideCommandHook(t, &migrateAdminCreds, func(context.Context, *sql.DB, *config.Config) error { return nil })
			overrideCommandHook(t, &saveAdminSettings, func(*config.Config, string) error {
				return errors.New("read-only")
			})
		}},
		{name: "exists", patch: func(t *testing.T) {
			closed := openDB(t)
			_ = closed.Close()
			overrideCommandHook(t, &openAdminDatabase, func(string) (*sql.DB, error) { return closed, nil })
			overrideCommandHook(t, &migrateAdminCreds, func(context.Context, *sql.DB, *config.Config) error { return nil })
			overrideCommandHook(t, &saveAdminSettings, func(*config.Config, string) error { return nil })
		}},
		{name: "create admin", patch: func(t *testing.T) {
			db := openDB(t)
			if _, err := db.Exec(`CREATE TRIGGER fail_admin_insert BEFORE INSERT ON users
BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
				t.Fatalf("create trigger: %v", err)
			}
			overrideCommandHook(t, &openAdminDatabase, func(string) (*sql.DB, error) { return db, nil })
			overrideCommandHook(t, &migrateAdminCreds, func(context.Context, *sql.DB, *config.Config) error { return nil })
			overrideCommandHook(t, &saveAdminSettings, func(*config.Config, string) error { return nil })
		}},
		{name: "set password", patch: func(t *testing.T) {
			db := openDB(t)
			if _, err := db.Exec(`INSERT INTO users (email, username, nickname, password_hash, is_admin, created_at, updated_at)
VALUES ('', 'admin', 'Admin', 'hash', 1, 1, 1)`); err != nil {
				t.Fatalf("seed admin: %v", err)
			}
			if _, err := db.Exec(`CREATE TRIGGER fail_admin_update BEFORE UPDATE ON users
BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
				t.Fatalf("create trigger: %v", err)
			}
			overrideCommandHook(t, &openAdminDatabase, func(string) (*sql.DB, error) { return db, nil })
			overrideCommandHook(t, &migrateAdminCreds, func(context.Context, *sql.DB, *config.Config) error { return nil })
			overrideCommandHook(t, &saveAdminSettings, func(*config.Config, string) error { return nil })
		}},
		{name: "revoke sessions", patch: func(t *testing.T) {
			db := openDB(t)
			if _, err := db.Exec(`INSERT INTO users (email, username, nickname, password_hash, is_admin, created_at, updated_at)
VALUES ('', 'admin', 'Admin', 'hash', 1, 1, 1)`); err != nil {
				t.Fatalf("seed admin: %v", err)
			}
			overrideCommandHook(t, &openAdminDatabase, func(string) (*sql.DB, error) { return db, nil })
			overrideCommandHook(t, &migrateAdminCreds, func(context.Context, *sql.DB, *config.Config) error { return nil })
			overrideCommandHook(t, &saveAdminSettings, func(*config.Config, string) error { return nil })
			overrideCommandHook(t, &revokeAdminSessions, func(*session.Store) error {
				return errors.New("revoke failed")
			})
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			overrideCommandHook(t, &loadAdminSettings, func(string) (*config.Config, error) {
				return &config.Config{}, nil
			})
			overrideCommandHook(t, &saveAdminSettings, func(*config.Config, string) error { return nil })
			tc.patch(t)
			feedStdin(t, "Password1\nPassword1\n")
			if code := runAdminSetPassword(nil); code != exitFailure {
				t.Fatalf("exit code = %d, want %d", code, exitFailure)
			}
		})
	}
}

// TestAdminSetPasswordWithoutSettings pins the "not installed yet" path. Load
// reports ErrNotInstalled by returning a nil config, and the command has to
// carry on with an empty one — otherwise a fresh site could never have its
// first password set from the CLI.
func TestAdminSetPasswordWithoutSettings(t *testing.T) {
	overrideCommandHook(t, &loadAdminSettings, func(string) (*config.Config, error) {
		return nil, nil
	})
	overrideCommandHook(t, &openAdminDatabase, func(string) (*sql.DB, error) {
		return nil, errors.New("disk full")
	})
	captureStreams(t, "")
	feedStdin(t, "Password1\nPassword1\n")

	if code := runAdminSetPassword(nil); code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
}

// TestParseAdminIdentityArgsVariants covers the four flag spellings and the
// rejections. They are separate from the command-level tests because an
// argument typo should be diagnosable without a password prompt in the way.
func TestParseAdminIdentityArgsVariants(t *testing.T) {
	t.Run("equals forms", func(t *testing.T) {
		username, nickname, err := parseAdminIdentityArgs([]string{"-username=alice", "--nickname=Alice"})
		if err != nil {
			t.Fatalf("parseAdminIdentityArgs: %v", err)
		}
		if username != "alice" || nickname != "Alice" {
			t.Fatalf("got %q/%q, want alice/Alice", username, nickname)
		}

		username, nickname, err = parseAdminIdentityArgs([]string{"--username=bob", "-nickname=Bob"})
		if err != nil {
			t.Fatalf("parseAdminIdentityArgs: %v", err)
		}
		if username != "bob" || nickname != "Bob" {
			t.Fatalf("got %q/%q, want bob/Bob", username, nickname)
		}
	})

	t.Run("nickname alone keeps the default username", func(t *testing.T) {
		username, nickname, err := parseAdminIdentityArgs([]string{"--nickname=Editor"})
		if err != nil {
			t.Fatalf("parseAdminIdentityArgs: %v", err)
		}
		if username != config.DefaultAdminName || nickname != "Editor" {
			t.Fatalf("got %q/%q, want %q/Editor", username, nickname, config.DefaultAdminName)
		}
	})

	t.Run("missing nickname value", func(t *testing.T) {
		if _, _, err := parseAdminIdentityArgs([]string{"--nickname"}); err == nil {
			t.Fatal("a trailing --nickname must be rejected")
		}
	})

	t.Run("empty value", func(t *testing.T) {
		if _, _, err := parseAdminIdentityArgs([]string{"--nickname=   "}); err == nil {
			t.Fatal("a blank nickname must be rejected")
		}
	})

	t.Run("duplicate flag", func(t *testing.T) {
		if _, _, err := parseAdminIdentityArgs([]string{"-username=a", "-username=b"}); err == nil {
			t.Fatal("specifying a flag twice must be rejected")
		}
	})

	t.Run("unknown argument", func(t *testing.T) {
		if _, _, err := parseAdminIdentityArgs([]string{"--email=x@example.com"}); err == nil {
			t.Fatal("an unknown flag must be rejected")
		}
	})
}

// TestCommandStreamsDefaultToTheProcessStreams pins the non-test wiring: the
// hooks exist so tests can substitute them, and their defaults must still be
// the real stdin/stdout/stderr.
func TestCommandStreamsDefaultToTheProcessStreams(t *testing.T) {
	if stdin() == nil {
		t.Fatal("the default stdin must not be nil")
	}
	if stdout() == nil {
		t.Fatal("the default stdout must not be nil")
	}
	if stderr() == nil {
		t.Fatal("the default stderr must not be nil")
	}
}
