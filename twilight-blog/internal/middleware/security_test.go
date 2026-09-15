package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// mustTrustedProxies 解析配置项，失败即终止测试。
func mustTrustedProxies(t *testing.T, entries ...string) TrustedProxies {
	t.Helper()
	proxies, err := ParseTrustedProxies(entries)
	if err != nil {
		t.Fatalf("ParseTrustedProxies(%v) error = %v", entries, err)
	}
	return proxies
}

// TestBuildIPExtractorIgnoresForwardedHeadersByDefault is the regression test for
// c.RealIP() trusting spoofable headers: with no proxy configured, only the
// network peer address may be used.
func TestBuildIPExtractorIgnoresForwardedHeadersByDefault(t *testing.T) {
	extractor := BuildIPExtractor(nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.7:1234"
	req.Header.Set(echo.HeaderXForwardedFor, "203.0.113.9")
	req.Header.Set(echo.HeaderXRealIP, "203.0.113.10")

	if got := extractor(req); got != "198.51.100.7" {
		t.Fatalf("client IP = %q, want the peer address 198.51.100.7", got)
	}
}

// TestBuildIPExtractorTrustsOnlyConfiguredProxies verifies that forwarded headers
// are honoured from a configured range and still ignored from anywhere else.
func TestBuildIPExtractorTrustsOnlyConfiguredProxies(t *testing.T) {
	extractor := BuildIPExtractor(mustTrustedProxies(t, "10.0.0.0/8", "192.168.1.5"))

	trusted := httptest.NewRequest(http.MethodGet, "/", nil)
	trusted.RemoteAddr = "10.1.2.3:1234"
	trusted.Header.Set(echo.HeaderXForwardedFor, "203.0.113.9")
	if got := extractor(trusted); got != "203.0.113.9" {
		t.Fatalf("client IP from trusted proxy = %q, want 203.0.113.9", got)
	}

	// A bare IP entry must behave like a /32 host route.
	hostRoute := httptest.NewRequest(http.MethodGet, "/", nil)
	hostRoute.RemoteAddr = "192.168.1.5:1234"
	hostRoute.Header.Set(echo.HeaderXForwardedFor, "203.0.113.11")
	if got := extractor(hostRoute); got != "203.0.113.11" {
		t.Fatalf("client IP from configured single host = %q, want 203.0.113.11", got)
	}

	untrusted := httptest.NewRequest(http.MethodGet, "/", nil)
	untrusted.RemoteAddr = "198.51.100.1:1234"
	untrusted.Header.Set(echo.HeaderXForwardedFor, "203.0.113.9")
	if got := extractor(untrusted); got != "198.51.100.1" {
		t.Fatalf("client IP from untrusted peer = %q, want the peer address 198.51.100.1", got)
	}
}

func TestParseTrustedProxiesRejectsInvalidRange(t *testing.T) {
	if _, err := ParseTrustedProxies([]string{"not-an-ip"}); err == nil {
		t.Fatal("ParseTrustedProxies accepted an invalid proxy address")
	}
	if _, err := ParseTrustedProxies([]string{"10.0.0.0/99"}); err == nil {
		t.Fatal("ParseTrustedProxies accepted an invalid CIDR")
	}
	if _, err := ParseTrustedProxies([]string{""}); err == nil {
		t.Fatal("ParseTrustedProxies accepted an empty entry")
	}
}

// TestForwardedIPExtractorFailsClosedWhenWholeChainIsTrusted 是本轮新增的守护测试。
//
// Echo 自带的 ExtractIPFromXFFHeader 在「整条链（含对端）都可信」时会返回
// ips[0]，也就是最左值——而那个值完全由客户端提供，没有任何一跳为它背书。
// 后果是把 trusted_proxies 放宽到某个网段之后，该网段内的任意主机只要写一个
// 同网段的假地址，就能让限流器认为自己是另一个客户端（每次请求换一个桶）。
//
// 这里钉住替代实现的选择：整条链都可信 => 无法确定客户端 => 返回真实对端。
func TestForwardedIPExtractorFailsClosedWhenWholeChainIsTrusted(t *testing.T) {
	extractor := BuildIPExtractor(mustTrustedProxies(t, "10.0.0.0/8"))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	// 客户端伪造一个「也在信任范围内」的地址。它排在真实对端的左侧，
	// 所以旧实现（Echo 的）会把它当成客户端。
	req.Header.Set(echo.HeaderXForwardedFor, "10.9.9.9")

	if got := extractor(req); got != "10.0.0.5" {
		t.Fatalf("client IP = %q, want the peer address 10.0.0.5 (the leftmost value is client-supplied)", got)
	}
}

// TestForwardedIPExtractorRejectsUnparseableHop 覆盖「垃圾条目不能用来选桶」。
func TestForwardedIPExtractorRejectsUnparseableHop(t *testing.T) {
	extractor := BuildIPExtractor(mustTrustedProxies(t, "10.0.0.0/8"))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set(echo.HeaderXForwardedFor, "203.0.113.9, not-an-ip")

	if got := extractor(req); got != "10.0.0.5" {
		t.Fatalf("client IP = %q, want the peer address 10.0.0.5", got)
	}
}

// TestForwardedIPExtractorHandlesMultipleHeaderLines 覆盖同一请求带多行 XFF 的情况：
// 合并后仍按「从右往左第一个不可信」处理。
func TestForwardedIPExtractorHandlesMultipleHeaderLines(t *testing.T) {
	extractor := BuildIPExtractor(mustTrustedProxies(t, "10.0.0.0/8"))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Add(echo.HeaderXForwardedFor, "1.2.3.4")
	req.Header.Add(echo.HeaderXForwardedFor, "203.0.113.9")

	if got := extractor(req); got != "203.0.113.9" {
		t.Fatalf("client IP = %q, want 203.0.113.9", got)
	}
}

// TestBuildIPExtractorResistsSpoofedForwardedChain 是「攻击者自带 X-Forwarded-For」
// 这一场景的守护测试。
//
// 默认部署是同机 nginx + trusted_proxies=回环，而 nginx 常用
// `$proxy_add_x_forwarded_for`（**追加**而非覆盖）。于是应用看到的是
// 「攻击者自己写的值, 攻击者真实地址, 127.0.0.1」。若实现取最左值，
// 攻击者只要换个头就能换一个限流桶，登录、评论、邮件限流全部形同虚设。
//
// 正确的规则是**从右往左取第一个不可信地址**：最右边的可信跳数由我们自己
// 的基础设施产生，第一个不可信地址才是对端真实地址，其左侧全部由客户端控制。
// 这条测试把规则钉住——一旦有人把 extractor 换成朴素取最左值，它会立刻失败。
func TestBuildIPExtractorResistsSpoofedForwardedChain(t *testing.T) {
	extractor := BuildIPExtractor(mustTrustedProxies(t, "127.0.0.1/32", "::1/128"))

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{
			name:       "spoofed leftmost value is ignored",
			remoteAddr: "127.0.0.1:5555",
			xff:        "1.2.3.4, 198.51.100.9",
			want:       "198.51.100.9",
		},
		{
			name:       "attacker claiming a trusted address does not help",
			remoteAddr: "127.0.0.1:5555",
			xff:        "127.0.0.1, 198.51.100.9",
			want:       "198.51.100.9",
		},
		{
			name:       "single-hop chain from the proxy",
			remoteAddr: "127.0.0.1:5555",
			xff:        "198.51.100.9",
			want:       "198.51.100.9",
		},
		{
			name:       "a malformed entry cannot be used to pick a bucket",
			remoteAddr: "127.0.0.1:5555",
			// The leftmost value is attacker-controlled, so it is never reached;
			// the malformed entry sits on the trusted side of the chain, and the
			// safe answer there is the peer address rather than a guess.
			xff:  "1.2.3.4, not-an-ip",
			want: "127.0.0.1",
		},
		{
			name:       "a direct client cannot smuggle an address at all",
			remoteAddr: "198.51.100.9:5555",
			xff:        "127.0.0.1",
			want:       "198.51.100.9",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set(echo.HeaderXForwardedFor, tc.xff)
			// X-Real-IP is a second, equally forgeable header. This extractor must
			// not consult it — otherwise switching proxies would silently reopen
			// the hole the XFF rule just closed.
			req.Header.Set(echo.HeaderXRealIP, "203.0.113.99")

			if got := extractor(req); got != tc.want {
				t.Fatalf("client IP = %q, want %q", got, tc.want)
			}
		})
	}
}

