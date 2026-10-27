package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/session"
)

// 本文件直接调用 handler，构造那些 HTTP 路由本身到不了的中间态
// （例如没有会话、会话缺少 user_id），以及用真实 service + 已关闭数据库
// 触发的「服务层报错 → handler 映射」分支。

func authContext(method, path, body string, sess *session.Session) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	if sess != nil {
		session.Set(c, sess)
	}
	return c, rec
}

func userSession(id int64) *session.Session {
	value := id
	return &session.Session{Role: session.RoleUser, UserID: &value}
}

func adminSession(id int64) *session.Session {
	value := id
	return &session.Session{Role: session.RoleAdmin, UserID: &value}
}

func closedDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	return db
}

type brokenSender struct{}

func (brokenSender) Send(string, string, string) error { return errors.New("mail down") }
func (brokenSender) SendVerification(string, string, string, string) error {
	return errors.New("mail down")
}

func brokenUserService(t *testing.T) *service.UserService {
	t.Helper()
	db := closedDatabase(t)
	return service.NewUserService(
		model.NewSQLiteUserRepository(db),
		model.NewSQLiteEmailVerificationRepository(db),
		brokenSender{},
	)
}

// adminStoreFuncs 是可逐方法替换的 AdminCredentialsStore。
type adminStoreFuncs struct {
	service.AdminCredentialsStore
	get            func(context.Context) (*model.User, error)
	validateAvatar func(context.Context, *int64) error
	updateIdentity func(context.Context, string, string) (*model.User, error)
	updateAvatar   func(context.Context, *int64) (*model.User, error)
	changePassword func(context.Context, string, string) (*model.User, error)
}

func (s *adminStoreFuncs) Get(ctx context.Context) (*model.User, error) {
	return s.get(ctx)
}
func (s *adminStoreFuncs) ValidateAvatar(ctx context.Context, id *int64) error {
	return s.validateAvatar(ctx, id)
}
func (s *adminStoreFuncs) UpdateIdentity(ctx context.Context, username, nickname string) (*model.User, error) {
	return s.updateIdentity(ctx, username, nickname)
}
func (s *adminStoreFuncs) UpdateAvatar(ctx context.Context, id *int64) (*model.User, error) {
	return s.updateAvatar(ctx, id)
}
func (s *adminStoreFuncs) ChangePassword(ctx context.Context, current, next string) (*model.User, error) {
	return s.changePassword(ctx, current, next)
}

func expectHTTPStatus(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an HTTP error %d, got nil", want)
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != want {
		t.Fatalf("err = %v, want HTTP %d", err, want)
	}
}

// TestAuthHandlersRejectBadBodies 覆盖每个入口的绑定失败与校验失败。
func TestAuthHandlersRejectBadBodies(t *testing.T) {
	h := &AuthHandler{config: &config.Config{}}
	cases := []struct {
		name string
		call func() error
	}{
		{name: "login bind", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/login", "{", nil)
			return h.Login(c)
		}},
		{name: "login validate", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/login", `{"username":"","password":""}`, nil)
			return h.Login(c)
		}},
		{name: "email code bind", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/email-code", "{", nil)
			return h.RequestEmailCode(c)
		}},
		{name: "email code validate", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/email-code", `{"email":""}`, nil)
			return h.RequestEmailCode(c)
		}},
		{name: "register bind", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/register", "{", nil)
			return h.Register(c)
		}},
		{name: "register validate", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/register", `{}`, nil)
			return h.Register(c)
		}},
		{name: "reset bind", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/reset-password", "{", nil)
			return h.ResetPassword(c)
		}},
		{name: "reset validate", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/reset-password", `{}`, nil)
			return h.ResetPassword(c)
		}},
		{name: "profile bind", call: func() error {
			c, _ := authContext(http.MethodPatch, "/api/me", "{", userSession(1))
			return h.UpdateOwnProfile(c)
		}},
		{name: "profile validate", call: func() error {
			c, _ := authContext(http.MethodPatch, "/api/me", `{}`, userSession(1))
			return h.UpdateOwnProfile(c)
		}},
		{name: "change password bind", call: func() error {
			c, _ := authContext(http.MethodPut, "/api/me/password", "{", userSession(1))
			return h.ChangePassword(c)
		}},
		{name: "change password validate", call: func() error {
			c, _ := authContext(http.MethodPut, "/api/me/password", `{}`, userSession(1))
			return h.ChangePassword(c)
		}},
		{name: "admin profile bind", call: func() error {
			c, _ := authContext(http.MethodPut, "/api/admin/profile", "{", adminSession(1))
			return h.UpdateAdminProfile(c)
		}},
		{name: "admin profile validate", call: func() error {
			c, _ := authContext(http.MethodPut, "/api/admin/profile", `{"name":""}`, adminSession(1))
			return h.UpdateAdminProfile(c)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectHTTPStatus(t, tc.call(), http.StatusBadRequest)
		})
	}
}

