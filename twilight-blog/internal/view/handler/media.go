package handler

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/viewmodel"

	"blog-engine/internal/logging"
)

// 媒体端点。
//
// 读公开、写限管理员：GET /api/media/:id 不需要鉴权，上传/列表/删除在 /api/admin 下。
// 由于读取只靠一个整数 ID，媒体实际上是「可枚举」的——这对博客配图是合理的
// （图片本来就要给所有人看），但不要拿它存任何机密文件。
type MediaHandler struct {
	service *service.MediaService
}

func NewMediaHandler(svc *service.MediaService) *MediaHandler {
	return &MediaHandler{service: svc}
}

// Upload 对应 POST /api/admin/media，接收 multipart 批量上传。
//
// 字段名同时接受 "files"（复数，前端在用）与 "file"（单数，兼容旧调用）。
// 大小与类型校验发生在 service.StoreFile，不在这一层。
//
// 批量上传采用**补偿式回滚**而非事务：中途失败时逐个删掉本批已落盘的媒体。
// 回滚本身失败只记 Warn 日志、不掩盖原始错误——调用方更需要知道上传为什么失败。
// 清理使用 context.Background()，因为此刻请求 context 可能已被取消，
// 但补偿动作必须执行完。
func (h *MediaHandler) Upload(c echo.Context) error {
	logging.Debugf("[handler:media] Upload called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	form, err := c.MultipartForm()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid multipart form")
	}

	files := form.File["files"]
	if len(files) == 0 {
		files = form.File["file"]
	}
	if len(files) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "files is required")
	}

	stored := make([]*model.Media, 0, len(files))
	for _, fileHeader := range files {
		media, err := h.service.StoreFile(c.Request().Context(), fileHeader)
		if err != nil {
			// Roll back the files stored earlier in this batch so a partially
			// failed upload does not leave orphaned media behind. A failure here
			// is reported but does not mask the original error: the caller still
			// needs to know why the upload failed.
			for _, created := range stored {
				if err := h.service.Delete(context.Background(), created.ID); err != nil {
					logging.Warnf("[handler:media] rollback of stored media failed id=%d: %v", created.ID, err)
				}
			}
			// 错误映射分三类，客户端的处置方式各不相同：
			//   - 400：这个文件本身不行（类型不支持、空文件、图片头读不出来），
			//     重试同一个文件没有意义；
			//   - 413：图片声明的解码尺寸超出预算（解压炸弹防线），换一张图；
			//   - 503：转码槽位被占满，属于**暂时**状态，稍后重试即可。
			//     它绝不能落到 500，否则调用方会以为请求本身有问题。
			switch {
			case errors.Is(err, service.ErrUnsupportedMediaType), errors.Is(err, service.ErrEmptyFile):
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			case errors.Is(err, service.ErrImageTooLarge):
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, err.Error())
			case errors.Is(err, service.ErrBusy):
				return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
			default:
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to store media")
			}
		}
		stored = append(stored, media)
	}

	mediaList := make([]model.Media, 0, len(stored))
	for _, media := range stored {
		mediaList = append(mediaList, *media)
	}
	return c.JSON(http.StatusCreated, viewmodel.NewMediaResponseList(mediaList))
}

// Get 对应 GET /api/media/:id（公开），以流式响应返回文件本体，
// 并允许客户端缓存一天（媒体内容按 StorageName 不可变，可以放心长缓存）。
//
// 「库里有记录但文件已从磁盘消失」返回 404 而不是 500：这是可恢复状态
// （例如有人手工删了文件），不该报成服务端故障。
func (h *MediaHandler) Get(c echo.Context) error {
	logging.Debugf("[handler:media] Get called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid media id")
	}

	media, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, model.ErrMediaNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "media not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get media")
	}

	// Path 会对数据库里的 storage_name 做校验（R7-02：恶意归档可以把一行
	// `../../..` 还原进来，未校验时这里就是任意文件读取）。它返回的错误是
	// **数据损坏**而不是「文件不在」，所以落到 500 而不是静默 404——后者会让
	// 库里那行坏数据永远不为人知。
	path, err := h.service.Path(media)
	if err != nil {
		logging.Errorf("[handler:media] refusing to serve media id=%d: %v", media.ID, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to open media")
	}

	// 数据库里的 MIME 同样不可信：restore 可以还原任意 blog.db。公开接口绝不能
	// 把 text/html / application/javascript / SVG 之类原样回放成同源页面或脚本。
	contentType, err := h.service.ContentType(media)
	if err != nil {
		logging.Errorf("[handler:media] refusing to serve media id=%d: %v", media.ID, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to open media")
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return echo.NewHTTPError(http.StatusNotFound, "media file not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to open media")
	}
	defer f.Close()

	c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=86400")
	return c.Stream(http.StatusOK, contentType, f)
}

// List 对应 GET /api/admin/media，分页列出媒体库。
func (h *MediaHandler) List(c echo.Context) error {
	logging.Debugf("[handler:media] List called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	media, total, err := h.service.List(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list media")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewMediaResponseList(media), total, p.Page, p.PageSize))
}

// Delete 对应 DELETE /api/admin/media/:id，立即删除。
//
// 媒体**没有回收站**：数据库记录与磁盘文件一起删掉，这与文章/页面/评论的
// 「软删 + 回收站」模型不同。误删无法通过接口恢复。
func (h *MediaHandler) Delete(c echo.Context) error {
	logging.Debugf("[handler:media] Delete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid media id")
	}

	if err := h.service.Delete(c.Request().Context(), id); err != nil {
		if errors.Is(err, model.ErrMediaNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "media not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete media")
	}

	return c.NoContent(http.StatusNoContent)
}
