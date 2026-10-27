package handler

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/session"
)

// recordingSender captures the verification code so a test can complete the
// register and reset flows without any mail infrastructure. It is deliberately
// a value receiver pointer: the code arrives by pointer so subtests can read it.
type recordingSender struct {
	code string
}

func (s *recordingSender) Send(string, string, string) error { return nil }

func (s *recordingSender) SendVerification(_, _, _, code string) error {
	s.code = code
	return nil
}

// liveAuthDB opens a migrated database and returns it with a cleanup.
func liveAuthDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seededUser(t *testing.T, db *sql.DB, username, email string, admin bool) *model.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("Password1"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := &model.User{
		Email:        email,
		Username:     username,
		Nickname:     username,
		PasswordHash: string(hash),
		IsAdmin:      admin,
		CreatedAt:    1,
		UpdatedAt:    1,
	}
	if err := model.NewSQLiteUserRepository(db).Create(context.Background(), user); err != nil {
		t.Fatalf("seed %s: %v", username, err)
	}
	return user
}

// failingSessionHandler wires a working user service to a session store that
// cannot write: every login reaches the point where the session is issued.
func failingSessionHandler(t *testing.T, db *sql.DB, sender *recordingSender) *AuthHandler {
	t.Helper()
	return &AuthHandler{
		config: &config.Config{},
		users: service.NewUserService(
			model.NewSQLiteUserRepository(db),
			model.NewSQLiteEmailVerificationRepository(db),
			sender,
		),
		sessions: session.NewStore(closedDatabase(t)),
	}
}

// TestLoginReportsSessionIssueFailures covers both identity branches after the
// password has already been accepted: an administrator session and a visitor
// session. Both share startSession, but the handler reaches it from two places.
func TestLoginReportsSessionIssueFailures(t *testing.T) {
	db := liveAuthDB(t)
	seededUser(t, db, "adminlogin", "admin-login@example.com", true)
	seededUser(t, db, "visitorlogin", "visitor-login@example.com", false)
	h := failingSessionHandler(t, db, &recordingSender{})

	for _, username := range []string{"adminlogin", "visitorlogin"} {
		t.Run(username, func(t *testing.T) {
			c, _ := authContext(http.MethodPost, "/api/auth/login",
				`{"username":"`+username+`","password":"Password1"}`, nil)
			expectHTTPStatus(t, h.Login(c), http.StatusInternalServerError)
		})
	}
}

// TestEmailCodeIssuedThenRegisterFailsToStartASession covers a successful code
// request (the 200 the SPA waits for) followed by a registration that creates
// the account but cannot sign the new visitor in.
func TestEmailCodeIssuedThenRegisterFailsToStartASession(t *testing.T) {
	db := liveAuthDB(t)
	sender := &recordingSender{}
	h := failingSessionHandler(t, db, sender)

	codeRequest, rec := authContext(http.MethodPost, "/api/auth/email-code",
		`{"email":"register-flow@example.com"}`, nil)
	if err := h.RequestEmailCode(codeRequest); err != nil {
		t.Fatalf("RequestEmailCode: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("email-code status = %d, body %s", rec.Code, rec.Body.String())
	}
	if sender.code == "" {
		t.Fatal("the verification code never reached the sender")
	}

	register, _ := authContext(http.MethodPost, "/api/auth/register",
		`{"email":"register-flow@example.com","code":"`+sender.code+
			`","username":"registerflow","nickname":"Register Flow","password":"Password1"}`, nil)
	expectHTTPStatus(t, h.Register(register), http.StatusInternalServerError)

	// 账号确实建好了——失败的只是会话签发。
	if _, err := model.NewSQLiteUserRepository(db).FindByUsername(context.Background(), "registerflow"); err != nil {
		t.Fatalf("the account was not created: %v", err)
	}
}

// TestResetPasswordSurvivesARevocationFailure covers the best-effort eviction:
// a broken session store must not turn a completed password reset into a 500.
// The old credential is already invalid; leaving other sessions alive is
// logged, not fatal.
func TestResetPasswordSurvivesARevocationFailure(t *testing.T) {
	db := liveAuthDB(t)
	seededUser(t, db, "resetflow", "reset-flow@example.com", false)
	sender := &recordingSender{}
	h := failingSessionHandler(t, db, sender)

	codeRequest, _ := authContext(http.MethodPost, "/api/auth/email-code",
		`{"email":"reset-flow@example.com"}`, nil)
	if err := h.RequestEmailCode(codeRequest); err != nil {
		t.Fatalf("RequestEmailCode: %v", err)
	}
	if sender.code == "" {
		t.Fatal("no code captured")
	}

	reset, rec := authContext(http.MethodPost, "/api/auth/reset-password",
		`{"email":"reset-flow@example.com","code":"`+sender.code+`","password":"Password2"}`, nil)
	if err := h.ResetPassword(reset); err != nil {
		t.Fatalf("ResetPassword must tolerate a revocation failure: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, body %s", rec.Code, rec.Body.String())
	}
}

// TestLoginRejectsAWrongPassword confirms the 401 path stays distinct from the
// session issue failures above: the two would otherwise be indistinguishable
// from the caller's side.
func TestLoginRejectsAWrongPassword(t *testing.T) {
	db := liveAuthDB(t)
	seededUser(t, db, "wrongpass", "wrong-pass@example.com", false)
	h := &AuthHandler{
		config: &config.Config{},
		users: service.NewUserService(
			model.NewSQLiteUserRepository(db),
			model.NewSQLiteEmailVerificationRepository(db),
			&recordingSender{},
		),
		sessions: session.NewStore(db),
	}

	c, _ := authContext(http.MethodPost, "/api/auth/login",
		`{"username":"wrongpass","password":"NotThePassword"}`, nil)
	expectHTTPStatus(t, h.Login(c), http.StatusUnauthorized)
}
