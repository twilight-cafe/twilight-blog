// Package translate 调用 OpenAI 兼容的 chat/completions 接口翻译文章。
//
// 刻意只实现一个协议子集：`POST <url>/chat/completions` + `Authorization: Bearer`。
// 大多数兼容服务（OpenAI、vLLM、Ollama 的 OpenAI 端点、各类中转）都支持它；
// 为差异更大的厂商增加分支，只会让「配了却调不通」更难排查。
//
// 本包不碰 HTTP 请求解析，也不读取配置：调用方把已经校验过的 Config 与 Request
// 传进来。这样 handler 能专注映射状态码，而翻译逻辑可以只用 httptest 验证。
package translate

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"time"
)

// Language 是文章语言。只支持站点已有的两套版本：中文与英文。
type Language string

const (
	LanguageChinese Language = "zh"
	LanguageEnglish Language = "en"
)

// ParseLanguage 解析语言代码；空串与未知值都返回错误。
//
// 不做大小写折叠是刻意的：语言代码会直接进入提示词与日志，接受 "ZH" 只会让
// 两套写法同时存在于配置与请求里，而它们没有语义差别。
func ParseLanguage(value string) (Language, error) {
	switch Language(value) {
	case LanguageChinese:
		return LanguageChinese, nil
	case LanguageEnglish:
		return LanguageEnglish, nil
	default:
		return "", fmt.Errorf("unsupported language %q: must be zh or en", value)
	}
}

// nativeName 返回提示词里使用的语言名。
//
// 只接受两个已知值；调用方必须先用 ParseLanguage 校验。未知值退化成代码本身，
// 而不是空串——空串会让提示词变成「Translate from  to」，模型只能猜。
func (l Language) nativeName() string {
	switch l {
	case LanguageChinese:
		return "Chinese (Simplified)"
	case LanguageEnglish:
		return "English"
	default:
		return string(l)
	}
}

// Request 是一次翻译的输入。Title 允许为空吗？不允许：文章必须有标题，
// 而「只翻译正文」在本站没有入口（编辑器总是带上当前页签的标题）。
type Request struct {
	Title   string
	Content string
	From    Language
	To      Language
}

// Result 是翻译结果。Title 恒非空；Content 允许为空（对应原文正文为空）。
type Result struct {
	Title   string
	Content string
}

// Config 是单次调用需要的配置。Timeout 为 0 表示不额外设置客户端超时，
// MaxTokens 为 0 表示不发送该字段（交给上游默认值）。
type Config struct {
	URL       string
	APIKey    string
	Model     string
	Timeout   time.Duration
	MaxTokens int
	// PromptTemplate 是自定义提示词模板；空串表示使用内置默认模板。
	PromptTemplate string
	// ThinkingEffort 是思维强度（minimal / low / medium / high / xhigh / max / ultra）；
	// 空串表示不发送 reasoning_effort 字段，交给模型默认值。
	ThinkingEffort string
}

// defaultPromptTemplate 是随二进制嵌入的默认模板。把它放在独立文件里
// （prompt.tmpl）而不是 Go 字符串常量，是为了让「翻译提示词」能被直接阅读与
// diff；设置页保存的自定义模板优先于它。
//
//go:embed prompt.tmpl
var defaultPromptTemplate string

// DefaultPromptTemplate 返回内置模板，设置页用它预填输入框。
func DefaultPromptTemplate() string {
	return defaultPromptTemplate
}

// 预先定义的可判别错误。handler 据此映射 503 / 502 / 504；
// 其余错误一律视为上游不可用（502），不把原始文本回显给客户端。
var (
	ErrNotConfigured = errors.New("translation is not configured")
	ErrInvalidInput  = errors.New("invalid translation input")
	ErrInvalidOutput = errors.New("translation provider returned an unexpected response")
	ErrTimeout       = errors.New("translation request timed out")
	// ErrInvalidPrompt 表示自定义提示词模板无法解析或渲染。正常保存时
	// viewmodel / config 已经校验过，这里兜的是手工改 settings.json 的情况。
	ErrInvalidPrompt = errors.New("translation prompt template is invalid")
)

// UpstreamError 表示上游返回了非 2xx 状态。只保留状态码：响应体可能包含
// 请求内容或厂商内部信息，既不写日志也不回显给客户端。
type UpstreamError struct {
	StatusCode int
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("translation provider responded with HTTP %d", e.StatusCode)
}

// maxResponseBytes 限制读取上游响应体的体积。正常响应是几 KB 到几十 KB；
// 没有上限时，一个被劫持或配错的上游可以用一个无限流把内存吃满。
const maxResponseBytes = 8 << 20

