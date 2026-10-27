package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/translate"
)

func overrideListModels(t *testing.T, fn func(context.Context, translate.Config) ([]string, error)) {
	t.Helper()
	original := listModelsFn
	listModelsFn = fn
	t.Cleanup(func() { listModelsFn = original })
}

// TestTranslateHandlerListModels 覆盖模型查询的请求解析、已保存值回退与响应形状。
func TestTranslateHandlerListModels(t *testing.T) {
	cfg := &config.Config{}
	configureTranslate(cfg)
	h := NewTranslateHandler(cfg, filepath.Join(t.TempDir(), "settings.json"))

	t.Run("explicit url and key", func(t *testing.T) {
		var seen translate.Config
		overrideListModels(t, func(_ context.Context, got translate.Config) ([]string, error) {
			seen = got
			return []string{"gpt-4o", "gpt-4o-mini"}, nil
		})

		c := newTranslateContext(t, http.MethodPost, "/api/admin/translate/models", map[string]string{
			"url": "https://other.example.com/v1", "api_key": "other-key",
		})
		if err := h.ListModels(c.ctx); err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if c.rec.Code != http.StatusOK {
			t.Fatalf("status = %d", c.rec.Code)
		}
		var body struct {
			Models []string `json:"models"`
		}
		if err := json.Unmarshal(c.rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Models) != 2 || body.Models[0] != "gpt-4o" {
			t.Fatalf("models = %v", body.Models)
		}
		if seen.URL != "https://other.example.com/v1" || seen.APIKey != "other-key" {
			t.Fatalf("forwarded config = %+v", seen)
		}
		if seen.Timeout.Seconds() != 90 {
			t.Fatalf("timeout = %v", seen.Timeout)
		}
	})

	t.Run("falls back to saved url and key", func(t *testing.T) {
		var seen translate.Config
		overrideListModels(t, func(_ context.Context, got translate.Config) ([]string, error) {
			seen = got
			return nil, nil
		})

		c := newTranslateContext(t, http.MethodPost, "/api/admin/translate/models", map[string]string{})
		if err := h.ListModels(c.ctx); err != nil {
			t.Fatalf("ListModels: %v", err)
		}
		if seen.URL != "https://api.example.com/v1" || seen.APIKey != "secret-key" {
			t.Fatalf("forwarded config = %+v", seen)
		}
		// nil 列表按空数组输出，前端不会拿到 null。
		if !strings.Contains(c.rec.Body.String(), `"models":[]`) {
			t.Fatalf("body = %s", c.rec.Body.String())
		}
	})

	t.Run("missing url", func(t *testing.T) {
		empty := NewTranslateHandler(&config.Config{}, filepath.Join(t.TempDir(), "settings.json"))
		c := newTranslateContext(t, http.MethodPost, "/api/admin/translate/models", map[string]string{})
		err := empty.ListModels(c.ctx)
		if err == nil || !strings.Contains(err.Error(), "url is required") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("invalid url", func(t *testing.T) {
		c := newTranslateContext(t, http.MethodPost, "/api/admin/translate/models", map[string]string{"url": "ftp://x"})
		if err := h.ListModels(c.ctx); err == nil {
			t.Fatal("invalid url must be rejected")
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/translate/models", strings.NewReader("{"))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e := echo.New()
		if err := h.ListModels(e.NewContext(req, rec)); err == nil {
			t.Fatal("malformed body must be rejected")
		}
	})

	statuses := []struct {
		name string
		err  error
		want int
	}{
		{name: "not configured", err: translate.ErrNotConfigured, want: http.StatusServiceUnavailable},
		{name: "timeout", err: translate.ErrTimeout, want: http.StatusGatewayTimeout},
		{name: "invalid prompt", err: translate.ErrInvalidPrompt, want: http.StatusServiceUnavailable},
		{name: "invalid output", err: translate.ErrInvalidOutput, want: http.StatusBadGateway},
		{name: "upstream", err: &translate.UpstreamError{StatusCode: 401}, want: http.StatusBadGateway},
		{name: "other", err: errors.New("dial failed"), want: http.StatusBadGateway},
	}
	for _, tc := range statuses {
		t.Run(tc.name, func(t *testing.T) {
			overrideListModels(t, func(context.Context, translate.Config) ([]string, error) {
				return nil, tc.err
			})
			c := newTranslateContext(t, http.MethodPost, "/api/admin/translate/models", map[string]string{})
			err := h.ListModels(c.ctx)
			var httpErr *echo.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != tc.want {
				t.Fatalf("err = %v, want HTTP %d", err, tc.want)
			}
		})
	}
}

// TestTranslateHandlerPromptTemplate 覆盖模板的保存、回显与“空串恢复默认”。
func TestTranslateHandlerPromptTemplate(t *testing.T) {
	cfg := &config.Config{}
	configureTranslate(cfg)
	h := NewTranslateHandler(cfg, filepath.Join(t.TempDir(), "settings.json"))

	custom := "  translate {{.Title}} and {{.Content}}  "
	c := newTranslateContext(t, http.MethodPut, "/api/admin/translate/config", map[string]string{
		"prompt_template": custom,
	})
	if err := h.UpdateConfig(c.ctx); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if got := cfg.Translate.PromptTemplate; got != "translate {{.Title}} and {{.Content}}" {
		t.Fatalf("stored prompt = %q", got)
	}
	var saved struct {
		PromptTemplate string `json:"prompt_template"`
	}
	if err := json.Unmarshal(c.rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if saved.PromptTemplate != "translate {{.Title}} and {{.Content}}" {
		t.Fatalf("response prompt = %q", saved.PromptTemplate)
	}

	reset := newTranslateContext(t, http.MethodPut, "/api/admin/translate/config", map[string]string{
		"prompt_template": "   ",
	})
	if err := h.UpdateConfig(reset.ctx); err != nil {
		t.Fatalf("UpdateConfig(reset): %v", err)
	}
	if cfg.Translate.PromptTemplate != "" {
		t.Fatalf("stored prompt after reset = %q", cfg.Translate.PromptTemplate)
	}
	var afterReset struct {
		PromptTemplate string `json:"prompt_template"`
	}
	if err := json.Unmarshal(reset.rec.Body.Bytes(), &afterReset); err != nil {
		t.Fatalf("decode reset response: %v", err)
	}
	if afterReset.PromptTemplate != translate.DefaultPromptTemplate() {
		t.Fatalf("response should expose the built-in template, got %q", afterReset.PromptTemplate)
	}
}

// TestTranslateHandlerThinkingEffort 覆盖思维强度的保存、归一化、回显与清空。
func TestTranslateHandlerThinkingEffort(t *testing.T) {
	cfg := &config.Config{}
	configureTranslate(cfg)
	h := NewTranslateHandler(cfg, filepath.Join(t.TempDir(), "settings.json"))

	c := newTranslateContext(t, http.MethodPut, "/api/admin/translate/config", map[string]string{
		"thinking_effort": "  HIGH  ",
	})
	if err := h.UpdateConfig(c.ctx); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if got := cfg.Translate.ThinkingEffort; got != "high" {
		t.Fatalf("stored thinking effort = %q", got)
	}
	var saved struct {
		ThinkingEffort string `json:"thinking_effort"`
	}
	if err := json.Unmarshal(c.rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if saved.ThinkingEffort != "high" {
		t.Fatalf("response thinking effort = %q", saved.ThinkingEffort)
	}

	reset := newTranslateContext(t, http.MethodPut, "/api/admin/translate/config", map[string]string{
		"thinking_effort": "",
	})
	if err := h.UpdateConfig(reset.ctx); err != nil {
		t.Fatalf("UpdateConfig(reset): %v", err)
	}
	if cfg.Translate.ThinkingEffort != "" {
		t.Fatalf("stored thinking effort after reset = %q", cfg.Translate.ThinkingEffort)
	}

	invalid := newTranslateContext(t, http.MethodPut, "/api/admin/translate/config", map[string]string{
		"thinking_effort": "extreme",
	})
	if err := h.UpdateConfig(invalid.ctx); err == nil {
		t.Fatal("an unknown effort must be rejected")
	}
}
