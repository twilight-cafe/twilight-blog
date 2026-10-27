package translate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// override 替换一个包级钩子并在用例结束后恢复。测试串行执行，不需要锁。
func override[T any](t *testing.T, target *T, value T) {
	t.Helper()
	original := *target
	*target = value
	t.Cleanup(func() { *target = original })
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingReader) Close() error             { return nil }

// TestParseLanguage 覆盖两个合法代码与所有非法输入。
func TestParseLanguage(t *testing.T) {
	for _, value := range []string{"zh", "en"} {
		parsed, err := ParseLanguage(value)
		if err != nil {
			t.Fatalf("ParseLanguage(%q): %v", value, err)
		}
		if string(parsed) != value {
			t.Fatalf("ParseLanguage(%q) = %q", value, parsed)
		}
	}
	for _, value := range []string{"", "ZH", "fr", "zh-CN"} {
		if _, err := ParseLanguage(value); err == nil {
			t.Fatalf("ParseLanguage(%q) must fail", value)
		}
	}
}

// TestLanguageNativeName 覆盖提示词里的语言名；未知值退化成代码本身，
// 避免提示词里出现空字符串。
func TestLanguageNativeName(t *testing.T) {
	cases := map[Language]string{
		LanguageChinese: "Chinese (Simplified)",
		LanguageEnglish: "English",
		"fr":            "fr",
	}
	for language, want := range cases {
		if got := language.nativeName(); got != want {
			t.Fatalf("nativeName(%q) = %q, want %q", language, got, want)
		}
	}
}

// TestTranslateRequiresCompleteConfig 逐一缺失三项配置，确认都返回
// ErrNotConfigured 而不是发出一个注定失败的网络请求。
func TestTranslateRequiresCompleteConfig(t *testing.T) {
	complete := Config{URL: "https://api.example.com/v1", APIKey: "k", Model: "m"}
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "url", mutate: func(c *Config) { c.URL = "  " }},
		{name: "api key", mutate: func(c *Config) { c.APIKey = "" }},
		{name: "model", mutate: func(c *Config) { c.Model = "\t" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := complete
			tc.mutate(&cfg)
			_, err := Translate(context.Background(), cfg, Request{
				Title: "标题", From: LanguageChinese, To: LanguageEnglish,
			})
			if !errors.Is(err, ErrNotConfigured) {
				t.Fatalf("err = %v, want ErrNotConfigured", err)
			}
		})
	}
}

