package view_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"blog-engine/internal/model"
)

// 本文件覆盖「目标不存在」与「目标存在但状态不允许」这两类答案。它们与
// handler_errors_e2e_test.go 的存储故障不同：这里数据库是好的，坏的是请求。

func TestHandlerReportsMissingTargets(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	seedContent(t, db)
	headers := admin.unsafe(nil)

	missing := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/articles/999999", nil},
		{http.MethodGet, "/api/moments/999999", nil},
		{http.MethodGet, "/api/pages/999999", nil},
		{http.MethodGet, "/api/tags/999999/articles?page=1&page_size=12", nil},
		{http.MethodGet, "/api/tags/999999/count", nil},
		{http.MethodGet, "/api/admin/emails/999999", nil},
		{http.MethodPost, "/api/admin/articles/999999/restore", nil},
		{http.MethodPost, "/api/admin/moments/999999/restore", nil},
		{http.MethodPost, "/api/admin/pages/999999/restore", nil},
		{http.MethodPost, "/api/admin/comments/999999/restore", nil},
		{http.MethodPut, "/api/admin/articles/999999", map[string]any{"title": "t"}},
		{http.MethodPut, "/api/admin/pages/999999", map[string]any{"title": "t", "content": "c"}},
		{http.MethodPut, "/api/admin/moments/999999", map[string]any{"content": "m"}},
		{http.MethodPut, "/api/admin/tags/999999", map[string]any{"name": "NoSuchTag"}},
		{http.MethodPut, "/api/admin/users/999999", map[string]any{
			"username": "nobody", "nickname": "Nobody", "email": "nobody@example.com",
		}},
	}
	for _, tc := range missing {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res := admin.call(tc.method, tc.path, tc.body, headers)
			expectStatus(t, res, http.StatusNotFound, tc.method+" "+tc.path)
		})
	}
}

// TestHandlerTagEndpointsCoverTheHappyPath is a companion to the 404 cases: it
// proves the tag read endpoints answer for a tag that exists, so the 404 above
// is about the missing row and not about a broken query.
func TestHandlerTagEndpointsCoverTheHappyPath(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	seedContent(t, db)

	list := admin.call(http.MethodGet, "/api/tags?page=1&page_size=12", nil, nil)
	expectStatus(t, list, http.StatusOK, "GET /api/tags")
	var tags struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	decodeInto(t, list, &tags)
	if len(tags.Items) == 0 {
		t.Fatal("the seeded article produced no tag")
	}
	id := strconv.FormatInt(tags.Items[0].ID, 10)
	expectStatus(t, admin.call(http.MethodGet, "/api/tags/"+id+"/articles?page=1&page_size=12", nil, nil),
		http.StatusOK, "GET /api/tags/:id/articles")
	expectStatus(t, admin.call(http.MethodGet, "/api/tags/"+id+"/count", nil, nil),
		http.StatusOK, "GET /api/tags/:id/count")
}

