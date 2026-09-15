// Package middleware 提供 echo 中间件：安全响应头、来源检查、
// 会话解析与 CSRF 校验、按 IP 限流。
//
// 本包整体建立在一个前提上：**认证走的是 httpOnly cookie，不是 Authorization 头**。
// 正因如此浏览器会自动携带凭据，跨站请求才构成真实威胁，于是有了三层独立防护：
//  1. SameSite=Lax —— 挡住绝大多数跨站 POST（在 session.SetCookies 里设置）；
//  2. OriginCheck —— 对写请求校验 Origin / Referer 是否同源；
//  3. RequireCSRF —— 双提交 token，要求请求头里带上只有同源脚本才读得到的值。
//
// 改动其中任何一层时，都要意识到另外两层的前提，别让防护出现缺口。
package middleware

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	"blog-engine/internal/logging"
)

// contentSecurityPolicy 刻意收得很紧。
//
// 除了常规加固，它对本应用还有一个关键副作用：`script-src 'self'` 且不含
// 'unsafe-inline' 时，浏览器会拒绝执行 `javascript:` 形式的 URL，
// 于是形如 [x](javascript:...) 的 Markdown 链接无法生效——这正是文章正文
// 有意不做 HTML 清洗之后仍然安全的前提之一。
//
// 表中为 Google Fonts 开了两个口子（style-src 与 font-src），
// 改动这张表时请一并确认字体仍能加载。
func contentSecurityPolicy() string {
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
		"font-src 'self' https://fonts.gstatic.com data:",
		"img-src 'self' data: blob:",
		"media-src 'self' blob:",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

// SecureHeaders 施加一组标准的安全响应头。
// 注意 HSTSMaxAge 为一年且**包含**子域（HSTSExcludeSubdomains 为 false），
// 这只有在确认全站都走 HTTPS 之后才合适。
func SecureHeaders() echo.MiddlewareFunc {
	return echomw.SecureWithConfig(echomw.SecureConfig{
		XSSProtection:         "1; mode=block",
		ContentTypeNosniff:    "nosniff",
		XFrameOptions:         "DENY",
		HSTSMaxAge:            31536000,
		HSTSExcludeSubdomains: false,
		ContentSecurityPolicy: contentSecurityPolicy(),
		ReferrerPolicy:        "strict-origin-when-cross-origin",
	})
}

// CORS 返回一个只允许配置来源的 CORS 中间件；没有配置来源时返回 nil（不挂载）。
//
// 默认不给任何跨源访问：SPA 与 API 同源部署（前端产物由 Go 直接内嵌），
// 同源就够了。注意 AllowCredentials 保持 false —— 跨源请求因此不会携带会话
// cookie，带身份的跨源调用仍然只能同源完成；这也让这份白名单不会意外扩大
// 认证攻击面。若日后确实需要跨源且带凭据，那是一处需要重新评估的安全决策，
// 而不是把这一项改成 true 就完事。
func CORS(allowedOrigins []string) echo.MiddlewareFunc {
	if len(allowedOrigins) == 0 {
		return nil
	}
	return echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins: allowedOrigins,
		AllowMethods: []string{
			http.MethodGet, http.MethodHead, http.MethodPost,
			http.MethodPut, http.MethodDelete, http.MethodOptions,
		},
		AllowHeaders: []string{
			echo.HeaderOrigin, echo.HeaderContentType,
			echo.HeaderAccept, echo.HeaderAuthorization,
		},
		// 会话凭据走 cookie，而这里**刻意不开** AllowCredentials：
		// 开了就等于允许白名单里的任意第三方站点带着用户身份发请求。
		// 需要身份时应当保持同源，而不是放开这一项。
		AllowCredentials: false,
		MaxAge:           600,
	})
}

