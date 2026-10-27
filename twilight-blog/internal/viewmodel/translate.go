package viewmodel

import (
	"fmt"
	"strings"
	"text/template"

	"blog-engine/internal/translate"
)

// MaxTranslateTitleLength 是标题的长度上限（按字符数）。
//
// 文章标题本身没有独立的长度限制，但翻译会把标题当作文本发给上游、再写回编辑器。
// 一个几十万字的「标题」既不是文章标题，也会让一次翻译的输入无谓地膨胀；
// 200 已远高于任何真实标题。
const MaxTranslateTitleLength = 200

// MaxTranslateContentLength 是正文的长度上限（按字符数）。
//
// 它与请求体上限（50MB）不是一回事：50MB 是传输层护栏，这里限制的是「一次翻译
// 愿意为多长的文章付费」。100,000 字符大约是一篇数万字的中文长文，远超正常
// 博客文章；超过它的输入更可能是误粘贴了一个日志文件。
const MaxTranslateContentLength = 100_000

// MaxTranslatePromptLength 是自定义提示词模板的长度上限（按字符数）。
// 模板只是几十行规则，20 000 字符已远超正常值；再长更像误粘贴。
const MaxTranslatePromptLength = 20_000

// TranslateRequest 是 POST /api/admin/translate 的请求体。
//
// From / To 必须显式给出，而不是由服务端根据「哪边为空」推断：编辑器知道
// 用户正在写哪个页签，把这个意图交给前端表达最直接，也避免服务端猜测错误后
// 把中文覆盖进英文正文。
type TranslateRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// Validate 清洗并校验一次翻译请求。
//
// 正文不做 TrimSpace：Markdown 的缩进（嵌套列表、代码块）是有意义的，
// 而标题必须去掉首尾空白——空标题翻译出来的结果同样没有意义。
func (r *TranslateRequest) Validate() error {
	r.Title = strings.TrimSpace(r.Title)
	if r.Title == "" {
		return fmt.Errorf("title is required")
	}
	if n := len([]rune(r.Title)); n > MaxTranslateTitleLength {
		return fmt.Errorf("title must be at most %d characters", MaxTranslateTitleLength)
	}
	if n := len([]rune(r.Content)); n > MaxTranslateContentLength {
		return fmt.Errorf("content must be at most %d characters", MaxTranslateContentLength)
	}
	if _, err := translate.ParseLanguage(r.From); err != nil {
		return err
	}
	if _, err := translate.ParseLanguage(r.To); err != nil {
		return err
	}
	if r.From == r.To {
		return fmt.Errorf("from and to must be different languages")
	}
	return nil
}

// TranslateResponse 是翻译结果。字段与编辑器里的两个页签一一对应。
type TranslateResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// TranslateConfigRequest 修改翻译配置。
//
// 每个字段都是可选三态：省略 = 保持现值。APIKey 额外有一条规则：
// **省略或空串都表示保持现值**——设置页永远拿不到已保存的密钥，若把空串当成
// 「清除」，站长只改模型就会把密钥一起清掉。
type TranslateConfigRequest struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	URL            *string `json:"url,omitempty"`
	Model          *string `json:"model,omitempty"`
	APIKey         *string `json:"api_key,omitempty"`
	TimeoutSeconds *int    `json:"timeout_seconds,omitempty"`
	MaxTokens      *int    `json:"max_tokens,omitempty"`
	// PromptTemplate 是提示词模板；nil = 保持现值，空串 = 恢复内置默认模板。
	PromptTemplate *string `json:"prompt_template,omitempty"`
	// ThinkingEffort 是思维强度；nil = 保持现值，空串 = 不发送该字段。
	ThinkingEffort *string `json:"thinking_effort,omitempty"`
}

