package view_test

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"testing"

	"blog-engine/internal/model"
)

// 本文件用**数据库层的故障注入**覆盖 HTTP 层的错误分支。
//
// 手法有两种，都不需要改生产代码：
//   - 写操作用 SQLite 触发器强制失败（BEFORE INSERT/UPDATE/DELETE … RAISE(ABORT)）；
//   - 读操作用「把表删掉」让查询直接报错。
//
// 为什么值得单独写一个文件：handler 层的错误分支（把服务层的错误映射成
// 4xx/5xx）在正常路径上一次都跑不到，而它们恰恰是「数据库抖动时用户看到什么」
// 的全部答案。逐个构造服务层替身要写几百行，而触发器几行就够，且走的是
// 真实的 HTTP 栈——中间件、CSRF、鉴权都照常生效。
//
// 每个用例都自带一个全新的站点：故障注入要么装触发器、要么删表，都会污染
// 同一个库，而污染是累积的（后一个用例会撞上前一个用例留下的触发器）。

// failWrites 给某张表装上「一写就失败」的触发器。
func failWrites(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	statements := []string{
		"CREATE TRIGGER IF NOT EXISTS fail_" + table + "_insert BEFORE INSERT ON " + table +
			" BEGIN SELECT RAISE(ABORT, 'injected failure'); END",
		"CREATE TRIGGER IF NOT EXISTS fail_" + table + "_update BEFORE UPDATE ON " + table +
			" BEGIN SELECT RAISE(ABORT, 'injected failure'); END",
		"CREATE TRIGGER IF NOT EXISTS fail_" + table + "_delete BEFORE DELETE ON " + table +
			" BEGIN SELECT RAISE(ABORT, 'injected failure'); END",
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("install the failing trigger on %s: %v", table, err)
		}
	}
}

// failDeletes 只让删除失败，保留插入与更新。用于「补偿删除自己失败了」这种
// 只有批量上传才会走到的分支。
func failDeletes(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		"CREATE TRIGGER IF NOT EXISTS fail_"+table+"_delete BEFORE DELETE ON "+table+
			" BEGIN SELECT RAISE(ABORT, 'injected failure'); END"); err != nil {
		t.Fatalf("install the failing delete trigger on %s: %v", table, err)
	}
}

// dropTable 让某张表的查询直接失败（读路径）。
func dropTable(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), "DROP TABLE "+table); err != nil {
		t.Fatalf("drop %s: %v", table, err)
	}
}

// renameColumn 让**读某一列**的查询失败，而不影响只读主键或角色的查询。
// users 表不能整张删掉：会话中间件还要靠它判定管理员身份，删表会把 500 变成 401。
func renameColumn(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		"ALTER TABLE "+table+" RENAME COLUMN "+column+" TO "+column+"_gone"); err != nil {
		t.Fatalf("rename %s.%s: %v", table, column, err)
	}
}

// seedContent 造出一篇文章、一条评论、一个标签、一条微语、一个页面与一张媒体，
// 让「按 id 操作」的端点有真实目标可用。
func seedContent(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	article := &model.Article{Title: "文章", Content: "正文", CreatedAt: 1, UpdatedAt: 1, Tags: []string{"Go"}}
	if err := model.NewSQLiteArticleRepository(db).Create(ctx, article); err != nil {
		t.Fatalf("seed an article: %v", err)
	}
	moment := &model.Moment{Content: "微语", CreatedAt: 1, UpdatedAt: 1}
	if err := model.NewSQLiteMomentRepository(db).Create(ctx, moment); err != nil {
		t.Fatalf("seed a moment: %v", err)
	}
	page := &model.Page{Title: "页面", Content: "内容", CreatedAt: 1, UpdatedAt: 1}
	if err := model.NewSQLitePageRepository(db).Create(ctx, page); err != nil {
		t.Fatalf("seed a page: %v", err)
	}
	comment := &model.Comment{
		AuthorType: "admin",
		AdminName:  "站长",
		TargetType: "article",
		TargetID:   article.ID,
		Content:    "评论",
		CreatedAt:  1,
		UpdatedAt:  1,
	}
	if err := model.NewSQLiteCommentRepository(db).Create(ctx, comment); err != nil {
		t.Fatalf("seed a comment: %v", err)
	}
	media := &model.Media{
		StorageName:  "seed.webp",
		OriginalName: "seed.png",
		MediaType:    model.MediaTypeImage,
		MimeType:     "image/webp",
		Size:         10,
		Converted:    true,
		CreatedAt:    1,
	}
	if err := model.NewSQLiteMediaRepository(db).Create(ctx, media); err != nil {
		t.Fatalf("seed a media item: %v", err)
	}
	// 一个访客账号：管理员行不能通过 /api/admin/users/:id 改（那是给访客账号用的）。
	visitor := &model.User{
		Email:     "visitor@example.com",
		Username:  "visitor",
		Nickname:  "访客",
		CreatedAt: 1,
		UpdatedAt: 1,
	}
	if err := model.NewSQLiteUserRepository(db).Create(ctx, visitor); err != nil {
		t.Fatalf("seed a visitor account: %v", err)
	}
}

