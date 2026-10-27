package view_test

import (
	"context"
	"net/http"
	"testing"

	"blog-engine/internal/model"
)

// pageBilingualResponse is the slice of the page shape these tests care about.
type pageBilingualResponse struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	TitleEN   string `json:"title_en"`
	Content   string `json:"content"`
	ContentEN string `json:"content_en"`
}

// TestPageBilingualFieldsOverHTTP 覆盖页面双语的完整 HTTP 路径：
// 创建时写入英文、公开读取时原样返回、只改中文时不丢英文、显式空串才清空英文。
func TestPageBilingualFieldsOverHTTP(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	headers := admin.unsafe(nil)

	created := admin.call(http.MethodPost, "/api/admin/pages", map[string]any{
		"title":      "关于",
		"content":    "中文正文",
		"title_en":   "About",
		"content_en": "English body",
	}, headers)
	expectStatus(t, created, http.StatusCreated, "POST /api/admin/pages")
	var page pageBilingualResponse
	decodeInto(t, created, &page)
	if page.TitleEN != "About" || page.ContentEN != "English body" {
		t.Fatalf("created english = (%q, %q)", page.TitleEN, page.ContentEN)
	}

	// 公开端点也要返回英文：前台按语言取用，缺字段就只能永远显示中文。
	read := admin.call(http.MethodGet, "/api/pages/"+itoa64(page.ID), nil, nil)
	expectStatus(t, read, http.StatusOK, "GET /api/pages/:id")
	decodeInto(t, read, &page)
	if page.TitleEN != "About" || page.ContentEN != "English body" {
		t.Fatalf("public read english = (%q, %q)", page.TitleEN, page.ContentEN)
	}

	// 只改中文标题：英文两份都必须保留（省略 ≠ 清空）。
	updateZH := admin.call(http.MethodPut, "/api/admin/pages/"+itoa64(page.ID), map[string]any{
		"title": "关于我们",
	}, headers)
	expectStatus(t, updateZH, http.StatusOK, "PUT /api/admin/pages/:id (zh only)")
	decodeInto(t, updateZH, &page)
	if page.Title != "关于我们" || page.TitleEN != "About" || page.ContentEN != "English body" {
		t.Fatalf("after zh-only update = (%q, %q, %q)", page.Title, page.TitleEN, page.ContentEN)
	}

	// 显式空串才是清空英文标题；英文正文仍然保留。
	clearEN := admin.call(http.MethodPut, "/api/admin/pages/"+itoa64(page.ID), map[string]any{
		"title":    "关于我们",
		"title_en": "",
	}, headers)
	expectStatus(t, clearEN, http.StatusOK, "PUT /api/admin/pages/:id (clear title_en)")
	decodeInto(t, clearEN, &page)
	if page.TitleEN != "" || page.ContentEN != "English body" {
		t.Fatalf("after clearing title_en = (%q, %q)", page.TitleEN, page.ContentEN)
	}

	// 数据库里同样按列存了两份；列表接口带上英文字段（后台编辑回填依赖它）。
	stored, err := model.NewSQLitePageRepository(db).GetByID(context.Background(), page.ID)
	if err != nil {
		t.Fatalf("read stored page: %v", err)
	}
	if stored.Title != "关于我们" || stored.ContentEN != "English body" {
		t.Fatalf("stored page = (%+v)", stored)
	}

	list := admin.call(http.MethodGet, "/api/pages?page=1&page_size=12", nil, nil)
	expectStatus(t, list, http.StatusOK, "GET /api/pages")
	var paginated struct {
		Items []pageBilingualResponse `json:"items"`
	}
	decodeInto(t, list, &paginated)
	if len(paginated.Items) != 1 || paginated.Items[0].ContentEN != "English body" {
		t.Fatalf("list items = %+v", paginated.Items)
	}
}
