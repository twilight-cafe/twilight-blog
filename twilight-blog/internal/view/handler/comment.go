package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/session"
	"blog-engine/internal/viewmodel"

	"blog-engine/internal/logging"
)

// 评论端点。
//
// 读公开、写需要身份：GET /api/comments（按 target 查询）任何人都能调用，
// 发表与点赞/取消点赞必须带会话。
//
// 隐私边界（本文件最重要的一条）：**同一个列表接口会因调用者身份返回不同内容**。
// 只有管理员会话才会带回评论者的邮箱——由 isAdminRequest 与
// NewCommentResponseList 的布尔参数共同决定。普通访客永远看不到他人邮箱。
//
// 点赞的主体是**访客账号**，管理员不参与：没有会话返回 401，
// 有会话但不是访客账号（即管理员）返回 403。两者刻意区分开。
type CommentHandler struct {
	service *service.CommentService
	// adminName 动态读取当前管理员展示名，使管理员发表的评论标注为数据库里
	// 那个昵称，而不是写死的字面量。以函数而非字符串注入，改名后立即生效；
	// 它接受 context，因为展示名现在住在 users 表里，需要查一次库。
	adminName func(ctx context.Context) string
}

func NewCommentHandler(svc *service.CommentService, adminName func(ctx context.Context) string) *CommentHandler {
	return &CommentHandler{service: svc, adminName: adminName}
}

// isAdminRequest 判断请求是否携带了有效的管理员会话。
// 只有管理员发起的调用才会渲染评论者邮箱，普通访客永远看不到他人邮箱。
func isAdminRequest(c echo.Context) bool {
	return session.IsAdmin(c)
}

// List 对应 GET /api/comments（公开），按 target_type + target_id 列出评论。
//
// 传入当前登录的访客 ID，用于在响应里标记 liked_by_me（游客传 nil 即可）。
//
// 分页语义注意：total 只统计**根评论**，而返回的切片含其下的子孙回复，
// 所以 len(items) 可能大于 total——前端不能用 total 去校验条目数。
// 这是仓储层 comment_repo.go 的既定行为，此处只是把它透出去。
func (h *CommentHandler) List(c echo.Context) error {
	logging.Debugf("[handler:comment] List called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	targetType := c.QueryParam("target_type")
	targetID, err := strconv.ParseInt(c.QueryParam("target_id"), 10, 64)
	if err != nil || targetID <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid target_id")
	}

	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	comments, total, err := h.service.ListByTarget(c.Request().Context(), targetType, targetID, currentUserIDFromContext(c), p)
	if err != nil {
		if errors.Is(err, model.ErrInvalidCommentTarget) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid target_type")
		}
		// 目标内容不存在或已下架：此时不应返回 200 空列表，那会让「文章已删除」
		// 与「文章还在但还没人评论」在前端看起来一模一样。
		if errors.Is(err, model.ErrCommentTargetNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "comment target not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list comments")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewCommentResponseList(comments, isAdminRequest(c)), total, p.Page, p.PageSize))
}

// Like 对应 POST /api/comments/:id/like，由访客账号点赞。
//
// 管理员拿不到 200，而是 403。**这个判断必须显式写出来**：以前它由
// 「userID == nil」隐含提供（管理员的 UserID 恒为 nil），而管理员现在有真实账号行，
// 那个条件不再成立——不补这一句，管理员会悄悄获得点赞能力，往 comment_likes 里
// 塞一行指向管理员账号的记录，与「访客互动」的语义混在一起。
// 这类语义翻转不报错、不失败编译，只会静默改变授权结果。
//
// 没有会话才是 401。响应固定按「非管理员」渲染，绝不把第三方邮箱回显给点赞者。
func (h *CommentHandler) Like(c echo.Context) error {
	logging.Debugf("[handler:comment] Like called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid comment id")
	}

	userID := currentUserIDFromContext(c)
	if session.IsAdmin(c) {
		return echo.NewHTTPError(http.StatusForbidden, "only visitor accounts can like comments")
	}
	if userID == nil {
		if _, ok := session.From(c); !ok {
			return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
		}
		return echo.NewHTTPError(http.StatusForbidden, "only visitor accounts can like comments")
	}

	comment, err := h.service.Like(c.Request().Context(), id, *userID)
	if err != nil {
		if errors.Is(err, model.ErrCommentNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "comment not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to like comment")
	}
	// Liking is performed by ordinary commenters, so never echo a third party's
	// email address back to them.
	return c.JSON(http.StatusOK, viewmodel.NewCommentResponse(comment, false))
}