// TestAuthHandlersMapServiceFailures 用「已关闭的数据库」让真实 service 报错，
// 验证每个 handler 都把存储故障映射成 5xx（而不是 4xx 或 panic）。
func TestAuthHandlersMapServiceFailures(t *testing.T) {
	users := brokenUserService(t)
	h := &AuthHandler{config: &config.Config{}, users: users}

	calls := []struct {
		name string
		call func() error
	}{
		{name: "login", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/login", `{"username":"alice","password":"Password1"}`, nil)
			return h.Login(c)
		}},
		{name: "email code", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/email-code", `{"email":"a@example.com"}`, nil)
			return h.RequestEmailCode(c)
		}},
		{name: "register", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/register",
				`{"email":"a@example.com","code":"12345678","username":"alice","nickname":"Alice","password":"Password1"}`, nil)
			return h.Register(c)
		}},
		{name: "reset", call: func() error {
			c, _ := authContext(http.MethodPost, "/api/auth/reset-password",
				`{"email":"a@example.com","code":"12345678","password":"Password1"}`, nil)
			return h.ResetPassword(c)
		}},
		{name: "update nickname", call: func() error {
			c, _ := authContext(http.MethodPatch, "/api/me", `{"nickname":"Alice"}`, userSession(1))
			return h.UpdateOwnProfile(c)
		}},
		{name: "update username", call: func() error {
			c, _ := authContext(http.MethodPatch, "/api/me", `{"username":"alice2"}`, userSession(1))
			return h.UpdateOwnProfile(c)
		}},
		{name: "change user password", call: func() error {
			c, _ := authContext(http.MethodPut, "/api/me/password",
				`{"current_password":"Password1","new_password":"Password2"}`, userSession(1))
			return h.ChangePassword(c)
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			expectHTTPStatus(t, tc.call(), http.StatusInternalServerError)
		})
	}
}

