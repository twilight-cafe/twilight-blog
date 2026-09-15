// Package handler 实现 HTTP 端点。
//
// 这一层只做三件事：解析并校验请求、调用 service、把结果与错误映射成 HTTP 响应。
// 业务规则一律下沉到 service，所以这里读起来应当很薄——若某个 handler 里出现了
// 条件分支之外的业务判断，多半是放错了层。
//
// 三个贯穿全包的结构性约定：
//   - 单条操作与批量操作共用同一个 service 方法（单条只是把 id 包成长度 1 的切片），
//     于是删除类接口**不校验目标是否存在**：未知 id 被 SQL 静默忽略，接口照样返回 204。
//     客户端不能把 204 当作「这些 id 全部处理成功」的证据。
//   - 路径参数统一走 parseID，非法或 <= 0 的 id 返回 400（参数不合法）而非 404（资源不存在）。
//   - 分页统一走 parsePagination，page 与 page_size 都是必填。
//
// 鉴权不在本层判断：公开路由与 /api/admin 分组由 router.go 划分，
// handler 只管假设「能进来的请求已经有权限」。
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/viewmodel"

	"blog-engine/internal/logging"
)

// ArticleHandler 处理文章的公开读取与管理端增删改（含回收站）。
type ArticleHandler struct {
	service *service.ArticleService
}

func NewArticleHandler(svc *service.ArticleService) *ArticleHandler {
	return &ArticleHandler{service: svc}
}

// List 对应 GET /api/articles，公开只读，列出未删除的文章。
func (h *ArticleHandler) List(c echo.Context) error {
	logging.Debugf("[handler:article] List called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	articles, total, err := h.service.List(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list articles")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewArticleResponseList(articles), total, p.Page, p.PageSize))
}

// Get 对应 GET /api/articles/:id，公开。软删的文章在这里查不到，返回 404。
func (h *ArticleHandler) Get(c echo.Context) error {
	logging.Debugf("[handler:article] Get called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid article id")
	}

	article, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "article not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get article")
	}

	return c.JSON(http.StatusOK, viewmodel.NewArticleResponse(article))
}

// Create 对应 POST /api/admin/articles，成功返回 201。
//
// 校验分两道：形状与取值范围在 viewmodel（空标题、发布时间像不像秒级时间戳），
// 「这张图能不能当头图」在 service（要查库，形状校验做不了）。两道都会映射成 400。
func (h *ArticleHandler) Create(c echo.Context) error {
	logging.Debugf("[handler:article] Create called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.CreateArticleRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// CoverSet 在 Create 上没有「别动」可表达——新建时头图就是请求里给的那张
	// （没给即没有），所以这里恒为 true，由 CoverMediaID 决定结果。
	article, err := h.service.Create(c.Request().Context(), service.ArticleWrite{
		Title:        req.Title,
		Content:      &req.Content,
		Tags:         &req.Tags,
		PublishedAt:  req.PublishedAt,
		CoverSet:     true,
		CoverMediaID: req.CoverMediaID,
	})
	if err != nil {
		return mapArticleError(err, "failed to create article")
	}

	return c.JSON(http.StatusCreated, viewmodel.NewArticleResponse(article))
}

// Update 对应 PUT /api/admin/articles/:id，覆盖式更新。
//
// 最容易踩的一点：Tags 在请求体里省略时保留原有标签，传空数组则清空。
// 这个区别来自 viewmodel.UpdateArticleRequest 的指针字段，不是这里做的判断。
// 头图同理但多一态：省略 = 别动，null = 摘掉，数字 = 换成它（见 viewmodel.OptionalID）。
func (h *ArticleHandler) Update(c echo.Context) error {
	logging.Debugf("[handler:article] Update called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid article id")
	}

	var req viewmodel.UpdateArticleRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	article, err := h.service.Update(c.Request().Context(), id, service.ArticleWrite{
		Title:        req.Title,
		Content:      req.Content,
		Tags:         req.Tags,
		PublishedAt:  req.PublishedAt,
		CoverSet:     req.CoverMediaID.Set,
		CoverMediaID: req.CoverMediaID.Value,
	})
	if err != nil {
		return mapArticleError(err, "failed to update article")
	}

	return c.JSON(http.StatusOK, viewmodel.NewArticleResponse(article))
}

// Delete 对应 DELETE /api/admin/articles/:id，进回收站（软删）。
//
// 目标不存在时同样返回 204——单条删除走的就是批量方法，见包注释。
func (h *ArticleHandler) Delete(c echo.Context) error {
	logging.Debugf("[handler:article] Delete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid article id")
	}

	if err := h.service.SoftDelete(c.Request().Context(), []int64{id}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete article")
	}

	return c.NoContent(http.StatusNoContent)
}