// OriginCheck 拒绝浏览器声明来自其他来源的写请求。
//
// 认证基于 httpOnly cookie，浏览器会自动携带，所以跨站请求确实是一个真实威胁；
// 本检查是三道防线中的第二道（另两道是 SameSite=Lax 与双提交 CSRF token）。
// 它还额外覆盖了那些**完全没有身份要求**的写端点（/install、/api/auth/email-code
// 等）——那里没有 CSRF 可谈，来源检查是唯一拦得住的东西。
//
// 只拦非安全方法：GET/HEAD/OPTIONS 直接放行（它们不应产生副作用）。
// 无 Origin 头的请求会退一步看 Referer；两者都没有（例如 curl）则放行，
// 因为非浏览器调用者无法伪造出受害者的请求。
//
// trustForwardedHost 只能在确实信任反向代理时开启（settings.json 的
// trusted_proxies）。它存在的原因是：代理改写 Host 之后，所有同源请求都会
// 看起来像跨源，从而拒绝掉全部写操作。allowedOrigins 里列出的来源始终被接受，
// 这是 Host 与 X-Forwarded-Host 都对不上真实来源时的逃生口。
//
// ⚠️ 采信与否按**本次请求的对端**判定，与 BuildIPExtractor 同一口径，
// 而不是「配置里有没有代理」这个全局布尔量。改动前用的正是后者，
// 而 trusted_proxies 默认非空（回环）——于是即便部署根本没有代理，
// X-Forwarded-Host 也会被无条件采信，恰恰在最需要这道防线的场景里失效。
func OriginCheck(allowedOrigins []string, proxies TrustedProxies) echo.MiddlewareFunc {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[normalizeOrigin(origin)] = struct{}{}
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			switch c.Request().Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(c)
			}

			origin := strings.TrimSpace(c.Request().Header.Get(echo.HeaderOrigin))
			if origin == "" {
				// 没有 Origin 头。浏览器发跨站非安全请求时一定会带上它，
				// 所以这里只可能是两种情况：老客户端的同源请求，
				// 或 curl 之类的非浏览器调用者——后者无法伪造受害者的请求。
				referer := strings.TrimSpace(c.Request().Header.Get("Referer"))
				if referer == "" || sameHost(referer, c, proxies) || originAllowed(referer, allowed) {
					return next(c)
				}
				logging.Warnf("[security] rejected cross-site request referer=%s path=%s", referer, c.Request().URL.Path)
				return echo.NewHTTPError(http.StatusForbidden, "cross-site request rejected")
			}

			// 沙箱化的 iframe 或 data: 文档会发出 "Origin: null"，
			// 要把它当作**不可信来源**处理，而不是当成「没有这个头」而放行。
			if strings.EqualFold(origin, "null") {
				logging.Warnf("[security] rejected opaque origin path=%s", c.Request().URL.Path)
				return echo.NewHTTPError(http.StatusForbidden, "cross-site request rejected")
			}

			if sameHost(origin, c, proxies) || originAllowed(origin, allowed) {
				return next(c)
			}

			logging.Warnf("[security] rejected cross-origin request origin=%s host=%s path=%s", origin, c.Request().Host, c.Request().URL.Path)
			return echo.NewHTTPError(http.StatusForbidden, "cross-site request rejected")
		}
	}
}

// normalizeOrigin 把来源归一成 "scheme://host" 的小写形式，便于比较。
// 解析失败时退化为「去空白、去尾斜杠、转小写」——配置里既可能写完整 URL，
// 也可能只写一个主机名，两种都要能处理。
func normalizeOrigin(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return strings.ToLower(strings.TrimRight(strings.TrimSpace(value), "/"))
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host)
}

