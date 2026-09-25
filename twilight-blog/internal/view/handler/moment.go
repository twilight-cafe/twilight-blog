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

// MomentHandler 处理动态（moments）的公开读取与管理端增删改。
//
// 与文章／页面同构的一整套 CRUD + 回收站，约定也一致：DELETE 是软删、
// /permanent 要求目标先在回收站、批量接口忽略未知 id 并统一返回 204。
// 差异在数据形状：正文是纯文本，关联的是媒体列表（MediaIDs）而非标签。
//
// 另注：永久删除动态时，moment_media 的关联行由外键 ON DELETE CASCADE 负责清理
// （database/sqlite.go 启动时会校验 foreign_keys=1），仓储层不显式删它。
// 那里一度有过一句显式删除，但它没有 deleted_at 条件，于是对未软删的动态会造成
// 「主体没删掉、关联却已被清空」的损坏，已移除。
type MomentHandler struct {
	service *service.MomentService
}

func NewMomentHandler(svc *service.MomentService) *MomentHandler {
	return &MomentHandler{service: svc}
}

// List 对应 GET /api/moments（公开）。
func (h *MomentHandler) List(c echo.Context) error {
	logging.Debugf("[handler:moment] List called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	moments, total, err := h.service.List(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list moments")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewMomentResponseList(moments), total, p.Page, p.PageSize))
}

// Get 对应 GET /api/moments/:id（公开）。
func (h *MomentHandler) Get(c echo.Context) error {
	logging.Debugf("[handler:moment] Get called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid moment id")
	}

	moment, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, model.ErrMomentNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "moment not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get moment")
	}

	return c.JSON(http.StatusOK, viewmodel.NewMomentResponse(moment))
}

// Create 对应 POST /api/admin/moments，成功返回 201。
// MediaIDs 省略等价于「没有配图」——动态没有「保留原有配图」这一说，新建时本就没有。
func (h *MomentHandler) Create(c echo.Context) error {
	logging.Debugf("[handler:moment] Create called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.CreateMomentRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	moment, err := h.service.Create(c.Request().Context(), req.Content, req.MediaIDs)
	if err != nil {
		return mapMomentError(err, "failed to create moment")
	}

	return c.JSON(http.StatusCreated, viewmodel.NewMomentResponse(moment))
}

// Update 对应 PUT /api/admin/moments/:id。
// MediaIDs 传 nil（省略）保留原有配图，传空数组则清空，语义与文章的 Tags 相同。
func (h *MomentHandler) Update(c echo.Context) error {
	logging.Debugf("[handler:moment] Update called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid moment id")
	}

	var req viewmodel.UpdateMomentRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	moment, err := h.service.Update(c.Request().Context(), id, req.Content, req.MediaIDs)
	if err != nil {
		if errors.Is(err, model.ErrMomentNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "moment not found")
		}
		return mapMomentError(err, "failed to update moment")
	}

	return c.JSON(http.StatusOK, viewmodel.NewMomentResponse(moment))
}

// Delete 对应 DELETE /api/admin/moments/:id，软删进回收站；未知 id 也返回 204。
func (h *MomentHandler) Delete(c echo.Context) error {
	logging.Debugf("[handler:moment] Delete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid moment id")
	}

	if err := h.service.SoftDelete(c.Request().Context(), []int64{id}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete moment")
	}

	return c.NoContent(http.StatusNoContent)
}

// BatchDelete 对应 DELETE /api/admin/moments。
func (h *MomentHandler) BatchDelete(c echo.Context) error {
	logging.Debugf("[handler:moment] BatchDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.SoftDelete(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete moments")
	}

	return c.NoContent(http.StatusNoContent)
}

// Restore 对应 POST /api/admin/moments/:id/restore。
func (h *MomentHandler) Restore(c echo.Context) error {
	logging.Debugf("[handler:moment] Restore called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid moment id")
	}

	if err := h.service.Restore(c.Request().Context(), id); err != nil {
		if errors.Is(err, model.ErrMomentNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "moment not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to restore moment")
	}

	return c.NoContent(http.StatusNoContent)
}

// ListDeleted 对应 GET /api/admin/trash/moments。
func (h *MomentHandler) ListDeleted(c echo.Context) error {
	logging.Debugf("[handler:moment] ListDeleted called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	moments, total, err := h.service.ListDeleted(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list deleted moments")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewMomentResponseList(moments), total, p.Page, p.PageSize))
}

// BatchRestore 对应 POST /api/admin/moments/batch-restore。
func (h *MomentHandler) BatchRestore(c echo.Context) error {
	logging.Debugf("[handler:moment] BatchRestore called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.BatchRestore(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to restore moments")
	}

	return c.NoContent(http.StatusNoContent)
}

// PermanentDelete 对应 DELETE /api/admin/moments/:id/permanent，目标须已在回收站。
func (h *MomentHandler) PermanentDelete(c echo.Context) error {
	logging.Debugf("[handler:moment] PermanentDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid moment id")
	}

	if err := h.service.HardDelete(c.Request().Context(), []int64{id}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to permanently delete moment")
	}

	return c.NoContent(http.StatusNoContent)
}

// BatchPermanentDelete 对应 DELETE /api/admin/moments/batch-permanent。
func (h *MomentHandler) BatchPermanentDelete(c echo.Context) error {
	logging.Debugf("[handler:moment] BatchPermanentDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.HardDelete(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to permanently delete moments")
	}

	return c.NoContent(http.StatusNoContent)
}

// mapMomentError 把动态写入路径上的 service 错误映射成 HTTP 响应。
//
// **调用方保证 err 非 nil**（两处调用都在 `if err != nil` 之后），所以这里不再
// 有一个 `if err == nil { return nil }` 的守卫——那是不可达的代码，而不可达分支
// 唯一的作用就是骗出一个只为凑覆盖率的假测试。与 mapArticleError 同构。
//
// 写入动态只有两类可预期的失败：
//   - *ValidationError：配图不合法（不存在、不是图片、id 非正数）—— 400。
//     它替代了此前那个裸 500：客户端给错了一个 media id 是客户端错误，
//     报成服务端故障会让调用方以为「代码有问题」而不是「那张图没了」；
//   - 其余一律落 default，记 Error 日志并回答 500。这条日志是必要的——
//     Create 分支此前完全没有错误日志，于是「保存动态失败」在 log.txt 里
//     一个字都找不到，只能靠复现。真的走到这里说明有一个没被映射的错误类型，
//     那正是需要被看见的那种事。
func mapMomentError(err error, fallback string) error {
	var validationErr *service.ValidationError
	switch {
	case errors.As(err, &validationErr):
		return echo.NewHTTPError(http.StatusBadRequest, validationErr.Message)
	default:
		logging.Errorf("[handler:moment] unhandled error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, fallback)
	}
}
