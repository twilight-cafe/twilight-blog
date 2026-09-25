package middleware

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
)

func TestCORSAbsentAndConfigured(t *testing.T) {
	if CORS(nil) != nil {
		t.Fatal("CORS(nil) should not mount a middleware")
	}
	if CORS([]string{}) != nil {
		t.Fatal("CORS(empty) should not mount a middleware")
	}

	e := echo.New()
	e.Use(CORS([]string{"https://allowed.example.com"}))
	e.GET("/", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set(echo.HeaderOrigin, "https://allowed.example.com")
	req.Header.Set(echo.HeaderAccessControlRequestMethod, http.MethodGet)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("CORS preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get(echo.HeaderAccessControlAllowOrigin); got != "https://allowed.example.com" {
		t.Fatalf("allow-origin = %q", got)
	}
}

func TestNormalizeOriginAndSameHostHelpers(t *testing.T) {
	if got := normalizeOrigin("https://Example.COM:443/"); got != "https://example.com:443" {
		t.Fatalf("normalizeOrigin URL = %q", got)
	}
	if got := normalizeOrigin("Example.com/"); got != "example.com" {
		t.Fatalf("normalizeOrigin host-only = %q", got)
	}
	if got := normalizeOrigin("  "); got != "" {
		t.Fatalf("normalizeOrigin(blank) = %q", got)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "http://blog.test/", nil)
	req.Host = "blog.test"
	c := e.NewContext(req, httptest.NewRecorder())
	if !sameHost("http://blog.test/x", c, nil) {
		t.Fatal("same host should match")
	}
	if sameHost("/relative", c, nil) {
		t.Fatal("a relative URL has no host and must not match")
	}
	if sameHost("http://other.test/", c, nil) {
		t.Fatal("a different host must not match when the peer is untrusted")
	}

	if got := forwardedHosts(""); got != nil {
		t.Fatalf("forwardedHosts(empty) = %v, want nil", got)
	}
	if got := forwardedHosts(" a.example.com , ,b.example.com "); len(got) != 2 || got[0] != "a.example.com" || got[1] != "b.example.com" {
		t.Fatalf("forwardedHosts = %v", got)
	}
}

func TestForwardedIPExtractorDirectPeerFallbacks(t *testing.T) {
	proxies := mustTrustedProxies(t, "10.0.0.0/8")
	extractor := forwardedIPExtractor(proxies)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = ""
	if got := extractor(req); got != "" {
		t.Fatalf("empty RemoteAddr extractor = %q, want empty", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.2.3"
	req.Header.Set(echo.HeaderXForwardedFor, "not-an-ip")
	if got := extractor(req); got != "10.1.2.3" {
		t.Fatalf("unparseable XFF extractor = %q, want peer", got)
	}
}

func TestTrustedProxiesContains(t *testing.T) {
	var nilProxies TrustedProxies
	if nilProxies.Contains(net.ParseIP("127.0.0.1")) {
		t.Fatal("empty TrustedProxies must contain nothing")
	}
	if nilProxies.Contains(nil) {
		t.Fatal("nil IP must not be contained")
	}
	proxies := mustTrustedProxies(t, "127.0.0.1/32")
	if !proxies.Contains(net.ParseIP("127.0.0.1")) {
		t.Fatal("loopback should be trusted")
	}
	if proxies.Contains(net.ParseIP("192.0.2.1")) {
		t.Fatal("unlisted IP should not be trusted")
	}
}

func TestDirectPeerAndHTTPSHelpers(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.7"
	if got := directPeerAddress(req); got != "198.51.100.7" {
		t.Fatalf("directPeerAddress without port = %q", got)
	}
	req.RemoteAddr = "198.51.100.7:8080"
	if got := directPeerAddress(req); got != "198.51.100.7" {
		t.Fatalf("directPeerAddress with port = %q", got)
	}

	e := echo.New()
	req = httptest.NewRequest(http.MethodGet, "https://blog.test/", nil)
	req.TLS = &tls.ConnectionState{}
	c := e.NewContext(req, httptest.NewRecorder())
	if !RequestIsHTTPS(c, false) {
		t.Fatal("TLS request should be HTTPS")
	}

	req = httptest.NewRequest(http.MethodGet, "http://blog.test/", nil)
	c = e.NewContext(req, httptest.NewRecorder())
	if RequestIsHTTPS(c, false) {
		t.Fatal("plain HTTP without trust should not be HTTPS")
	}
	req.Header.Set("X-Forwarded-Proto", "https, http")
	if !RequestIsHTTPS(c, true) {
		t.Fatal("trusted X-Forwarded-Proto first value should mean HTTPS")
	}
	req.Header.Set("X-Forwarded-Proto", "http")
	if RequestIsHTTPS(c, true) {
		t.Fatal("http X-Forwarded-Proto should not be HTTPS")
	}

	if !CookieSecureFor(c, config.CookieSecureAlways, false) {
		t.Fatal("always must set Secure")
	}
	if CookieSecureFor(c, config.CookieSecureNever, false) {
		t.Fatal("never must not set Secure")
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	if !CookieSecureFor(c, config.CookieSecureAuto, true) {
		t.Fatal("auto with trusted forwarded https should set Secure")
	}
}

func TestRateLimiterFallsBackToPositiveAllowanceAndDenies(t *testing.T) {
	e := echo.New()
	e.Use(perIPRateLimiter("coverage", 0, time.Minute))
	e.GET("/", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodGet, "/", nil)
	firstReq.RemoteAddr = "203.0.113.9:1234"
	e.ServeHTTP(first, firstReq)
	if first.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", first.Code)
	}
	second := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	e.ServeHTTP(second, req)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", second.Code)
	}
	if second.Header().Get(echo.HeaderRetryAfter) == "" {
		t.Fatal("429 response should carry Retry-After")
	}
}

func TestRateLimiterIdentifierErrorReturnsUnavailable(t *testing.T) {
	original := rateLimitIdentifierExtractor
	wantErr := errors.New("identifier unavailable")
	rateLimitIdentifierExtractor = func(c echo.Context) (string, error) {
		return "", wantErr
	}
	t.Cleanup(func() { rateLimitIdentifierExtractor = original })

	e := echo.New()
	e.Use(perIPRateLimiter("coverage-error", 1, time.Minute))
	e.GET("/", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("identifier error status = %d, want 500", rec.Code)
	}
}

func TestRateLimiterShortWindowUsesOneSecondRetryAfter(t *testing.T) {
	e := echo.New()
	e.Use(perIPRateLimiter("coverage-short-window", 1, 100*time.Millisecond))
	e.GET("/", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "203.0.113.10:1234"
		e.ServeHTTP(rec, req)
		if i == 1 {
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("second request status = %d, want 429", rec.Code)
			}
			if got := rec.Header().Get(echo.HeaderRetryAfter); got != "1" {
				t.Fatalf("Retry-After = %q, want 1", got)
			}
		}
	}
}

func TestForwardedIPExtractorTrustedPeerWithoutForwardedHeader(t *testing.T) {
	proxies := mustTrustedProxies(t, "10.0.0.0/8")
	extractor := forwardedIPExtractor(proxies)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.2.3"
	if got := extractor(req); got != "10.1.2.3" {
		t.Fatalf("extractor without XFF = %q, want trusted peer", got)
	}
}