// TestMeAndLogoutEdgeCases 覆盖路由中间件不会放行的会话形态，以及登出失败。
func TestMeAndLogoutEdgeCases(t *testing.T) {
	t.Run("me without a session", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}}
		c, _ := authContext(http.MethodGet, "/api/me", "", nil)
		expectHTTPStatus(t, h.Me(c), http.StatusUnauthorized)
	})

	t.Run("visitor session without a user id", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}}
		c, _ := authContext(http.MethodGet, "/api/me", "", &session.Session{Role: session.RoleUser})
		expectHTTPStatus(t, h.Me(c), http.StatusUnauthorized)
	})

	t.Run("visitor account lookup fails", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, users: brokenUserService(t)}
		c, _ := authContext(http.MethodGet, "/api/me", "", userSession(1))
		expectHTTPStatus(t, h.Me(c), http.StatusUnauthorized)
	})

	t.Run("administrator row is gone", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, admins: &adminStoreFuncs{
			get: func(context.Context) (*model.User, error) { return nil, model.ErrUserNotFound },
		}}
		c, _ := authContext(http.MethodGet, "/api/me", "", adminSession(1))
		expectHTTPStatus(t, h.Me(c), http.StatusUnauthorized)
	})

	t.Run("administrator identity is returned", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, admins: &adminStoreFuncs{
			get: func(context.Context) (*model.User, error) {
				return &model.User{ID: 1, Username: "admin", Nickname: "站长"}, nil
			},
		}}
		c, rec := authContext(http.MethodGet, "/api/me", "", adminSession(1))
		if err := h.Me(c); err != nil {
			t.Fatalf("Me: %v", err)
		}
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "站长") {
			t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("logout without a token clears cookies", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}}
		c, rec := authContext(http.MethodPost, "/api/auth/logout", "", nil)
		if err := h.Logout(c); err != nil {
			t.Fatalf("Logout: %v", err)
		}
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("logout revocation fails", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, sessions: session.NewStore(closedDatabase(t))}
		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		req.AddCookie(&http.Cookie{Name: "tw_session", Value: strings.Repeat("a", 64)})
		c := echo.New().NewContext(req, httptest.NewRecorder())
		expectHTTPStatus(t, h.Logout(c), http.StatusInternalServerError)
	})
}

// TestChangePasswordIdentityBranches 覆盖管理员/访客两条改密路径与轮换失败。
func TestChangePasswordIdentityBranches(t *testing.T) {
	t.Run("no session", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}}
		c, _ := authContext(http.MethodPut, "/api/me/password", `{}`, nil)
		expectHTTPStatus(t, h.ChangePassword(c), http.StatusForbidden)
	})

	t.Run("visitor session without a user id", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}}
		c, _ := authContext(http.MethodPut, "/api/me/password",
			`{"current_password":"Password1","new_password":"Password2"}`,
			&session.Session{Role: session.RoleUser})
		expectHTTPStatus(t, h.ChangePassword(c), http.StatusForbidden)
	})

	t.Run("administrator wrong current password", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, admins: &adminStoreFuncs{
			changePassword: func(context.Context, string, string) (*model.User, error) {
				return nil, service.ErrInvalidCredentials
			},
		}}
		c, _ := authContext(http.MethodPut, "/api/me/password",
			`{"current_password":"wrong","new_password":"Password2"}`, adminSession(1))
		expectHTTPStatus(t, h.ChangePassword(c), http.StatusUnauthorized)
	})

	t.Run("administrator change fails generically", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, admins: &adminStoreFuncs{
			changePassword: func(context.Context, string, string) (*model.User, error) {
				return nil, errors.New("db down")
			},
		}}
		c, _ := authContext(http.MethodPut, "/api/me/password",
			`{"current_password":"Password1","new_password":"Password2"}`, adminSession(1))
		expectHTTPStatus(t, h.ChangePassword(c), http.StatusInternalServerError)
	})

	t.Run("administrator session reissue fails", func(t *testing.T) {
		stub := &adminStoreFuncs{changePassword: func(context.Context, string, string) (*model.User, error) {
			return &model.User{ID: 1, IsAdmin: true}, nil
		}}
		h := &AuthHandler{config: &config.Config{}, admins: stub, sessions: session.NewStore(closedDatabase(t))}
		c, _ := authContext(http.MethodPut, "/api/me/password",
			`{"current_password":"Password1","new_password":"Password2"}`, adminSession(1))
		expectHTTPStatus(t, h.ChangePassword(c), http.StatusInternalServerError)
	})
}

