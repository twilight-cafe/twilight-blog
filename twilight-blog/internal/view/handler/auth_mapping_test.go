package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/model"
	"blog-engine/internal/service"
)

// adminStoreStub 只实现被测方法；嵌入接口让其余方法在误调用时直接 panic。
type adminStoreStub struct {
	service.AdminCredentialsStore
	identity func(context.Context) (string, string, error)
}

func (s adminStoreStub) Identity(ctx context.Context) (string, string, error) {
	return s.identity(ctx)
}

// TestMapUserAuthError 覆盖账号错误映射表的每一个分支。
func TestMapUserAuthError(t *testing.T) {
	validation := &service.ValidationError{Message: "bad input"}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "validation", err: validation, want: http.StatusBadRequest},
		{name: "bad verification code", err: service.ErrInvalidVerificationCode, want: http.StatusBadRequest},
		{name: "email registered", err: service.ErrEmailAlreadyRegistered, want: http.StatusConflict},
		{name: "username taken", err: service.ErrUsernameTaken, want: http.StatusConflict},
		{name: "account not found", err: service.ErrAccountNotFound, want: http.StatusNotFound},
		{name: "invalid credentials", err: service.ErrInvalidCredentials, want: http.StatusUnauthorized},
		{name: "daily email limit", err: service.ErrDailyEmailLimitReached, want: http.StatusTooManyRequests},
		{name: "per-address limit", err: service.ErrEmailCodeAddressLimit, want: http.StatusTooManyRequests},
		{name: "too frequent", err: service.ErrEmailCodeTooFrequent, want: http.StatusTooManyRequests},
		{name: "email disabled", err: service.ErrEmailSendingDisabled, want: http.StatusServiceUnavailable},
		{name: "unknown", err: errors.New("boom"), want: http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mapUserAuthError(tc.err)
			httpErr, ok := err.(*echo.HTTPError)
			if !ok || httpErr.Code != tc.want {
				t.Fatalf("err = %v, want HTTP %d", err, tc.want)
			}
		})
	}

	if got := mapUserAuthError(nil); got != nil {
		t.Fatalf("nil must map to nil, got %v", got)
	}
}

// TestAdminProfileError 覆盖管理员资料写入的三种结果。
func TestAdminProfileError(t *testing.T) {
	validation := &service.ValidationError{Message: "avatar is not an image"}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "validation", err: validation, want: http.StatusBadRequest},
		{name: "username taken", err: service.ErrUsernameTaken, want: http.StatusConflict},
		{name: "unknown", err: errors.New("db down"), want: http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := adminProfileError(tc.err)
			httpErr, ok := err.(*echo.HTTPError)
			if !ok || httpErr.Code != tc.want {
				t.Fatalf("err = %v, want HTTP %d", err, tc.want)
			}
		})
	}
}

// TestAdminName 覆盖管理员展示名读取的三种结局：成功、账号真的不存在、
// 以及数据库/其他故障。后两种都返回空串（调用方回退到匿名署名），区别只在日志。
func TestAdminName(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		handler := &AuthHandler{admins: adminStoreStub{identity: func(context.Context) (string, string, error) {
			return "admin", "站长", nil
		}}}
		if got := handler.AdminName(context.Background()); got != "站长" {
			t.Fatalf("AdminName = %q", got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		handler := &AuthHandler{admins: adminStoreStub{identity: func(context.Context) (string, string, error) {
			return "", "", model.ErrUserNotFound
		}}}
		if got := handler.AdminName(context.Background()); got != "" {
			t.Fatalf("AdminName = %q, want an empty fallback", got)
		}
	})

	t.Run("repository failure", func(t *testing.T) {
		handler := &AuthHandler{admins: adminStoreStub{identity: func(context.Context) (string, string, error) {
			return "", "", errors.New("db down")
		}}}
		if got := handler.AdminName(context.Background()); got != "" {
			t.Fatalf("AdminName = %q, want an empty fallback", got)
		}
	})
}
