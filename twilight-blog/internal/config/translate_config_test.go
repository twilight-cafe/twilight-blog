package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boolPointer(value bool) *bool { return &value }

// TestTranslateEnabledSemantics 钉住「键缺失 = 启用」这条向后兼容规则。
func TestTranslateEnabledSemantics(t *testing.T) {
	var missing *TranslateConfig
	if !missing.IsEnabled() {
		t.Fatal("a nil section must count as enabled")
	}
	if !(&TranslateConfig{}).IsEnabled() {
		t.Fatal("a missing enabled key must count as enabled")
	}
	if (&TranslateConfig{Enabled: boolPointer(false)}).IsEnabled() {
		t.Fatal("an explicit false must disable the feature")
	}
	if !(&TranslateConfig{Enabled: boolPointer(true)}).IsEnabled() {
		t.Fatal("an explicit true must keep the feature enabled")
	}
}

// TestTranslateIsConfigured 覆盖「开关 + 三要素」的组合。
func TestTranslateIsConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  *TranslateConfig
		want bool
	}{
		{name: "nil", cfg: nil},
		{name: "disabled", cfg: &TranslateConfig{Enabled: boolPointer(false), URL: "https://x", APIKey: "k", Model: "m"}},
		{name: "no url", cfg: &TranslateConfig{APIKey: "k", Model: "m"}},
		{name: "blank url", cfg: &TranslateConfig{URL: "  ", APIKey: "k", Model: "m"}},
		{name: "no key", cfg: &TranslateConfig{URL: "https://x", Model: "m"}},
		{name: "no model", cfg: &TranslateConfig{URL: "https://x", APIKey: "k"}},
		{name: "complete", cfg: &TranslateConfig{URL: " https://x ", APIKey: " k ", Model: " m "}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsConfigured(); got != tc.want {
				t.Fatalf("IsConfigured = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestNormalizeTranslateConfig 覆盖去空白、nil 兜底与两个默认值。
func TestNormalizeTranslateConfig(t *testing.T) {
	normalized := normalizeTranslateConfig(nil)
	if normalized == nil {
		t.Fatal("normalize must never return nil")
	}
	if normalized.TimeoutSeconds != DefaultTranslateTimeoutSeconds ||
		normalized.MaxTokens != DefaultTranslateMaxTokens {
		t.Fatalf("defaults = %+v", normalized)
	}

	explicit := &TranslateConfig{
		URL: "  https://api.example.com/v1  ", APIKey: "  key  ", Model: "  model  ",
		TimeoutSeconds: 120, MaxTokens: 8192,
	}
	normalized = normalizeTranslateConfig(explicit)
	if normalized.URL != "https://api.example.com/v1" || normalized.APIKey != "key" || normalized.Model != "model" {
		t.Fatalf("trims failed: %+v", normalized)
	}
	if normalized.TimeoutSeconds != 120 || normalized.MaxTokens != 8192 {
		t.Fatalf("explicit values were overwritten: %+v", normalized)
	}
}

// TestValidateTranslateURL 覆盖空值、解析失败、协议、主机、凭据、片段与合法形态。
func TestValidateTranslateURL(t *testing.T) {
	valid := []string{
		"",
		"   ",
		"https://api.openai.com/v1",
		"http://127.0.0.1:11434/v1",
		"https://api.example.com/v1/chat/completions?api-version=1",
	}
	for _, value := range valid {
		if err := ValidateTranslateURL(value); err != nil {
			t.Fatalf("ValidateTranslateURL(%q): %v", value, err)
		}
	}

	invalid := []string{
		"://bad",
		"ftp://example.com/v1",
		"https://",
		"https://user:pass@example.com/v1",
		"https://example.com/v1#fragment",
	}
	for _, value := range invalid {
		if err := ValidateTranslateURL(value); err == nil {
			t.Fatalf("ValidateTranslateURL(%q) must fail", value)
		}
	}
}

// TestValidateTranslateConfig 覆盖 nil、半套配置与完整配置。
func TestValidateTranslateConfig(t *testing.T) {
	if err := ValidateTranslateConfig(nil); err != nil {
		t.Fatalf("nil must be accepted: %v", err)
	}
	if err := ValidateTranslateConfig(&TranslateConfig{Model: "m", APIKey: "k"}); err != nil {
		t.Fatalf("configuration without a URL is dormant and must be accepted: %v", err)
	}
	if err := ValidateTranslateConfig(&TranslateConfig{URL: "ftp://x", Model: "m", APIKey: "k"}); err == nil {
		t.Fatal("an invalid URL must be refused")
	}
	if err := ValidateTranslateConfig(&TranslateConfig{URL: "https://x", APIKey: "k"}); err == nil {
		t.Fatal("a missing model must be refused")
	}
	if err := ValidateTranslateConfig(&TranslateConfig{URL: "https://x", Model: "m"}); err == nil {
		t.Fatal("a missing api key must be refused")
	}
	if err := ValidateTranslateConfig(&TranslateConfig{URL: "https://x", Model: "m", APIKey: "k"}); err != nil {
		t.Fatalf("a complete configuration must be accepted: %v", err)
	}
}

// TestValidateTranslatePrompt 覆盖模板的空白、超长与可解析性三条分支。
func TestValidateTranslatePrompt(t *testing.T) {
	if err := validateTranslatePrompt("   "); err != nil {
		t.Fatalf("blank prompt must be accepted: %v", err)
	}
	if err := validateTranslatePrompt("translate {{.Title}}"); err != nil {
		t.Fatalf("valid prompt must be accepted: %v", err)
	}
	if err := validateTranslatePrompt(strings.Repeat("x", 20_001)); err == nil {
		t.Fatal("over-long prompt must be refused")
	}
	if err := validateTranslatePrompt("{{.Title"); err == nil {
		t.Fatal("unparsable prompt must be refused")
	}
	// ValidateTranslateConfig 也要把模板校验串起来。
	if err := ValidateTranslateConfig(&TranslateConfig{
		URL: "https://x", Model: "m", APIKey: "k", PromptTemplate: "{{.Title",
	}); err == nil {
		t.Fatal("an invalid prompt must make the whole config invalid")
	}
}

// TestTranslateThinkingEffort 覆盖档位的归一化、合法取值与非法取值。
func TestTranslateThinkingEffort(t *testing.T) {
	if got := normalizeTranslateConfig(&TranslateConfig{ThinkingEffort: "  HIGH "}).ThinkingEffort; got != "high" {
		t.Fatalf("normalized effort = %q", got)
	}
	for _, value := range []string{"", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", " HIGH "} {
		if err := ValidateTranslateConfig(&TranslateConfig{ThinkingEffort: value}); err != nil {
			t.Fatalf("effort %q must be accepted: %v", value, err)
		}
	}
	for _, value := range []string{"extreme", "high-ish", "1"} {
		if err := ValidateTranslateConfig(&TranslateConfig{ThinkingEffort: value}); err == nil {
			t.Fatalf("effort %q must be refused", value)
		}
	}

	cfg := &Config{}
	if err := cfg.SetTranslateConfig(TranslateConfig{ThinkingEffort: " medium "}); err != nil {
		t.Fatalf("SetTranslateConfig: %v", err)
	}
	if got := cfg.TranslateConfigValue().ThinkingEffort; got != "medium" {
		t.Fatalf("stored effort = %q", got)
	}
}

// TestValidateTranslateNumbers 覆盖超时与输出预算的边界。
func TestValidateTranslateNumbers(t *testing.T) {
	valid := []*TranslateConfig{
		nil,
		{},
		{TimeoutSeconds: MinTranslateTimeoutSeconds, MaxTokens: MinTranslateMaxTokens},
		{TimeoutSeconds: DefaultTranslateTimeoutSeconds, MaxTokens: DefaultTranslateMaxTokens},
		{TimeoutSeconds: MaxTranslateTimeoutSeconds, MaxTokens: MaxTranslateMaxTokens},
	}
	for _, cfg := range valid {
		if err := validateTranslateNumbers(cfg); err != nil {
			t.Fatalf("valid numbers rejected (%+v): %v", cfg, err)
		}
	}

	invalid := []*TranslateConfig{
		{TimeoutSeconds: -1},
		{TimeoutSeconds: MinTranslateTimeoutSeconds - 1},
		{TimeoutSeconds: MaxTranslateTimeoutSeconds + 1},
		{MaxTokens: -1},
		{MaxTokens: MinTranslateMaxTokens - 1},
		{MaxTokens: MaxTranslateMaxTokens + 1},
	}
	for _, cfg := range invalid {
		if err := validateTranslateNumbers(cfg); err == nil {
			t.Fatalf("invalid numbers accepted: %+v", cfg)
		}
	}
}

// TestTranslateConfigValueDefaultsAndCopies 覆盖 nil 配置、默认补齐与深拷贝。
func TestTranslateConfigValueDefaultsAndCopies(t *testing.T) {
	empty := (&Config{}).TranslateConfigValue()
	if empty.TimeoutSeconds != DefaultTranslateTimeoutSeconds || empty.MaxTokens != DefaultTranslateMaxTokens {
		t.Fatalf("empty config defaults = %+v", empty)
	}

	cfg := &Config{Translate: &TranslateConfig{
		Enabled: boolPointer(true), URL: " https://api.example.com/v1 ", APIKey: " key ",
		Model: " model ", TimeoutSeconds: 30, MaxTokens: 1024,
	}}
	value := cfg.TranslateConfigValue()
	if value.URL != "https://api.example.com/v1" || value.APIKey != "key" || value.Model != "model" {
		t.Fatalf("value was not normalized: %+v", value)
	}
	// 修改副本不能影响配置本体（含 Enabled 指针）。
	value.APIKey = "changed"
	*value.Enabled = false
	if cfg.Translate.APIKey != " key " || !*cfg.Translate.Enabled {
		t.Fatalf("the returned value aliases the config: %+v", cfg.Translate)
	}
}

// TestSetTranslateConfig 覆盖校验失败、成功写入与输入指针不被别名共享。
func TestSetTranslateConfig(t *testing.T) {
	cfg := &Config{}
	if err := cfg.SetTranslateConfig(TranslateConfig{URL: "ftp://x", Model: "m", APIKey: "k"}); err == nil {
		t.Fatal("an invalid config must be refused")
	}
	if err := cfg.SetTranslateConfig(TranslateConfig{URL: "https://x", Model: "m", APIKey: "k", TimeoutSeconds: 1}); err == nil {
		t.Fatal("an out-of-range timeout must be refused")
	}

	enabled := true
	next := TranslateConfig{
		Enabled: &enabled, URL: " https://api.example.com/v1 ", APIKey: " k ",
		Model: " m ", TimeoutSeconds: 120, MaxTokens: 2048,
	}
	if err := cfg.SetTranslateConfig(next); err != nil {
		t.Fatalf("SetTranslateConfig: %v", err)
	}
	if !cfg.Translate.IsConfigured() {
		t.Fatalf("the config is not usable after saving: %+v", cfg.Translate)
	}
	// 调用方随后改自己的指针，不应穿透到配置。
	enabled = false
	next.APIKey = "other"
	if *cfg.Translate.Enabled != true || cfg.Translate.APIKey != "k" {
		t.Fatalf("the stored config aliases the caller input: %+v", cfg.Translate)
	}
}

// TestApplyDefaultsFillsTranslateSection 覆盖 nil 段的创建与默认值补齐。
func TestApplyDefaultsFillsTranslateSection(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.Translate == nil {
		t.Fatal("ApplyDefaults must create the translate section")
	}
	if cfg.Translate.TimeoutSeconds != DefaultTranslateTimeoutSeconds ||
		cfg.Translate.MaxTokens != DefaultTranslateMaxTokens {
		t.Fatalf("defaults = %+v", cfg.Translate)
	}

	cfg.Translate = &TranslateConfig{TimeoutSeconds: 30, MaxTokens: 1024}
	cfg.ApplyDefaults()
	if cfg.Translate.TimeoutSeconds != 30 || cfg.Translate.MaxTokens != 1024 {
		t.Fatalf("explicit values were overwritten: %+v", cfg.Translate)
	}
}

// TestLoadTranslateConfig 覆盖配置文件里的合法与非法翻译段。
func TestLoadTranslateConfig(t *testing.T) {
	write := func(t *testing.T, translate any) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "settings.json")
		encoded, err := json.Marshal(map[string]any{"translate": translate})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		return path
	}

	t.Run("valid", func(t *testing.T) {
		path := write(t, map[string]any{
			"enabled": true, "url": " https://api.example.com/v1 ", "api_key": " secret ",
			"model": " gpt-test ", "timeout_seconds": 120, "max_tokens": 2048,
		})
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		value := cfg.TranslateConfigValue()
		if value.URL != "https://api.example.com/v1" || value.APIKey != "secret" || value.Model != "gpt-test" {
			t.Fatalf("value = %+v", value)
		}
		if value.TimeoutSeconds != 120 || value.MaxTokens != 2048 {
			t.Fatalf("numbers = %+v", value)
		}
	})

	t.Run("defaults are written back", func(t *testing.T) {
		path := write(t, map[string]any{"url": "", "api_key": "k", "model": "m"})
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := cfg.Save(path); err != nil {
			t.Fatalf("Save: %v", err)
		}
		stored, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var decoded struct {
			Translate TranslateConfig `json:"translate"`
		}
		if err := json.Unmarshal(stored, &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if decoded.Translate.TimeoutSeconds != DefaultTranslateTimeoutSeconds ||
			decoded.Translate.MaxTokens != DefaultTranslateMaxTokens {
			t.Fatalf("defaults were not persisted: %+v", decoded.Translate)
		}
	})

	invalid := []struct {
		name      string
		translate map[string]any
	}{
		{name: "bad url", translate: map[string]any{"url": "ftp://x", "api_key": "k", "model": "m"}},
		{name: "missing model", translate: map[string]any{"url": "https://x", "api_key": "k"}},
		{name: "missing key", translate: map[string]any{"url": "https://x", "model": "m"}},
		{name: "negative timeout", translate: map[string]any{"timeout_seconds": -1}},
		{name: "short timeout", translate: map[string]any{"timeout_seconds": 1}},
		{name: "long timeout", translate: map[string]any{"timeout_seconds": 601}},
		{name: "negative tokens", translate: map[string]any{"max_tokens": -1}},
		{name: "small tokens", translate: map[string]any{"max_tokens": 1}},
		{name: "large tokens", translate: map[string]any{"max_tokens": 65536}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			path := write(t, tc.translate)
			if _, err := Load(path); err == nil {
				t.Fatal("an invalid translate section must fail the load")
			}
		})
	}
}

// TestSaveKeepsPlaintextTranslateKey 确认翻译密钥会随 settings.json 正常落盘
// （与邮件密钥不同：后者会被换成环境变量引用）。
func TestSaveKeepsPlaintextTranslateKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	cfg := &Config{Translate: &TranslateConfig{
		URL: "https://api.example.com/v1", APIKey: "plain-secret", Model: "m",
	}}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(stored), `"api_key": "plain-secret"`) {
		t.Fatalf("the translate key must be persisted:\n%s", stored)
	}
}