// softDeleteArticle 把文章放进回收站：恢复类端点只对回收站里的行有效。
func softDeleteArticle(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := model.NewSQLiteArticleRepository(db).SoftDelete(context.Background(), []int64{1}, 2); err != nil {
		t.Fatalf("soft-delete the seeded article: %v", err)
	}
}

// softDeleteMoment 把微语放进回收站：恢复端点只对回收站里的行有效。
func softDeleteMoment(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := model.NewSQLiteMomentRepository(db).SoftDelete(context.Background(), []int64{1}, 2); err != nil {
		t.Fatalf("soft-delete the seeded moment: %v", err)
	}
}

func TestAdminWriteEndpointsReportStorageFailures(t *testing.T) {
	cases := []struct {
		name    string
		table   string
		method  string
		path    string
		body    any
		prepare func(t *testing.T, db *sql.DB)
	}{
		{name: "create an article", table: "articles", method: http.MethodPost, path: "/api/admin/articles", body: map[string]any{"title": "t", "content": "c"}},
		{name: "update an article", table: "articles", method: http.MethodPut, path: "/api/admin/articles/1", body: map[string]any{"title": "t"}},
		{name: "soft-delete an article", table: "articles", method: http.MethodDelete, path: "/api/admin/articles/1"},
		{name: "batch-delete articles", table: "articles", method: http.MethodDelete, path: "/api/admin/articles", body: map[string]any{"ids": []int{1}}},
		{name: "restore an article", table: "articles", method: http.MethodPost, path: "/api/admin/articles/1/restore", prepare: softDeleteArticle},
		{name: "batch-restore articles", table: "articles", method: http.MethodPost, path: "/api/admin/articles/batch-restore", body: map[string]any{"ids": []int{1}}, prepare: softDeleteArticle},
		{name: "permanently delete an article", table: "articles", method: http.MethodDelete, path: "/api/admin/articles/1/permanent", prepare: softDeleteArticle},
		{name: "batch-permanently delete articles", table: "articles", method: http.MethodDelete, path: "/api/admin/articles/batch-permanent", body: map[string]any{"ids": []int{1}}, prepare: softDeleteArticle},
		{name: "create a moment", table: "moments", method: http.MethodPost, path: "/api/admin/moments", body: map[string]any{"content": "m"}},
		{name: "update a moment", table: "moments", method: http.MethodPut, path: "/api/admin/moments/1", body: map[string]any{"content": "m"}},
		{name: "soft-delete a moment", table: "moments", method: http.MethodDelete, path: "/api/admin/moments/1"},
		{name: "restore a moment", table: "moments", method: http.MethodPost, path: "/api/admin/moments/1/restore", prepare: softDeleteMoment},
		{name: "permanently delete a moment", table: "moments", method: http.MethodDelete, path: "/api/admin/moments/1/permanent", prepare: softDeleteMoment},
		{name: "batch-permanently delete moments", table: "moments", method: http.MethodDelete, path: "/api/admin/moments/batch-permanent", body: map[string]any{"ids": []int{1}}, prepare: softDeleteMoment},
		{name: "create a page", table: "pages", method: http.MethodPost, path: "/api/admin/pages", body: map[string]any{"title": "p", "content": "c"}},
		{name: "update a page", table: "pages", method: http.MethodPut, path: "/api/admin/pages/1", body: map[string]any{"title": "p"}},
		{name: "soft-delete a page", table: "pages", method: http.MethodDelete, path: "/api/admin/pages/1"},
		{name: "restore a page", table: "pages", method: http.MethodPost, path: "/api/admin/pages/1/restore", prepare: softDeletePage},
		{name: "permanently delete a page", table: "pages", method: http.MethodDelete, path: "/api/admin/pages/1/permanent", prepare: softDeletePage},
		{name: "batch-permanently delete pages", table: "pages", method: http.MethodDelete, path: "/api/admin/pages/batch-permanent", body: map[string]any{"ids": []int{1}}, prepare: softDeletePage},
		{name: "rename a tag", table: "tags", method: http.MethodPut, path: "/api/admin/tags/1", body: map[string]any{"name": "Golang"}},
		{name: "soft-delete a comment", table: "comments", method: http.MethodDelete, path: "/api/admin/comments/1"},
		{name: "batch-delete comments", table: "comments", method: http.MethodDelete, path: "/api/admin/comments", body: map[string]any{"ids": []int{1}}},
		{name: "update a user", table: "users", method: http.MethodPut, path: "/api/admin/users/2", body: map[string]any{"username": "visitor2", "nickname": "访客", "email": "visitor2@example.com"}},
		{name: "delete a media item", table: "media", method: http.MethodDelete, path: "/api/admin/media/1"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server, db, _ := newTestServer(t)
			admin := installFreshBlog(t, server)
			seedContent(t, db)
			if test.prepare != nil {
				test.prepare(t, db)
			}
			failWrites(t, db, test.table)

			res := admin.call(test.method, test.path, test.body, admin.unsafe(nil))
			if res.status < 500 {
				t.Fatalf("%s %s: status = %d, want a server error (body: %s)",
					test.method, test.path, res.status, truncate(res.body))
			}
		})
	}
}