// Validate 清洗可选的字符串字段并校验数值区间。
//
// URL 的格式与「URL 非空时 model/api_key 必须齐备」由 config 层在合并之后统一
// 校验：那两条规则依赖当前保存的密钥，单看请求体判断不了（密钥省略 = 沿用）。
func (r *TranslateConfigRequest) Validate() error {
	if r.URL != nil {
		trimmed := strings.TrimSpace(*r.URL)
		r.URL = &trimmed
	}
	if r.Model != nil {
		trimmed := strings.TrimSpace(*r.Model)
		r.Model = &trimmed
	}
	if r.APIKey != nil {
		trimmed := strings.TrimSpace(*r.APIKey)
		r.APIKey = &trimmed
	}
	if r.TimeoutSeconds != nil {
		value := *r.TimeoutSeconds
		if value < MinTranslateTimeoutSeconds || value > MaxTranslateTimeoutSeconds {
			return fmt.Errorf("timeout_seconds must be between %d and %d",
				MinTranslateTimeoutSeconds, MaxTranslateTimeoutSeconds)
		}
	}
	if r.MaxTokens != nil {
		value := *r.MaxTokens
		if value < MinTranslateMaxTokens || value > MaxTranslateMaxTokens {
			return fmt.Errorf("max_tokens must be between %d and %d",
				MinTranslateMaxTokens, MaxTranslateMaxTokens)
		}
	}
	if r.PromptTemplate != nil {
		trimmed := strings.TrimSpace(*r.PromptTemplate)
		if n := len([]rune(trimmed)); n > MaxTranslatePromptLength {
			return fmt.Errorf("prompt_template must be at most %d characters", MaxTranslatePromptLength)
		}
		// 非空模板必须在保存时就确认可解析，不能等到用户点翻译才炸。
		if trimmed != "" {
			if _, err := template.New("prompt").Parse(trimmed); err != nil {
				return fmt.Errorf("prompt_template is not a valid template: %v", err)
			}
		}
		r.PromptTemplate = &trimmed
	}
	if r.ThinkingEffort != nil {
		trimmed := strings.ToLower(strings.TrimSpace(*r.ThinkingEffort))
		if err := validateThinkingEffort(trimmed); err != nil {
			return err
		}
		r.ThinkingEffort = &trimmed
	}
	return nil
}

// validateThinkingEffort 与 config 层的档位校验保持同一组取值。
// 空串合法，表示不发送 reasoning_effort。
func validateThinkingEffort(value string) error {
	switch value {
	case "", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return nil
	default:
		return fmt.Errorf("thinking_effort must be empty, minimal, low, medium, high, xhigh, max or ultra")
	}
}

// TranslateConfigResponse 是管理端读取的翻译配置**脱敏视图**。
//
// 密钥永远不回显：即使端点仅管理员可读，把密钥送进浏览器也意味着它会出现在
// DevTools、截图与前端状态里。设置页只需要知道「是否已配置」，编辑时留空表示
// 保持现值。
type TranslateConfigResponse struct {
	Enabled          bool   `json:"enabled"`
	URL              string `json:"url"`
	Model            string `json:"model"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
	MaxTokens        int    `json:"max_tokens"`
	APIKeyConfigured bool   `json:"api_key_configured"`
	Configured       bool   `json:"configured"`
	// PromptTemplate 是**生效中**的模板：未自定义时返回内置默认值，
	// 设置页可以直接在文本框里编辑。
	PromptTemplate string `json:"prompt_template"`
	// ThinkingEffort 是当前思维强度档位；空串表示不发送该字段。
	ThinkingEffort string `json:"thinking_effort"`
}

// ListModelsRequest 查询某个 OpenAI 兼容服务支持哪些模型。
//
// URL 与 APIKey 都可省略：省略 URL 表示用已保存的地址；省略/空 APIKey
// 表示沿用已保存的密钥（设置页永远拿不到明文密钥）。
type ListModelsRequest struct {
	URL    string `json:"url"`
	APIKey string `json:"api_key"`
}

// ListModelsResponse 是模型 id 列表，按上游返回顺序去重。
type ListModelsResponse struct {
	Models []string `json:"models"`
}

// 与 config 包的区间常量镜像。刻意不 import config：viewmodel 是 HTTP 边界，
// config 是持久化层；两边各自定义、各自测试，漂移时由 handler e2e 发现
// （保存越界值必须 400）。
const (
	MinTranslateTimeoutSeconds = 5
	MaxTranslateTimeoutSeconds = 600
	MinTranslateMaxTokens      = 256
	MaxTranslateMaxTokens      = 32768
)
