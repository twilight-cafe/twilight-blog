package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/logging"
	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/viewmodel"
)

// UserHandler 是管理员视角的访客账号视图（仅挂在 /api/admin 下）。
//
// 只有 List 与 Update 两个接口，**刻意没有创建与删除**：账号只能由访客自助注册
// 产生；删除会牵动评论等外键关联，需要时直接操作数据库，不在 API 上开这个口子。
type UserHandler struct {
	service *service.UserService
}

func NewUserHandler(svc *service.UserService) *UserHandler {
	return &UserHandler{service: svc}
}

// List 对应 GET /api/admin/users，分页列出访客账号。
func (h *UserHandler) List(c echo.Context) error {
	logging.Debugf("[handler:user] List called path=%s", c.Request().URL.Path)

	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	users, total, err := h.service.ListUsers(c.Request().Context(), p)
	if err != nil {
		logging.Errorf("[handler:user] list failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list users")
	}

	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(
		viewmodel.NewUserResponseList(users), total, p.Page, p.PageSize))
}

// Update 对应 PUT /api/admin/users/:id，允许管理员订正**访客**账号的登录名、
// 展示名与邮箱。用户名或邮箱冲突返回 409（话术不同，操作员才知道该改哪一项）；
// service 的 ValidationError 映射为 400（客户端错误），其余未知错误才落到 500。
//
// 单独处理 ErrNotAVisitorAccount：管理员行住在同一张表里，但它的资料只能从
// PUT /api/admin/profile 改。以前这里会静默地改到管理员行，而管理员行的 email
// 是一枚「不可注册/不可找回」的哨兵——把它写脏等于给站长账号开一条邮箱接管路径。
func (h *UserHandler) Update(c echo.Context) error {
	logging.Debugf("[handler:user] Update called path=%s", c.Request().URL.Path)

	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid user id")
	}

	var req viewmodel.AdminUpdateUserRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	user, err := h.service.AdminUpdateUser(c.Request().Context(), id, req.Username, req.Nickname, req.Email)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrUserNotFound):
			return echo.NewHTTPError(http.StatusNotFound, "user not found")
		case errors.Is(err, model.ErrNotAVisitorAccount):
			logging.Warnf("[handler:user] refused to edit the administrator row through the visitor endpoint id=%d", id)
			return echo.NewHTTPError(http.StatusConflict,
				"the administrator account cannot be edited here; use PUT /api/admin/profile")
		case errors.Is(err, service.ErrUsernameTaken):
			return echo.NewHTTPError(http.StatusConflict, "username is already taken")
		case errors.Is(err, service.ErrEmailAlreadyRegistered):
			return echo.NewHTTPError(http.StatusConflict, "email is already registered")
		default:
			var validationErr *service.ValidationError
			if errors.As(err, &validationErr) {
				return echo.NewHTTPError(http.StatusBadRequest, validationErr.Message)
			}
			logging.Errorf("[handler:user] update failed id=%d: %v", id, err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to update user")
		}
	}

	return c.JSON(http.StatusOK, viewmodel.NewUserResponse(user))
}
