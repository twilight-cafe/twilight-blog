package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/logging"
	"blog-engine/internal/session"
)

// Authenticate 把请求携带的会话 cookie 解析后放进请求上下文。
//
// 它**从不拒绝任何请求**：记不记得住身份是这里的事，要不要身份是各个路由的事。
// 会话不存在或已过期都只是「当作未登录」放行，只有数据库层面出错才记日志。
func Authenticate(store *session.Store) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			token := session.TokenFromRequest(c)
			if token == "" {
				return next(c)
			}

			sess, err := store.Lookup(c.Request().Context(), token)
			if err != nil {
				if !errors.Is(err, session.ErrSessionNotFound) && !errors.Is(err, session.ErrSessionExpired) {
					logging.Errorf("[session] lookup failed: %v", err)
				}
				return next(c)
			}

			session.Set(c, sess)
			return next(c)
		}
	}
}

// RequireCSRF 对**携带会话**的写请求强制校验双提交 CSRF token。
//
// 没有会话的请求不受影响：没有可被骑劫的环境身份，而且要求登录请求本身
// 先带上 token 只会把登录也一起挡掉（登录时还没有会话，自然也拿不到 token）。
//
// 它必须挂在 Authenticate 之后，否则这里读不到已解析的会话。
func RequireCSRF(store *session.Store) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			switch c.Request().Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(c)
			}

			sess, ok := session.From(c)
			if !ok {
				return next(c)
			}

			if !store.VerifyCSRF(c.Request().Context(), sess, session.CSRFFromRequest(c)) {
				logging.Warnf("[session] rejected request with invalid CSRF token method=%s path=%s", c.Request().Method, c.Request().URL.Path)
				return echo.NewHTTPError(http.StatusForbidden, "invalid or missing CSRF token")
			}
			return next(c)
		}
	}
}

// RequireSession 拒绝完全未登录的请求（管理员或访客都算已登录）。
func RequireSession() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if _, ok := session.From(c); !ok {
				logging.Warnf("[session] unauthenticated request method=%s path=%s", c.Request().Method, c.Request().URL.Path)
				return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
			}
			return next(c)
		}
	}
}

// RequireAdmin 只允许管理员。
//
// 注意未登录返回 401、已登录但角色不对返回 403——两者刻意区分，
// 便于前端分别处理「去登录」与「无权访问」。
func RequireAdmin() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			sess, ok := session.From(c)
			if !ok {
				logging.Warnf("[session] unauthenticated admin request method=%s path=%s", c.Request().Method, c.Request().URL.Path)
				return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
			}
			if !sess.IsAdmin() {
				logging.Warnf("[session] admin-only route rejected role=%s method=%s path=%s", sess.Role, c.Request().Method, c.Request().URL.Path)
				return echo.NewHTTPError(http.StatusForbidden, "admin privileges required")
			}
			return next(c)
		}
	}
}

// RequestIsHTTPS 判断浏览器是否是经 HTTPS 到达本服务的。
//
// 在终止 TLS 的代理后面，Go 只看到明文 HTTP，因此要参考 X-Forwarded-Proto；
// 但只在信任代理时才看，否则任何客户端都能自称 HTTPS。
// 该头可能是一串逗号分隔的值，取**第一段**——它才是客户端那一段。
func RequestIsHTTPS(c echo.Context, trustForwarded bool) bool {
	if c.Request().TLS != nil {
		return true
	}
	if !trustForwarded {
		return false
	}

	proto := c.Request().Header.Get("X-Forwarded-Proto")
	if index := strings.IndexByte(proto, ','); index >= 0 {
		proto = proto[:index]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// CookieSecureFor 依据配置模式决定会话 cookie 是否带 Secure。
// always / never 直接给出结论；其余（含空值，即 auto）交给 RequestIsHTTPS 判断。
func CookieSecureFor(c echo.Context, mode string, trustForwarded bool) bool {
	switch mode {
	case config.CookieSecureAlways:
		return true
	case config.CookieSecureNever:
		return false
	default:
		return RequestIsHTTPS(c, trustForwarded)
	}
}
