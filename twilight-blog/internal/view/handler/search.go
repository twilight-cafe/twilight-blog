package handler

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/search"
	"blog-engine/internal/viewmodel"

	"blog-engine/internal/logging"
)

// maxSearchQueryLength 是搜索关键词的**字符数**上限。
//
// 存在的理由有两条，都不是性能：
//   - 这个参数会原样进入日志（下方 Debugf 里的 query=%q），也原样发给 Meilisearch。
//     没有上限时，一个请求就能把 20 000 字节塞进日志与下游（实测旧版本接受这么大的 q）；
//     配合 10 MiB 的日志轮转阈值，这足以把真实记录快速挤出保留窗口。
//   - 它没有任何正当用途：真实搜索词不会有 200 个字符。
//
// 用字符数而不是字节数，理由与昵称、评论一致：一个中文词按字节算会凭空多算两倍。
const maxSearchQueryLength = 200

// 全文搜索端点。
//
// 搜索是可选能力：Meilisearch 未安装或未就绪时 client 为 nil，
// 此时本端点返回 500 而**不是**空结果——让「搜索不可用」与「没有匹配」
// 在语义上可区分，前端据此提示用户，而不是展示一个会误导人的空列表。
//
// SearchHandler 直接持有搜索客户端，不经 service 层：搜索不产生写入，
// 也没有可复用的业务规则，因此没有对应的 service。
type SearchHandler struct {
	client *search.Client
}

func NewSearchHandler(client *search.Client) *SearchHandler {
	return &SearchHandler{client: client}
}

// Search 对应 GET /api/search，查询参数 q 与分页参数。
//
// q 去空白后必须非空；分页复用 parsePagination，因此同样受 maxPageSize 约束。
// 下游调用使用请求自带的 context，客户端断开时会一并取消对 Meilisearch 的请求。
func (h *SearchHandler) Search(c echo.Context) error {
	logging.Debugf("[handler:search] Search called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	if h.client == nil {
		logging.Errorf("[handler:search] search unavailable: client is nil")
		return echo.NewHTTPError(http.StatusInternalServerError, "search is not available")
	}

	query := strings.TrimSpace(c.QueryParam("q"))
	if query == "" {
		logging.Debugf("[handler:search] missing q parameter method=%s path=%s", c.Request().Method, c.Request().URL.Path)
		return echo.NewHTTPError(http.StatusBadRequest, "q is required")
	}
	if utf8.RuneCountInString(query) > maxSearchQueryLength {
		logging.Debugf("[handler:search] over-long q parameter length=%d", utf8.RuneCountInString(query))
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("q must be at most %d characters", maxSearchQueryLength))
	}

	p, err := parsePagination(c)
	if err != nil {
		logging.Debugf("[handler:search] invalid pagination: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// lang 决定只搜哪一种语言的标题/正文；缺省按中文（旧客户端的行为）。
	lang := search.ParseLanguage(c.QueryParam("lang"))

	results, total, err := h.client.Search(c.Request().Context(), query, p.Page, p.PageSize, lang)
	if err != nil {
		logging.Errorf("[handler:search] search failed query=%q: %v", query, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to search")
	}
	logging.Debugf("[handler:search] search success query=%q lang=%s hits=%d total=%d", query, lang, len(results), total)
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(results, total, p.Page, p.PageSize))
}
