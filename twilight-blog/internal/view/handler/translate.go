package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/logging"
	"blog-engine/internal/translate"
	"blog-engine/internal/viewmodel"
)

// translateText 是本包对翻译实现的间接层。生产值是 translate.Translate；
// 测试替换它来验证 400/502/503/504 的映射，而不必真的构造每一种上游故障。
var translateText = translate.Translate

// listModelsFn 是模型查询的间接层，测试替换它来覆盖状态码映射。
var listModelsFn = translate.ListModels

// TranslateHandler 负责 AI 翻译端点及其配置的读写。
//
// 它持有 *config.Config 而不是一份构造时拍下的配置：设置页保存后，下一次
// 翻译必须立刻使用新地址/模型，不能要求重启进程。
type TranslateHandler struct {
	config       *config.Config
	settingsPath string
}

func NewTranslateHandler(cfg *config.Config, settingsPath string) *TranslateHandler {
	return &TranslateHandler{config: cfg, settingsPath: settingsPath}
}

// Translate 对应 POST /api/admin/translate。
//
// 输入与输出都不写日志：正文是用户的未发布内容，翻译结果同样如此。
// 日志里最多出现语言方向与结果长度。
func (h *TranslateHandler) Translate(c echo.Context) error {
	logging.Debugf("[handler:translate] Translate called path=%s", c.Request().URL.Path)

	var req viewmodel.TranslateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	cfg := h.config.TranslateConfigValue()
	if !cfg.IsConfigured() {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "AI translation is not configured")
	}

	result, err := translateText(c.Request().Context(), translate.Config{
		URL:            cfg.URL,
		APIKey:         cfg.APIKey,
		Model:          cfg.Model,
		Timeout:        time.Duration(cfg.TimeoutSeconds) * time.Second,
		MaxTokens:      cfg.MaxTokens,
		PromptTemplate: cfg.PromptTemplate,
		ThinkingEffort: cfg.ThinkingEffort,
	}, translate.Request{
		Title:   req.Title,
		Content: req.Content,
		From:    translate.Language(req.From),
		To:      translate.Language(req.To),
	})
	if err != nil {
		return mapTranslateError(err)
	}

	logging.Infof("[translate] translated from=%s to=%s title_len=%d content_len=%d",
		req.From, req.To, len([]rune(result.Title)), len([]rune(result.Content)))
	return c.JSON(http.StatusOK, viewmodel.TranslateResponse{
		Title:   result.Title,
		Content: result.Content,
	})
}

// GetConfig 对应 GET /api/admin/translate/config（仅管理员）。
// 响应里没有密钥，只有「是否已配置」。
func (h *TranslateHandler) GetConfig(c echo.Context) error {
	logging.Debugf("[handler:translate] GetConfig called path=%s", c.Request().URL.Path)
	return c.JSON(http.StatusOK, h.configResponse())
}

// UpdateConfig 对应 PUT /api/admin/translate/config（仅管理员）。
//
// 写盘失败会把内存回滚成旧值：否则进程内用新配置、磁盘上还是旧配置，
// 重启后「刚才明明保存成功」变成无法复现的抱怨（与站点设置同一条规则）。
func (h *TranslateHandler) UpdateConfig(c echo.Context) error {
	logging.Debugf("[handler:translate] UpdateConfig called path=%s", c.Request().URL.Path)

	var req viewmodel.TranslateConfigRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	previous := h.config.TranslateConfigValue()
	next := previous
	if req.Enabled != nil {
		next.Enabled = req.Enabled
	}
	if req.URL != nil {
		next.URL = *req.URL
	}
	if req.Model != nil {
		next.Model = *req.Model
	}
	if req.TimeoutSeconds != nil {
		next.TimeoutSeconds = *req.TimeoutSeconds
	}
	if req.MaxTokens != nil {
		next.MaxTokens = *req.MaxTokens
	}
	// 空串 = 保持现有密钥。设置页永远拿不到已保存的密钥，所以它提交的空值
	// 只可能表示「这次没改」，而不是「清除」。
	if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		next.APIKey = *req.APIKey
	}
	if req.PromptTemplate != nil {
		next.PromptTemplate = *req.PromptTemplate
	}
	if req.ThinkingEffort != nil {
		next.ThinkingEffort = *req.ThinkingEffort
	}

	if err := h.config.SetTranslateConfig(next); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.config.Save(h.settingsPath); err != nil {
		// previous 来自同一配置对象，且它此前已经通过 Load/Save 的校验；
		// 回滚不会失败，因此不写一条永远为 nil 的错误分支。
		_ = h.config.SetTranslateConfig(previous)
		logging.Errorf("[translate] persist settings failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save translation settings")
	}

	current := h.config.TranslateConfigValue()
	logging.Infof("[translate] translation settings updated configured=%t", current.IsConfigured())
	return c.JSON(http.StatusOK, h.configResponse())
}

