package view_test

import (
	"net/http"
	"testing"
)

// TestRequestValidationIsRejected 覆盖 HTTP 边界上的**校验分支**。
//
// 这些分支在正常路径上一次都跑不到，却正是「用户填错了会看到什么」的全部答案。
// 它们散落在十几个 handler 里，因此这里用一张表把「端点 + 请求 + 期望状态」
// 摆平——每加一个端点只需要加一行，而不是再写一个用例。
func TestRequestValidationIsRejected(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	seedContent(t, db)
	headers := admin.unsafe(nil)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
	}{
		// 评论
		{"comment list without a target id", http.MethodGet, "/api/comments?target_type=article", nil, http.StatusBadRequest},
		{"comment list with a broken target id", http.MethodGet, "/api/comments?target_type=article&target_id=abc", nil, http.StatusBadRequest},
		{"comment list without pagination", http.MethodGet, "/api/comments?target_type=article&target_id=1", nil, http.StatusBadRequest},
		{"comment list with an unknown target type", http.MethodGet, "/api/comments?target_type=weird&target_id=1&page=1&page_size=12", nil, http.StatusBadRequest},
		{"comment list for a missing target", http.MethodGet, "/api/comments?target_type=article&target_id=999&page=1&page_size=12", nil, http.StatusNotFound},
		{"like with a broken id", http.MethodPost, "/api/comments/abc/like", nil, http.StatusBadRequest},
		{"like as the administrator", http.MethodPost, "/api/comments/1/like", nil, http.StatusForbidden},
		{"unlike with a broken id", http.MethodDelete, "/api/comments/abc/like", nil, http.StatusBadRequest},
		{"unlike as the administrator", http.MethodDelete, "/api/comments/1/like", nil, http.StatusForbidden},
		{"create a comment with an unknown target type", http.MethodPost, "/api/comments", map[string]any{
			"target_type": "weird", "target_id": 1, "content": "c",
		}, http.StatusBadRequest},

		// 文章
		{"create an article without a title", http.MethodPost, "/api/admin/articles", map[string]any{
			"title": "   ", "content": "c",
		}, http.StatusBadRequest},
		{"update an article with a broken id", http.MethodPut, "/api/admin/articles/abc", map[string]any{
			"title": "t",
		}, http.StatusBadRequest},
		{"delete an article with a broken id", http.MethodDelete, "/api/admin/articles/abc", nil, http.StatusBadRequest},
		{"restore an article with a broken id", http.MethodPost, "/api/admin/articles/abc/restore", nil, http.StatusBadRequest},
		{"permanently delete an article with a broken id", http.MethodDelete, "/api/admin/articles/abc/permanent", nil, http.StatusBadRequest},
		{"batch-delete articles without ids", http.MethodDelete, "/api/admin/articles", map[string]any{
			"ids": []int{},
		}, http.StatusBadRequest},
		{"list articles without pagination", http.MethodGet, "/api/articles", nil, http.StatusBadRequest},
		{"read an article with a broken id", http.MethodGet, "/api/articles/abc", nil, http.StatusBadRequest},

		// 微语
		{"create a moment without content", http.MethodPost, "/api/admin/moments", map[string]any{
			"content": "   ",
		}, http.StatusBadRequest},
		{"update a moment with a broken id", http.MethodPut, "/api/admin/moments/abc", map[string]any{
			"content": "m",
		}, http.StatusBadRequest},
		{"delete a moment with a broken id", http.MethodDelete, "/api/admin/moments/abc", nil, http.StatusBadRequest},
		{"list moments without pagination", http.MethodGet, "/api/moments", nil, http.StatusBadRequest},

		// 页面
		{"create a page without a title", http.MethodPost, "/api/admin/pages", map[string]any{
			"title": "   ", "content": "c",
		}, http.StatusBadRequest},
		{"update a page with a broken id", http.MethodPut, "/api/admin/pages/abc", map[string]any{
			"title": "p",
		}, http.StatusBadRequest},
		{"delete a page with a broken id", http.MethodDelete, "/api/admin/pages/abc", nil, http.StatusBadRequest},
		{"list pages without pagination", http.MethodGet, "/api/pages", nil, http.StatusBadRequest},

		// 标签
		{"rename a tag with a broken id", http.MethodPut, "/api/admin/tags/abc", map[string]any{
			"name": "Golang",
		}, http.StatusBadRequest},
		{"rename a tag without a name", http.MethodPut, "/api/admin/tags/1", map[string]any{
			"name": "   ",
		}, http.StatusBadRequest},
		{"list tags without pagination", http.MethodGet, "/api/tags", nil, http.StatusBadRequest},
		{"count a tag with a broken id", http.MethodGet, "/api/tags/abc/count", nil, http.StatusBadRequest},
		{"list the articles of a tag with a broken id", http.MethodGet, "/api/tags/abc/articles", nil, http.StatusBadRequest},

		// 媒体
		{"read a media item with a broken id", http.MethodGet, "/api/media/abc", nil, http.StatusBadRequest},
		{"delete a media item with a broken id", http.MethodDelete, "/api/admin/media/abc", nil, http.StatusBadRequest},
		{"rename a media item with a broken id", http.MethodPatch, "/api/admin/media/abc", map[string]any{
			"original_name": "x.png",
		}, http.StatusBadRequest},
		{"rename a media item without a name", http.MethodPatch, "/api/admin/media/1", map[string]any{
			"original_name": "   ",
		}, http.StatusBadRequest},

		// 站点设置
		{"update the site with an out-of-range radius", http.MethodPut, "/api/admin/site", map[string]any{
			"description": "站点", "border_radius": 99,
		}, http.StatusBadRequest},
		{"update the site with an out-of-range banner height", http.MethodPut, "/api/admin/site", map[string]any{
			"description": "站点", "post_banner_height": 99,
		}, http.StatusBadRequest},
		{"update the site with a malformed notification address", http.MethodPut, "/api/admin/site", map[string]any{
			"description": "站点", "comment_notify_to": "not-an-address",
		}, http.StatusBadRequest},

		// 用户
		{"update a user with a broken id", http.MethodPut, "/api/admin/users/abc", map[string]any{
			"username": "u", "nickname": "n", "email": "u@example.com",
		}, http.StatusBadRequest},
		{"list users without pagination", http.MethodGet, "/api/admin/users", nil, http.StatusBadRequest},

		// 认证
		{"change the password without the current one", http.MethodPut, "/api/me/password", map[string]any{
			"current_password": "wrong", "new_password": "newpassword1",
		}, http.StatusUnauthorized},
		{"request an email code for a malformed address", http.MethodPost, "/api/auth/email-code", map[string]any{
			"email": "not-an-address",
		}, http.StatusBadRequest},
		{"register with a malformed address", http.MethodPost, "/api/auth/register", map[string]any{
			"email": "not-an-address", "username": "u", "nickname": "n", "password": "password1", "code": "123456",
		}, http.StatusBadRequest},
		{"reset a password with a malformed address", http.MethodPost, "/api/auth/reset-password", map[string]any{
			"email": "not-an-address", "code": "123456", "new_password": "newpassword1",
		}, http.StatusBadRequest},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			res := admin.call(test.method, test.path, test.body, headers)
			if res.status != test.want {
				t.Fatalf("%s %s: status = %d, want %d (body: %s)",
					test.method, test.path, res.status, test.want, truncate(res.body))
			}
		})
	}
}

