package view_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"blog-engine/internal/config"
)

// TestTranslateEndToEnd 走完整 HTTP 栈：安装 → 登录 → 读取脱敏配置 →
// 发起翻译（真实 httptest OpenAI 兼容服务）→ 修改配置。
func TestTranslateEndToEnd(t *testing.T) {
	var seenAuth string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("provider path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"title\":\"Hello\",\"content\":\"# Body\"}"}}]}`)
	}))
	defer provider.Close()

	server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.Translate = &config.TranslateConfig{
			URL: provider.URL + "/v1", APIKey: "e2e-secret", Model: "e2e-model",
			TimeoutSeconds: 30, MaxTokens: 1024,
		}
	})
	admin := newAPI(t, server.URL)
	expectStatus(t, admin.call(http.MethodPost, "/install", map[string]string{"password": adminPassword}, nil),
		http.StatusOK, "POST /install")
	expectStatus(t, admin.call(http.MethodPost, "/api/auth/login",
		map[string]string{"username": "admin", "password": adminPassword}, nil),
		http.StatusOK, "POST /api/auth/login")

	// 未登录的翻译请求必须被管理员分组挡住。
	anon := newAPI(t, server.URL)
	expectStatus(t, anon.call(http.MethodPost, "/api/admin/translate", map[string]string{
		"title": "t", "from": "zh", "to": "en",
	}, nil), http.StatusUnauthorized, "POST /api/admin/translate anonymously")

	configResponse := admin.call(http.MethodGet, "/api/admin/translate/config", nil, nil)
	expectStatus(t, configResponse, http.StatusOK, "GET /api/admin/translate/config")
	if bytes.Contains(configResponse.body, []byte("e2e-secret")) {
		t.Fatalf("the config response leaks the API key: %s", configResponse.body)
	}
	var settings struct {
		Configured       bool `json:"configured"`
		APIKeyConfigured bool `json:"api_key_configured"`
		TimeoutSeconds   int  `json:"timeout_seconds"`
	}
	decodeInto(t, configResponse, &settings)
	if !settings.Configured || !settings.APIKeyConfigured || settings.TimeoutSeconds != 30 {
		t.Fatalf("config response = %+v", settings)
	}

	translated := admin.call(http.MethodPost, "/api/admin/translate", map[string]string{
		"title": "标题", "content": "# 正文", "from": "zh", "to": "en",
	}, admin.unsafe(nil))
	expectStatus(t, translated, http.StatusOK, "POST /api/admin/translate")
	var result struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	decodeInto(t, translated, &result)
	if result.Title != "Hello" || result.Content != "# Body" {
		t.Fatalf("translation = %+v", result)
	}
	if seenAuth != "Bearer e2e-secret" {
		t.Fatalf("provider saw Authorization %q", seenAuth)
	}

	// 配置修改：空白密钥保持原值，模型与开关立即生效。
	updated := admin.call(http.MethodPut, "/api/admin/translate/config", map[string]any{
		"enabled": false, "model": "e2e-model-2", "api_key": "   ",
	}, admin.unsafe(nil))
	expectStatus(t, updated, http.StatusOK, "PUT /api/admin/translate/config")
	var updatedSettings struct {
		Enabled          bool   `json:"enabled"`
		Model            string `json:"model"`
		APIKeyConfigured bool   `json:"api_key_configured"`
	}
	decodeInto(t, updated, &updatedSettings)
	if updatedSettings.Enabled || updatedSettings.Model != "e2e-model-2" || !updatedSettings.APIKeyConfigured {
		t.Fatalf("updated settings = %+v", updatedSettings)
	}

	// 关掉开关后，翻译端点必须回答 503，而不是继续调用上游。
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/translate", map[string]string{
		"title": "标题", "from": "zh", "to": "en",
	}, admin.unsafe(nil)), http.StatusServiceUnavailable, "POST /api/admin/translate while disabled")
}

// TestTranslateUnconfiguredOverHTTP 确认「未配置」是 503，而不是 500/502。
func TestTranslateUnconfiguredOverHTTP(t *testing.T) {
	server, _, _ := newTestServer(t)
	admin := newAPI(t, server.URL)
	expectStatus(t, admin.call(http.MethodPost, "/install", map[string]string{"password": adminPassword}, nil),
		http.StatusOK, "POST /install")
	expectStatus(t, admin.call(http.MethodPost, "/api/auth/login",
		map[string]string{"username": "admin", "password": adminPassword}, nil),
		http.StatusOK, "POST /api/auth/login")

	res := admin.call(http.MethodPost, "/api/admin/translate", map[string]string{
		"title": "标题", "content": "正文", "from": "zh", "to": "en",
	}, admin.unsafe(nil))
	expectStatus(t, res, http.StatusServiceUnavailable, "POST /api/admin/translate without configuration")

	configResponse := admin.call(http.MethodGet, "/api/admin/translate/config", nil, nil)
	expectStatus(t, configResponse, http.StatusOK, "GET /api/admin/translate/config")
	var settings struct {
		Configured bool `json:"configured"`
	}
	decodeInto(t, configResponse, &settings)
	if settings.Configured {
		t.Fatalf("an unconfigured site reported configured: %+v", settings)
	}
}