// configResponse 把配置对象转成脱敏响应。GetConfig 与 UpdateConfig 共用，
// 避免两处响应漂移。
func (h *TranslateHandler) configResponse() viewmodel.TranslateConfigResponse {
	value := h.config.TranslateConfigValue()
	prompt := value.PromptTemplate
	if strings.TrimSpace(prompt) == "" {
		prompt = translate.DefaultPromptTemplate()
	}
	return viewmodel.TranslateConfigResponse{
		Enabled:          value.IsEnabled(),
		URL:              value.URL,
		Model:            value.Model,
		TimeoutSeconds:   value.TimeoutSeconds,
		MaxTokens:        value.MaxTokens,
		APIKeyConfigured: strings.TrimSpace(value.APIKey) != "",
		Configured:       value.IsConfigured(),
		PromptTemplate:   prompt,
		ThinkingEffort:   value.ThinkingEffort,
	}
}

// ListModels 对应 POST /api/admin/translate/models（仅管理员）。
//
// 请求可只带 URL；缺省 URL / APIKey 时沿用已保存的配置。它是一次只读的外部
// 调用，不会修改设置。上游返回的模型 id 原样交给设置页做下拉候选。
func (h *TranslateHandler) ListModels(c echo.Context) error {
	logging.Debugf("[handler:translate] ListModels called path=%s", c.Request().URL.Path)

	var req viewmodel.ListModelsRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	saved := h.config.TranslateConfigValue()
	url := strings.TrimSpace(req.URL)
	if url == "" {
		url = saved.URL
	}
	if url == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "url is required")
	}
	if err := config.ValidateTranslateURL(url); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		apiKey = saved.APIKey
	}

	models, err := listModelsFn(c.Request().Context(), translate.Config{
		URL:     url,
		APIKey:  apiKey,
		Timeout: time.Duration(saved.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		return mapTranslateError(err)
	}
	if models == nil {
		models = []string{}
	}
	logging.Infof("[translate] listed %d models", len(models))
	return c.JSON(http.StatusOK, viewmodel.ListModelsResponse{Models: models})
}

// mapTranslateError 把翻译失败映射成 HTTP 响应。
//
// 三类可预期的失败各有自己的状态码：
//   - 输入错误：400（正常流程里 Validate 已经挡住，这里是纵深防御）；
//   - 未配置：503（站长还没填配置，不是上游故障）；
//   - 超时：504；
//   - 上游非 2xx / 响应坏形态 / 网络错误：502。
//
// 响应正文只给固定文案。`err` 里可能出现上游地址（不含密钥）与 JSON 解析细节，
// 那些属于日志，不属于浏览器。
func mapTranslateError(err error) error {
	switch {
	case errors.Is(err, translate.ErrNotConfigured):
		return echo.NewHTTPError(http.StatusServiceUnavailable, "AI translation is not configured")
	case errors.Is(err, translate.ErrInvalidInput):
		return echo.NewHTTPError(http.StatusBadRequest, "invalid translation input")
	case errors.Is(err, translate.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		logging.Errorf("[translate] request timed out: %v", err)
		return echo.NewHTTPError(http.StatusGatewayTimeout, "translation timed out")
	case errors.Is(err, translate.ErrInvalidOutput):
		logging.Errorf("[translate] invalid provider response: %v", err)
		return echo.NewHTTPError(http.StatusBadGateway, "translation provider returned an invalid response")
	case errors.Is(err, translate.ErrInvalidPrompt):
		logging.Errorf("[translate] invalid prompt template: %v", err)
		return echo.NewHTTPError(http.StatusServiceUnavailable, "translation prompt template is invalid")
	default:
		var upstream *translate.UpstreamError
		if errors.As(err, &upstream) {
			logging.Errorf("[translate] provider responded with HTTP %d", upstream.StatusCode)
		} else {
			logging.Errorf("[translate] request failed: %v", err)
		}
		return echo.NewHTTPError(http.StatusBadGateway, "translation provider failed")
	}
}