// TestAnonymousEndpointsRejectMissingSessions 覆盖「没有会话」这条路径。
//
// 它必须用**另一个浏览器**：管理员的会话还在，同一个客户端读 /api/me 当然是 200。
func TestAnonymousEndpointsRejectMissingSessions(t *testing.T) {
	server, _, _ := newTestServer(t)
	installFreshBlog(t, server)

	anonymous := newAPI(t, server.URL)
	if res := anonymous.call(http.MethodGet, "/api/me", nil, nil); res.status != http.StatusUnauthorized {
		t.Fatalf("GET /api/me: status = %d, want 401 (body: %s)", res.status, truncate(res.body))
	}
	if res := anonymous.call(http.MethodPatch, "/api/me", map[string]any{"nickname": "n"}, nil); res.status != http.StatusUnauthorized {
		t.Fatalf("PATCH /api/me: status = %d, want 401 (body: %s)", res.status, truncate(res.body))
	}
	if res := anonymous.call(http.MethodPost, "/api/comments/1/like", nil, nil); res.status != http.StatusUnauthorized {
		t.Fatalf("POST /api/comments/1/like: status = %d, want 401 (body: %s)", res.status, truncate(res.body))
	}
}

// TestInstallRejectsASecondInstallation 覆盖安装端的「已经装好了」分支。
func TestInstallRejectsASecondInstallation(t *testing.T) {
	server, _, _ := newTestServer(t)
	installFreshBlog(t, server)

	res := newAPI(t, server.URL).call(http.MethodPost, "/install", map[string]string{"password": "anotherpassword1"}, nil)
	if res.status != http.StatusConflict {
		t.Fatalf("POST /install: status = %d, want 409 (body: %s)", res.status, truncate(res.body))
	}
}

// TestInstallRejectsAWeakPassword 覆盖安装端的强度校验分支。
func TestInstallRejectsAWeakPassword(t *testing.T) {
	server, _, _ := newTestServer(t)

	res := newAPI(t, server.URL).call(http.MethodPost, "/install", map[string]string{"password": "short"}, nil)
	if res.status != http.StatusBadRequest {
		t.Fatalf("POST /install: status = %d, want 400 (body: %s)", res.status, truncate(res.body))
	}
}
