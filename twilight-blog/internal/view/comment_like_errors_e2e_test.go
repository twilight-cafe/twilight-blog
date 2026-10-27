package view_test

import (
	"net/http"
	"testing"

	"blog-engine/internal/config"
)

// TestCommentLikeAndCreateErrorPaths 覆盖评论互动里「目标不存在」与存储故障
// 两条分支；它们既不是参数形状错误，也不是成功路径。
func TestCommentLikeAndCreateErrorPaths(t *testing.T) {
	// 本用例要连续触发多次评论写入，关掉按 IP 限流以免把断言变成 429。
	server, db, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		disabled := false
		cfg.RateLimit = &config.RateLimitConfig{Enabled: &disabled}
	})
	installFreshBlog(t, server)
	seedContent(t, db)
	seedInputVisitor(t, db)

	visitor := newAPI(t, server.URL)
	expectStatus(t, visitor.call(http.MethodPost, "/api/auth/login",
		map[string]string{"username": "input-visitor", "password": "Visitor1pass"}, nil),
		http.StatusOK, "login")

	headers := visitor.unsafe(nil)
	expectStatus(t, visitor.call(http.MethodPost, "/api/comments/999/like", nil, headers),
		http.StatusNotFound, "like a missing comment")
	expectStatus(t, visitor.call(http.MethodDelete, "/api/comments/999/like", nil, headers),
		http.StatusNotFound, "unlike a missing comment")

	// 目标文章不存在。
	expectStatus(t, visitor.call(http.MethodPost, "/api/comments", map[string]any{
		"target_type": "article", "target_id": 999, "content": "你好",
	}, headers), http.StatusNotFound, "comment on a missing target")

	// 父评论不存在。
	expectStatus(t, visitor.call(http.MethodPost, "/api/comments", map[string]any{
		"target_type": "article", "target_id": 1, "parent_id": 999, "content": "回复",
	}, headers), http.StatusBadRequest, "reply to a missing parent")

	// 存储故障：点赞/取消点赞/创建都必须 500，而不是 404 或 400。
	dropTable(t, db, "comments")
	expectStatus(t, visitor.call(http.MethodPost, "/api/comments/1/like", nil, headers),
		http.StatusInternalServerError, "like with a broken store")
	expectStatus(t, visitor.call(http.MethodDelete, "/api/comments/1/like", nil, headers),
		http.StatusInternalServerError, "unlike with a broken store")
	expectStatus(t, visitor.call(http.MethodPost, "/api/comments", map[string]any{
		"target_type": "article", "target_id": 1, "content": "创建失败",
	}, headers), http.StatusInternalServerError, "create with a broken store")
}