// Unlike 对应 DELETE /api/comments/:id/like。
// 身份要求与 Like 相同：仅访客账号，无会话 401、管理员 403。
// 「管理员 403」与 Like 一样必须显式判断，理由见那里的说明。
func (h *CommentHandler) Unlike(c echo.Context) error {
	logging.Debugf("[handler:comment] Unlike called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid comment id")
	}

	userID := currentUserIDFromContext(c)
	if session.IsAdmin(c) {
		return echo.NewHTTPError(http.StatusForbidden, "only visitor accounts can unlike comments")
	}
	if userID == nil {
		if _, ok := session.From(c); !ok {
			return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
		}
		return echo.NewHTTPError(http.StatusForbidden, "only visitor accounts can unlike comments")
	}

	comment, err := h.service.Unlike(c.Request().Context(), id, *userID)
	if err != nil {
		if errors.Is(err, model.ErrCommentNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "comment not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to unlike comment")
	}
	return c.JSON(http.StatusOK, viewmodel.NewCommentResponse(comment, false))
}

// Create 对应 POST /api/comments，成功返回 201。
//
// 署名由身份决定，请求体里没有昵称可填：
//   - 管理员：用配置里的站长展示名；
//   - 访客账号：用该账号自己的昵称（service 层从账号资料里读）。
//
// 两者都没有则 401——**未登录不能评论**，这条规则由路由上的 requireSession
// 兜住，这里再判一次是为了让直接调用 handler 的场景也拿到 401 而不是 500。
//
// 错误映射上有个易被误改的点：父评论不存在返回 400 而不是 404
// ——parent_id 是请求参数，指向不存在的对象属于客户端错误。
func (h *CommentHandler) Create(c echo.Context) error {
	logging.Debugf("[handler:comment] Create called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.CreateCommentRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	sess, ok := session.From(c)
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}

	var comment *model.Comment
	var err error
	if sess.IsAdmin() {
		adminName := "Admin"
		if h.adminName != nil {
			if name := h.adminName(c.Request().Context()); name != "" {
				adminName = name
			}
		}
		comment, err = h.service.CreateAsAdmin(c.Request().Context(), adminName, req.TargetType, req.TargetID, req.ParentID, req.Content)
	} else if sess.UserID != nil {
		comment, err = h.service.CreateAsUser(c.Request().Context(), *sess.UserID, req.TargetType, req.TargetID, req.ParentID, req.Content)
	} else {
		return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}
	if err != nil {
		switch {
		case errors.Is(err, model.ErrInvalidCommentTarget):
			return echo.NewHTTPError(http.StatusBadRequest, "invalid comment target")
		case errors.Is(err, model.ErrCommentNotFound):
			return echo.NewHTTPError(http.StatusBadRequest, "parent comment not found")
		case errors.Is(err, model.ErrCommentTargetNotFound):
			return echo.NewHTTPError(http.StatusNotFound, "comment target not found")
		default:
			// Validation failures (missing nickname, over-long content) are client
			// errors and must surface as 400, not as a misleading 500.
			var validationErr *service.ValidationError
			if errors.As(err, &validationErr) {
				return echo.NewHTTPError(http.StatusBadRequest, validationErr.Message)
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to create comment")
		}
	}

	return c.JSON(http.StatusCreated, viewmodel.NewCommentResponse(comment, sess.IsAdmin()))
}

func (h *CommentHandler) ListAll(c echo.Context) error {
	logging.Debugf("[handler:comment] ListAll called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	comments, total, err := h.service.ListAll(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list comments")
	}
	// 管理员专用路由：审核者需要看到每条评论背后的邮箱，所以恒按管理员渲染。
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewCommentResponseList(comments, true), total, p.Page, p.PageSize))
}