func TestPublicReadEndpointsReportStorageFailures(t *testing.T) {
	cases := []struct {
		name  string
		table string
		path  string
	}{
		{"list articles", "articles", "/api/articles?page=1&page_size=12"},
		{"list tags", "tags", "/api/tags?page=1&page_size=12"},
		{"list moments", "moments", "/api/moments?page=1&page_size=12"},
		{"list pages", "pages", "/api/pages?page=1&page_size=12"},
		{"list comments", "comments", "/api/comments?target_type=article&target_id=1&page=1&page_size=12"},
		{"admin comment list", "comments", "/api/admin/comments?page=1&page_size=12"},
		{"admin email list", "emails", "/api/admin/emails?page=1&page_size=12"},
		{"admin media list", "media", "/api/admin/media?page=1&page_size=12"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server, db, _ := newTestServer(t)
			admin := installFreshBlog(t, server)
			seedContent(t, db)
			dropTable(t, db, test.table)

			res := admin.call(http.MethodGet, test.path, nil, nil)
			if res.status < 500 {
				t.Fatalf("GET %s: status = %d, want a server error (body: %s)",
					test.path, res.status, truncate(res.body))
			}
		})
	}
}

func TestCommentWriteEndpointsReportStorageFailures(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	seedContent(t, db)
	headers := admin.unsafe(nil)

	failWrites(t, db, "comments")

	res := admin.call(http.MethodPost, "/api/comments", map[string]any{
		"target_type": "article",
		"target_id":   1,
		"content":     "再来一条",
	}, headers)
	if res.status < 500 {
		t.Fatalf("POST /api/comments: status = %d, want a server error (body: %s)", res.status, truncate(res.body))
	}
}

func TestSiteSettingsUpdateReportsWriteFailures(t *testing.T) {
	server, _, settingsPath := newTestServer(t)
	admin := installFreshBlog(t, server)

	// settings.json 变成一个目录：写不进去（保存必然失败），而读得到
	// 「不是一个文件」的错误。安装流程已经不写这个文件了，所以这里直接建目录。
	if err := os.Mkdir(settingsPath, 0o755); err != nil {
		t.Fatalf("replace settings.json with a directory: %v", err)
	}

	res := admin.call(http.MethodPut, "/api/admin/site", map[string]any{"description": "改不动"}, admin.unsafe(nil))
	if res.status < 500 {
		t.Fatalf("PUT /api/admin/site: status = %d, want a server error (body: %s)", res.status, truncate(res.body))
	}
}

func TestArticleAndPageReadsReportStorageFailures(t *testing.T) {
	t.Run("an article", func(t *testing.T) {
		server, db, _ := newTestServer(t)
		admin := installFreshBlog(t, server)
		seedContent(t, db)
		dropTable(t, db, "articles")

		res := admin.call(http.MethodGet, "/api/articles/1", nil, nil)
		if res.status < 500 {
			t.Fatalf("GET /api/articles/1: status = %d, want a server error (body: %s)", res.status, truncate(res.body))
		}
	})

	t.Run("a moment", func(t *testing.T) {
		server, db, _ := newTestServer(t)
		admin := installFreshBlog(t, server)
		seedContent(t, db)
		dropTable(t, db, "moments")

		res := admin.call(http.MethodGet, "/api/moments/1", nil, nil)
		if res.status < 500 {
			t.Fatalf("GET /api/moments/1: status = %d, want a server error (body: %s)", res.status, truncate(res.body))
		}
	})

	t.Run("a media item", func(t *testing.T) {
		server, db, _ := newTestServer(t)
		admin := installFreshBlog(t, server)
		seedContent(t, db)
		dropTable(t, db, "media")

		res := admin.call(http.MethodGet, "/api/media/1", nil, nil)
		if res.status < 500 {
			t.Fatalf("GET /api/media/1: status = %d, want a server error (body: %s)", res.status, truncate(res.body))
		}
	})

	t.Run("a page", func(t *testing.T) {
		server, db, _ := newTestServer(t)
		admin := installFreshBlog(t, server)
		seedContent(t, db)
		dropTable(t, db, "pages")

		res := admin.call(http.MethodGet, "/api/pages/1", nil, nil)
		if res.status < 500 {
			t.Fatalf("GET /api/pages/1: status = %d, want a server error (body: %s)", res.status, truncate(res.body))
		}
	})
}