// TestTranslateRejectsInvalidInput 覆盖四条输入校验：空标题、同语言、
// 非法源语言、非法目标语言。
func TestTranslateRejectsInvalidInput(t *testing.T) {
	cfg := Config{URL: "https://api.example.com/v1", APIKey: "k", Model: "m"}
	cases := []struct {
		name string
		in   Request
	}{
		{name: "empty title", in: Request{Title: "  ", From: LanguageChinese, To: LanguageEnglish}},
		{name: "same language", in: Request{Title: "t", From: LanguageChinese, To: LanguageChinese}},
		{name: "bad source", in: Request{Title: "t", From: "fr", To: LanguageEnglish}},
		{name: "bad target", in: Request{Title: "t", From: LanguageChinese, To: "de"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Translate(context.Background(), cfg, tc.in); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

// TestResolveEndpoint 覆盖基址/尾斜杠/完整端点/查询串/非法地址。
func TestResolveEndpoint(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		bad  bool
	}{
		{raw: "https://api.example.com/v1", want: "https://api.example.com/v1/chat/completions"},
		{raw: " https://api.example.com/v1/ ", want: "https://api.example.com/v1/chat/completions"},
		{raw: "https://api.example.com/v1/chat/completions", want: "https://api.example.com/v1/chat/completions"},
		{raw: "https://api.example.com/openai/chat/completions?api-version=2024-01-01",
			want: "https://api.example.com/openai/chat/completions?api-version=2024-01-01"},
		{raw: "http://127.0.0.1:11434/v1/", want: "http://127.0.0.1:11434/v1/chat/completions"},
		{raw: "ftp://example.com/v1", bad: true},
		{raw: "https://", bad: true},
		{raw: "://missing-scheme", bad: true},
	}
	for _, tc := range cases {
		got, err := resolveEndpoint(tc.raw)
		if tc.bad {
			if err == nil {
				t.Fatalf("resolveEndpoint(%q) must fail, got %q", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("resolveEndpoint(%q): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("resolveEndpoint(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestTranslateRendersCustomPromptTemplate 覆盖设置页保存的自定义模板：
// 标题与正文既可用原始字段，也可用 JSON 安全字段，一起代入同一条消息。
func TestTranslateRendersCustomPromptTemplate(t *testing.T) {
	var seen chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"title\":\"T\",\"content\":\"C\"}"}}]}`)
	}))
	defer server.Close()

	_, err := Translate(context.Background(), Config{
		URL: server.URL, APIKey: "k", Model: "m",
		PromptTemplate: "langs={{.From}}->{{.To}}; title={{.Title}}; json={{.TitleJSON}}; body={{.Content}}",
	}, Request{Title: `A "B"`, Content: "line1\nline2", From: LanguageChinese, To: LanguageEnglish})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(seen.Messages) != 1 {
		t.Fatalf("messages = %d", len(seen.Messages))
	}
	prompt := seen.Messages[0].Content
	for _, want := range []string{
		"langs=Chinese (Simplified)->English",
		`title=A "B"`,
		`json="A \"B\""`,
		"body=line1\nline2",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt %q lacks %q", prompt, want)
		}
	}
	if DefaultPromptTemplate() == "" {
		t.Fatal("the embedded default template must not be empty")
	}
}

// TestTranslateRejectsBrokenPromptTemplate 覆盖手工改坏 settings.json 的情况：
// 解析失败与执行失败都返回 ErrInvalidPrompt，而不是把半成品发给上游。
func TestTranslateRejectsBrokenPromptTemplate(t *testing.T) {
	cases := []string{"{{.Title", "{{.Missing}}"}
	for _, prompt := range cases {
		_, err := Translate(context.Background(), Config{
			URL: "https://api.example.com/v1", APIKey: "k", Model: "m", PromptTemplate: prompt,
		}, Request{Title: "t", From: LanguageChinese, To: LanguageEnglish})
		if !errors.Is(err, ErrInvalidPrompt) {
			t.Fatalf("template %q: err = %v, want ErrInvalidPrompt", prompt, err)
		}
	}
}

// TestTranslateSendsOpenAIRequest 用真实 httptest 服务验证请求形状与成功解析。
func TestTranslateSendsOpenAIRequest(t *testing.T) {
	var seen chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"role": "assistant",
						// 模型常见的「礼貌性包装」：说明文字 + 代码围栏。
						"content": "Here is the translation:\n```json\n" +
							"{\"title\":\"Hello World\",\"content\":\"# Heading\\n\\nBody\"}\n```\n",
					},
				},
			},
		})
	}))
	defer server.Close()

	result, err := Translate(context.Background(), Config{
		URL: server.URL + "/v1/", APIKey: "test-key", Model: "gpt-test", MaxTokens: 512,
	}, Request{
		Title: "你好，世界", Content: "# 标题\n\n正文", From: LanguageChinese, To: LanguageEnglish,
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if result.Title != "Hello World" || result.Content != "# Heading\n\nBody" {
		t.Fatalf("unexpected result: %+v", result)
	}

	if seen.Model != "gpt-test" {
		t.Fatalf("model = %q", seen.Model)
	}
	if seen.MaxTokens != 512 {
		t.Fatalf("max_tokens = %d", seen.MaxTokens)
	}
	if seen.Temperature != 0.2 {
		t.Fatalf("temperature = %v", seen.Temperature)
	}
	if len(seen.Messages) != 1 {
		t.Fatalf("messages = %d, want one prompt message", len(seen.Messages))
	}
	prompt := seen.Messages[0].Content
	if !strings.Contains(prompt, "Chinese (Simplified)") || !strings.Contains(prompt, "English") {
		t.Fatalf("prompt lacks language names: %q", prompt)
	}
	start := strings.Index(prompt, "{")
	end := strings.LastIndex(prompt, "}")
	var original map[string]string
	if start < 0 || end < start {
		t.Fatalf("prompt contains no JSON document: %q", prompt)
	}
	if err := json.Unmarshal([]byte(prompt[start:end+1]), &original); err != nil {
		t.Fatalf("document in prompt is not JSON: %v", err)
	}
	if original["title"] != "你好，世界" || original["content"] != "# 标题\n\n正文" {
		t.Fatalf("user payload = %#v", original)
	}
}

// TestTranslateSendsThinkingEffortOnlyWhenConfigured 钉住 reasoning_effort 的
// 语义：配置了就发送，留空时字段完全缺席（旧服务/不支持该参数的模型不受影响）。
func TestTranslateSendsThinkingEffortOnlyWhenConfigured(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		bodies = append(bodies, body)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"title\":\"T\",\"content\":\"C\"}"}}]}`)
	}))
	defer server.Close()

	for _, effort := range []string{" high ", ""} {
		cfg := Config{URL: server.URL, APIKey: "k", Model: "m", ThinkingEffort: effort}
		if _, err := Translate(context.Background(), cfg, Request{
			Title: "t", From: LanguageChinese, To: LanguageEnglish,
		}); err != nil {
			t.Fatalf("Translate(%q): %v", effort, err)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d", len(bodies))
	}
	if got := bodies[0]["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort = %#v", got)
	}
	if _, exists := bodies[1]["reasoning_effort"]; exists {
		t.Fatalf("reasoning_effort must be absent when empty: %#v", bodies[1])
	}
}

// TestTranslateUsesFullEndpointAndOmitsEmptyMaxTokens 覆盖完整端点写法
// （不再拼一次路径）与 max_tokens=0（字段缺席）。
func TestTranslateUsesFullEndpointAndOmitsEmptyMaxTokens(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("api-version") != "2024-01-01" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"title\":\"你好\",\"content\":\"\"}"}}]}`)
	}))
	defer server.Close()

	result, err := Translate(context.Background(), Config{
		URL:    server.URL + "/custom/chat/completions?api-version=2024-01-01",
		APIKey: "k", Model: "m",
	}, Request{Title: "Hello", Content: "", From: LanguageEnglish, To: LanguageChinese})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if result.Title != "你好" || result.Content != "" {
		t.Fatalf("result = %+v", result)
	}
	if _, exists := body["max_tokens"]; exists {
		t.Fatalf("max_tokens must be omitted when zero: %#v", body)
	}
	system, _ := body["messages"].([]any)[0].(map[string]any)
	if text, _ := system["content"].(string); !strings.Contains(text, "from English to Chinese (Simplified)") {
		t.Fatalf("prompt = %q", text)
	}
}

