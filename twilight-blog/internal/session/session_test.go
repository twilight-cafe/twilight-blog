package session

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/database"
)

func newTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return NewStore(db), db
}

func createTestUser(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()

	res, err := db.Exec(
		`INSERT INTO users (email, nickname, password_hash, created_at, updated_at) VALUES (?, '', '', 1, 1)`,
		email,
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}

// createTestAdmin 插入 is_admin = 1 的那一行，即「凭据已迁进 users 表之后」的
// 管理员账号形态。
//
// 它是必需的，而不是可有可无的便利函数：Lookup 会回查 users.is_admin 与会话行里的
// role 是否一致，所以一个 RoleAdmin 会话必须真的对应一行 is_admin = 1 的账号，
// 否则会被当成角色脱节而吊销。单管理员唯一索引保证每个库最多一行。
func createTestAdmin(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()

	res, err := db.Exec(
		`INSERT INTO users (email, nickname, password_hash, is_admin, created_at, updated_at) VALUES (?, '', '', 1, 1, 1)`,
		email,
	)
	if err != nil {
		t.Fatalf("insert administrator: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}

// TestAdminSessionRoundTrip covers the session that belongs to the administrator.
//
// ⚠️ 这里断言 UserID **非 nil**，与改动前正好相反。旧版管理员没有 users 行，
// user_id 是 NULL；现在管理员也是 users 表里 is_admin = 1 的一行，会话指向那个
// 真实 id。这不是风格问题：RevokeUserSessions(adminID) 正是靠这个 id 才能找到
// 管理员的会话，「改密后其他设备下线」那条保证完全建立在它上面。
//
// 反证方式：把 startAdminSession 里的 userID 改回 nil → 本条失败。
func TestAdminSessionRoundTrip(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()

	adminID := createTestAdmin(t, db, "admin-sentinel@example.com")
	adminIDCopy := adminID

	token, csrf, expiresAt, err := store.Create(ctx, RoleAdmin, &adminIDCopy, AdminTTL)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if token == "" || csrf == "" {
		t.Fatal("Create() returned an empty token")
	}
	if token == csrf {
		t.Fatal("session and CSRF tokens must differ")
	}
	if expiresAt <= time.Now().Unix() {
		t.Fatal("expiry must be in the future")
	}

	sess, err := store.Lookup(ctx, token)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !sess.IsAdmin() {
		t.Fatalf("role = %q, want admin", sess.Role)
	}
	if sess.UserID == nil {
		t.Fatal("an administrator session must carry the administrator's real account id, " +
			"otherwise RevokeUserSessions cannot find it")
	}
	if *sess.UserID != adminID {
		t.Fatalf("UserID = %d, want %d", *sess.UserID, adminID)
	}

	// 那个 id 必须真的能用来吊销管理员会话——这是上面那句断言的实际意义。
	if err := store.RevokeUserSessions(ctx, adminID); err != nil {
		t.Fatalf("RevokeUserSessions() error = %v", err)
	}
	if _, err := store.Lookup(ctx, token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("the administrator session survived RevokeUserSessions: %v", err)
	}
}

func TestUserSessionCarriesUserID(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()

	userID := createTestUser(t, db, "visitor@example.com")
	userIDCopy := userID

	token, _, _, err := store.Create(ctx, RoleUser, &userIDCopy, UserTTL)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	sess, err := store.Lookup(ctx, token)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if sess.IsAdmin() {
		t.Fatal("a user session must not report itself as admin")
	}
	if sess.UserID == nil || *sess.UserID != userID {
		t.Fatalf("UserID = %v, want %d", sess.UserID, userID)
	}
}

func TestLookupRejectsUnknownAndEmptyTokens(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	for _, token := range []string{"", "   ", "deadbeef"} {
		if _, err := store.Lookup(ctx, token); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("Lookup(%q) error = %v, want ErrSessionNotFound", token, err)
		}
	}
}

func TestExpiredSessionIsRejectedAndRemoved(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()

	token, _, _, err := store.Create(ctx, RoleAdmin, nil, -time.Minute)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := store.Lookup(ctx, token); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("Lookup() error = %v, want ErrSessionExpired", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token_hash = ?`, HashToken(token)).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("expired session row still present (count=%d)", count)
	}
}

func TestVerifyCSRF(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()

	adminID := createTestAdmin(t, db, "csrf-admin@example.com")
	adminIDCopy := adminID

	token, csrf, _, err := store.Create(ctx, RoleAdmin, &adminIDCopy, AdminTTL)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	sess, err := store.Lookup(ctx, token)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}

	if !store.VerifyCSRF(ctx, sess, csrf) {
		t.Fatal("the matching CSRF token was rejected")
	}
	for _, wrong := range []string{"", "   ", "nope", csrf + "x"} {
		if store.VerifyCSRF(ctx, sess, wrong) {
			t.Fatalf("VerifyCSRF accepted %q", wrong)
		}
	}
	if store.VerifyCSRF(ctx, nil, csrf) {
		t.Fatal("VerifyCSRF accepted a nil session")
	}
}

func TestRevoke(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	token, _, _, err := store.Create(ctx, RoleAdmin, nil, AdminTTL)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := store.Revoke(ctx, token); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if _, err := store.Lookup(ctx, token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("token still valid after Revoke: %v", err)
	}
	// Revoking an unknown token is a no-op, not an error.
	if err := store.Revoke(ctx, "unknown"); err != nil {
		t.Fatalf("Revoke(unknown) error = %v", err)
	}
}

func TestRevokeUserSessions(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()

	userID := createTestUser(t, db, "visitor@example.com")
	otherID := createTestUser(t, db, "other@example.com")
	userIDCopy, otherIDCopy := userID, otherID

	first, _, _, err := store.Create(ctx, RoleUser, &userIDCopy, UserTTL)
	if err != nil {
		t.Fatalf("Create(first) error = %v", err)
	}
	second, _, _, err := store.Create(ctx, RoleUser, &userIDCopy, UserTTL)
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	untouched, _, _, err := store.Create(ctx, RoleUser, &otherIDCopy, UserTTL)
	if err != nil {
		t.Fatalf("Create(other) error = %v", err)
	}

	if err := store.RevokeUserSessions(ctx, userID); err != nil {
		t.Fatalf("RevokeUserSessions() error = %v", err)
	}

	for _, token := range []string{first, second} {
		if _, err := store.Lookup(ctx, token); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("session %q survived RevokeUserSessions", token[:8])
		}
	}
	if _, err := store.Lookup(ctx, untouched); err != nil {
		t.Fatalf("another user's session was revoked: %v", err)
	}
}

// TestLookupRejectsSessionWhoseRoleDriftedFromTheAccount 钉住一条授权不变量：
// 会话行里的 role 是签发时复制下来的**冗余副本**，不能单独作为授权依据，
// 必须与 users.is_admin 一致。
//
// 这条测试对应一个真实存在过的形态：凭据还在 settings.json 里时签发的管理员会话
// user_id 为 NULL，凭据迁移只搬 users 行、从不碰 sessions，于是那行会话在迁移后
// **依然是有效的管理员凭证**，而且 RevokeUserSessions 按 user_id 删也够不到它。
//
// 反证方式：把 Lookup 里的 roleMatchesAccount 判断去掉 → 本条失败（会话会被放行）。
func TestLookupRejectsSessionWhoseRoleDriftedFromTheAccount(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()

	adminID := createTestAdmin(t, db, "drift-admin@example.com")
	adminIDCopy := adminID

	// ① 依赖迁移前的形态：role=admin 但 user_id 为 NULL。
	legacyToken, _, _, err := store.Create(ctx, RoleAdmin, nil, AdminTTL)
	if err != nil {
		t.Fatalf("Create(legacy admin) error = %v", err)
	}
	if _, err := store.Lookup(ctx, legacyToken); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("a role=admin session with user_id NULL was accepted (error = %v); "+
			"this is exactly the pre-migration administrator session", err)
	}

	// ② 会话指向的是访客账号，却声称自己是管理员。
	visitorID := createTestUser(t, db, "drift-visitor@example.com")
	visitorIDCopy := visitorID
	drifted, _, _, err := store.Create(ctx, RoleAdmin, &visitorIDCopy, AdminTTL)
	if err != nil {
		t.Fatalf("Create(drifted) error = %v", err)
	}
	if _, err := store.Lookup(ctx, drifted); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("a role=admin session pointing at a non-admin account was accepted (error = %v)", err)
	}

	// ③ 访客会话指向管理员账号，同样不该通过。
	adminAsUser, _, _, err := store.Create(ctx, RoleUser, &adminIDCopy, UserTTL)
	if err != nil {
		t.Fatalf("Create(admin as user) error = %v", err)
	}
	if _, err := store.Lookup(ctx, adminAsUser); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("a role=user session pointing at the administrator account was accepted (error = %v)", err)
	}

	// ④ 账号的 is_admin 被改成 0 之后，原有管理员会话立即失效。
	valid, _, _, err := store.Create(ctx, RoleAdmin, &adminIDCopy, AdminTTL)
	if err != nil {
		t.Fatalf("Create(valid admin) error = %v", err)
	}
	if _, err := store.Lookup(ctx, valid); err != nil {
		t.Fatalf("a well-formed administrator session was rejected: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET is_admin = 0 WHERE id = ?`, adminID); err != nil {
		t.Fatalf("demote administrator: %v", err)
	}
	if _, err := store.Lookup(ctx, valid); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("the session survived the account losing administrator rights (error = %v)", err)
	}
}

// TestRevokeLegacySessions 钉住迁移后的清理：user_id IS NULL 的会话行必须被删掉，
// 且正常的会话不受影响。
//
// 反证方式：让 RevokeLegacySessions 执行 `WHERE user_id IS NOT NULL`（或直接
// 不删）→ 本条失败。
func TestRevokeLegacySessions(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()

	// 直接插一行 user_id 为 NULL 的会话，模拟迁移前的遗留数据
	// （Create 现在不会产生这种行，所以只能手工构造）。
	if _, err := db.Exec(`
INSERT INTO sessions (token_hash, role, user_id, csrf_hash, expires_at, created_at)
VALUES (?, 'admin', NULL, ?, ?, ?)
`, HashToken("legacy-token"), HashToken("legacy-csrf"), time.Now().Add(time.Hour).Unix(), time.Now().Unix()); err != nil {
		t.Fatalf("insert legacy session: %v", err)
	}

	adminID := createTestAdmin(t, db, "keep-admin@example.com")
	adminIDCopy := adminID
	keep, _, _, err := store.Create(ctx, RoleAdmin, &adminIDCopy, AdminTTL)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	removed, err := store.RevokeLegacySessions(ctx)
	if err != nil {
		t.Fatalf("RevokeLegacySessions() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("RevokeLegacySessions() removed %d rows, want 1", removed)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id IS NULL`).Scan(&count); err != nil {
		t.Fatalf("count legacy sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("legacy session rows still present: %d", count)
	}

	// 正常会话必须活下来——把清理写成「删光 sessions」是一个很容易犯的错。
	if _, err := store.Lookup(ctx, keep); err != nil {
		t.Fatalf("a legitimate administrator session was revoked: %v", err)
	}

	// 幂等：再跑一次什么都不删，也不报错。
	if again, err := store.RevokeLegacySessions(ctx); err != nil || again != 0 {
		t.Fatalf("second RevokeLegacySessions() = (%d, %v), want (0, nil)", again, err)
	}
}

// TestCookiesAreHttpOnlyOnlyForSession pins the security property that makes the
// scheme work: JS cannot read the session cookie, but can read the CSRF cookie.
func TestCookiesAreHttpOnlyOnlyForSession(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec)

	expires := time.Now().Add(time.Hour).Unix()
	SetCookies(c, true, "session-token", "csrf-token", expires)

	cookies := map[string]*http.Cookie{}
	for _, cookie := range rec.Result().Cookies() {
		cookies[cookie.Name] = cookie
	}

	sessionCookie, ok := cookies[SessionCookieName]
	if !ok {
		t.Fatal("session cookie was not set")
	}
	if !sessionCookie.HttpOnly {
		t.Fatal("the session cookie must be HttpOnly")
	}
	if !sessionCookie.Secure {
		t.Fatal("the session cookie must be Secure when requested")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v, want Lax", sessionCookie.SameSite)
	}

	csrfCookie, ok := cookies[CSRFCookieName]
	if !ok {
		t.Fatal("CSRF cookie was not set")
	}
	if csrfCookie.HttpOnly {
		t.Fatal("the CSRF cookie must be readable by JS")
	}

	// Clearing must expire both.
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec2)
	ClearCookies(c2, true)
	for _, cookie := range rec2.Result().Cookies() {
		if cookie.Value != "" || cookie.MaxAge >= 0 {
			t.Fatalf("ClearCookies did not expire %s", cookie.Name)
		}
	}
}
