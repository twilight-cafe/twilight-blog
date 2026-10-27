package viewmodel

import (
	"strings"
	"testing"
)

// TestTranslateRequestValidate 覆盖翻译请求的每一条校验与清洗规则。
func TestTranslateRequestValidate(t *testing.T) {
	valid := TranslateRequest{
		Title: "  标题  ", Content: "  正文缩进  ", From: "zh", To: "en",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a valid request was rejected: %v", err)
	}
	if valid.Title != "标题" {
		t.Fatalf("title was not trimmed: %q", valid.Title)
	}
	// 正文的前导空格可能属于 Markdown 结构，不能清掉。
	if valid.Content != "  正文缩进  " {
		t.Fatalf("content must preserve whitespace: %q", valid.Content)
	}

	cases := []struct {
		name string
		req  TranslateRequest
		want string
	}{
		{name: "blank title", req: TranslateRequest{Title: "  ", From: "zh", To: "en"}, want: "title is required"},
		{
			name: "long title",
			req:  TranslateRequest{Title: strings.Repeat("题", MaxTranslateTitleLength+1), From: "zh", To: "en"},
			want: "title must be at most",
		},
		{
			name: "long content",
			req:  TranslateRequest{Title: "t", Content: strings.Repeat("文", MaxTranslateContentLength+1), From: "zh", To: "en"},
			want: "content must be at most",
		},
		{name: "bad source", req: TranslateRequest{Title: "t", From: "fr", To: "en"}, want: "unsupported language"},
		{name: "bad target", req: TranslateRequest{Title: "t", From: "zh", To: "de"}, want: "unsupported language"},
		{name: "same language", req: TranslateRequest{Title: "t", From: "zh", To: "zh"}, want: "must be different"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestTranslateTitleLengthCountsRunes 钉住「按字符数而非字节数」：
// 一个由多字节中文组成、字符数未超限的标题必须通过。
func TestTranslateTitleLengthCountsRunes(t *testing.T) {
	req := TranslateRequest{
		Title: strings.Repeat("题", MaxTranslateTitleLength), From: "zh", To: "en",
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("a title at the character limit must pass: %v", err)
	}
}

// TestTranslateConfigRequestValidate 覆盖可选字段的清洗与数值边界。
func TestTranslateConfigRequestValidate(t *testing.T) {
	empty := TranslateConfigRequest{}
	if err := empty.Validate(); err != nil {
		t.Fatalf("an empty patch must be valid: %v", err)
	}

	url := "  https://api.example.com/v1  "
	model := "  gpt-test "
	key := "  secret  "
	timeout := MinTranslateTimeoutSeconds
	maxTokens := MaxTranslateMaxTokens
	req := TranslateConfigRequest{
		URL: &url, Model: &model, APIKey: &key,
		TimeoutSeconds: &timeout, MaxTokens: &maxTokens,
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("a valid patch was rejected: %v", err)
	}
	if *req.URL != "https://api.example.com/v1" || *req.Model != "gpt-test" || *req.APIKey != "secret" {
		t.Fatalf("strings were not trimmed: %+v", req)
	}

	badTimeout := MinTranslateTimeoutSeconds - 1
	tooLargeTimeout := MaxTranslateTimeoutSeconds + 1
	badTokens := MinTranslateMaxTokens - 1
	tooLargeTokens := MaxTranslateMaxTokens + 1
	cases := []struct {
		name string
		req  TranslateConfigRequest
		want string
	}{
		{name: "short timeout", req: TranslateConfigRequest{TimeoutSeconds: &badTimeout}, want: "timeout_seconds"},
		{name: "long timeout", req: TranslateConfigRequest{TimeoutSeconds: &tooLargeTimeout}, want: "timeout_seconds"},
		{name: "small tokens", req: TranslateConfigRequest{MaxTokens: &badTokens}, want: "max_tokens"},
		{name: "large tokens", req: TranslateConfigRequest{MaxTokens: &tooLargeTokens}, want: "max_tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestTranslateConfigPromptTemplateValidation 覆盖提示词模板的三条规则：
// 去首尾空白、空串恢复默认、过长与不可解析的模板被拒绝。
func TestTranslateConfigPromptTemplateValidation(t *testing.T) {
	custom := "  translate {{.Title}} / {{.Content}}  "
	req := TranslateConfigRequest{PromptTemplate: &custom}
	if err := req.Validate(); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
	if *req.PromptTemplate != "translate {{.Title}} / {{.Content}}" {
		t.Fatalf("template = %q", *req.PromptTemplate)
	}

	empty := "   "
	reset := TranslateConfigRequest{PromptTemplate: &empty}
	if err := reset.Validate(); err != nil {
		t.Fatalf("empty template must be valid: %v", err)
	}
	if *reset.PromptTemplate != "" {
		t.Fatalf("empty template = %q", *reset.PromptTemplate)
	}

	tooLong := strings.Repeat("x", MaxTranslatePromptLength+1)
	if err := (&TranslateConfigRequest{PromptTemplate: &tooLong}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "prompt_template") {
		t.Fatalf("over-long template err = %v", err)
	}

	broken := "{{.Title"
	if err := (&TranslateConfigRequest{PromptTemplate: &broken}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "prompt_template") {
		t.Fatalf("broken template err = %v", err)
	}
}

// TestTranslateConfigThinkingEffortValidation 覆盖思维强度的去空白、大小写
// 归一化、空串与未知档位。
func TestTranslateConfigThinkingEffortValidation(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"  HIGH ", "high"},
		{"minimal", "minimal"},
		{"low", "low"},
		{"medium", "medium"},
		{"xhigh", "xhigh"},
		{"max", "max"},
		{"ultra", "ultra"},
		{"   ", ""},
	} {
		value := tc.input
		req := TranslateConfigRequest{ThinkingEffort: &value}
		if err := req.Validate(); err != nil {
			t.Fatalf("effort %q rejected: %v", tc.input, err)
		}
		if *req.ThinkingEffort != tc.want {
			t.Fatalf("effort %q normalized to %q, want %q", tc.input, *req.ThinkingEffort, tc.want)
		}
	}

	for _, value := range []string{"extreme", "high-ish", "2"} {
		v := value
		if err := (&TranslateConfigRequest{ThinkingEffort: &v}).Validate(); err == nil ||
			!strings.Contains(err.Error(), "thinking_effort") {
			t.Fatalf("effort %q err = %v", value, err)
		}
	}
}

// TestTranslateConfigRequestAcceptsTimeoutsAtBothBounds 钉住两个端点值本身合法。
func TestTranslateConfigRequestAcceptsTimeoutsAtBothBounds(t *testing.T) {
	for _, value := range []int{MinTranslateTimeoutSeconds, MaxTranslateTimeoutSeconds} {
		v := value
		req := TranslateConfigRequest{TimeoutSeconds: &v}
		if err := req.Validate(); err != nil {
			t.Fatalf("timeout %d must be accepted: %v", value, err)
		}
	}
	for _, value := range []int{MinTranslateMaxTokens, MaxTranslateMaxTokens} {
		v := value
		req := TranslateConfigRequest{MaxTokens: &v}
		if err := req.Validate(); err != nil {
			t.Fatalf("max tokens %d must be accepted: %v", value, err)
		}
	}
}