// TestTranslateReportsUpstreamStatus 覆盖非 2xx 分支，并断言错误里没有响应体
// （响应体可能包含敏感信息）。
func TestTranslateReportsUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "secret upstream details")
	}))
	defer server.Close()

	_, err := Translate(context.Background(), Config{
		URL: server.URL, APIKey: "k", Model: "m",
	}, Request{Title: "t", From: LanguageChinese, To: LanguageEnglish})
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("err = %v, want UpstreamError", err)
	}
	if upstream.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", upstream.StatusCode)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("the upstream body must not leak into the error: %v", err)
	}
}

// TestTranslateRejectsBadCompletions 覆盖响应解析的每一种坏形态。
func TestTranslateRejectsBadCompletions(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "not json", body: `not json`},
		{name: "no choices", body: `{"choices":[]}`},
		{name: "blank message", body: `{"choices":[{"message":{"content":"   "}}]}`},
		{name: "no object", body: `{"choices":[{"message":{"content":"there is no json"}}]}`},
		{name: "reversed braces", body: `{"choices":[{"message":{"content":"}{"}}]}`},
		{name: "wrong field type", body: `{"choices":[{"message":{"content":"{\"title\":1}"}}]}`},
		{name: "empty title", body: `{"choices":[{"message":{"content":"{\"title\":\"  \",\"content\":\"x\"}"}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			_, err := Translate(context.Background(), Config{
				URL: server.URL, APIKey: "k", Model: "m",
			}, Request{Title: "t", From: LanguageChinese, To: LanguageEnglish})
			if !errors.Is(err, ErrInvalidOutput) {
				t.Fatalf("err = %v, want ErrInvalidOutput", err)
			}
		})
	}
}

// TestTranslateRejectsBadEndpoint 覆盖 resolveEndpoint 在 Translate 内的错误分支：
// 配置里的 URL 虽然非空，但协议不受支持。
func TestTranslateRejectsBadEndpoint(t *testing.T) {
	_, err := Translate(context.Background(), Config{
		URL: "ftp://example.com/v1", APIKey: "k", Model: "m",
	}, Request{Title: "t", From: LanguageChinese, To: LanguageEnglish})
	if err == nil || !strings.Contains(err.Error(), "invalid translation endpoint") {
		t.Fatalf("err = %v", err)
	}
}

// TestTranslateRequestConstructionFailure 用钩子强制 newRequest 失败。
func TestTranslateRequestConstructionFailure(t *testing.T) {
	override(t, &newRequest, func(context.Context, string, string, io.Reader) (*http.Request, error) {
		return nil, errors.New("bad request")
	})
	_, err := Translate(context.Background(), Config{
		URL: "https://api.example.com/v1", APIKey: "k", Model: "m",
	}, Request{Title: "t", From: LanguageChinese, To: LanguageEnglish})
	if err == nil || !strings.Contains(err.Error(), "build translation request") {
		t.Fatalf("err = %v", err)
	}
}

// TestTranslateNetworkFailure 覆盖 Transport 返回普通错误的分支。
func TestTranslateNetworkFailure(t *testing.T) {
	override(t, &newHTTPClient, func(time.Duration) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})}
	})
	_, err := Translate(context.Background(), Config{
		URL: "https://api.example.com/v1", APIKey: "k", Model: "m",
	}, Request{Title: "t", From: LanguageChinese, To: LanguageEnglish})
	if err == nil || !strings.Contains(err.Error(), "perform translation request") {
		t.Fatalf("err = %v", err)
	}
}

// TestTranslateTimeout 覆盖 client.Do 返回 context.DeadlineExceeded 的分支。
func TestTranslateTimeout(t *testing.T) {
	override(t, &newHTTPClient, func(time.Duration) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})}
	})
	_, err := Translate(context.Background(), Config{
		URL: "https://api.example.com/v1", APIKey: "k", Model: "m",
	}, Request{Title: "t", From: LanguageChinese, To: LanguageEnglish})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

// TestTranslateReadFailure 覆盖响应体读取失败的分支。
func TestTranslateReadFailure(t *testing.T) {
	override(t, &newHTTPClient, func(time.Duration) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       failingReader{},
				Header:     make(http.Header),
			}, nil
		})}
	})
	_, err := Translate(context.Background(), Config{
		URL: "https://api.example.com/v1", APIKey: "k", Model: "m",
	}, Request{Title: "t", From: LanguageChinese, To: LanguageEnglish})
	if err == nil || !strings.Contains(err.Error(), "read translation response") {
		t.Fatalf("err = %v", err)
	}
}

// TestDecodeTranslatedVariants 覆盖围栏剥离的三种形态与 JSON 截取。
func TestDecodeTranslatedVariants(t *testing.T) {
	cases := []struct {
		name  string
		input string
		title string
		body  string
		bad   bool
	}{
		{name: "plain", input: `{"title":"T","content":"C"}`, title: "T", body: "C"},
		{
			name:  "fenced with language",
			input: "```json\n{\"title\":\"T\",\"content\":\"C\"}\n```",
			title: "T", body: "C",
		},
		{
			name:  "fenced without newline",
			input: "```{\"title\":\"T\",\"content\":\"C\"}```",
			title: "T", body: "C",
		},
		{
			name:  "surrounded by prose",
			input: "Sure! {\"title\":\"T\",\"content\":\"C\"} Hope this helps.",
			title: "T", body: "C",
		},
		{name: "no closing brace", input: `{"title":"T"`, bad: true},
		{name: "reversed", input: `}{`, bad: true},
		{name: "malformed", input: `{"title":}`, bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document, err := decodeTranslated(tc.input)
			if tc.bad {
				if !errors.Is(err, ErrInvalidOutput) {
					t.Fatalf("err = %v, want ErrInvalidOutput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeTranslated: %v", err)
			}
			if document.Title != tc.title || document.Content != tc.body {
				t.Fatalf("document = %+v", document)
			}
		})
	}
}

// TestUpstreamErrorMessage 固定错误文案，避免以后改成一个会泄露响应体的格式。
func TestUpstreamErrorMessage(t *testing.T) {
	err := &UpstreamError{StatusCode: 429}
	if got := err.Error(); got != "translation provider responded with HTTP 429" {
		t.Fatalf("message = %q", got)
	}
}
