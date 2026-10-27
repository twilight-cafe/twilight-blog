package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/translate"
	"blog-engine/internal/viewmodel"
)

type translateJSONContext struct {
	ctx echo.Context
	rec *httptest.ResponseRecorder
}

func newTranslateContext(t *testing.T, method, path string, body any) translateJSONContext {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e := echo.New()
	return translateJSONContext{ctx: e.NewContext(req, rec), rec: rec}
}

func overrideTranslate(t *testing.T, fn func(context.Context, translate.Config, translate.Request) (translate.Result, error)) {
	t.Helper()
	original := translateText
	translateText = fn
	t.Cleanup(func() { translateText = original })
}

func configureTranslate(cfg *config.Config) {
	enabled := true
	cfg.Translate = &config.TranslateConfig{
		Enabled:        &enabled,
		URL:            "https://api.example.com/v1",
		APIKey:         "secret-key",
		Model:          "gpt-test",
		TimeoutSeconds: 90,
		MaxTokens:      2048,
		ThinkingEffort: "medium",
	}
}

// TestTranslateHandlerRejectsMalformedRequests 覆盖绑定失败与 viewmodel 校验失败。
func TestTranslateHandlerRejectsMalformedRequests(t *testing.T) {
	cfg := &config.Config{}
	handler := NewTranslateHandler(cfg, filepath.Join(t.TempDir(), "settings.json"))

	bad := translateJSONContext{
		ctx: func() echo.Context {
			req := httptest.NewRequest(http.MethodPost, "/api/admin/translate", strings.NewReader("{"))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			return echo.New().NewContext(req, rec)
		}(),
		rec: httptest.NewRecorder(),
	}
	if err := handler.Translate(bad.ctx); err == nil {
		t.Fatal("malformed JSON must be rejected")
	} else if httpErr, ok := err.(*echo.HTTPError); !ok || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("err = %v, want 400", err)
	}

	invalid := newTranslateContext(t, http.MethodPost, "/api/admin/translate", map[string]string{
		"title": "", "content": "c", "from": "zh", "to": "en",
	})
	if err := handler.Translate(invalid.ctx); err == nil {
		t.Fatal("a blank title must be rejected")
	} else if httpErr, ok := err.(*echo.HTTPError); !ok || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("err = %v, want 400", err)
	}
}

// TestTranslateHandlerRequiresConfiguration 确认未配置时返回 503，且不会调用上游。
func TestTranslateHandlerRequiresConfiguration(t *testing.T) {
	cfg := &config.Config{}
	handler := NewTranslateHandler(cfg, filepath.Join(t.TempDir(), "settings.json"))
	called := false
	overrideTranslate(t, func(context.Context, translate.Config, translate.Request) (translate.Result, error) {
		called = true
		return translate.Result{}, nil
	})

	c := newTranslateContext(t, http.MethodPost, "/api/admin/translate", map[string]string{
		"title": "标题", "content": "正文", "from": "zh", "to": "en",
	})
	err := handler.Translate(c.ctx)
	if called {
		t.Fatal("the provider must not be called when the feature is unconfigured")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want 503", err)
	}
}

// TestTranslateHandlerSuccess 验证成功路径与传给翻译实现的全部参数。
func TestTranslateHandlerSuccess(t *testing.T) {
	cfg := &config.Config{}
	configureTranslate(cfg)
	handler := NewTranslateHandler(cfg, filepath.Join(t.TempDir(), "settings.json"))

	var gotConfig translate.Config
	var gotRequest translate.Request
	overrideTranslate(t, func(_ context.Context, c translate.Config, r translate.Request) (translate.Result, error) {
		gotConfig = c
		gotRequest = r
		return translate.Result{Title: "Hello", Content: "Body"}, nil
	})

	c := newTranslateContext(t, http.MethodPost, "/api/admin/translate", map[string]string{
		"title": "标题", "content": "正文", "from": "zh", "to": "en",
	})
	if err := handler.Translate(c.ctx); err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if c.rec.Code != http.StatusOK {
		t.Fatalf("status = %d", c.rec.Code)
	}
	if gotConfig.URL != cfg.Translate.URL || gotConfig.APIKey != "secret-key" ||
		gotConfig.Model != "gpt-test" || gotConfig.Timeout != 90*time.Second || gotConfig.MaxTokens != 2048 ||
		gotConfig.ThinkingEffort != "medium" {
		t.Fatalf("config passed downstream = %+v", gotConfig)
	}
	if gotRequest.Title != "标题" || gotRequest.Content != "正文" ||
		gotRequest.From != translate.LanguageChinese || gotRequest.To != translate.LanguageEnglish {
		t.Fatalf("request passed downstream = %+v", gotRequest)
	}

	var response viewmodel.TranslateResponse
	if err := json.Unmarshal(c.rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Title != "Hello" || response.Content != "Body" {
		t.Fatalf("response = %+v", response)
	}
}

// TestTranslateHandlerMapsProviderErrors 逐一覆盖 400/502/503/504 与未知错误。
func TestTranslateHandlerMapsProviderErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "not configured", err: translate.ErrNotConfigured, want: http.StatusServiceUnavailable},
		{name: "invalid input", err: translate.ErrInvalidInput, want: http.StatusBadRequest},
		{name: "timeout sentinel", err: translate.ErrTimeout, want: http.StatusGatewayTimeout},
		{name: "context deadline", err: context.DeadlineExceeded, want: http.StatusGatewayTimeout},
		{name: "invalid output", err: translate.ErrInvalidOutput, want: http.StatusBadGateway},
		{name: "upstream status", err: &translate.UpstreamError{StatusCode: http.StatusTooManyRequests}, want: http.StatusBadGateway},
		{name: "unknown", err: errors.New("socket closed"), want: http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mapTranslateError(tc.err)
			httpErr, ok := err.(*echo.HTTPError)
			if !ok || httpErr.Code != tc.want {
				t.Fatalf("err = %v, want HTTP %d", err, tc.want)
			}
		})
	}
}