// BatchDelete 对应 DELETE /api/admin/articles，体为 {"ids": [...]}。
// 一条 UPDATE ... WHERE id IN (...) 完成，未知 id 被静默忽略，统一返回 204。
func (h *ArticleHandler) BatchDelete(c echo.Context) error {
	logging.Debugf("[handler:article] BatchDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.SoftDelete(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete articles")
	}

	return c.NoContent(http.StatusNoContent)
}

// Restore 对应 POST /api/admin/articles/:id/restore，从回收站恢复。
// 对未删除的文章调用是幂等空操作，依然返回 204。
func (h *ArticleHandler) Restore(c echo.Context) error {
	logging.Debugf("[handler:article] Restore called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid article id")
	}

	if err := h.service.Restore(c.Request().Context(), id); err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "article not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to restore article")
	}

	return c.NoContent(http.StatusNoContent)
}

// ListDeleted 对应 GET /api/admin/trash/articles，即回收站列表。
func (h *ArticleHandler) ListDeleted(c echo.Context) error {
	logging.Debugf("[handler:article] ListDeleted called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	articles, total, err := h.service.ListDeleted(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list deleted articles")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewArticleResponseList(articles), total, p.Page, p.PageSize))
}

// BatchRestore 对应 POST /api/admin/articles/batch-restore。
// 仓储层会返回实际受影响的行数，这里并不检查它——所以「恢复了一部分」不会被察觉。
func (h *ArticleHandler) BatchRestore(c echo.Context) error {
	logging.Debugf("[handler:article] BatchRestore called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.BatchRestore(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to restore articles")
	}

	return c.NoContent(http.StatusNoContent)
}

// PermanentDelete 对应 DELETE /api/admin/articles/:id/permanent。
//
// 硬删除的前置条件是目标**已在回收站中**（仓储层 SQL 带 deleted_at IS NOT NULL）。
// 对一篇未删除的文章调用它不会删掉任何东西，但接口仍返回 204，
// 因为这里拿不到受影响行数。这是「不可逆操作必须经过回收站」的护栏，
// 别为了方便把它改成无条件删除。
func (h *ArticleHandler) PermanentDelete(c echo.Context) error {
	logging.Debugf("[handler:article] PermanentDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid article id")
	}

	if err := h.service.HardDelete(c.Request().Context(), []int64{id}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to permanently delete article")
	}

	return c.NoContent(http.StatusNoContent)
}

// BatchPermanentDelete 对应 DELETE /api/admin/articles/batch-permanent。
// 与单条永久删除同样受「目标须已在回收站」的约束。
func (h *ArticleHandler) BatchPermanentDelete(c echo.Context) error {
	logging.Debugf("[handler:article] BatchPermanentDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.HardDelete(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to permanently delete articles")
	}

	return c.NoContent(http.StatusNoContent)
}

// parseID 解析路径中的整数 id，并拒绝 <= 0 的值。
//
// 于是 /api/articles/abc 与 /api/articles/0 都返回 400（参数不合法），
// 而不是 404（资源不存在）。这个区分是有意的，别为了「更友好」把它们并成 404。
func parseID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

// mapArticleError 把文章写入路径上的 service 错误映射成 HTTP 响应。
//
// **调用方保证 err 非 nil**（两处调用都在 `if err != nil` 之后）：正因为如此，
// 这里不再有一个 `if err == nil { return nil }` 的守卫——那是不可达的代码，
// 而不可达分支唯一的作用就是骗出一个只为了凑覆盖率的假测试。
//
// 写入文章只有两类可预期的失败：
//   - *ValidationError：客户端给的头图不合法（不存在、不是图片），或者那篇
//     文章已经被删到回收站里了 —— 都该是 400；
//   - model.ErrNotFound：目标文章不存在或已被软删 —— 404。
//
// 其余一律落 default，记 Error 日志并回答 500。**刻意不打日志到 Debug**：
// 真的走到这里说明有一个没被映射的错误类型，那正是需要被看见的那种事。
func mapArticleError(err error, fallback string) error {
	var validationErr *service.ValidationError
	switch {
	case errors.As(err, &validationErr):
		return echo.NewHTTPError(http.StatusBadRequest, validationErr.Message)
	case errors.Is(err, model.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "article not found")
	default:
		logging.Errorf("[handler:article] unhandled error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, fallback)
	}
}
