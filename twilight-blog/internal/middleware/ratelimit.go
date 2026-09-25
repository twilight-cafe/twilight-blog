package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"

	"blog-engine/internal/logging"
)

// RateLimitOptions 保存各受限期端点按 IP 的额度。
type RateLimitOptions struct {
	LoginPerMinute   int
	EmailPerMinute   int
	EmailPerHour     int
	CommentPerMinute int
	VerifyPerMinute  int
	InstallPerMinute int
	SearchPerMinute  int
}

// RateLimiters 为每个端点类别各持一个限流器，都是按客户端 IP 分桶的令牌桶。
//
// 分开而不共用，是为了让「登录被刷爆」不至于顺带把评论也一起锁死。
type RateLimiters struct {
	Login     echo.MiddlewareFunc
	Email     echo.MiddlewareFunc
	EmailHour echo.MiddlewareFunc
	Verify    echo.MiddlewareFunc
	Comment   echo.MiddlewareFunc
	Install   echo.MiddlewareFunc
	Search    echo.MiddlewareFunc
}

// NewRateLimiters 构造限流器集合。
//
// 额度都按客户端 IP 计，这正是必须先配好 BuildIPExtractor 的原因：
// 可伪造的 X-Forwarded-For 会让攻击者不断更换身份，从而绕过这里的每一个桶。
//
// 传给底层的 label 只用于日志与错误信息，改名不影响限流行为。
func NewRateLimiters(opts RateLimitOptions) *RateLimiters {
	return &RateLimiters{
		Login:     perIPRateLimiter("login", opts.LoginPerMinute, time.Minute),
		Email:     perIPRateLimiter("comment-auth-email", opts.EmailPerMinute, time.Minute),
		EmailHour: perIPRateLimiter("comment-auth-email-hourly", opts.EmailPerHour, time.Hour),
		Verify:    perIPRateLimiter("comment-auth-verify", opts.VerifyPerMinute, time.Minute),
		Comment:   perIPRateLimiter("comment-create", opts.CommentPerMinute, time.Minute),
		Install:   perIPRateLimiter("install", opts.InstallPerMinute, time.Minute),
		// 搜索是公开的只读端点，但它每处理一个请求就要外呼一次 Meilisearch，
		// 所以「读操作」不等于「廉价操作」。
		Search: perIPRateLimiter("search", opts.SearchPerMinute, time.Minute),
	}
}

// perIPRateLimiter 为每个客户端 IP 提供 window 时间内 allowance 次请求。
//
// 令牌桶参数的含义：Rate 把额度摊平到整段时间（而不是「整分钟攒满后一次性放行」），
// Burst 等于 allowance，所以额度的语义是「任意时刻最多突发 allowance 次」。
// 额度 <= 0 时兜底为 1，避免配错成 0 导致接口永久不可用。
// ExpiresIn 设为两倍窗口，让不活跃 IP 的桶能被回收，避免内存无限增长。
//
// 超限时返回 429 并带上 Retry-After；给出的值是整个窗口长度，
// 属于「至少等这么久」的上界估计。
var rateLimitIdentifierExtractor = func(c echo.Context) (string, error) {
	return c.RealIP(), nil
}

func perIPRateLimiter(label string, allowance int, window time.Duration) echo.MiddlewareFunc {
	if allowance <= 0 {
		allowance = 1
	}

	store := echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
		Rate:      rate.Limit(float64(allowance) / window.Seconds()),
		Burst:     allowance,
		ExpiresIn: window * 2,
	})

	return echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store:               store,
		IdentifierExtractor: rateLimitIdentifierExtractor,
		ErrorHandler: func(c echo.Context, err error) error {
			logging.Errorf("[ratelimit] cannot identify client for %s: %v", label, err)
			return echo.NewHTTPError(http.StatusInternalServerError, "rate limiter unavailable")
		},
		DenyHandler: func(c echo.Context, identifier string, err error) error {
			retryAfter := int(window.Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
			logging.Warnf("[ratelimit] %s limit exceeded identifier=%s method=%s path=%s", label, identifier, c.Request().Method, c.Request().URL.Path)
			c.Response().Header().Set(echo.HeaderRetryAfter, strconv.Itoa(retryAfter))
			return echo.NewHTTPError(http.StatusTooManyRequests, "too many requests; please slow down and try again later")
		},
	})
}