func TestHandlerReportsConflictsAndRejectedNames(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	seedContent(t, db)
	headers := admin.unsafe(nil)

	t.Run("the administrator row cannot be edited here", func(t *testing.T) {
		res := admin.call(http.MethodPut, "/api/admin/users/1", map[string]any{
			"username": "notallowed", "nickname": "Not Allowed", "email": "notallowed@example.com",
		}, headers)
		expectStatus(t, res, http.StatusConflict, "PUT /api/admin/users/1")
	})

	t.Run("a duplicate tag name is a conflict", func(t *testing.T) {
		extra := &model.Article{Title: "另一篇", Content: "正文", CreatedAt: 2, UpdatedAt: 2, Tags: []string{"Rust"}}
		if err := model.NewSQLiteArticleRepository(db).Create(context.Background(), extra); err != nil {
			t.Fatalf("seed a second tag: %v", err)
		}
		list := admin.call(http.MethodGet, "/api/tags?page=1&page_size=12", nil, nil)
		var tags struct {
			Items []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
		}
		decodeInto(t, list, &tags)
		rustID := int64(0)
		for _, tag := range tags.Items {
			if strings.EqualFold(tag.Name, "Rust") {
				rustID = tag.ID
			}
		}
		if rustID == 0 {
			t.Fatalf("tags = %+v, want a Rust entry", tags.Items)
		}
		res := admin.call(http.MethodPut, "/api/admin/tags/"+strconv.FormatInt(rustID, 10),
			map[string]any{"name": "Go"}, headers)
		expectStatus(t, res, http.StatusConflict, "renaming Rust to Go")
	})

	t.Run("a malformed email reaches the service and is a 400", func(t *testing.T) {
		// viewmodel 只要求非空；地址格式由 service 判定，所以这条请求会走到
		// handler 的 ValidationError 映射。
		res := admin.call(http.MethodPut, "/api/admin/users/2", map[string]any{
			"username": "visitor", "nickname": "访客", "email": "not-an-email",
		}, headers)
		expectStatus(t, res, http.StatusBadRequest, "updating a visitor with a malformed email")
	})

	t.Run("an empty tag name is a 400", func(t *testing.T) {
		res := admin.call(http.MethodPut, "/api/admin/tags/1", map[string]any{"name": "   "}, headers)
		expectStatus(t, res, http.StatusBadRequest, "renaming a tag to nothing")
	})
}

// TestHandlerTrashAndAccountListFailures covers the list endpoints whose whole
// query is gone. They are separate from the public list failures in
// handler_errors_e2e_test.go because each has its own handler method.
func TestHandlerTrashAndAccountListFailures(t *testing.T) {
	cases := []struct {
		name  string
		table string
		path  string
	}{
		{"articles in the trash", "articles", "/api/admin/trash/articles?page=1&page_size=12"},
		{"moments in the trash", "moments", "/api/admin/trash/moments?page=1&page_size=12"},
		{"pages in the trash", "pages", "/api/admin/trash/pages?page=1&page_size=12"},
		{"comments in the trash", "comments", "/api/admin/trash/comments?page=1&page_size=12"},
		{"tag articles", "article_tags", "/api/tags/1/articles?page=1&page_size=12"},
		{"tag count", "article_tags", "/api/tags/1/count"},
		{"stored emails", "emails", "/api/admin/emails/1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, db, _ := newTestServer(t)
			admin := installFreshBlog(t, server)
			seedContent(t, db)
			dropTable(t, db, tc.table)

			res := admin.call(http.MethodGet, tc.path, nil, nil)
			if res.status < 500 {
				t.Fatalf("GET %s: status = %d, want a server error (body: %s)",
					tc.path, res.status, truncate(res.body))
			}
		})
	}
}

// TestUserListReportsAStorageFailure renames a column the list query reads.
// Dropping users outright would break the session lookup first, turning the
// answer into a 401 and testing the middleware instead of the handler.
func TestUserListReportsAStorageFailure(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	seedContent(t, db)
	renameColumn(t, db, "users", "nickname")

	res := admin.call(http.MethodGet, "/api/admin/users?page=1&page_size=12", nil, nil)
	if res.status < 500 {
		t.Fatalf("status = %d, want a server error (body: %s)", res.status, truncate(res.body))
	}
}

// TestArticleUpdateReportsAnUnmappedStorageFailure covers mapArticleError's
// default arm: the article table is gone, so Update fails with a repository
// error rather than a validation or not-found one.
func TestArticleUpdateReportsAnUnmappedStorageFailure(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	seedContent(t, db)
	dropTable(t, db, "articles")

	res := admin.call(http.MethodPut, "/api/admin/articles/1", map[string]any{"title": "t"}, admin.unsafe(nil))
	if res.status < 500 {
		t.Fatalf("status = %d, want a server error (body: %s)", res.status, truncate(res.body))
	}
}
