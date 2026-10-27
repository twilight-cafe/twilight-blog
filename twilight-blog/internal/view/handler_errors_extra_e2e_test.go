package view_test

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"blog-engine/internal/model"
)

func softDeletePage(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := model.NewSQLitePageRepository(db).SoftDelete(context.Background(), []int64{1}, 2); err != nil {
		t.Fatalf("soft-delete the seeded page: %v", err)
	}
}

func softDeleteComment(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := model.NewSQLiteCommentRepository(db).SoftDelete(context.Background(), []int64{1}, 2); err != nil {
		t.Fatalf("soft-delete the seeded comment: %v", err)
	}
}

// TestAdditionalWriteEndpointsReportStorageFailures 补上 handler_errors_e2e_test.go
// 没有列到的批量/恢复端点：它们与单条端点共享错误映射，但调用点各自独立。
func TestAdditionalWriteEndpointsReportStorageFailures(t *testing.T) {
	cases := []struct {
		name    string
		table   string
		method  string
		path    string
		body    any
		prepare func(*testing.T, *sql.DB)
	}{
		{name: "batch-delete moments", table: "moments", method: http.MethodDelete, path: "/api/admin/moments", body: map[string]any{"ids": []int{1}}},
		{name: "batch-restore moments", table: "moments", method: http.MethodPost, path: "/api/admin/moments/batch-restore", body: map[string]any{"ids": []int{1}}, prepare: softDeleteMoment},
		{name: "batch-permanent moments", table: "moments", method: http.MethodDelete, path: "/api/admin/moments/batch-permanent", body: map[string]any{"ids": []int{1}}, prepare: softDeleteMoment},
		{name: "batch-delete pages", table: "pages", method: http.MethodDelete, path: "/api/admin/pages", body: map[string]any{"ids": []int{1}}},
		{name: "batch-restore pages", table: "pages", method: http.MethodPost, path: "/api/admin/pages/batch-restore", body: map[string]any{"ids": []int{1}}, prepare: softDeletePage},
		{name: "batch-permanent pages", table: "pages", method: http.MethodDelete, path: "/api/admin/pages/batch-permanent", body: map[string]any{"ids": []int{1}}, prepare: softDeletePage},
		{name: "restore comment", table: "comments", method: http.MethodPost, path: "/api/admin/comments/1/restore", prepare: softDeleteComment},
		{name: "batch-restore comments", table: "comments", method: http.MethodPost, path: "/api/admin/comments/batch-restore", body: map[string]any{"ids": []int{1}}, prepare: softDeleteComment},
		{name: "permanent comment", table: "comments", method: http.MethodDelete, path: "/api/admin/comments/1/permanent", prepare: softDeleteComment},
		{name: "batch-permanent comments", table: "comments", method: http.MethodDelete, path: "/api/admin/comments/batch-permanent", body: map[string]any{"ids": []int{1}}, prepare: softDeleteComment},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, db, _ := newTestServer(t)
			admin := installFreshBlog(t, server)
			seedContent(t, db)
			if tc.prepare != nil {
				tc.prepare(t, db)
			}
			failWrites(t, db, tc.table)

			res := admin.call(tc.method, tc.path, tc.body, admin.unsafe(nil))
			if res.status < 500 {
				t.Fatalf("%s %s: status = %d, want a server error (body: %s)",
					tc.method, tc.path, res.status, truncate(res.body))
			}
		})
	}
}

// TestAdminGetSite 覆盖管理端站点设置读接口（公开快照 + 通知邮箱）。
func TestAdminGetSite(t *testing.T) {
	server, _, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	res := admin.call(http.MethodGet, "/api/admin/site", nil, nil)
	expectStatus(t, res, http.StatusOK, "GET /api/admin/site")
}