// newHTTPClient 是测试的间接层：生产按 Timeout 构造真实客户端，测试可替换成
// 一个返回受控错误的 Transport，从而验证超时与网络错误分支而不必真的等待。
var newHTTPClient = func(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// newRequest 同样是测试的间接层：URL 已经过 resolveEndpoint 校验，真实世界里
// http.NewRequestWithContext 几乎不会失败；保留这条错误路径是为了让格式错误的
// 配置以一种可解释的方式返回，而不是 panic。
var newRequest = http.NewRequestWithContext

// Translate 调用一次 chat/completions 并解析出译文。
//
// 输入与输出的正文都不会进入日志或错误文本。错误只带状态码/类别，
// 因为错误最终可能被 handler 回显给浏览器。
func Translate(ctx context.Context, cfg Config, in Request) (Result, error) {
	if strings.TrimSpace(cfg.URL) == "" ||
		strings.TrimSpace(cfg.APIKey) == "" ||
		strings.TrimSpace(cfg.Model) == "" {
		return Result{}, ErrNotConfigured
	}
	if strings.TrimSpace(in.Title) == "" {
		return Result{}, fmt.Errorf("%w: title is required", ErrInvalidInput)
	}
	if in.From == in.To {
		return Result{}, fmt.Errorf("%w: source and target languages must differ", ErrInvalidInput)
	}
	if _, err := ParseLanguage(string(in.From)); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if _, err := ParseLanguage(string(in.To)); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	endpoint, err := resolveEndpoint(cfg.URL)
	if err != nil {
		return Result{}, fmt.Errorf("invalid translation endpoint: %w", err)
	}

	prompt, err := renderPrompt(cfg.PromptTemplate, in)
	if err != nil {
		return Result{}, err
	}

	// 只包含字符串与数字字段，json.Marshal 不会失败；这里刻意不写一个
	// 永远为 nil 的错误分支（那会变成一条无法覆盖的死代码）。
	payload, _ := json.Marshal(chatRequest{
		Model: cfg.Model,
		Messages: []chatMessage{
			// 整个提示词（含规则与待翻译文档）由模板一次渲染成一条消息。
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
		MaxTokens:   cfg.MaxTokens,
		// 空串被 omitempty 丢掉：未配置思维强度时请求体与旧版本完全一致。
		ReasoningEffort: strings.TrimSpace(cfg.ThinkingEffort),
	})

	req, err := newRequest(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return Result{}, fmt.Errorf("build translation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.APIKey))

	client := newHTTPClient(cfg.Timeout)
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("%w: %v", ErrTimeout, err)
		}
		return Result{}, fmt.Errorf("perform translation request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, &UpstreamError{StatusCode: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Result{}, fmt.Errorf("read translation response: %w", err)
	}

	var completion chatResponse
	if err := json.Unmarshal(body, &completion); err != nil {
		return Result{}, fmt.Errorf("%w: decode completion: %v", ErrInvalidOutput, err)
	}
	if len(completion.Choices) == 0 || strings.TrimSpace(completion.Choices[0].Message.Content) == "" {
		return Result{}, fmt.Errorf("%w: completion contains no message", ErrInvalidOutput)
	}

	translated, err := decodeTranslated(completion.Choices[0].Message.Content)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(translated.Title) == "" {
		return Result{}, fmt.Errorf("%w: translated title is empty", ErrInvalidOutput)
	}
	return Result{
		Title:   strings.TrimSpace(translated.Title),
		Content: translated.Content,
	}, nil
}

// resolveEndpoint 接受「基址」与「完整端点」两种写法。
//
// 基址（例如 https://api.openai.com/v1）补齐 /chat/completions；已经以它结尾的
// 完整地址原样使用。用 url.Parse 而不是字符串拼接，是为了保留查询串
// （Azure 一类的 `?api-version=` 就靠它），同时避免把路径拼到 `?` 后面。
func resolveEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", errors.New("host is required")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/chat/completions") {
		path += "/chat/completions"
	}
	parsed.Path = path
	return parsed.String(), nil
}

// resolveModelsEndpoint 由同一个基址推导出 /models。
//
// 调用方可能填的是基址、/v1、完整的 /chat/completions，或已经写好的 /models；
// 三种形态都归一到「基址 + /models」，保留查询串（Azure 的 api-version）。
func resolveModelsEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", errors.New("host is required")
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, "/models"):
		// 已经是模型端点，保持原样。
	case strings.HasSuffix(path, "/chat/completions"):
		path = strings.TrimSuffix(path, "/chat/completions") + "/models"
	default:
		path += "/models"
	}
	parsed.Path = path
	return parsed.String(), nil
}