func runOriginCheck(method, target, origin, referer string, allowed []string) int {
	return runOriginCheckForwarded(method, target, origin, referer, allowed, "", false)
}

// runOriginCheckForwarded 跑一次 OriginCheck。
//
// trustForwarded 现在不是布尔开关而是「对端是否落在 trusted_proxies 内」，
// 所以这里用 httptest 的默认对端地址（192.0.2.1）来模拟「请求确实来自代理」：
// 需要采信 X-Forwarded-Host 时就把该地址配成可信代理，否则给空集合。
func runOriginCheckForwarded(method, target, origin, referer string, allowed []string, forwardedHost string, trustForwarded bool) int {
	proxies := TrustedProxies(nil)
	if trustForwarded {
		var err error
		proxies, err = ParseTrustedProxies([]string{"192.0.2.1/32"})
		if err != nil {
			panic(err)
		}
	}

	e := echo.New()
	e.Use(OriginCheck(allowed, proxies))
	e.Any("/*", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	req := httptest.NewRequest(method, target, nil)
	if origin != "" {
		req.Header.Set(echo.HeaderOrigin, origin)
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if forwardedHost != "" {
		req.Header.Set("X-Forwarded-Host", forwardedHost)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code
}

func TestOriginCheck(t *testing.T) {
	const allowedOrigin = "https://admin.example.com"

	cases := []struct {
		name    string
		method  string
		target  string
		origin  string
		referer string
		allowed []string
		want    int
	}{
		{"same-origin post", http.MethodPost, "http://blog.test/api/comments", "http://blog.test", "", nil, http.StatusOK},
		{"cross-origin post rejected", http.MethodPost, "http://blog.test/api/comments", "http://evil.test", "", nil, http.StatusForbidden},
		{"opaque origin rejected", http.MethodPost, "http://blog.test/api/comments", "null", "", nil, http.StatusForbidden},
		{"no origin allowed", http.MethodPost, "http://blog.test/api/comments", "", "", nil, http.StatusOK},
		{"cross-site referer rejected", http.MethodPost, "http://blog.test/api/comments", "", "http://evil.test/page", nil, http.StatusForbidden},
		{"same-site referer allowed", http.MethodPost, "http://blog.test/api/comments", "", "http://blog.test/page", nil, http.StatusOK},
		{"cross-origin get is not blocked", http.MethodGet, "http://blog.test/api/articles", "http://evil.test", "", nil, http.StatusOK},
		{"allowlisted origin permitted", http.MethodPost, "http://blog.test/api/admin/articles", allowedOrigin, "", []string{allowedOrigin}, http.StatusOK},
		{"unlisted origin still rejected", http.MethodPost, "http://blog.test/api/admin/articles", "http://other.test", "", []string{allowedOrigin}, http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runOriginCheck(tc.method, tc.target, tc.origin, tc.referer, tc.allowed); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSecureHeadersRejectsInlineScriptScheme documents that the CSP is what
// neutralises javascript: links in article bodies, which are intentionally not
// HTML-sanitized.
// TestOriginCheckForwardedHost covers a reverse proxy that replaces the Host
// header: without honouring X-Forwarded-Host every same-site write would be
// rejected, but the header must only be believed when proxies are trusted.
func TestOriginCheckForwardedHost(t *testing.T) {
	const target = "http://backend.internal:8080/api/admin/articles"
	const publicOrigin = "https://blog.example.com"

	trusted := runOriginCheckForwarded(http.MethodPost, target, publicOrigin, "", nil, "blog.example.com", true)
	if trusted != http.StatusOK {
		t.Fatalf("trusted forwarded host status = %d, want 200", trusted)
	}

	untrusted := runOriginCheckForwarded(http.MethodPost, target, publicOrigin, "", nil, "blog.example.com", false)
	if untrusted != http.StatusForbidden {
		t.Fatalf("untrusted forwarded host status = %d, want 403 (header must be ignored)", untrusted)
	}

	// A forged forwarded host must not rescue a genuinely cross-site request.
	forged := runOriginCheckForwarded(http.MethodPost, target, "http://evil.test", "", nil, "blog.example.com", true)
	if forged != http.StatusForbidden {
		t.Fatalf("forged origin status = %d, want 403", forged)
	}

	// The allowlist remains an escape hatch regardless of Host rewriting.
	allowlisted := runOriginCheckForwarded(http.MethodPost, target, publicOrigin, "", []string{publicOrigin}, "other.internal", true)
	if allowlisted != http.StatusOK {
		t.Fatalf("allowlisted origin status = %d, want 200", allowlisted)
	}
}

// TestOriginCheckIgnoresForwardedHostFromUntrustedPeer 是本轮新增的守护测试。
//
// 改动前 trustForwardedHost 是 `len(cfg.TrustedProxies) > 0`——一个全局布尔量，
// 而 trusted_proxies 的默认值是回环（非空）。于是**没有任何代理的直连部署**
// 也会无条件采信 X-Forwarded-Host，恰恰在最需要这道防线的场景里把三层防护
// 削成两层。现在改成按请求判定：对端不在可信范围内就一律忽略该头。
func TestOriginCheckIgnoresForwardedHostFromUntrustedPeer(t *testing.T) {
	const target = "http://backend.internal:8080/api/admin/articles"
	const publicOrigin = "https://blog.example.com"

	// 对端是 httptest 的默认地址 192.0.2.1；可信列表里只有一个不相干的网段，
	// 因此这是一次「直连」请求，X-Forwarded-Host 必须被忽略。
	proxies := mustTrustedProxies(t, "127.0.0.1/32")

	e := echo.New()
	e.Use(OriginCheck(nil, proxies))
	e.Any("/*", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, target, nil)
	req.Header.Set(echo.HeaderOrigin, publicOrigin)
	req.Header.Set("X-Forwarded-Host", "blog.example.com")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: a direct client must not be able to authorise itself with X-Forwarded-Host", rec.Code)
	}
}

func TestSecureHeadersSetsCSP(t *testing.T) {
	e := echo.New()
	e.Use(SecureHeaders())
	e.GET("/", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("Content-Security-Policy header is missing")
	}
	if !contains(csp, "script-src 'self'") || contains(csp, "unsafe-inline'") && !contains(csp, "style-src 'self' 'unsafe-inline'") {
		t.Fatalf("unexpected CSP: %s", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want DENY", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
