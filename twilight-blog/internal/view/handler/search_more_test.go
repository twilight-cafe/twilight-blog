package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/search"
)

// newEcho 造一个用于单端点测试的 Echo 实例。
func newEcho() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	return e
}

// performRequest 打一个请求并返回记录器。
func performRequest(t *testing.T, e *echo.Echo, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

// TestSearchHandlerBranches 覆盖搜索端点的每一条退出路径。
//
// 搜索端点在 client 为 nil 时根本不会注册（router 的既定行为），所以「搜索不可用」
// 这一条只能在这里直接构造 handler 来验；而 q 为空、q 过长、分页非法、下游报错
// 这几条都走真实的 HTTP 请求。
func TestSearchHandlerBranches(t *testing.T) {
	t.Run("the client is missing", func(t *testing.T) {
		e := newEcho()
		e.GET("/api/search", NewSearchHandler(nil).Search)

		res := performRequest(t, e, http.MethodGet, "/api/search?q=go&page=1&page_size=12")
		if res.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (body: %s)", res.Code, res.Body.String())
		}
	})

	t.Run("q is required", func(t *testing.T) {
		e := newEcho()
		e.GET("/api/search", NewSearchHandler(search.NewClient("http://127.0.0.1:1", "")).Search)

		res := performRequest(t, e, http.MethodGet, "/api/search?q=%20%20&page=1&page_size=12")
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", res.Code, res.Body.String())
		}
	})

	t.Run("q is too long", func(t *testing.T) {
		e := newEcho()
		e.GET("/api/search", NewSearchHandler(search.NewClient("http://127.0.0.1:1", "")).Search)

		long := strings.Repeat("字", maxSearchQueryLength+1)
		res := performRequest(t, e, http.MethodGet, "/api/search?q="+long+"&page=1&page_size=12")
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", res.Code, res.Body.String())
		}
	})

	t.Run("the pagination is invalid", func(t *testing.T) {
		e := newEcho()
		e.GET("/api/search", NewSearchHandler(search.NewClient("http://127.0.0.1:1", "")).Search)

		res := performRequest(t, e, http.MethodGet, "/api/search?q=go&page=0")
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", res.Code, res.Body.String())
		}
	})

	t.Run("the downstream search fails", func(t *testing.T) {
		downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer downstream.Close()

		e := newEcho()
		e.GET("/api/search", NewSearchHandler(search.NewClient(downstream.URL, "")).Search)

		res := performRequest(t, e, http.MethodGet, "/api/search?q=go&page=1&page_size=12")
		if res.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (body: %s)", res.Code, res.Body.String())
		}
	})

	t.Run("the search succeeds", func(t *testing.T) {
		downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hits": []any{map[string]any{
					"kind":           "article",
					"id":             1,
					"title":          "命中",
					"content":        "正文",
					"created_at":     10,
					"updated_at":     20,
					"tags":           []any{"Go"},
					"cover_media_id": nil,
				}},
				"totalHits": 1,
			})
		}))
		defer downstream.Close()

		e := newEcho()
		e.GET("/api/search", NewSearchHandler(search.NewClient(downstream.URL, "")).Search)

		res := performRequest(t, e, http.MethodGet, "/api/search?q=go&page=1&page_size=12")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "命中") {
			t.Fatalf("body = %s", res.Body.String())
		}
	})
}
