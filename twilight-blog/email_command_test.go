package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/mail"
	"blog-engine/internal/model"
)

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{
			name: "short flag separate value",
			args: []string{"-target", "abc@def.com"},
			want: "abc@def.com",
		},
		{
			name: "long flag separate value",
			args: []string{"--target", "abc@def.com"},
			want: "abc@def.com",
		},
		{
			name: "short flag equals value",
			args: []string{"-target=abc@def.com"},
			want: "abc@def.com",
		},
		{
			name: "long flag equals value",
			args: []string{"--target=abc@def.com"},
			want: "abc@def.com",
		},
		{
			name:    "missing value",
			args:    []string{"-target"},
			wantErr: true,
		},
		{
			name:    "missing target",
			args:    []string{},
			wantErr: true,
		},
		{
			name:    "empty target",
			args:    []string{"-target", "   "},
			wantErr: true,
		},
		{
			name:    "invalid email",
			args:    []string{"-target", "not-an-email"},
			wantErr: true,
		},
		{
			name:    "unknown argument",
			args:    []string{"-target", "abc@def.com", "--cc", "x@y.com"},
			wantErr: true,
		},
		{
			name:    "duplicate target",
			args:    []string{"-target", "abc@def.com", "-target", "other@def.com"},
			wantErr: true,
		},
		{
			name: "display name is normalised",
			args: []string{"--target=Someone <someone@example.com>"},
			want: "someone@example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTarget(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseTarget(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("parseTarget(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

type recordingMailer struct {
	to      string
	subject string
	body    string
	err     error
}

func (m *recordingMailer) Send(to, subject, body string) error {
	m.to, m.subject, m.body = to, subject, body
	return m.err
}

// withMailerForCommand swaps the mailer factory for one test and restores it.
func withMailerForCommand(t *testing.T, mailer mail.Mailer, err error) *recordingMailer {
	t.Helper()
	recorder, _ := mailer.(*recordingMailer)
	previous := newMailerForCommand
	newMailerForCommand = func(*config.Config) (mail.Mailer, error) { return mailer, err }
	t.Cleanup(func() { newMailerForCommand = previous })
	return recorder
}

func TestRunEmailCommandAndRunEmailTest(t *testing.T) {
	t.Run("usage errors", func(t *testing.T) {
		if err := runEmailCommand(nil); err == nil {
			t.Fatal("missing subcommand should fail")
		}
		if err := runEmailCommand([]string{"nope"}); err == nil {
			t.Fatal("unknown subcommand should fail")
		}
	})

	t.Run("success sends and records the email", func(t *testing.T) {
		inTempWorkdir(t)
		seedSite(t, nil)
		if err := os.WriteFile(settingsPath, []byte(`{"media_dir":"media","mail":{"from":"from@example.com"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(config.MailAPIKeyEnvVar, "re_test_key")
		recorder := withMailerForCommand(t, &recordingMailer{}, nil)
		out, _ := captureStreams(t, "")
		if err := runEmailTest([]string{"-target", "to@example.com"}); err != nil {
			t.Fatalf("runEmailTest: %v", err)
		}
		if recorder.to != "to@example.com" || recorder.subject == "" || recorder.body == "" {
			t.Fatalf("mailer got (%q, %q, %q)", recorder.to, recorder.subject, recorder.body)
		}
		if !strings.Contains(out.String(), "Test email sent to to@example.com") {
			t.Fatalf("stdout = %q", out.String())
		}
		withDB(t, func(db *sql.DB) {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM emails`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("emails = %d, want 1", count)
			}
		})
	})

	t.Run("missing from is rejected before sending", func(t *testing.T) {
		inTempWorkdir(t)
		seedSite(t, nil)
		recorder := withMailerForCommand(t, &recordingMailer{}, nil)
		if err := runEmailTest([]string{"-target", "to@example.com"}); err == nil {
			t.Fatal("missing mail.from should fail")
		}
		if recorder.to != "" {
			t.Fatal("mailer must not be called when mail.from is missing")
		}
	})

	t.Run("missing api key is rejected before sending", func(t *testing.T) {
		inTempWorkdir(t)
		seedSite(t, nil)
		if err := os.WriteFile(settingsPath, []byte(`{"media_dir":"media","mail":{"from":"from@example.com"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		recorder := withMailerForCommand(t, &recordingMailer{}, nil)
		if err := runEmailTest([]string{"-target", "to@example.com"}); err == nil {
			t.Fatal("missing API key should fail")
		}
		if recorder.to != "" {
			t.Fatal("mailer must not be called when the API key is missing")
		}
	})

	t.Run("mailer construction errors are surfaced", func(t *testing.T) {
		inTempWorkdir(t)
		seedSite(t, nil)
		if err := os.WriteFile(settingsPath, []byte(`{"media_dir":"media","mail":{"from":"from@example.com"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(config.MailAPIKeyEnvVar, "re_test_key")
		withMailerForCommand(t, nil, errors.New("construct failed"))
		if err := runEmailTest([]string{"-target", "to@example.com"}); err == nil {
			t.Fatal("mailer construction error should be surfaced")
		}
	})

	t.Run("send errors are wrapped", func(t *testing.T) {
		inTempWorkdir(t)
		seedSite(t, nil)
		if err := os.WriteFile(settingsPath, []byte(`{"media_dir":"media","mail":{"from":"from@example.com"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(config.MailAPIKeyEnvVar, "re_test_key")
		withMailerForCommand(t, &recordingMailer{err: errors.New("smtp down")}, nil)
		if err := runEmailTest([]string{"-target", "to@example.com"}); err == nil || !strings.Contains(err.Error(), "send test email") {
			t.Fatalf("send error = %v", err)
		}
	})

	t.Run("loadInstalledConfig returns an installed database", func(t *testing.T) {
		inTempWorkdir(t)
		seedSite(t, nil)
		cfg, db, err := loadInstalledConfig()
		if err != nil {
			t.Fatalf("loadInstalledConfig: %v", err)
		}
		defer db.Close()
		if cfg == nil {
			t.Fatal("nil config")
		}
		if exists, err := model.NewSQLiteAdminRepository(db).Exists(context.Background()); err != nil || !exists {
			t.Fatalf("admin exists = (%v, %v)", exists, err)
		}
	})

	t.Run("newMailer and newEmailService", func(t *testing.T) {
		if _, err := newMailer(&config.Config{}); err != nil {
			t.Fatalf("newMailer(empty): %v", err)
		}
		cfg := &config.Config{Mail: &config.MailConfig{From: "f@example.com", APIKey: "env:" + config.MailAPIKeyEnvVar}}
		if _, err := newMailer(cfg); err != nil {
			t.Fatalf("newMailer(configured): %v", err)
		}
		db := openTestDBForMain(t)
		defer db.Close()
		if svc := newEmailService(cfg, db, &recordingMailer{}); svc == nil {
			t.Fatal("newEmailService returned nil")
		}
	})
}

func openTestDBForMain(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "email.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db
}
