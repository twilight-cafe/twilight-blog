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

// PageHandler 处理独立页面的公开读取与管理端增删改。
//
// 与 article.go 完全对称：DELETE 是软删、永久删除走 /permanent 且要求目标
// 已在回收站、批量接口静默忽略未知 id 并统一返回 204、id 非法返回 400。
// 差异只在数据形状——页面没有标签，也没有评论关联，只有标题与正文。
type PageHandler struct {
	service *service.PageService
}

func NewPageHandler(svc *service.PageService) *PageHandler {
	return &PageHandler{service: svc}
}

// List 对应 GET /api/pages（公开）。
func (h *PageHandler) List(c echo.Context) error {
	logging.Debugf("[handler:page] List called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	pages, total, err := h.service.List(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list pages")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewPageResponseList(pages), total, p.Page, p.PageSize))
}

// Get 对应 GET /api/pages/:id（公开）。
func (h *PageHandler) Get(c echo.Context) error {
	logging.Debugf("[handler:page] Get called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid page id")
	}

	page, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, model.ErrPageNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "page not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get page")
	}

	return c.JSON(http.StatusOK, viewmodel.NewPageResponse(page))
}

// Create 对应 POST /api/admin/pages，成功返回 201。
func (h *PageHandler) Create(c echo.Context) error {
	logging.Debugf("[handler:page] Create called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.CreatePageRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	page, err := h.service.Create(c.Request().Context(), req.Title, req.TitleEN, req.Content, req.ContentEN)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create page")
	}

	return c.JSON(http.StatusCreated, viewmodel.NewPageResponse(page))
}

// Update 对应 PUT /api/admin/pages/:id，覆盖式更新。
func (h *PageHandler) Update(c echo.Context) error {
	logging.Debugf("[handler:page] Update called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid page id")
	}

	var req viewmodel.UpdatePageRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	page, err := h.service.Update(c.Request().Context(), id, req.Title, req.TitleEN, req.Content, req.ContentEN)
	if err != nil {
		if errors.Is(err, model.ErrPageNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "page not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update page")
	}

	return c.JSON(http.StatusOK, viewmodel.NewPageResponse(page))
}

// Delete 对应 DELETE /api/admin/pages/:id，软删进回收站；未知 id 也返回 204。
func (h *PageHandler) Delete(c echo.Context) error {
	logging.Debugf("[handler:page] Delete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid page id")
	}

	if err := h.service.SoftDelete(c.Request().Context(), []int64{id}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete page")
	}

	return c.NoContent(http.StatusNoContent)
}

// BatchDelete 对应 DELETE /api/admin/pages。
func (h *PageHandler) BatchDelete(c echo.Context) error {
	logging.Debugf("[handler:page] BatchDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.SoftDelete(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete pages")
	}

	return c.NoContent(http.StatusNoContent)
}

// Restore 对应 POST /api/admin/pages/:id/restore。
func (h *PageHandler) Restore(c echo.Context) error {
	logging.Debugf("[handler:page] Restore called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid page id")
	}

	if err := h.service.Restore(c.Request().Context(), id); err != nil {
		if errors.Is(err, model.ErrPageNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "page not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to restore page")
	}

	return c.NoContent(http.StatusNoContent)
}

// ListDeleted 对应 GET /api/admin/trash/pages。
func (h *PageHandler) ListDeleted(c echo.Context) error {
	logging.Debugf("[handler:page] ListDeleted called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	pages, total, err := h.service.ListDeleted(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list deleted pages")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewPageResponseList(pages), total, p.Page, p.PageSize))
}

// BatchRestore 对应 POST /api/admin/pages/batch-restore。
func (h *PageHandler) BatchRestore(c echo.Context) error {
	logging.Debugf("[handler:page] BatchRestore called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.BatchRestore(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to restore pages")
	}

	return c.NoContent(http.StatusNoContent)
}

// PermanentDelete 对应 DELETE /api/admin/pages/:id/permanent，目标须已在回收站。
func (h *PageHandler) PermanentDelete(c echo.Context) error {
	logging.Debugf("[handler:page] PermanentDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid page id")
	}

	if err := h.service.HardDelete(c.Request().Context(), []int64{id}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to permanently delete page")
	}

	return c.NoContent(http.StatusNoContent)
}

// BatchPermanentDelete 对应 DELETE /api/admin/pages/batch-permanent。
func (h *PageHandler) BatchPermanentDelete(c echo.Context) error {
	logging.Debugf("[handler:page] BatchPermanentDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.HardDelete(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to permanently delete pages")
	}

	return c.NoContent(http.StatusNoContent)
}
