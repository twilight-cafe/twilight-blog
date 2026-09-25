package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAPIKeyBlankExplicitEnvNameUsesDefault(t *testing.T) {
	t.Setenv(MailAPIKeyEnvVar, "from-default")
	key, source := (&MailConfig{APIKey: "env:"}).ResolveAPIKey()
	if key != "from-default" {
		t.Fatalf("ResolveAPIKey(blank env name) = (%q, %q), want default env value", key, source)
	}
}

func TestLoadRemainingValidationAndMigrationBranches(t *testing.T) {
	t.Run("read error", func(t *testing.T) {
		if _, err := Load(t.TempDir()); err == nil {
			t.Fatal("Load must report a non-NotExist read error")
		}
	})

	t.Run("config json parse", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write settings: %v", err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("Load must report a settings JSON parse error")
		}
	})

	t.Run("admin identity parse", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte(`{"admin_name":123}`), 0o600); err != nil {
			t.Fatalf("write settings: %v", err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("Load must report an admin identity parse error")
		}
	})

	cases := []struct {
		name string
		body map[string]any
	}{
		{"cookie secure", map[string]any{"cookie_secure": "sometimes"}},
		{"media dir", map[string]any{"media_dir": "../escape"}},
		{"log level", map[string]any{"log_level": "verbose"}},
		{"port", map[string]any{"port": 70000}},
		{"max upload", map[string]any{"max_upload_size": -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			writeSettings(t, path, tc.body)
			if _, err := Load(path); err == nil {
				t.Fatalf("Load accepted %s=%v", tc.name, tc.body)
			}
		})
	}

	t.Run("password hash wins over plaintext and triggers save", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		writeSettings(t, path, map[string]any{
			"password_hash": testPasswordHash,
			"password":      "OldPass123",
		})
		if _, err := Load(path); err != nil {
			t.Fatalf("Load migration error = %v", err)
		}
	})

	t.Run("weak plaintext password", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		writeSettings(t, path, map[string]any{"password": "short"})
		if _, err := Load(path); err == nil {
			t.Fatal("Load must report a SetPassword validation error")
		}
	})

	t.Run("migration save", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		writeSettings(t, path, map[string]any{"admin_name": "oldname"})

		original := writeFileAtomicFn
		writeFileAtomicFn = func(string, []byte, os.FileMode) error { return errors.New("save failed") }
		t.Cleanup(func() { writeFileAtomicFn = original })

		if _, err := Load(path); err == nil {
			t.Fatal("Load must report a migration Save error")
		}
	})
}

func TestAdminAccessorDefaultFallbacks(t *testing.T) {
	cfg := &Config{}
	if got := cfg.AdminLoginName(); got != DefaultAdminName {
		t.Fatalf("AdminLoginName() = %q, want %q", got, DefaultAdminName)
	}
	if got := cfg.AdminDisplayName(); got != DefaultAdminName {
		t.Fatalf("AdminDisplayName() = %q, want %q", got, DefaultAdminName)
	}
}

func TestValidateCookieSecureRejectsUnknown(t *testing.T) {
	if err := validateCookieSecure("maybe"); err == nil {
		t.Fatal("validateCookieSecure must reject an unknown value")
	}
}

func TestValidatePasswordStrengthFailureBranches(t *testing.T) {
	if err := ValidatePasswordStrength("   "); err == nil {
		t.Fatal("empty password must be rejected")
	}
	if err := ValidatePasswordStrength(strings.Repeat("a1", MaxPasswordBytes/2+1)); err == nil {
		t.Fatal("password over the byte limit must be rejected")
	}
}

func TestSetPasswordRejectsWeakAndReportsHashFailure(t *testing.T) {
	cfg := &Config{}
	if err := cfg.SetPassword("short"); err == nil {
		t.Fatal("SetPassword must reject a weak password")
	}

	original := bcryptGenerate
	bcryptGenerate = func([]byte, int) ([]byte, error) { return nil, errors.New("bcrypt failed") }
	t.Cleanup(func() { bcryptGenerate = original })

	if err := cfg.SetPassword("ValidPass1"); err == nil {
		t.Fatal("SetPassword must report a bcrypt generation error")
	}
}

func TestEffectiveRateLimitNilConfigUsesDefaults(t *testing.T) {
	cfg := (&Config{}).EffectiveRateLimit()
	if cfg.CommentPerMinute <= 0 {
		t.Fatalf("EffectiveRateLimit nil fallback = %+v", cfg)
	}
}

func TestNormalizeStringListSkipsEmptyAndDuplicates(t *testing.T) {
	got := normalizeStringList([]string{"", " a ", "a", " b ", "  "})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("normalizeStringList = %v, want [a b]", got)
	}
}

