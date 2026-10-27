package handler

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"blog-engine/internal/database"
	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/session"
)

// The comment handler has identity branches the router can never produce:
// a session with no user id, or no session at all on a route protected by
// middleware. They still decide 401 vs 403, so they are pinned directly.

type recordingNotifier struct {
	notified []*model.Comment
}

func (n *recordingNotifier) Notify(comment *model.Comment) {
	n.notified = append(n.notified, comment)
}

// TestLikeAndUnlikeRequireAVisitorAccount covers the matrix before the service
// is touched: no session (401), session without a user id (403), administrator
// (403). None of them may reach the like repository.
func TestLikeAndUnlikeRequireAVisitorAccount(t *testing.T) {
	h := &CommentHandler{}

	cases := []struct {
		name string
		sess *session.Session
		want int
	}{
		{"no session", nil, http.StatusUnauthorized},
		{"session without a user id", &session.Session{Role: session.RoleUser}, http.StatusForbidden},
		{"administrator", adminSession(1), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run("like "+tc.name, func(t *testing.T) {
			c, _ := authContext(http.MethodPost, "/api/comments/1/like", "", tc.sess)
			c.SetParamNames("id")
			c.SetParamValues("1")
			expectHTTPStatus(t, h.Like(c), tc.want)
		})
		t.Run("unlike "+tc.name, func(t *testing.T) {
			c, _ := authContext(http.MethodDelete, "/api/comments/1/like", "", tc.sess)
			c.SetParamNames("id")
			c.SetParamValues("1")
			expectHTTPStatus(t, h.Unlike(c), tc.want)
		})
	}
}

// commentFixture opens a migrated database with one article and one visitor.
func commentFixture(t *testing.T) (*sql.DB, *model.Article, *model.User) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "comments.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	article := &model.Article{Title: "文章", Content: "正文", CreatedAt: 1, UpdatedAt: 1}
	if err := model.NewSQLiteArticleRepository(db).Create(context.Background(), article); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	user := &model.User{
		Email: "visitor@example.com", Username: "visitor", Nickname: "访客",
		CreatedAt: 1, UpdatedAt: 1,
	}
	if err := model.NewSQLiteUserRepository(db).Create(context.Background(), user); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return db, article, user
}

func commentServiceFor(db *sql.DB) *service.CommentService {
	return service.NewCommentService(
		model.NewSQLiteCommentRepository(db),
		model.NewSQLiteUserRepository(db),
		service.NewCommentTargetLookups(
			model.NewSQLiteArticleRepository(db),
			model.NewSQLiteMomentRepository(db),
			model.NewSQLitePageRepository(db),
		),
	)
}

// TestCreateCommentWithoutASession covers the guard that keeps the route
// middleware from being the only thing enforcing "sign in to comment".
func TestCreateCommentWithoutASession(t *testing.T) {
	db, article, _ := commentFixture(t)
	h := &CommentHandler{service: commentServiceFor(db)}

	c, _ := authContext(http.MethodPost, "/api/comments",
		`{"target_type":"article","target_id":`+itoa(article.ID)+`,"content":"你好"}`, nil)
	expectHTTPStatus(t, h.Create(c), http.StatusUnauthorized)
}

// TestCreateCommentWithASessionButNoUser covers a visitor-shaped session whose
// account link is missing. It is 401 rather than 403: there is no account to
// attribute the comment to.
func TestCreateCommentWithASessionButNoUser(t *testing.T) {
	db, article, _ := commentFixture(t)
	h := &CommentHandler{service: commentServiceFor(db)}

	c, _ := authContext(http.MethodPost, "/api/comments",
		`{"target_type":"article","target_id":`+itoa(article.ID)+`,"content":"你好"}`,
		&session.Session{Role: session.RoleUser})
	expectHTTPStatus(t, h.Create(c), http.StatusUnauthorized)
}

// TestCreateCommentNotifiesTheOwner covers the optional notifier. It is only
// reached for a successful visitor comment, never for the administrator's own.
func TestCreateCommentNotifiesTheOwner(t *testing.T) {
	db, article, user := commentFixture(t)
	notifier := &recordingNotifier{}
	h := &CommentHandler{service: commentServiceFor(db), notifier: notifier}

	c, rec := authContext(http.MethodPost, "/api/comments",
		`{"target_type":"article","target_id":`+itoa(article.ID)+`,"content":"你好"}`,
		userSession(user.ID))
	if err := h.Create(c); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if len(notifier.notified) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifier.notified))
	}

	// 管理员评论不触发通知：那条评论就是站长自己写的。
	adminNotifier := &recordingNotifier{}
	adminHandler := &CommentHandler{service: commentServiceFor(db), notifier: adminNotifier}
	c, _ = authContext(http.MethodPost, "/api/comments",
		`{"target_type":"article","target_id":`+itoa(article.ID)+`,"content":"站长回复"}`,
		adminSession(1))
	if err := adminHandler.Create(c); err != nil {
		t.Fatalf("Create as admin: %v", err)
	}
	if len(adminNotifier.notified) != 0 {
		t.Fatal("an administrator comment must not notify the administrator")
	}
}

// TestCreateCommentRejectsABadParent covers the 400 mapping for parent_id.
func TestCreateCommentRejectsABadParent(t *testing.T) {
	db, article, _ := commentFixture(t)
	h := &CommentHandler{service: commentServiceFor(db)}

	c, _ := authContext(http.MethodPost, "/api/comments",
		`{"target_type":"article","target_id":`+itoa(article.ID)+`,"parent_id":999,"content":"回复"}`,
		adminSession(1))
	err := h.Create(c)
	expectHTTPStatus(t, err, http.StatusBadRequest)
	if !strings.Contains(err.Error(), "parent comment not found") {
		t.Fatalf("err = %v", err)
	}
}

// TestCreateCommentMapsServiceValidationToBadRequest covers the last error arm
// in Create: the account's stored nickname is longer than the comment limit.
// Such a row can only exist through direct database surgery, which is exactly
// why the handler must not turn it into a 500.
func TestCreateCommentMapsServiceValidationToBadRequest(t *testing.T) {
	db, article, _ := commentFixture(t)
	longNickname := strings.Repeat("名", model.MaxCommentNicknameLength+1)
	user := &model.User{
		Email: "long@example.com", Username: "longnick", Nickname: longNickname,
		CreatedAt: 1, UpdatedAt: 1,
	}
	if err := model.NewSQLiteUserRepository(db).Create(context.Background(), user); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	h := &CommentHandler{service: commentServiceFor(db)}

	c, _ := authContext(http.MethodPost, "/api/comments",
		`{"target_type":"article","target_id":`+itoa(article.ID)+`,"content":"你好"}`,
		userSession(user.ID))
	expectHTTPStatus(t, h.Create(c), http.StatusBadRequest)
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