// ListModels 查询上游支持哪些模型 id。
//
// 只要求 URL；APIKey 可以为空——本地 Ollama / vLLM 常常不校验密钥。
// 上游返回的常见形态有三种（data 数组、models 数组、顶层数组），这里都接受，
// 并按原顺序去重。空列表是合法结果（有些服务确实没有可列出的模型）。
func ListModels(ctx context.Context, cfg Config) ([]string, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, ErrNotConfigured
	}
	endpoint, err := resolveModelsEndpoint(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid models endpoint: %w", err)
	}

	req, err := newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := newHTTPClient(cfg.Timeout).Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %v", ErrTimeout, err)
		}
		return nil, fmt.Errorf("perform models request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &UpstreamError{StatusCode: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read models response: %w", err)
	}
	return decodeModels(body)
}

// decodeModels 从上游响应里提取模型 id，兼容 data/models/顶层数组三种形态。
func decodeModels(body []byte) ([]string, error) {
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		for _, raw := range []json.RawMessage{envelope.Data, envelope.Models} {
			trimmed := bytes.TrimSpace(raw)
			if len(trimmed) == 0 {
				continue
			}
			if string(trimmed) == "[]" {
				// 明确返回空数组是合法结果，不是坏形态。
				return []string{}, nil
			}
			if models := decodeModelList(raw); len(models) > 0 {
				return dedupeModels(models), nil
			}
		}
	}
	if trimmed := bytes.TrimSpace(body); string(trimmed) == "[]" {
		return []string{}, nil
	}
	if models := decodeModelList(body); len(models) > 0 {
		return dedupeModels(models), nil
	}
	return nil, fmt.Errorf("%w: response contains no model list", ErrInvalidOutput)
}

// decodeModelList 接受 ["a","b"] 与 [{"id":"a"}] 两种数组形态。
func decodeModelList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err == nil {
		return names
	}
	var entries []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	models := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.ID != "" {
			models = append(models, entry.ID)
		} else if entry.Name != "" {
			models = append(models, entry.Name)
		}
	}
	return models
}

// dedupeModels 按原顺序去重并丢掉空串。
func dedupeModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	return result
}

// promptData 是模板可用的字段。
//
// Title / Content 是原文；TitleJSON / ContentJSON 是已经 JSON 转义过的版本
// （带引号）。默认模板用后者，避免标题或正文里的引号、换行破坏 JSON 结构。
type promptData struct {
	From        string
	To          string
	Title       string
	Content     string
	TitleJSON   string
	ContentJSON string
}

// renderPrompt 用 text/template 渲染一条完整的提示词。
//
// 模板为空时使用内置默认模板；自定义模板来自设置页。标题与正文仍然以 JSON
// 形式嵌入，这样它们内部的引号/换行不会破坏提示词结构。
func renderPrompt(source string, in Request) (string, error) {
	if strings.TrimSpace(source) == "" {
		source = defaultPromptTemplate
	}
	tmpl, err := template.New("prompt").Parse(source)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidPrompt, err)
	}
	titleJSON, _ := json.Marshal(in.Title)
	contentJSON, _ := json.Marshal(in.Content)
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, promptData{
		From:        in.From.nativeName(),
		To:          in.To.nativeName(),
		Title:       in.Title,
		Content:     in.Content,
		TitleJSON:   string(titleJSON),
		ContentJSON: string(contentJSON),
	}); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidPrompt, err)
	}
	return rendered.String(), nil
}

// chatRequest / chatResponse 只声明本包用到的字段。上游可能返回更多字段，
// encoding/json 会忽略它们。
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	// ReasoningEffort 只在配置了思维强度时出现：不支持该参数的服务收到它
	// 可能直接 400，而「不设置」必须保持与旧版本完全一致的请求体。
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

type translatedDocument struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// decodeTranslated 从模型输出里取出 JSON 对象。
//
// 即使要求「只返回 JSON」，模型仍可能包一层 ```json 围栏，或在前后加一句
// 说明。直接 Unmarshal 整段会因为这些无害的包装而失败，所以先剥围栏，
// 再截取第一个 `{` 到最后一个 `}`。截取后仍解析失败才算真正的坏响应。
func decodeTranslated(raw string) (translatedDocument, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "```") {
		// 去掉第一行（``` 或 ```json）与结尾的围栏；缺结尾围栏时只去掉开头，
		// 后面的截取逻辑仍然能工作。
		if newline := strings.IndexByte(trimmed, '\n'); newline >= 0 {
			trimmed = trimmed[newline+1:]
		} else {
			trimmed = strings.TrimPrefix(trimmed, "```")
		}
		trimmed = strings.TrimSpace(trimmed)
		if strings.HasSuffix(trimmed, "```") {
			trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "```"))
		}
	}

	start := strings.IndexByte(trimmed, '{')
	end := strings.LastIndexByte(trimmed, '}')
	if start < 0 || end < start {
		return translatedDocument{}, fmt.Errorf("%w: translated message contains no JSON object", ErrInvalidOutput)
	}

	var document translatedDocument
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &document); err != nil {
		return translatedDocument{}, fmt.Errorf("%w: decode translated document: %v", ErrInvalidOutput, err)
	}
	return document, nil
}
