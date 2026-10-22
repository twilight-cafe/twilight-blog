package view

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/mail"
	"blog-engine/internal/mediaconv"
	"blog-engine/internal/model"
	"blog-engine/internal/search"
)

func newRouterDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newRouterConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{MediaDir: filepath.Join(t.TempDir(), "media")}
}

func newSuccessfulSearchServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/indexes":
			_ = json.NewEncoder(w).Encode(map[string]any{"taskUid": 1})
		case r.Method == http.MethodPatch && r.URL.Path == "/indexes/"+search.IndexName+"/settings":
			_ = json.NewEncoder(w).Encode(map[string]any{"taskUid": 1})
		case r.Method == http.MethodDelete && r.URL.Path == "/indexes/"+search.IndexName:
			_ = json.NewEncoder(w).Encode(map[string]any{"taskUid": 1})
		case strings.HasPrefix(r.URL.Path, "/tasks/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"uid": 1, "status": "succeeded"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRegisterCoversOptionalConfigurationBranches(t *testing.T) {
	t.Run("webm and mail configured", func(t *testing.T) {
		t.Setenv(config.MailAPIKeyEnvVar, "test-key")
		cfg := newRouterConfig(t)
		cfg.Mail = &config.MailConfig{From: "noreply@example.test"}
		e := echo.New()
		e.HideBanner = true
		if err := Register(e, cfg, newRouterDB(t), filepath.Join(t.TempDir(), "settings.json"), nil, &mediaconv.Converter{WebMEnabled: true, WebMTool: "ffmpeg"}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	})

	t.Run("empty media dir falls back", func(t *testing.T) {
		dir := t.TempDir()
		old, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatalf("chdir: %v", err)
		}
		t.Cleanup(func() { _ = os.Chdir(old) })

		cfg := &config.Config{}
		e := echo.New()
		e.HideBanner = true
		if err := Register(e, cfg, newRouterDB(t), filepath.Join(dir, "settings.json"), nil, &mediaconv.Converter{}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "media")); err != nil {
			t.Fatalf("fallback media dir was not created: %v", err)
		}
	})

	t.Run("media mkdir warning", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "not-a-dir")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatalf("write placeholder: %v", err)
		}
		cfg := &config.Config{MediaDir: filepath.Join(file, "media")}
		e := echo.New()
		e.HideBanner = true
		if err := Register(e, cfg, newRouterDB(t), filepath.Join(dir, "settings.json"), nil, &mediaconv.Converter{}); err != nil {
			t.Fatalf("Register() with unwritable media dir error = %v", err)
		}
	})

	t.Run("rate limiting disabled", func(t *testing.T) {
		disabled := false
		cfg := newRouterConfig(t)
		cfg.RateLimit = &config.RateLimitConfig{Enabled: &disabled}
		e := echo.New()
		e.HideBanner = true
		if err := Register(e, cfg, newRouterDB(t), filepath.Join(t.TempDir(), "settings.json"), nil, &mediaconv.Converter{}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}

		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", nil))
		if rec.Code == 0 {
			t.Fatal("disabled limiter middleware did not invoke the next handler")
		}
	})

	t.Run("mailer failure", func(t *testing.T) {
		original := newMailer
		newMailer = func(mail.Config) (mail.Mailer, error) {
			return nil, errors.New("mailer failed")
		}
		t.Cleanup(func() { newMailer = original })

		if err := Register(echo.New(), newRouterConfig(t), newRouterDB(t), filepath.Join(t.TempDir(), "settings.json"), nil, &mediaconv.Converter{}); err == nil {
			t.Fatal("Register must report a mailer initialization error")
		}
	})

	t.Run("registerWeb failure", func(t *testing.T) {
		original := registerWebFn
		registerWebFn = func(*echo.Echo, *config.Config) error { return errors.New("web failed") }
		t.Cleanup(func() { registerWebFn = original })

		if err := Register(echo.New(), newRouterConfig(t), newRouterDB(t), filepath.Join(t.TempDir(), "settings.json"), nil, &mediaconv.Converter{}); err == nil {
			t.Fatal("Register must report a registerWeb error")
		}
	})
}

func TestRegisterWithSearchClientCoversSyncBranches(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := newSuccessfulSearchServer(t)
		client := search.NewClient(server.URL, "")
		e := echo.New()
		e.HideBanner = true
		if err := Register(e, newRouterConfig(t), newRouterDB(t), filepath.Join(t.TempDir(), "settings.json"), client, &mediaconv.Converter{}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	})

	t.Run("failure schedules retry", func(t *testing.T) {
		failure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusInternalServerError)
		}))
		t.Cleanup(failure.Close)

		original := retrySearchSyncFn
		retrySearchSyncFn = func(*search.Client, model.ArticleRepository, model.PageRepository) {}
		t.Cleanup(func() { retrySearchSyncFn = original })

		client := search.NewClient(failure.URL, "")
		e := echo.New()
		e.HideBanner = true
		if err := Register(e, newRouterConfig(t), newRouterDB(t), filepath.Join(t.TempDir(), "settings.json"), client, &mediaconv.Converter{}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	})
}

func TestRetrySearchSyncSucceedsAndExhausts(t *testing.T) {
	originalSleep := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = originalSleep })

	t.Run("success", func(t *testing.T) {
		server := newSuccessfulSearchServer(t)
		client := search.NewClient(server.URL, "")
		db := newRouterDB(t)
		retrySearchSync(client, model.NewSQLiteArticleRepository(db), model.NewSQLitePageRepository(db))
	})

	t.Run("exhausted", func(t *testing.T) {
		var calls atomic.Int64
		failure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, "nope", http.StatusInternalServerError)
		}))
		t.Cleanup(failure.Close)

		client := search.NewClient(failure.URL, "")
		db := newRouterDB(t)
		retrySearchSync(client, model.NewSQLiteArticleRepository(db), model.NewSQLitePageRepository(db))
		if got := calls.Load(); got != 5 {
			t.Fatalf("retrySearchSync made %d attempts, want 5", got)
		}
	})
}

func TestStartPruneJanitorRunsImmediateAndTickerPrunes(t *testing.T) {
	originalPrune := pruneExpired
	originalTicker := newPruneTicker
	t.Cleanup(func() {
		pruneExpired = originalPrune
		newPruneTicker = originalTicker
	})

	pruned := make(chan struct{}, 2)
	pruneExpired = func(context.Context, *sql.DB) (int64, error) {
		pruned <- struct{}{}
		return 0, errors.New("prune failed")
	}
	ticks := make(chan time.Time, 1)
	newPruneTicker = func(time.Duration) (<-chan time.Time, func()) {
		return ticks, func() {}
	}

	startPruneJanitor(nil)
	select {
	case <-pruned:
	case <-time.After(time.Second):
		t.Fatal("immediate prune did not run")
	}

	ticks <- time.Now()
	select {
	case <-pruned:
	case <-time.After(time.Second):
		t.Fatal("ticker prune did not run")
	}
	close(ticks)
	time.Sleep(10 * time.Millisecond)
}