// TestTranslateHandlerWrapsProviderFailureOverHTTP 从 handler 走一遍错误分支，
// 确保 502/504 真的写进响应，而不只是映射函数返回了正确的错误。
func TestTranslateHandlerWrapsProviderFailureOverHTTP(t *testing.T) {
	cfg := &config.Config{}
	configureTranslate(cfg)
	handler := NewTranslateHandler(cfg, filepath.Join(t.TempDir(), "settings.json"))

	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "timeout", err: translate.ErrTimeout, want: http.StatusGatewayTimeout},
		{name: "upstream", err: &translate.UpstreamError{StatusCode: 500}, want: http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			overrideTranslate(t, func(context.Context, translate.Config, translate.Request) (translate.Result, error) {
				return translate.Result{}, tc.err
			})
			c := newTranslateContext(t, http.MethodPost, "/api/admin/translate", map[string]string{
				"title": "标题", "from": "zh", "to": "en",
			})
			err := handler.Translate(c.ctx)
			httpErr, ok := err.(*echo.HTTPError)
			if !ok || httpErr.Code != tc.want {
				t.Fatalf("err = %v, want %d", err, tc.want)
			}
		})
	}
}

// TestTranslateHandlerGetConfigRedactsKey 覆盖配置读取：未配置与已配置，
// 并断言响应里没有密钥。
func TestTranslateHandlerGetConfigRedactsKey(t *testing.T) {
	unconfigured := &config.Config{}
	h1 := NewTranslateHandler(unconfigured, filepath.Join(t.TempDir(), "settings.json"))
	c1 := newTranslateContext(t, http.MethodGet, "/api/admin/translate/config", nil)
	if err := h1.GetConfig(c1.ctx); err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	var first viewmodel.TranslateConfigResponse
	if err := json.Unmarshal(c1.rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if first.Configured || first.APIKeyConfigured {
		t.Fatalf("unconfigured response = %+v", first)
	}
	if first.TimeoutSeconds != config.DefaultTranslateTimeoutSeconds || first.MaxTokens != config.DefaultTranslateMaxTokens {
		t.Fatalf("defaults = %+v", first)
	}

	configured := &config.Config{}
	configureTranslate(configured)
	h2 := NewTranslateHandler(configured, filepath.Join(t.TempDir(), "settings.json"))
	c2 := newTranslateContext(t, http.MethodGet, "/api/admin/translate/config", nil)
	if err := h2.GetConfig(c2.ctx); err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if strings.Contains(c2.rec.Body.String(), "secret-key") || strings.Contains(c2.rec.Body.String(), `"api_key":`) {
		t.Fatalf("the response must not expose the key: %s", c2.rec.Body.String())
	}
	var second viewmodel.TranslateConfigResponse
	if err := json.Unmarshal(c2.rec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !second.Configured || !second.APIKeyConfigured || second.URL != configured.Translate.URL ||
		second.Model != "gpt-test" || second.TimeoutSeconds != 90 || second.MaxTokens != 2048 {
		t.Fatalf("configured response = %+v", second)
	}
}

// TestTranslateHandlerUpdateConfigMergesFields 覆盖「省略 = 保持现值」、
// 「空密钥 = 保持现值」与成功保存。
func TestTranslateHandlerUpdateConfigMergesFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	cfg := &config.Config{}
	configureTranslate(cfg)
	handler := NewTranslateHandler(cfg, path)

	c := newTranslateContext(t, http.MethodPut, "/api/admin/translate/config", map[string]any{
		"enabled": false, "url": " https://api.example.com/v2 ", "model": " new-model ",
		"timeout_seconds": 120, "max_tokens": 4096, "api_key": "   ",
	})
	if err := handler.UpdateConfig(c.ctx); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	value := cfg.TranslateConfigValue()
	if value.IsEnabled() || value.URL != "https://api.example.com/v2" || value.Model != "new-model" {
		t.Fatalf("value = %+v", value)
	}
	if value.APIKey != "secret-key" {
		t.Fatalf("a blank api_key must keep the existing key, got %q", value.APIKey)
	}
	if value.TimeoutSeconds != 120 || value.MaxTokens != 4096 {
		t.Fatalf("numbers = %+v", value)
	}
	stored, err := readFileString(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(stored, "secret-key") {
		t.Fatalf("the saved file lacks the retained key:\n%s", stored)
	}

	// 显式给新密钥时替换。
	c2 := newTranslateContext(t, http.MethodPut, "/api/admin/translate/config", map[string]any{
		"api_key": "new-secret",
	})
	if err := handler.UpdateConfig(c2.ctx); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if got := cfg.TranslateConfigValue().APIKey; got != "new-secret" {
		t.Fatalf("api_key = %q, want new-secret", got)
	}
}

// TestTranslateHandlerUpdateConfigRejectsInvalid 覆盖绑定、数值、URL、半套配置
// 四条 400 分支。
func TestTranslateHandlerUpdateConfigRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	t.Run("bind failure", func(t *testing.T) {
		handler := NewTranslateHandler(&config.Config{}, path)
		req := httptest.NewRequest(http.MethodPut, "/api/admin/translate/config", strings.NewReader("{"))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		err := handler.UpdateConfig(echo.New().NewContext(req, rec))
		httpErr, ok := err.(*echo.HTTPError)
		if !ok || httpErr.Code != http.StatusBadRequest {
			t.Fatalf("err = %v, want 400", err)
		}
	})

	t.Run("numeric validation", func(t *testing.T) {
		handler := NewTranslateHandler(&config.Config{}, path)
		c := newTranslateContext(t, http.MethodPut, "/api/admin/translate/config", map[string]any{
			"timeout_seconds": 1,
		})
		err := handler.UpdateConfig(c.ctx)
		httpErr, ok := err.(*echo.HTTPError)
		if !ok || httpErr.Code != http.StatusBadRequest {
			t.Fatalf("err = %v, want 400", err)
		}
	})

	t.Run("bad url", func(t *testing.T) {
		handler := NewTranslateHandler(&config.Config{}, path)
		c := newTranslateContext(t, http.MethodPut, "/api/admin/translate/config", map[string]any{
			"url": "ftp://example.com", "model": "m", "api_key": "k",
		})
		err := handler.UpdateConfig(c.ctx)
		httpErr, ok := err.(*echo.HTTPError)
		if !ok || httpErr.Code != http.StatusBadRequest {
			t.Fatalf("err = %v, want 400", err)
		}
	})

	t.Run("incomplete configuration", func(t *testing.T) {
		handler := NewTranslateHandler(&config.Config{}, path)
		c := newTranslateContext(t, http.MethodPut, "/api/admin/translate/config", map[string]any{
			"url": "https://api.example.com/v1", "model": "m",
		})
		err := handler.UpdateConfig(c.ctx)
		httpErr, ok := err.(*echo.HTTPError)
		if !ok || httpErr.Code != http.StatusBadRequest {
			t.Fatalf("err = %v, want 400", err)
		}
	})
}

// TestTranslateHandlerUpdateConfigRollsBackOnSaveFailure 让 Save 失败，
// 验证内存回滚与 500。
func TestTranslateHandlerUpdateConfigRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	// 用目录当 settingsPath：rename 到已存在目录必然失败，从而稳定制造写盘错误。
	cfg := &config.Config{}
	configureTranslate(cfg)
	handler := NewTranslateHandler(cfg, dir)

	c := newTranslateContext(t, http.MethodPut, "/api/admin/translate/config", map[string]any{
		"model": "must-not-stick",
	})
	err := handler.UpdateConfig(c.ctx)
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusInternalServerError {
		t.Fatalf("err = %v, want 500", err)
	}
	if got := cfg.TranslateConfigValue().Model; got != "gpt-test" {
		t.Fatalf("the in-memory config was not rolled back: %q", got)
	}
}

// readFileString 是测试侧的小工具，避免每个用例都写一遍 os.ReadFile。
func readFileString(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}
