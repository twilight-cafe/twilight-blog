package view_test

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/model"
)

// 本文件把「客户端给错了」的分支一次跑遍：非法 id、缺失分页参数、坏 JSON、
// 空字段、越界批量 id。它们散落在十几个 handler 里，但共享同一种断言：
// 必须是 4xx，而且绝不能落到 500。
func TestHandlerRejectsMalformedInput(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	seedContent(t, db)
	seedInputVisitor(t, db)

	headers := admin.unsafe(nil)
	visitor := newAPI(t, server.URL)
	expectStatus(t, visitor.call(http.MethodPost, "/api/auth/login",
		map[string]string{"username": "input-visitor", "password": "Visitor1pass"}, nil),
		http.StatusOK, "POST /api/auth/login as the visitor")
	visitorHeaders := visitor.unsafe(nil)

	invalidIDs := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/articles/abc"},
		{http.MethodGet, "/api/moments/abc"},
		{http.MethodGet, "/api/pages/abc"},
		{http.MethodGet, "/api/tags/abc/articles"},
		{http.MethodGet, "/api/tags/abc/count"},
		{http.MethodPut, "/api/admin/articles/abc"},
		{http.MethodPost, "/api/admin/articles/abc/restore"},
		{http.MethodDelete, "/api/admin/articles/abc/permanent"},
		{http.MethodDelete, "/api/admin/articles/abc"},
		{http.MethodPut, "/api/admin/tags/abc"},
		{http.MethodPut, "/api/admin/moments/abc"},
		{http.MethodPost, "/api/admin/moments/abc/restore"},
		{http.MethodDelete, "/api/admin/moments/abc/permanent"},
		{http.MethodDelete, "/api/admin/moments/abc"},
		{http.MethodPut, "/api/admin/pages/abc"},
		{http.MethodPost, "/api/admin/pages/abc/restore"},
		{http.MethodDelete, "/api/admin/pages/abc/permanent"},
		{http.MethodDelete, "/api/admin/pages/abc"},
		{http.MethodPatch, "/api/admin/media/abc"},
		{http.MethodDelete, "/api/admin/media/abc"},
		{http.MethodDelete, "/api/admin/comments/abc"},
		{http.MethodPost, "/api/admin/comments/abc/restore"},
		{http.MethodDelete, "/api/admin/comments/abc/permanent"},
		{http.MethodPost, "/api/comments/abc/like"},
		{http.MethodDelete, "/api/comments/abc/like"},
		{http.MethodGet, "/api/admin/emails/abc"},
		{http.MethodPut, "/api/admin/users/abc"},
	}
	for _, tc := range invalidIDs {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			who := admin
			h := headers
			if tc.path == "/api/comments/abc/like" {
				who = visitor
				h = visitorHeaders
			}
			res := who.call(tc.method, tc.path, nil, h)
			expectStatus(t, res, http.StatusBadRequest, tc.method+" "+tc.path)
		})
	}

	missingPagination := []string{
		"/api/articles",
		"/api/tags",
		"/api/moments",
		"/api/pages",
		"/api/tags/1/articles",
		"/api/comments?target_type=article&target_id=1",
		"/api/admin/trash/articles",
		"/api/admin/trash/moments",
		"/api/admin/trash/pages",
		"/api/admin/trash/comments",
		"/api/admin/comments",
		"/api/admin/emails",
		"/api/admin/media",
		"/api/admin/users",
	}
	for _, path := range missingPagination {
		t.Run("missing pagination "+path, func(t *testing.T) {
			who := admin
			if path == "/api/articles" || path == "/api/tags" || path == "/api/moments" ||
				path == "/api/pages" || path == "/api/comments?target_type=article&target_id=1" {
				who = visitor
			}
			res := who.call(http.MethodGet, path, nil, nil)
			expectStatus(t, res, http.StatusBadRequest, "GET "+path)
		})
	}

	// 分页参数的每一条非法形态。它们各自对应 parsePagination 里的一道闸，
	// 而四道闸的回答都是一样的 400——这里要的是「每一道都真的被走到」。
	badPagination := []string{
		"/api/articles?page=1",
		"/api/articles?page=1&page_size=0",
		"/api/articles?page=1&page_size=13",
		"/api/articles?page=999999999&page_size=1",
	}
	for _, path := range badPagination {
		t.Run("bad pagination "+path, func(t *testing.T) {
			res := visitor.call(http.MethodGet, path, nil, nil)
			expectStatus(t, res, http.StatusBadRequest, "GET "+path)
		})
	}

	badBodies := []struct {
		method string
		path   string
		body   string
		who    *api
		header map[string]string
	}{
		{http.MethodPost, "/api/admin/articles", "{", admin, headers},
		{http.MethodPost, "/api/admin/articles", `{}`, admin, headers},
		{http.MethodPut, "/api/admin/articles/1", "{", admin, headers},
		{http.MethodPut, "/api/admin/articles/1", `{}`, admin, headers},
		{http.MethodPost, "/api/admin/articles/batch-restore", "{", admin, headers},
		{http.MethodPost, "/api/admin/articles/batch-restore", `{}`, admin, headers},
		{http.MethodDelete, "/api/admin/articles/batch-permanent", "{", admin, headers},
		{http.MethodDelete, "/api/admin/articles/batch-permanent", `{}`, admin, headers},
		{http.MethodDelete, "/api/admin/articles", "{", admin, headers},
		{http.MethodDelete, "/api/admin/articles", `{}`, admin, headers},
		{http.MethodPost, "/api/admin/moments", "{", admin, headers},
		{http.MethodPut, "/api/admin/moments/1", "{", admin, headers},
		{http.MethodPut, "/api/admin/moments/1", `{}`, admin, headers},
		{http.MethodPost, "/api/admin/moments/batch-restore", "{", admin, headers},
		{http.MethodPost, "/api/admin/moments/batch-restore", `{}`, admin, headers},
		{http.MethodDelete, "/api/admin/moments/batch-permanent", "{", admin, headers},
		{http.MethodDelete, "/api/admin/moments/batch-permanent", `{}`, admin, headers},
		{http.MethodDelete, "/api/admin/moments", "{", admin, headers},
		{http.MethodDelete, "/api/admin/moments", `{}`, admin, headers},
		{http.MethodPost, "/api/admin/pages", "{", admin, headers},
		{http.MethodPost, "/api/admin/pages", `{}`, admin, headers},
		{http.MethodPut, "/api/admin/pages/1", "{", admin, headers},
		{http.MethodPut, "/api/admin/pages/1", `{}`, admin, headers},
		{http.MethodPost, "/api/admin/pages/batch-restore", "{", admin, headers},
		{http.MethodPost, "/api/admin/pages/batch-restore", `{}`, admin, headers},
		{http.MethodDelete, "/api/admin/pages/batch-permanent", "{", admin, headers},
		{http.MethodDelete, "/api/admin/pages/batch-permanent", `{}`, admin, headers},
		{http.MethodDelete, "/api/admin/pages", "{", admin, headers},
		{http.MethodDelete, "/api/admin/pages", `{}`, admin, headers},
		{http.MethodPut, "/api/admin/tags/1", "{", admin, headers},
		{http.MethodPut, "/api/admin/tags/1", `{}`, admin, headers},
		{http.MethodPut, "/api/admin/tags/1", `{"name":""}`, admin, headers},
		{http.MethodPatch, "/api/admin/media/1", "{", admin, headers},
		{http.MethodPatch, "/api/admin/media/1", `{"original_name":""}`, admin, headers},
		{http.MethodPut, "/api/admin/site", "{", admin, headers},
		{http.MethodPut, "/api/admin/site", `{"description":"d","code_block_max_lines":1}`, admin, headers},
		{http.MethodPut, "/api/admin/site", `{"description":"d","home_page_size":0}`, admin, headers},
		{http.MethodPut, "/api/admin/site", `{"description":"d","border_radius":25}`, admin, headers},
		{http.MethodPut, "/api/admin/site", `{"description":"d","post_banner_height":900}`, admin, headers},
		{http.MethodPut, "/api/admin/site", `{"description":"d","comment_notify_to":"bad"}`, admin, headers},
		{http.MethodPut, "/api/admin/profile", "{", admin, headers},
		{http.MethodPut, "/api/admin/profile", `{"name":""}`, admin, headers},
		{http.MethodPut, "/api/admin/users/2", "{", admin, headers},
		{http.MethodDelete, "/api/admin/comments", "{", admin, headers},
		{http.MethodDelete, "/api/admin/comments", `{}`, admin, headers},
		{http.MethodPost, "/api/admin/comments/batch-restore", "{", admin, headers},
		{http.MethodPost, "/api/admin/comments/batch-restore", `{}`, admin, headers},
		{http.MethodDelete, "/api/admin/comments/batch-permanent", "{", admin, headers},
		{http.MethodDelete, "/api/admin/comments/batch-permanent", `{}`, admin, headers},
		{http.MethodPut, "/api/admin/users/2", `{}`, admin, headers},
		{http.MethodPost, "/api/comments", "{", visitor, visitorHeaders},
		{http.MethodPost, "/api/comments", `{}`, visitor, visitorHeaders},
	}
	for _, tc := range badBodies {
		t.Run(tc.method+" "+tc.path+" "+tc.body, func(t *testing.T) {
			res := callRaw(t, tc.who, tc.method, tc.path, tc.body, tc.header)
			expectStatus(t, res, http.StatusBadRequest, tc.method+" "+tc.path)
		})
	}

	// 批量 id 超出上限 / 含 0。
	tooMany := make([]int, 1001)
	for i := range tooMany {
		tooMany[i] = i + 1
	}
	for _, path := range []string{"/api/admin/articles", "/api/admin/moments", "/api/admin/pages"} {
		res := admin.call(http.MethodDelete, path, map[string]any{"ids": tooMany}, headers)
		expectStatus(t, res, http.StatusBadRequest, "batch ids over the limit")
		res = admin.call(http.MethodDelete, path, map[string]any{"ids": []int{0}}, headers)
		expectStatus(t, res, http.StatusBadRequest, "batch ids with zero")
	}
}

// callRaw 发一个未经 JSON 编码的请求体，用来测试绑定失败。
func callRaw(t *testing.T, a *api, method, path, body string, headers map[string]string) response {
	t.Helper()
	req, err := http.NewRequest(method, a.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: data, cookies: resp.Cookies()}
}

func seedInputVisitor(t *testing.T, db *sql.DB) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("Visitor1pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := model.NewSQLiteUserRepository(db).Create(context.Background(), &model.User{
		Email: "input@example.com", Username: "input-visitor", Nickname: "访客",
		PasswordHash: string(hash), CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed visitor: %v", err)
	}
}
