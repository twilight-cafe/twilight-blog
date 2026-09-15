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

// 发件记录查询端点（管理员）。
//
// 这里的「email」是出站邮件的审计留痕：每发一封验证码邮件都会落一行记录。
// 因此只有 List / Get 两个只读接口——不允许改、也不允许重发，
// 否则这份记录就不再可信。
//
// EmailHandler 仅挂在 /api/admin 分组下。
type EmailHandler struct {
	service *service.EmailService
}

func NewEmailHandler(svc *service.EmailService) *EmailHandler {
	return &EmailHandler{service: svc}
}

func (h *EmailHandler) List(c echo.Context) error {
	logging.Debugf("[handler:email] List called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	p, err := parsePagination(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	emails, total, err := h.service.List(c.Request().Context(), p)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list emails")
	}
	return c.JSON(http.StatusOK, viewmodel.NewPaginatedResponse(viewmodel.NewEmailResponseList(emails), total, p.Page, p.PageSize))
}

func (h *EmailHandler) Get(c echo.Context) error {
	logging.Debugf("[handler:email] Get called method=%s path=%s", c.Request().Method, c.Request().URL.Path)
	id, err := parseID(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid email id")
	}

	email, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, model.ErrEmailNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "email not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get email")
	}
	return c.JSON(http.StatusOK, viewmodel.NewEmailResponse(email))
}