// TestUpdateAdminProfileBranches 覆盖预检、身份写入与头像写入的每一层失败。
func TestUpdateAdminProfileBranches(t *testing.T) {
	current := &model.User{ID: 1, Username: "admin", Nickname: "站长"}

	t.Run("loading the administrator fails", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, admins: &adminStoreFuncs{
			get: func(context.Context) (*model.User, error) { return nil, errors.New("db down") },
		}}
		c, _ := authContext(http.MethodPut, "/api/admin/profile", `{"name":"新名字"}`, adminSession(1))
		expectHTTPStatus(t, h.UpdateAdminProfile(c), http.StatusInternalServerError)
	})

	t.Run("avatar precheck rejects", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, admins: &adminStoreFuncs{
			get: func(context.Context) (*model.User, error) { return current, nil },
			validateAvatar: func(context.Context, *int64) error {
				return &service.ValidationError{Message: "not an image"}
			},
		}}
		c, _ := authContext(http.MethodPut, "/api/admin/profile", `{"name":"站长","avatar_media_id":9}`, adminSession(1))
		expectHTTPStatus(t, h.UpdateAdminProfile(c), http.StatusBadRequest)
	})

	t.Run("identity update fails generically", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, admins: &adminStoreFuncs{
			get:            func(context.Context) (*model.User, error) { return current, nil },
			updateIdentity: func(context.Context, string, string) (*model.User, error) { return nil, errors.New("db down") },
		}}
		c, _ := authContext(http.MethodPut, "/api/admin/profile", `{"name":"新名字"}`, adminSession(1))
		expectHTTPStatus(t, h.UpdateAdminProfile(c), http.StatusInternalServerError)
	})

	t.Run("identity update conflicts", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, admins: &adminStoreFuncs{
			get:            func(context.Context) (*model.User, error) { return current, nil },
			updateIdentity: func(context.Context, string, string) (*model.User, error) { return nil, service.ErrUsernameTaken },
		}}
		c, _ := authContext(http.MethodPut, "/api/admin/profile", `{"name":"新名字"}`, adminSession(1))
		expectHTTPStatus(t, h.UpdateAdminProfile(c), http.StatusConflict)
	})

	t.Run("avatar write fails", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, admins: &adminStoreFuncs{
			get:            func(context.Context) (*model.User, error) { return current, nil },
			validateAvatar: func(context.Context, *int64) error { return nil },
			updateIdentity: func(context.Context, string, string) (*model.User, error) { return current, nil },
			updateAvatar:   func(context.Context, *int64) (*model.User, error) { return nil, errors.New("db down") },
		}}
		c, _ := authContext(http.MethodPut, "/api/admin/profile", `{"name":"站长","avatar_media_id":2}`, adminSession(1))
		expectHTTPStatus(t, h.UpdateAdminProfile(c), http.StatusInternalServerError)
	})

	t.Run("success", func(t *testing.T) {
		h := &AuthHandler{config: &config.Config{}, admins: &adminStoreFuncs{
			get: func(context.Context) (*model.User, error) { return current, nil },
			updateIdentity: func(_ context.Context, username, nickname string) (*model.User, error) {
				return &model.User{ID: 1, Username: username, Nickname: nickname}, nil
			},
		}}
		c, rec := authContext(http.MethodPut, "/api/admin/profile", `{"name":"新站长"}`, adminSession(1))
		if err := h.UpdateAdminProfile(c); err != nil {
			t.Fatalf("UpdateAdminProfile: %v", err)
		}
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "新站长") {
			t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
		}
	})
}

// TestIssueSessionFailure 直接覆盖会话签发失败（两种角色共用同一条实现）。
func TestIssueSessionFailure(t *testing.T) {
	h := &AuthHandler{config: &config.Config{}, sessions: session.NewStore(closedDatabase(t))}
	c, _ := authContext(http.MethodPost, "/api/auth/login", "", nil)
	expectHTTPStatus(t, h.startUserSession(c, 1), http.StatusInternalServerError)
	expectHTTPStatus(t, h.startAdminSession(c, 1), http.StatusInternalServerError)

	if err := h.reissueSession(c, &model.User{ID: 1, IsAdmin: true}); err == nil {
		t.Fatal("admin reissue must fail with a closed store")
	}
	if err := h.reissueSession(c, &model.User{ID: 2}); err == nil {
		t.Fatal("user reissue must fail with a closed store")
	}
}
