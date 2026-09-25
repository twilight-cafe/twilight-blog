package view_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"blog-engine/internal/model"
)

// TestAdminCRUDAndRecycleBinRoutes 用真实 HTTP 路由跑一遍后台每个资源组的
// 写入/软删/回收站/恢复/永久删除，以及评论、邮件、用户和标签这些此前没有
// 端到端入口的路径。
//
// 这些分支都不是安全边界，但它们是后台按钮真正会打到的接口；没有请求走到时，
// 任何 SQL 列错位、JSON 形状错误或事务顺序问题都只会在用户点击后才暴露。
func TestAdminCRUDAndRecycleBinRoutes(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)

	createArticle := func(title string) int64 {
		t.Helper()
		res := admin.call(http.MethodPost, "/api/admin/articles", map[string]any{
			"title": title, "content": "正文-" + title, "tags": []string{"Go"},
		}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusCreated, "POST /api/admin/articles")
		var article struct {
			ID int64 `json:"id"`
		}
		decodeInto(t, res, &article)
		return article.ID
	}

	articleID := createArticle("CRUD article")
	if articleID == 0 {
		t.Fatal("created article has id 0")
	}
	expectStatus(t, admin.call(http.MethodPut, "/api/admin/articles/"+strconv.FormatInt(articleID, 10), map[string]any{
		"title": "CRUD article v2", "content": "正文-v2", "tags": []string{"Go"},
	}, admin.unsafe(nil)), http.StatusOK, "PUT /api/admin/articles/:id")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/articles/"+strconv.FormatInt(articleID, 10), nil, admin.unsafe(nil)), http.StatusNoContent, "DELETE /api/admin/articles/:id")
	trash := admin.call(http.MethodGet, "/api/admin/trash/articles?page=1&page_size=12", nil, nil)
	expectStatus(t, trash, http.StatusOK, "GET /api/admin/trash/articles")
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/articles/"+strconv.FormatInt(articleID, 10)+"/restore", nil, admin.unsafe(nil)), http.StatusNoContent, "POST restore article")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/articles/"+strconv.FormatInt(articleID, 10), nil, admin.unsafe(nil)), http.StatusNoContent, "DELETE article again")
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/articles/batch-restore", map[string]any{"ids": []int64{articleID}}, admin.unsafe(nil)), http.StatusNoContent, "POST batch-restore article")

	batchArticleID := createArticle("Batch article")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/articles", map[string]any{"ids": []int64{batchArticleID}}, admin.unsafe(nil)), http.StatusNoContent, "DELETE /api/admin/articles (batch)")
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/articles/batch-restore", map[string]any{"ids": []int64{batchArticleID}}, admin.unsafe(nil)), http.StatusNoContent, "batch-restore articles")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/articles", map[string]any{"ids": []int64{batchArticleID}}, admin.unsafe(nil)), http.StatusNoContent, "batch delete articles again")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/articles/batch-permanent", map[string]any{"ids": []int64{batchArticleID}}, admin.unsafe(nil)), http.StatusNoContent, "batch permanent delete articles")

	// 评论挂在 articleID 上；先建一条公开评论，随后从后台走完整回收站流程。
	comment := admin.call(http.MethodPost, "/api/comments", map[string]any{
		"target_type": "article", "target_id": articleID, "content": "第一条评论",
	}, admin.unsafe(nil))
	expectStatus(t, comment, http.StatusCreated, "POST /api/comments as admin")
	var commentBody struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, comment, &commentBody)
	if commentBody.ID == 0 {
		t.Fatal("comment id is 0")
	}
	expectStatus(t, admin.call(http.MethodGet, "/api/comments?target_type=article&target_id="+strconv.FormatInt(articleID, 10)+"&page=1&page_size=12", nil, nil), http.StatusOK, "GET /api/comments")
	expectStatus(t, admin.call(http.MethodGet, "/api/admin/comments?page=1&page_size=12", nil, nil), http.StatusOK, "GET /api/admin/comments")
	expectStatus(t, admin.call(http.MethodPost, "/api/comments/"+strconv.FormatInt(commentBody.ID, 10)+"/like", nil, admin.unsafe(nil)), http.StatusForbidden, "admin like is visitor-only")
	expectStatus(t, admin.call(http.MethodDelete, "/api/comments/"+strconv.FormatInt(commentBody.ID, 10)+"/like", nil, admin.unsafe(nil)), http.StatusForbidden, "admin unlike is visitor-only")

	// 注册一个访客，覆盖喜欢的成功路径，也作为用户管理/重置密码的数据。
	seedVerificationCode(t, db, "crud-visitor@example.com", seededCode)
	visitor := newAPI(t, server.URL)
	expectStatus(t, visitor.call(http.MethodPost, "/api/auth/register", map[string]string{
		"email": "crud-visitor@example.com", "code": seededCode,
		"username": "crudvisitor", "password": userPassword, "nickname": "CRUD Visitor",
	}, nil), http.StatusCreated, "POST /api/auth/register")
	expectStatus(t, visitor.call(http.MethodPost, "/api/comments/"+strconv.FormatInt(commentBody.ID, 10)+"/like", nil, visitor.unsafe(nil)), http.StatusOK, "visitor like")
	expectStatus(t, visitor.call(http.MethodDelete, "/api/comments/"+strconv.FormatInt(commentBody.ID, 10)+"/like", nil, visitor.unsafe(nil)), http.StatusOK, "visitor unlike")

	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/comments/"+strconv.FormatInt(commentBody.ID, 10), nil, admin.unsafe(nil)), http.StatusNoContent, "DELETE /api/admin/comments/:id")
	expectStatus(t, admin.call(http.MethodGet, "/api/admin/trash/comments?page=1&page_size=12", nil, nil), http.StatusOK, "GET /api/admin/trash/comments")
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/comments/"+strconv.FormatInt(commentBody.ID, 10)+"/restore", nil, admin.unsafe(nil)), http.StatusNoContent, "POST restore comment")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/comments/"+strconv.FormatInt(commentBody.ID, 10), nil, admin.unsafe(nil)), http.StatusNoContent, "DELETE comment again")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/comments/"+strconv.FormatInt(commentBody.ID, 10)+"/permanent", nil, admin.unsafe(nil)), http.StatusNoContent, "permanent delete comment")

	// 批量接口直接插一条评论，避免再消耗评论创建限流额度。
	second := &model.Comment{AuthorType: "admin", AdminName: "Admin", TargetType: "article", TargetID: articleID, Content: "batch comment", CreatedAt: 100, UpdatedAt: 100}
	if err := model.NewSQLiteCommentRepository(db).Create(context.Background(), second); err != nil {
		t.Fatalf("seed second comment: %v", err)
	}
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/comments", map[string]any{"ids": []int64{second.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch delete comments")
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/comments/batch-restore", map[string]any{"ids": []int64{second.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch restore comments")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/comments", map[string]any{"ids": []int64{second.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch delete comments again")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/comments/batch-permanent", map[string]any{"ids": []int64{second.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch permanent delete comments")

	// 标签：文章创建时隐式产生“Go”，这里覆盖计数、相关文章与改名。
	tagsRes := admin.call(http.MethodGet, "/api/tags?page=1&page_size=12", nil, nil)
	expectStatus(t, tagsRes, http.StatusOK, "GET /api/tags")
	var tags struct {
		Items []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	decodeInto(t, tagsRes, &tags)
	if len(tags.Items) == 0 {
		t.Fatal("article creation produced no tag")
	}
	tagID := tags.Items[0].ID
	expectStatus(t, admin.call(http.MethodGet, "/api/tags/"+strconv.FormatInt(tagID, 10)+"/articles?page=1&page_size=12", nil, nil), http.StatusOK, "GET /api/tags/:id/articles")
	expectStatus(t, admin.call(http.MethodGet, "/api/tags/"+strconv.FormatInt(tagID, 10)+"/count", nil, nil), http.StatusOK, "GET /api/tags/:id/count")
	expectStatus(t, admin.call(http.MethodPut, "/api/admin/tags/"+strconv.FormatInt(tagID, 10), map[string]string{"name": "Golang"}, admin.unsafe(nil)), http.StatusOK, "PUT /api/admin/tags/:id")

	// 用户管理：列表、编辑（唯一约束的失败路径另外覆盖）。
	usersRes := admin.call(http.MethodGet, "/api/admin/users?page=1&page_size=12", nil, nil)
	expectStatus(t, usersRes, http.StatusOK, "GET /api/admin/users")
	var users struct {
		Items []struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"items"`
	}
	decodeInto(t, usersRes, &users)
	if len(users.Items) != 1 {
		t.Fatalf("users = %+v, want the registered visitor", users.Items)
	}
	visitorID := users.Items[0].ID
	expectStatus(t, admin.call(http.MethodPut, "/api/admin/users/"+strconv.FormatInt(visitorID, 10), map[string]string{
		"username": "crudvisitor2", "nickname": "CRUD Visitor 2", "email": "crud-visitor2@example.com",
	}, admin.unsafe(nil)), http.StatusOK, "PUT /api/admin/users/:id")

	// 邮件日志：后台只能读，直接写一条真实记录即可。
	emailRepo := model.NewSQLiteEmailRepository(db)
	if err := emailRepo.Create(context.Background(), &model.Email{
		FromEmail: "from@example.com", ToEmail: "to@example.com", Subject: "s", Body: "b", SentAt: 10, CreatedAt: 11,
	}); err != nil {
		t.Fatalf("seed email: %v", err)
	}
	emailsRes := admin.call(http.MethodGet, "/api/admin/emails?page=1&page_size=12", nil, nil)
	expectStatus(t, emailsRes, http.StatusOK, "GET /api/admin/emails")
	var emails struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	decodeInto(t, emailsRes, &emails)
	if len(emails.Items) != 1 {
		t.Fatalf("emails = %+v, want one", emails.Items)
	}
	expectStatus(t, admin.call(http.MethodGet, "/api/admin/emails/"+strconv.FormatInt(emails.Items[0].ID, 10), nil, nil), http.StatusOK, "GET /api/admin/emails/:id")

	// 页面：创建、更新、软删、回收站、恢复、批量恢复。
	pageRes := admin.call(http.MethodPost, "/api/admin/pages", map[string]any{"title": "About", "content": "page body"}, admin.unsafe(nil))
	expectStatus(t, pageRes, http.StatusCreated, "POST /api/admin/pages")
	var page struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, pageRes, &page)
	expectStatus(t, admin.call(http.MethodGet, "/api/pages?page=1&page_size=12", nil, nil), http.StatusOK, "GET /api/pages")
	expectStatus(t, admin.call(http.MethodPut, "/api/admin/pages/"+strconv.FormatInt(page.ID, 10), map[string]any{"title": "About v2"}, admin.unsafe(nil)), http.StatusOK, "PUT /api/admin/pages/:id")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/pages/"+strconv.FormatInt(page.ID, 10), nil, admin.unsafe(nil)), http.StatusNoContent, "DELETE /api/admin/pages/:id")
	expectStatus(t, admin.call(http.MethodGet, "/api/admin/trash/pages?page=1&page_size=12", nil, nil), http.StatusOK, "GET /api/admin/trash/pages")
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/restore", nil, admin.unsafe(nil)), http.StatusNoContent, "restore page")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/pages/"+strconv.FormatInt(page.ID, 10), nil, admin.unsafe(nil)), http.StatusNoContent, "delete page again")
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/pages/batch-restore", map[string]any{"ids": []int64{page.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch restore page")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/permanent", nil, admin.unsafe(nil)), http.StatusNoContent, "permanent delete page")

	batchPageRes := admin.call(http.MethodPost, "/api/admin/pages", map[string]any{"title": "Batch page", "content": "body"}, admin.unsafe(nil))
	expectStatus(t, batchPageRes, http.StatusCreated, "POST batch page")
	var batchPage struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, batchPageRes, &batchPage)
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/pages", map[string]any{"ids": []int64{batchPage.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch delete pages")
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/pages/batch-restore", map[string]any{"ids": []int64{batchPage.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch restore pages")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/pages", map[string]any{"ids": []int64{batchPage.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch delete pages again")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/pages/batch-permanent", map[string]any{"ids": []int64{batchPage.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch permanent delete pages")

	// 动态：创建、读取、更新、软删、回收站、恢复、批量恢复、永久删除。
	momentRes := admin.call(http.MethodPost, "/api/admin/moments", map[string]any{"content": "moment body", "media_ids": []int64{}}, admin.unsafe(nil))
	expectStatus(t, momentRes, http.StatusCreated, "POST /api/admin/moments")
	var moment struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, momentRes, &moment)
	expectStatus(t, admin.call(http.MethodGet, "/api/moments?page=1&page_size=12", nil, nil), http.StatusOK, "GET /api/moments")
	expectStatus(t, admin.call(http.MethodGet, "/api/moments/"+strconv.FormatInt(moment.ID, 10), nil, nil), http.StatusOK, "GET /api/moments/:id")
	expectStatus(t, admin.call(http.MethodPut, "/api/admin/moments/"+strconv.FormatInt(moment.ID, 10), map[string]any{"content": "moment v2"}, admin.unsafe(nil)), http.StatusOK, "PUT /api/admin/moments/:id")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/moments/"+strconv.FormatInt(moment.ID, 10), nil, admin.unsafe(nil)), http.StatusNoContent, "DELETE /api/admin/moments/:id")
	expectStatus(t, admin.call(http.MethodGet, "/api/admin/trash/moments?page=1&page_size=12", nil, nil), http.StatusOK, "GET /api/admin/trash/moments")
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/moments/"+strconv.FormatInt(moment.ID, 10)+"/restore", nil, admin.unsafe(nil)), http.StatusNoContent, "restore moment")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/moments/"+strconv.FormatInt(moment.ID, 10), nil, admin.unsafe(nil)), http.StatusNoContent, "delete moment again")
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/moments/batch-restore", map[string]any{"ids": []int64{moment.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch restore moment")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/moments/"+strconv.FormatInt(moment.ID, 10)+"/permanent", nil, admin.unsafe(nil)), http.StatusNoContent, "permanent delete moment")

	batchMomentRes := admin.call(http.MethodPost, "/api/admin/moments", map[string]any{"content": "batch moment", "media_ids": []int64{}}, admin.unsafe(nil))
	expectStatus(t, batchMomentRes, http.StatusCreated, "POST batch moment")
	var batchMoment struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, batchMomentRes, &batchMoment)
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/moments", map[string]any{"ids": []int64{batchMoment.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch delete moments")
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/moments/batch-restore", map[string]any{"ids": []int64{batchMoment.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch restore moments")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/moments", map[string]any{"ids": []int64{batchMoment.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch delete moments again")
	expectStatus(t, admin.call(http.MethodDelete, "/api/admin/moments/batch-permanent", map[string]any{"ids": []int64{batchMoment.ID}}, admin.unsafe(nil)), http.StatusNoContent, "batch permanent delete moments")

	// 密码重置也要覆盖：先给刚注册的访客签一枚新码，再走公开重置端点。
	seedVerificationCode(t, db, "crud-visitor2@example.com", seededCode)
	expectStatus(t, newAPI(t, server.URL).call(http.MethodPost, "/api/auth/reset-password", map[string]string{
		"email": "crud-visitor2@example.com", "code": seededCode, "password": "NewVisitor1pass",
	}, nil), http.StatusOK, "POST /api/auth/reset-password")
}
