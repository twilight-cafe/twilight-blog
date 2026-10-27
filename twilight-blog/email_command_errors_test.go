package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/mail"
)

func overrideCommandHook[T any](t *testing.T, target *T, value T) {
	t.Helper()
	original := *target
	*target = value
	t.Cleanup(func() { *target = original })
}

func TestLoadInstalledConfigErrors(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "email.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// 大多数分支都不需要真的写 settings.json；个别用例会覆盖成失败。
	overrideCommandHook(t, &saveEmailSettings, func(*config.Config, string) error { return nil })

	cases := []struct {
		name  string
		patch func(t *testing.T)
	}{
		{name: "default logging", patch: func(t *testing.T) {
			overrideCommandHook(t, &configureEmailLogging, func(string) error { return errors.New("no log") })
		}},
		{name: "load settings", patch: func(t *testing.T) {
			overrideCommandHook(t, &loadEmailSettings, func(string) (*config.Config, error) {
				return nil, errors.New("bad json")
			})
		}},
		{name: "not installed yet", patch: func(t *testing.T) {
			// config.Load 用 nil, nil 表示「还没有 settings.json」。
			overrideCommandHook(t, &loadEmailSettings, func(string) (*config.Config, error) {
				return nil, nil
			})
			overrideCommandHook(t, &openEmailDatabase, func(string) (*sql.DB, error) {
				return nil, errors.New("disk full")
			})
		}},
		{name: "configure level", patch: func(t *testing.T) {
			calls := 0
			overrideCommandHook(t, &configureEmailLogging, func(string) error {
				calls++
				if calls >= 2 {
					return errors.New("bad level")
				}
				return nil
			})
		}},
		{name: "sync settings", patch: func(t *testing.T) {
			overrideCommandHook(t, &saveEmailSettings, func(*config.Config, string) error {
				return errors.New("read-only")
			})
		}},
		{name: "open database", patch: func(t *testing.T) {
			overrideCommandHook(t, &openEmailDatabase, func(string) (*sql.DB, error) {
				return nil, errors.New("disk full")
			})
		}},
		{name: "migrate", patch: func(t *testing.T) {
			overrideCommandHook(t, &openEmailDatabase, func(string) (*sql.DB, error) { return db, nil })
			overrideCommandHook(t, &migrateEmailAdmin, func(context.Context, *sql.DB, *config.Config) error {
				return errors.New("migration failed")
			})
		}},
		{name: "second save", patch: func(t *testing.T) {
			overrideCommandHook(t, &openEmailDatabase, func(string) (*sql.DB, error) { return db, nil })
			overrideCommandHook(t, &migrateEmailAdmin, func(context.Context, *sql.DB, *config.Config) error { return nil })
			calls := 0
			overrideCommandHook(t, &saveEmailSettings, func(*config.Config, string) error {
				calls++
				if calls >= 2 {
					return errors.New("read-only")
				}
				return nil
			})
		}},
		{name: "exists", patch: func(t *testing.T) {
			overrideCommandHook(t, &openEmailDatabase, func(string) (*sql.DB, error) { return db, nil })
			overrideCommandHook(t, &migrateEmailAdmin, func(context.Context, *sql.DB, *config.Config) error { return nil })
			overrideCommandHook(t, &emailAdminExists, func(*sql.DB) (bool, error) {
				return false, errors.New("db down")
			})
		}},
		{name: "not installed", patch: func(t *testing.T) {
			overrideCommandHook(t, &openEmailDatabase, func(string) (*sql.DB, error) { return db, nil })
			overrideCommandHook(t, &migrateEmailAdmin, func(context.Context, *sql.DB, *config.Config) error { return nil })
			overrideCommandHook(t, &emailAdminExists, func(*sql.DB) (bool, error) { return false, nil })
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.patch(t)
			if _, _, err := loadInstalledConfig(); err == nil {
				t.Fatal("a failing step must surface")
			}
		})
	}
}

// TestRunEmailCommandDispatchesTest covers the "test" arm of the subcommand
// switch. It deliberately stops at argument parsing so no message is sent.
func TestRunEmailCommandDispatchesTest(t *testing.T) {
	if err := runEmailCommand([]string{"test"}); err == nil {
		t.Fatal("a missing -target must surface through the dispatcher")
	}
}

// TestNewMailerForCommandDefault builds the real mailer from an empty config.
// The default is otherwise shadowed by every test that swaps the hook; this
// pins that it constructs a usable mailer rather than, say, returning nil.
func TestNewMailerForCommandDefault(t *testing.T) {
	mailer, err := newMailerForCommand(&config.Config{})
	if err != nil {
		t.Fatalf("building the default mailer: %v", err)
	}
	if mailer == nil {
		t.Fatal("the default mailer must not be nil")
	}
}

func TestRunEmailTestMailerFailure(t *testing.T) {
	overrideCommandHook(t, &loadEmailSettings, func(string) (*config.Config, error) {
		cfg := &config.Config{}
		cfg.Mail = &config.MailConfig{From: "blog@example.com"}
		return cfg, nil
	})
	overrideCommandHook(t, &openEmailDatabase, func(string) (*sql.DB, error) {
		return database.Open(filepath.Join(t.TempDir(), "mailer.db"))
	})
	overrideCommandHook(t, &migrateEmailAdmin, func(context.Context, *sql.DB, *config.Config) error { return nil })
	overrideCommandHook(t, &emailAdminExists, func(*sql.DB) (bool, error) { return true, nil })
	overrideCommandHook(t, &newMailerForCommand, func(*config.Config) (mail.Mailer, error) {
		return nil, errors.New("no mailer")
	})
	t.Setenv(config.MailAPIKeyEnvVar, "key")
	if err := runEmailTest([]string{"-target", "to@example.com"}); err == nil {
		t.Fatal("a failing mailer constructor must surface")
	}
}