func TestSaveReportsMarshalAndAtomicWriteErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{PasswordHash: testPasswordHash, settingsPath: filepath.Join(dir, "settings.json")}
	cfg.ApplyDefaults()

	originalMarshal := marshalConfig
	marshalConfig = func(any, string, string) ([]byte, error) { return nil, errors.New("marshal failed") }
	t.Cleanup(func() { marshalConfig = originalMarshal })
	if err := cfg.Save(filepath.Join(dir, "settings.json")); err == nil {
		t.Fatal("Save must report a marshal error")
	}
	marshalConfig = originalMarshal

	originalWrite := writeFileAtomicFn
	writeFileAtomicFn = func(string, []byte, os.FileMode) error { return errors.New("write failed") }
	t.Cleanup(func() { writeFileAtomicFn = originalWrite })
	if err := cfg.Save(filepath.Join(dir, "settings.json")); err == nil {
		t.Fatal("Save must report an atomic write error")
	}
	writeFileAtomicFn = originalWrite

	// The path's parent does not exist, so the real atomic writer fails at CreateTemp.
	missing := filepath.Join(dir, "missing", "settings.json")
	if err := cfg.Save(missing); err == nil {
		t.Fatal("Save must report a writeFileAtomic error for a missing directory")
	}
}

func TestWriteFileAtomicFailureBranches(t *testing.T) {
	t.Run("create temp", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", "settings.json")
		if err := writeFileAtomic(path, []byte("x"), 0o600); err == nil {
			t.Fatal("writeFileAtomic must report a CreateTemp error")
		}
	})

	t.Run("chmod", func(t *testing.T) {
		original := chmodTemp
		chmodTemp = func(*os.File, os.FileMode) error { return errors.New("chmod failed") }
		t.Cleanup(func() { chmodTemp = original })

		if err := writeFileAtomic(filepath.Join(t.TempDir(), "settings.json"), []byte("x"), 0o600); err == nil {
			t.Fatal("writeFileAtomic must report a Chmod error")
		}
	})

	t.Run("write", func(t *testing.T) {
		original := writeTemp
		writeTemp = func(*os.File, []byte) (int, error) { return 0, errors.New("write failed") }
		t.Cleanup(func() { writeTemp = original })

		if err := writeFileAtomic(filepath.Join(t.TempDir(), "settings.json"), []byte("x"), 0o600); err == nil {
			t.Fatal("writeFileAtomic must report a Write error")
		}
	})

	t.Run("sync", func(t *testing.T) {
		original := syncTemp
		syncTemp = func(*os.File) error { return errors.New("sync failed") }
		t.Cleanup(func() { syncTemp = original })

		if err := writeFileAtomic(filepath.Join(t.TempDir(), "settings.json"), []byte("x"), 0o600); err == nil {
			t.Fatal("writeFileAtomic must report a Sync error")
		}
	})

	t.Run("close", func(t *testing.T) {
		original := closeTemp
		closeTemp = func(f *os.File) error {
			_ = f.Close()
			return errors.New("close failed")
		}
		t.Cleanup(func() { closeTemp = original })

		if err := writeFileAtomic(filepath.Join(t.TempDir(), "settings.json"), []byte("x"), 0o600); err == nil {
			t.Fatal("writeFileAtomic must report a Close error")
		}
	})

	t.Run("rename", func(t *testing.T) {
		original := renameSettings
		renameSettings = func(string, string) error { return errors.New("rename failed") }
		t.Cleanup(func() { renameSettings = original })

		if err := writeFileAtomic(filepath.Join(t.TempDir(), "settings.json"), []byte("x"), 0o600); err == nil {
			t.Fatal("writeFileAtomic must report a Rename error")
		}
	})
}

func TestResolveAPIKeyUnsetDefaultEnvironment(t *testing.T) {
	t.Setenv(MailAPIKeyEnvVar, "")
	key, source := (&MailConfig{}).ResolveAPIKey()
	if key != "" || source != "unset" {
		t.Fatalf("ResolveAPIKey with empty default env = (%q, %q), want unset", key, source)
	}
}

func TestRateLimitConfigIsEnabled(t *testing.T) {
	if !(RateLimitConfig{}).IsEnabled() {
		t.Fatal("a missing Enabled setting must default to enabled")
	}
	off := false
	if (RateLimitConfig{Enabled: &off}).IsEnabled() {
		t.Fatal("Enabled=false must report disabled")
	}
	on := true
	if !(RateLimitConfig{Enabled: &on}).IsEnabled() {
		t.Fatal("Enabled=true must report enabled")
	}
}

func TestLoadReturnsErrNotInstalledForMissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Load missing file error = %v, want ErrNotInstalled", err)
	}
	if cfg == nil {
		t.Fatal("Load missing file must still return a usable Config")
	}
}

func TestValidatePasswordStrengthRequiresLetterAndDigit(t *testing.T) {
	if err := ValidatePasswordStrength("!!!!!!!!"); err == nil {
		t.Fatal("a password without letters or digits must be rejected")
	}
}

func TestEffectiveRateLimitWithConfiguredValue(t *testing.T) {
	cfg := (&Config{RateLimit: &RateLimitConfig{CommentPerMinute: 7}}).EffectiveRateLimit()
	if cfg.CommentPerMinute != 7 {
		t.Fatalf("EffectiveRateLimit configured value = %d, want 7", cfg.CommentPerMinute)
	}
	if cfg.SearchPerMinute <= 0 {
		t.Fatalf("EffectiveRateLimit did not apply defaults: %+v", cfg)
	}
}
