package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/viewmodel"

	"blog-engine/internal/logging"
)

// 标签端点。
//
// 标签没有独立的生命周期：它由文章写入时的 tags 字段自动产生（见 tag_service.go），
// 所以这里没有 Create / Delete，只有查询与改名。
// 改名是**全量**的，会波及所有关联文章，因此不允许重名（409）。
type TagHandler struct {
	service *service.TagService
}

func NewTagHandler(svc *service.TagService) *TagHandler {
	return &TagHandler{service: svc}
}

// List 对应 GET /api/tags（公开）。
func (h *TagHandler) List(c echo.Context) error {
	logging.Debugf("[handler:tag] List called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	tags, total, err := h.service.List(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list tags")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewTagResponseList(tags), total, p.Page, p.PageSize))
}

// ListArticles 对应 GET /api/tags/:id/articles（公开）。
func (h *TagHandler) ListArticles(c echo.Context) error {
	logging.Debugf("[handler:tag] ListArticles called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid tag id")
	}

	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	articles, total, err := h.service.ListArticles(c.Request().Context(), id, p)
	if err != nil {
		if errors.Is(err, model.ErrTagNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "tag not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list articles by tag")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewArticleResponseList(articles), total, p.Page, p.PageSize))
}

// Count 对应 GET /api/tags/:id/count（公开），只返回计数不返回列表，
// 供标签云之类的界面省下一次完整查询。
func (h *TagHandler) Count(c echo.Context) error {
	logging.Debugf("[handler:tag] Count called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid tag id")
	}

	count, err := h.service.Count(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, model.ErrTagNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "tag not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count articles by tag")
	}
	return c.JSON(http.StatusOK, viewmodel.CountResponse{Count: count})
}

// Rename 对应 PUT /api/admin/tags/:id，重命名一个标签，影响所有关联文章。
//
// 标签名在库里有唯一约束，重名返回 409 而不是默默合并——合并会让某批文章的
// 标签无声消失，语义上更难解释。
func (h *TagHandler) Rename(c echo.Context) error {
	logging.Debugf("[handler:tag] Rename called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid tag id")
	}

	var req viewmodel.RenameTagRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	tag, err := h.service.Rename(c.Request().Context(), id, req.Name, req.NameEN)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrInvalidTagName):
			return echo.NewHTTPError(http.StatusBadRequest, "invalid tag name")
		case errors.Is(err, model.ErrTagNotFound):
			return echo.NewHTTPError(http.StatusNotFound, "tag not found")
		case errors.Is(err, model.ErrTagNameExists):
			return echo.NewHTTPError(http.StatusConflict, "tag name already exists")
		default:
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to rename tag")
		}
	}

	return c.JSON(http.StatusOK, viewmodel.NewTagResponse(tag))
}