// sameHost 判断 value（一个 Origin 或绝对 Referer）是否指向本次请求所用的主机。
//
// X-Forwarded-Host 只在**本次请求的对端确实落在 trusted_proxies 内**时才参考。
// 注意判定用的是 TCP 对端地址而不是 c.RealIP()：后者由 XFF 推导而来，
// 而 XFF 正是攻击者能伪造的东西——用它来决定「要不要相信另一个可伪造的头」
// 等于把两道锁串在同一根绳子上。
func sameHost(value string, c echo.Context, proxies TrustedProxies) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return false
	}

	if strings.EqualFold(parsed.Host, c.Request().Host) {
		return true
	}

	// The configured proxy may have replaced Host with the upstream address.
	// X-Forwarded-Host is only consulted when this request actually came from a
	// trusted proxy, otherwise any client could set it and bypass this check.
	if !proxies.Contains(net.ParseIP(directPeerAddress(c.Request()))) {
		return false
	}

	for _, candidate := range forwardedHosts(c.Request().Header.Get("X-Forwarded-Host")) {
		if strings.EqualFold(parsed.Host, candidate) {
			return true
		}
	}
	return false
}

// forwardedHosts 拆分 X-Forwarded-Host —— 代理链上可能有多级，都要能匹配上。
func forwardedHosts(header string) []string {
	if header == "" {
		return nil
	}
	parts := strings.Split(header, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// originAllowed 判断归一化后的来源是否在白名单里；白名单为空时一律不通过。
func originAllowed(value string, allowed map[string]struct{}) bool {
	if len(allowed) == 0 {
		return false
	}
	_, ok := allowed[normalizeOrigin(value)]
	return ok
}

// BuildIPExtractor 决定 Echo 如何得出客户端 IP。
//
// 必须显式配置：不设 IPExtractor 时，Echo 的默认行为会直接采信
// X-Forwarded-For / X-Real-IP，任何客户端都能伪造这些头，
// 从而污染日志、并把限流的桶打散（换个假 IP 就等于换了份额度）。
//
// 只有两种安全配置：
//   - 前面没有代理：用网络对端地址，完全忽略转发头；
//   - 有已知代理：只有当对端地址落在显式配置的范围内时才采信转发头。
//
// 注意这里**不使用** echo.ExtractIPFromXFFHeader：它在「整条链都可信」时会返回
// 最左值，而那个值完全由客户端提供（详见 forwardedIPExtractor 的说明）。
func BuildIPExtractor(proxies TrustedProxies) echo.IPExtractor {
	if !proxies.Any() {
		logging.Debugf("[security] no trusted_proxies configured; using the network peer address as the client IP")
		return echo.ExtractIPDirect()
	}

	logging.Infof("[security] trusting forwarded headers from %d configured proxy range(s)", len(proxies))
	return forwardedIPExtractor(proxies)
}

// forwardedIPExtractor 从 X-Forwarded-For 解析客户端地址，规则与 Echo 相同
// （从右往左取第一个不可信地址），但补上了 Echo 漏掉的一个分支。
//
// XFF 的语义是「每一跳在末尾追加它看到的对端地址」，所以从右往左读：
//
//	XFF: 1.2.3.4, 10.0.0.9     对端: 127.0.0.1
//	     ↑客户端伪造  ↑代理追加   ↑同机 nginx
//	从右：127.0.0.1 可信 → 跳过；10.0.0.9 不可信 → 它就是客户端。
//	最左的 1.2.3.4 永远够不到。
//
// **与 Echo 的差别**：当整条链（含对端）都落在信任范围内时，Echo 返回 ips[0]——
// 也就是最左值，一个纯粹由客户端提供、没有任何一跳为它背书的字符串。
// 于是把 trusted_proxies 放宽到某个网段（例如 10.0.0.0/8）之后，
// 该网段内的任意主机只要带上 `X-Forwarded-For: 10.1.2.3` 就能换一个限流桶。
// 这里改为 fail closed：整条链都可信说明**无法确定**客户端是谁，返回真实对端。
//
// ⚠️ 本函数无法修复的一种配置：边缘代理**透传**客户端自带的 XFF 而不是替换/追加它
// （例如 `proxy_set_header X-Forwarded-For $http_x_forwarded_for;`）。那时应用收到的
// 链与「代理正确追加」在形状上完全一致（只有一项、且不可信），任何算法都无法区分。
// 唯一可靠的处置在部署侧：边缘代理必须**丢弃或覆盖**入站的 XFF。
func forwardedIPExtractor(proxies TrustedProxies) echo.IPExtractor {
	return func(req *http.Request) string {
		directIP := directPeerAddress(req)
		direct := net.ParseIP(directIP)

		// 对端不在信任范围内：转发头一律不采信。这一条要在解析 XFF 之前判断，
		// 否则公网直连的部署会重新暴露在伪造头之下。
		if direct == nil || !proxies.Contains(direct) {
			return directIP
		}

		values := req.Header.Values(echo.HeaderXForwardedFor)
		if len(values) == 0 {
			return directIP
		}

		hops := strings.Split(strings.Join(values, ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(hops[i])
			candidate = strings.TrimPrefix(candidate, "[")
			candidate = strings.TrimSuffix(candidate, "]")

			ip := net.ParseIP(candidate)
			if ip == nil {
				// 解析不了就说明整条链不可信（可能是攻击者塞的垃圾）；
				// 猜测其中任何一项都可能被用来选桶，所以退回对端。
				return directIP
			}
			if !proxies.Contains(ip) {
				return ip.String()
			}
		}

		// 整条链都可信：最左值是客户端提供的，不能当作客户端地址。
		return directIP
	}
}

// directPeerAddress 返回 TCP 连接对端的 IP（不含端口）。
// 与 Echo 内部 extractIP 的口径一致，但不读取任何请求头。
func directPeerAddress(req *http.Request) string {
	if host, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		return host
	}
	return req.RemoteAddr
}

// TrustedProxies 是解析后的可信代理网段集合。
//
// 单独做成一个类型是为了让「信任谁」这件事只解析一次、并且能按**请求**判定：
// IP 提取与 OriginCheck 都需要问同一个问题「本次请求的对端是不是我们的代理」，
// 两处各写一份判断迟早会漂移（改动前 OriginCheck 用的就是「配置里有没有代理」
// 这个全局布尔量，与这里的按请求判定并不一致）。
type TrustedProxies []*net.IPNet

// ParseTrustedProxies 把配置项解析成网段：裸 IP 会被补成 /32（IPv4）或 /128（IPv6），
// 带斜杠的交给 net.ParseCIDR。空项与非法值都会报错，让启动失败。
func ParseTrustedProxies(entries []string) (TrustedProxies, error) {
	parsed := make(TrustedProxies, 0, len(entries))
	for _, entry := range entries {
		ipRange, err := parseIPRange(entry)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, ipRange)
	}
	return parsed, nil
}

// Contains 报告 ip 是否落在任一可信网段内。空集合一律返回 false（fail closed）。
func (t TrustedProxies) Contains(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, candidate := range t {
		if candidate.Contains(ip) {
			return true
		}
	}
	return false
}

// Any 报告是否配置了至少一个可信网段。
func (t TrustedProxies) Any() bool { return len(t) > 0 }

// parseIPRange 把配置项解析成网段：裸 IP 会被补成 /32（IPv4）或 /128（IPv6），
// 带斜杠的交给 net.ParseCIDR。空项与非法值都会报错，让启动失败。
func parseIPRange(entry string) (*net.IPNet, error) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil, fmt.Errorf("trusted proxy entry must not be empty")
	}

	if !strings.Contains(entry, "/") {
		ip := net.ParseIP(entry)
		if ip == nil {
			return nil, fmt.Errorf("invalid trusted proxy address %q", entry)
		}
		bits := 128
		if ip.To4() != nil {
			bits = 32
			ip = ip.To4()
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
	}

	_, ipNet, err := net.ParseCIDR(entry)
	if err != nil {
		return nil, fmt.Errorf("invalid trusted proxy range %q: %w", entry, err)
	}
	return ipNet, nil
}