// ListDeleted 对应 GET /api/admin/trash/comments，回收站列表。
//
// 软删一条根评论会让它的子孙回复也从**前台**消失（虽然子孙自身未被标记删除，
// 仍会出现在回收站里），所以这里的条目数可能多于前台可见数。
func (h *CommentHandler) ListDeleted(c echo.Context) error {
	logging.Debugf("[handler:comment] ListDeleted called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	comments, total, err := h.service.ListDeleted(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list deleted comments")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewCommentResponseList(comments, true), total, p.Page, p.PageSize))
}

// Delete 对应 DELETE /api/admin/comments/:id，软删；未知 id 也返回 204。
func (h *CommentHandler) Delete(c echo.Context) error {
	logging.Debugf("[handler:comment] Delete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid comment id")
	}

	if err := h.service.SoftDelete(c.Request().Context(), []int64{id}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete comment")
	}
	return c.NoContent(http.StatusNoContent)
}

// BatchDelete 对应 DELETE /api/admin/comments。
func (h *CommentHandler) BatchDelete(c echo.Context) error {
	logging.Debugf("[handler:comment] BatchDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.SoftDelete(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete comments")
	}
	return c.NoContent(http.StatusNoContent)
}

// Restore 对应 POST /api/admin/comments/:id/restore。
func (h *CommentHandler) Restore(c echo.Context) error {
	logging.Debugf("[handler:comment] Restore called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid comment id")
	}

	if err := h.service.Restore(c.Request().Context(), id); err != nil {
		if errors.Is(err, model.ErrCommentNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "comment not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to restore comment")
	}
	return c.NoContent(http.StatusNoContent)
}

// BatchRestore 对应 POST /api/admin/comments/batch-restore。
func (h *CommentHandler) BatchRestore(c echo.Context) error {
	logging.Debugf("[handler:comment] BatchRestore called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.BatchRestore(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to restore comments")
	}
	return c.NoContent(http.StatusNoContent)
}

// PermanentDelete 对应 DELETE /api/admin/comments/:id/permanent，目标须已在回收站。
func (h *CommentHandler) PermanentDelete(c echo.Context) error {
	logging.Debugf("[handler:comment] PermanentDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid comment id")
	}

	if err := h.service.HardDelete(c.Request().Context(), []int64{id}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to permanently delete comment")
	}
	return c.NoContent(http.StatusNoContent)
}

// BatchPermanentDelete 对应 DELETE /api/admin/comments/batch-permanent。
func (h *CommentHandler) BatchPermanentDelete(c echo.Context) error {
	logging.Debugf("[handler:comment] BatchPermanentDelete called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	var req viewmodel.BatchDeleteRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.service.HardDelete(c.Request().Context(), req.IDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to permanently delete comments")
	}
	return c.NoContent(http.StatusNoContent)
}

// currentUserIDFromContext 返回当前会话绑定的账号 id；未登录返回 nil。
//
// ⚠️ 它对**管理员同样返回非 nil**（管理员现在也有 users 行）。凡是需要
// 「仅访客」的地方都必须先显式判断 session.IsAdmin(c)，见 Like / Unlike 的注释——
// 旧版靠这个函数返回 nil 来挡住管理员，那个行为已经不存在了。
func currentUserIDFromContext(c echo.Context) *int64 {
	return session.UserID(c)
}
