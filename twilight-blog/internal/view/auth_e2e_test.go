package view_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/mediaconv"
	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/testtool"
	"blog-engine/internal/view"
)

// Registration needs a verification code that normally arrives by email. No mail
// provider is reachable from a test, so the code is seeded directly, exactly as
// the service would have stored it (SHA-256 of the plaintext).
const seededCode = "135790"

const (
	adminPassword = "Smoke1ng2024"
	userPassword  = "Visitor1pass"
	userEmail     = "visitor@example.com"
	// userName 与上面的邮箱、与下面的昵称 "Alice" 都是**不同的值**，
	// 这样任何一处把「登录标识」与「展示名」混用的改动都会让断言失败。
	userName       = "visitor-alice"
	seededEmail    = "legacy@example.com"
	seededUsername = "legacy-user"
)

/* -------------------------------------------------------------------------- */
/* Harness                                                                     */
/* -------------------------------------------------------------------------- */

type response struct {
	status  int
	header  http.Header
	body    []byte
	cookies []*http.Cookie
}

// api is one browser: a cookie jar plus the base URL. Every unsafe request echoes
// the readable CSRF cookie by hand, which is what the SPA's HTTP layer does.
type api struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newAPI(t *testing.T, base string) *api {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	return &api{t: t, base: base, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (a *api) call(method, path string, body any, headers map[string]string) response {
	a.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			a.t.Fatalf("marshal request body for %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		a.t.Fatalf("build request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		a.t.Fatalf("read %s %s response: %v", method, path, err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: data, cookies: resp.Cookies()}
}

// csrf mirrors the browser: the token is read from the readable cookie and echoed
// in a header, never taken from the httpOnly session cookie.
func (a *api) csrf() string {
	return a.cookie("tw_csrf")
}

func (a *api) cookie(name string) string {
	a.t.Helper()
	parsed, err := url.Parse(a.base)
	if err != nil {
		a.t.Fatalf("parse base url: %v", err)
	}
	for _, c := range a.client.Jar.Cookies(parsed) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// unsafe returns headers carrying this session's CSRF token.
func (a *api) unsafe(extra map[string]string) map[string]string {
	headers := map[string]string{}
	for key, value := range extra {
		headers[key] = value
	}
	headers["X-CSRF-Token"] = a.csrf()
	return headers
}

// withSession builds a browser holding one session cookie captured earlier, which
// is how a revoked or stolen token gets replayed.
func withSession(t *testing.T, base, token string) *api {
	t.Helper()
	client := newAPI(t, base)
	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	client.client.Jar.SetCookies(parsed, []*http.Cookie{{Name: "tw_session", Value: token, Path: "/"}})
	return client
}

func expectStatus(t *testing.T, res response, want int, what string) {
	t.Helper()
	if res.status != want {
		t.Fatalf("%s: status = %d, want %d (body: %s)", what, res.status, want, truncate(res.body))
	}
}

func truncate(body []byte) string {
	const limit = 300
	if len(body) <= limit {
		return string(body)
	}
	return string(body[:limit]) + "…"
}

func decodeInto(t *testing.T, res response, target any) {
	t.Helper()
	if err := json.Unmarshal(res.body, target); err != nil {
		t.Fatalf("decode response %q: %v", truncate(res.body), err)
	}
}

type meResponse struct {
	Role string `json:"role"`
	Name string `json:"name"`
	// Username is the administrator's sign-in name, which is deliberately a
	// different value from Name (the display name).
	Username string `json:"username"`
	User     *struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
		Nickname string `json:"nickname"`
	} `json:"user"`
}

type installStatus struct {
	Installed bool `json:"installed"`
}

type userList struct {
	Items []struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
		Nickname string `json:"nickname"`
	} `json:"items"`
	Total int64 `json:"total"`
}

/* -------------------------------------------------------------------------- */
/* Test                                                                        */
/* -------------------------------------------------------------------------- */

func TestCookieSessionEndToEnd(t *testing.T) {
	server, db, settingsPath := newTestServer(t)

	anon := newAPI(t, server.URL)
	admin := newAPI(t, server.URL)
	visitor := newAPI(t, server.URL)

	t.Run("install", func(t *testing.T) {
		var status installStatus
		decodeInto(t, anon.call(http.MethodGet, "/install_status", nil, nil), &status)
		if status.Installed {
			t.Fatalf("a fresh server reported installed=true")
		}

		// No session, so there is nothing to protect: the route answers 401 and
		// the SPA decides where to send the browser.
		expectStatus(t, anon.call(http.MethodGet, "/api/me", nil, nil), http.StatusUnauthorized, "GET /api/me before install")

		res := anon.call(http.MethodPost, "/install", map[string]string{"password": adminPassword}, nil)
		expectStatus(t, res, http.StatusOK, "POST /install")
		decodeInto(t, res, &status)
		if !status.Installed {
			t.Fatalf("install did not report the blog as installed")
		}

		// 「已安装」的权威答案现在在数据库里：users 表里 is_admin = 1 的那一行。
		// settings.json 在**全新安装**时会写入默认值（cfg.Save 已在前面跑过），
		// 所以它是存在的；但即使它不存在也不影响「已装好」这个事实——
		// ErrNotInstalled 只表示「这次启动时文件还没写出来」，不是失败。
		if _, err := config.Load(settingsPath); err != nil && !errors.Is(err, config.ErrNotInstalled) {
			t.Fatalf("settings.json is unusable after install: %v", err)
		}
		installed, err := model.NewSQLiteAdminRepository(db).Exists(context.Background())
		if err != nil {
			t.Fatalf("check the administrator row: %v", err)
		}
		if !installed {
			t.Fatal("install reported success but created no administrator row")
		}
	})

	t.Run("email delivery degrades without a key", func(t *testing.T) {
		// The code is stored before the send is attempted, but with no Resend key
		// the caller must be told email is unavailable instead of waiting for a
		// code that will never arrive.
		res := anon.call(http.MethodPost, "/api/auth/email-code", map[string]string{"email": "probe@example.com"}, nil)
		expectStatus(t, res, http.StatusServiceUnavailable, "POST /api/auth/email-code without a mail key")
	})

	t.Run("registration signs the account in", func(t *testing.T) {
		seedVerificationCode(t, db, userEmail, seededCode)

		res := visitor.call(http.MethodPost, "/api/auth/register", map[string]string{
			"email":    userEmail,
			"code":     seededCode,
			"username": userName,
			"password": userPassword,
			"nickname": "Alice",
		}, nil)
		expectStatus(t, res, http.StatusCreated, "POST /api/auth/register")

		var me meResponse
		decodeInto(t, res, &me)
		if me.Role != "user" || me.User == nil || me.User.Email != userEmail {
			t.Fatalf("register returned an unexpected identity: %s", truncate(res.body))
		}
		if me.User.Username != userName {
			t.Fatalf("register stored username %q, want %q", me.User.Username, userName)
		}

		// Registration issues a session, so the new account is usable at once.
		decodeInto(t, visitor.call(http.MethodGet, "/api/me", nil, nil), &me)
		if me.Role != "user" || me.User == nil || me.User.Nickname != "Alice" {
			t.Fatalf("GET /api/me after register returned %s", truncate(res.body))
		}
		// The display name and the sign-in name must be two separate values.
		if me.User.Username != userName || me.User.Username == me.User.Nickname {
			t.Fatalf("username and nickname are not distinct: %s", truncate(res.body))
		}
		if visitor.cookie("tw_session") == "" || visitor.cookie("tw_csrf") == "" {
			t.Fatalf("register did not set both session cookies")
		}

		// Replaying the whole registration must not create a second account, and it
		// must not reveal that the address is already taken either: the code was
		// consumed by the first call, so the replay dies at verification.
		//
		// 这条断言是**账号枚举预言机**的回归守卫。当占用检查排在验证码检查之前时，
		// 一个乱填的码就能区分两种状态：已注册地址得到 409、空闲地址得到 400——
		// 那是一个不需要验证码、可以逐个试出全站注册用户端点的预言机。
		replay := newAPI(t, server.URL).call(http.MethodPost, "/api/auth/register", map[string]string{
			"email":    userEmail,
			"code":     seededCode,
			"username": userName,
			"password": userPassword,
			"nickname": "Alice",
		}, nil)
		expectStatus(t, replay, http.StatusBadRequest, "POST /api/auth/register replaying the whole request")

		// 同样的请求、同样的响应，唯一的差别是那个地址到底存不存在。
		// 两者都必须和「码不对」无法区分。
		free := newAPI(t, server.URL).call(http.MethodPost, "/api/auth/register", map[string]string{
			"email":    "never-registered@example.com",
			"code":     seededCode,
			"username": "another-free-name",
			"password": userPassword,
			"nickname": "Nobody",
		}, nil)
		expectStatus(t, free, http.StatusBadRequest, "POST /api/auth/register with an unused code")
		if string(replay.body) != string(free.body) {
			t.Fatalf("a registered address is distinguishable from a free one:\n registered: %s\n free:       %s",
				truncate(replay.body), truncate(free.body))
		}

		// The code itself is single-use: the same code for a different address is
		// rejected as consumed, which is what stops one mailbox from vouching for
		// several accounts.
		stolen := newAPI(t, server.URL).call(http.MethodPost, "/api/auth/register", map[string]string{
			"email":    "stolen@example.com",
			"code":     seededCode,
			"username": "stolen-code-user",
			"password": userPassword,
			"nickname": "Mallory",
		}, nil)
		expectStatus(t, stolen, http.StatusBadRequest, "POST /api/auth/register reusing a consumed code")
	})

	t.Run("the administrator login name is not available to visitors", func(t *testing.T) {
		// Usernames are a single namespace: sign-in decides between the
		// administrator and a visitor account by username alone, so a visitor
		// holding the administrator's name would be shadowed by it forever.
		// The check is case-insensitive, like every other username comparison.
		seedVerificationCode(t, db, "copycat@example.com", seededCode)

		res := newAPI(t, server.URL).call(http.MethodPost, "/api/auth/register", map[string]string{
			"email":    "copycat@example.com",
			"code":     seededCode,
			"username": "ADMIN",
			"password": userPassword,
			"nickname": "Copycat",
		}, nil)
		expectStatus(t, res, http.StatusConflict, "POST /api/auth/register with the administrator's username")

		// The refusal must be the ordinary "username is already taken" one. A
		// dedicated message would publish the administrator's login name to anyone
		// who can call the unauthenticated registration endpoint.
		if !strings.Contains(string(res.body), "username") {
			t.Fatalf("the refusal did not look like a username conflict: %s", truncate(res.body))
		}
	})

	t.Run("login, cookies and limiter", func(t *testing.T) {
		// A weak password is refused by the server policy even though the browser
		// enforces the same rule: the API is reachable directly.
		weak := newAPI(t, server.URL).call(http.MethodPost, "/api/auth/register", map[string]string{
			"email":    "other@example.com",
			"code":     "000000",
			"username": "other-bob",
			"password": "short",
			"nickname": "Bob",
		}, nil)
		expectStatus(t, weak, http.StatusBadRequest, "POST /api/auth/register with a weak password")

		// The administrator's name and password are both checked; a wrong pair
		// never issues a session. The name is sent in a different case on purpose:
		// usernames are compared case-insensitively everywhere (the users table is
		// COLLATE NOCASE), and the session must still report the configured
		// spelling rather than whatever was typed.
		expectStatus(t,
			admin.call(http.MethodPost, "/api/auth/login", map[string]string{"username": "root", "password": adminPassword}, nil),
			http.StatusUnauthorized, "POST /api/auth/login with a wrong username")

		adminLogin := admin.call(http.MethodPost, "/api/auth/login", map[string]string{"username": "ADMIN", "password": adminPassword}, nil)
		expectStatus(t, adminLogin, http.StatusOK, "POST /api/auth/login")

		var me meResponse
		decodeInto(t, adminLogin, &me)
		if me.Role != "admin" || me.Name != "admin" {
			t.Fatalf("admin login returned an unexpected identity: %s", truncate(adminLogin.body))
		}
		if me.Username != "admin" {
			t.Fatalf("admin session reported username %q, want admin", me.Username)
		}
		assertSessionCookies(t, adminLogin)

		// The same endpoint signs a visitor in: there is no "which identity are
		// you" switch left in the API, and the caller cannot get it wrong.
		// This browser already holds the session registration granted it, so the
		// request carries a session and the CSRF token is required — exactly as the
		// SPA's HTTP layer supplies it. Signing in again replaces the session.
		visitorLogin := visitor.call(http.MethodPost, "/api/auth/login", map[string]string{"username": userName, "password": userPassword}, visitor.unsafe(nil))
		expectStatus(t, visitorLogin, http.StatusOK, "POST /api/auth/login as a visitor account")
		decodeInto(t, visitorLogin, &me)
		if me.Role != "user" || me.User == nil {
			t.Fatalf("visitor login returned an unexpected identity: %s", truncate(visitorLogin.body))
		}

		// Three sign-ins happened above. The bucket is now empty, and because the
		// deprecated alias and the unified endpoint share one per-IP bucket,
		// switching endpoint buys nothing. Answering 429 rather than 404 is also
		// what keeps /api/auth/user/login honest: the route still exists, and it is
		// the same handler behind the same limiter.
		limited := newAPI(t, server.URL).call(http.MethodPost, "/api/auth/user/login", map[string]string{"username": userName, "password": userPassword}, nil)
		expectStatus(t, limited, http.StatusTooManyRequests, "sign-in beyond the per-IP allowance")
		if limited.header.Get("Retry-After") == "" {
			t.Fatalf("rate-limited response carried no Retry-After header")
		}
	})

	t.Run("csrf is required for state changes", func(t *testing.T) {
		// No header at all. The browser still sends the readable CSRF cookie, so a
		// server-side fallback to that cookie would silently pass here: it must not
		// exist, and this is the assertion that catches it.
		expectStatus(t,
			admin.call(http.MethodPut, "/api/admin/profile", map[string]string{"name": "Hijacked"}, nil),
			http.StatusForbidden, "PUT /api/admin/profile without a CSRF header")

		expectStatus(t,
			admin.call(http.MethodPut, "/api/admin/profile", map[string]string{"name": "Hijacked"}, map[string]string{"X-CSRF-Token": "not-the-token"}),
			http.StatusForbidden, "PUT /api/admin/profile with a wrong CSRF token")

		expectStatus(t,
			admin.call(http.MethodPut, "/api/admin/profile", map[string]string{"name": "Twilight Admin"}, admin.unsafe(nil)),
			http.StatusOK, "PUT /api/admin/profile with the CSRF token")

		var me meResponse
		decodeInto(t, admin.call(http.MethodGet, "/api/me", nil, nil), &me)
		if me.Name != "Twilight Admin" {
			t.Fatalf("admin rename did not stick: %s", truncate(admin.call(http.MethodGet, "/api/me", nil, nil).body))
		}

		// Renaming the display name must not touch the sign-in name. Before this
		// was two fields, the rename above silently changed how the operator logs
		// in — hence the pair of assertions below. Whether the *endpoint* still
		// accepts the old sign-in name is kept out of this subtest on purpose: the
		// per-IP sign-in budget was already spent in the previous one, and a 429
		// would make the result say nothing about the rename. The invariant itself
		// (display name and sign-in name move independently) is pinned by
		// config's own test, and the response shape — both fields, distinct — is
		// pinned right here.
		if me.Username != "admin" {
			t.Fatalf("changing the display name moved the sign-in name to %q", me.Username)
		}
		if me.Name == me.Username {
			t.Fatalf("the session reported one value for both names: %s", truncate(admin.call(http.MethodGet, "/api/me", nil, nil).body))
		}

		// A visitor session must not reach administrator routes...
		expectStatus(t, visitor.call(http.MethodGet, "/api/admin/users", nil, nil), http.StatusForbidden, "GET /api/admin/users as a visitor")
		// ...and a session-less request is unauthorized, not forbidden.
		expectStatus(t, newAPI(t, server.URL).call(http.MethodGet, "/api/admin/users", nil, nil), http.StatusUnauthorized, "GET /api/admin/users anonymously")
	})

	t.Run("administrator manages visitor accounts", func(t *testing.T) {
		// A password-less account, as left behind by a commenter who never
		// registered. It owns both a conflicting address and a conflicting
		// sign-in name, so both unique constraints can be exercised below.
		seedUserWithoutPassword(t, db, seededEmail, seededUsername, "Legacy")

		listRes := admin.call(http.MethodGet, "/api/admin/users?page=1&page_size=12", nil, nil)
		expectStatus(t, listRes, http.StatusOK, "GET /api/admin/users")
		var list userList
		decodeInto(t, listRes, &list)
		if list.Total != 2 {
			t.Fatalf("admin user list total = %d, want 2 (%s)", list.Total, truncate(listRes.body))
		}
		if got := usernameOf(t, list, userEmail); got != userName {
			t.Fatalf("the list reported username %q for %s, want %q", got, userEmail, userName)
		}

		// An address another account already owns is refused.
		expectStatus(t,
			admin.call(http.MethodPut, "/api/admin/users/"+strconv.FormatInt(visitorID(t, list, userEmail), 10), map[string]string{"username": userName, "nickname": "Alice", "email": seededEmail}, admin.unsafe(nil)),
			http.StatusConflict, "PUT /api/admin/users/:id onto another account's email")

		// A sign-in name another account owns is refused too. The two collisions
		// must not be conflated: the message tells the operator which field to fix.
		conflict := admin.call(http.MethodPut, "/api/admin/users/"+strconv.FormatInt(visitorID(t, list, userEmail), 10), map[string]string{"username": seededUsername, "nickname": "Alice", "email": "free-address@example.com"}, admin.unsafe(nil))
		expectStatus(t, conflict, http.StatusConflict, "PUT /api/admin/users/:id onto another account's username")
		if !strings.Contains(string(conflict.body), "username") {
			t.Fatalf("a username collision was reported as something else: %s", truncate(conflict.body))
		}

		id := visitorID(t, list, userEmail)
		expectStatus(t,
			admin.call(http.MethodPut, "/api/admin/users/"+strconv.FormatInt(id, 10), map[string]string{"username": "alice-cooper", "nickname": "Alice Cooper", "email": "alice@example.com"}, admin.unsafe(nil)),
			http.StatusOK, "PUT /api/admin/users/:id")

		// The corrected identity is visible to its owner straight away, and the
		// three fields moved independently of one another.
		meRes := visitor.call(http.MethodGet, "/api/me", nil, nil)
		expectStatus(t, meRes, http.StatusOK, "GET /api/me after the account edit")
		var me meResponse
		decodeInto(t, meRes, &me)
		if me.User == nil || me.User.Nickname != "Alice Cooper" || me.User.Email != "alice@example.com" {
			t.Fatalf("the account edit is not visible to its owner: %s", truncate(meRes.body))
		}
		if me.User.Username != "alice-cooper" {
			t.Fatalf("the sign-in name was not updated: %s", truncate(meRes.body))
		}

		// The same invariant from the other side: the administrator may not move
		// their own login name onto a visitor's username. Either collision would
		// leave one of the two unable to sign in, and sign-in has no way to tell
		// them apart. The refusal must not move the stored login name either.
		expectStatus(t,
			admin.call(http.MethodPut, "/api/admin/profile", map[string]string{"name": "Twilight Admin", "username": "alice-cooper"}, admin.unsafe(nil)),
			http.StatusConflict, "PUT /api/admin/profile onto a visitor's username")

		var adminMe meResponse
		decodeInto(t, admin.call(http.MethodGet, "/api/me", nil, nil), &adminMe)
		if adminMe.Username != "admin" {
			t.Fatalf("a refused rename moved the administrator login name to %q", adminMe.Username)
		}

		// An unsafe request from one session cannot borrow another session's token.
		expectStatus(t,
			visitor.call(http.MethodPatch, "/api/me", map[string]string{"nickname": "Alice"}, map[string]string{"X-CSRF-Token": admin.csrf()}),
			http.StatusForbidden, "PATCH /api/me with the administrator's CSRF token")
	})

	t.Run("password change rotates the session", func(t *testing.T) {
		before := visitor.cookie("tw_session")
		if before == "" {
			t.Fatalf("the visitor has no session to rotate")
		}

		expectStatus(t,
			visitor.call(http.MethodPut, "/api/me/password", map[string]string{"current_password": "not-it-1", "new_password": "Visitor2pass"}, visitor.unsafe(nil)),
			http.StatusUnauthorized, "PUT /api/me/password with a wrong current password")

		expectStatus(t,
			visitor.call(http.MethodPut, "/api/me/password", map[string]string{"current_password": userPassword, "new_password": "Visitor2pass"}, visitor.unsafe(nil)),
			http.StatusNoContent, "PUT /api/me/password")

		// This device is signed back in with a fresh token...
		expectStatus(t, visitor.call(http.MethodGet, "/api/me", nil, nil), http.StatusOK, "GET /api/me after the password change")
		if after := visitor.cookie("tw_session"); after == "" || after == before {
			t.Fatalf("the session token was not rotated (before=%q after=%q)", before, after)
		}
		// ...while the token that was valid a moment ago is revoked.
		expectStatus(t, withSession(t, server.URL, before).call(http.MethodGet, "/api/me", nil, nil), http.StatusUnauthorized, "GET /api/me replaying the pre-change session")
	})

	t.Run("logout revokes the session and clears the cookies", func(t *testing.T) {
		adminToken := admin.cookie("tw_session")
		if adminToken == "" {
			t.Fatalf("no admin session to log out")
		}

		res := admin.call(http.MethodPost, "/api/auth/logout", nil, admin.unsafe(nil))
		expectStatus(t, res, http.StatusNoContent, "POST /api/auth/logout")

		for _, c := range res.cookies {
			if c.MaxAge >= 0 && c.Value != "" {
				t.Fatalf("logout left cookie %q set to %q", c.Name, c.Value)
			}
		}

		expectStatus(t, admin.call(http.MethodGet, "/api/me", nil, nil), http.StatusUnauthorized, "GET /api/me after logout")
		// The revoked token must not work even when replayed by hand.
		expectStatus(t, withSession(t, server.URL, adminToken).call(http.MethodGet, "/api/me", nil, nil), http.StatusUnauthorized, "GET /api/me replaying a revoked session")
	})

	t.Run("writing a comment needs a session", func(t *testing.T) {
		expectStatus(t,
			newAPI(t, server.URL).call(http.MethodPost, "/api/comments", map[string]any{"target_type": "article", "target_id": 1, "content": "hello"}, nil),
			http.StatusUnauthorized, "POST /api/comments anonymously")
	})

	t.Run("spa is served with a history fallback", func(t *testing.T) {
		// The front end is embedded in the binary, so a stale or missing asset
		// directory would only show up here: the index must be the built one.
		// 外壳由 Go 模板渲染（见 index.go）：入口 chunk 与 CSS 从 Vite manifest
		// 解析出来，文件名带内容哈希，但**不再**以 "index-" 开头——所以这里
		// 只能断言「引用了带哈希的 /assets/ 产物」，不能断言具体前缀。
		index := anon.call(http.MethodGet, "/", nil, nil)
		expectStatus(t, index, http.StatusOK, "GET /")
		if !strings.Contains(string(index.body), "<div id=\"root\">") {
			t.Fatalf("GET / did not return the SPA shell: %s", truncate(index.body))
		}
		if !strings.Contains(string(index.body), `src="/assets/main-`) {
			t.Fatalf("the rendered shell references no hashed entry bundle; check the manifest resolution")
		}

		// 模板注入的站点描述：爬虫不执行 JS，读到的必须是真实文案而不是空标签。
		if !strings.Contains(string(index.body), `<meta name="description" content="`) {
			t.Fatalf("the rendered shell carries no description meta tag")
		}

		// Client-side routes fall back to the shell...
		expectStatus(t, anon.call(http.MethodGet, "/admin/login", nil, nil), http.StatusOK, "GET /admin/login as a page")
		// ...while an unknown API route stays a 404 instead of pretending to exist.
		expectStatus(t, anon.call(http.MethodGet, "/api/does-not-exist", nil, nil), http.StatusNotFound, "GET an unknown API route")
	})
}

/* -------------------------------------------------------------------------- */
/* Migration ordering (T4)                                                     */
/* -------------------------------------------------------------------------- */

// TestMigrateAdminCredentialsRunsBeforeServing is the regression guard for the
// ordering constraint that keeps an upgrade from opening a takeover window.
//
// 场景：一台装有旧版二进制的站点升级上来，管理员凭据还在 settings.json 里。
// 若凭据没被搬进库，那么第一个到达的请求会看到「未安装」，于是未鉴权的
// POST /install 重新开放——任何能访问站点的人都能设一个新的管理员密码。
// 窗口内没有任何日志会显示异常。
//
// ⚠️ 这条测试的守护范围要说清楚，免得它给人虚假的安心：
// 它验证的是**整条引导路径**（迁移 → Register）的最终状态，而不是「迁移发生在
// 服务开始之前」这个时间点。真实架构里 view.Register 不可能调用 model 的迁移
// （那会构成 import cycle），所以「迁移被放进 Register 内部」这个位置无法用测试
// 表达，下面这次改动也测不出来。真正钉住这个顺序的是 main.go 里那段带 ⚠️ 的
// 注释与调用次序——那才是这一条的权威，本条测试只保证「改了引导路径之后，
// 一个老站点起来时仍然是已安装、旧密码仍然能登录」。
//
// 反证方式：从引导路径里删掉 MigrateAdminCredentials 调用（而不是把它挪到
// Register 之后），installed 断言会立刻失败。
func TestMigrateAdminCredentialsRunsBeforeServing(t *testing.T) {
	legacyHash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash legacy password: %v", err)
	}

	server, db, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		// 这一版之前的 settings.json：凭据在文件里，库里还没有管理员行。
		cfg.PasswordHash = string(legacyHash)
		cfg.AdminUsername = "admin"
		cfg.AdminNickname = "admin"
	})

	anon := newAPI(t, server.URL)

	// 第一个到达的请求就必须看到「已安装」。这正是「迁移早于服务」的可观测形式。
	var status installStatus
	decodeInto(t, anon.call(http.MethodGet, "/install_status", nil, nil), &status)
	if !status.Installed {
		t.Fatal("the very first request saw installed=false: the credential migration ran after the server started serving")
	}

	// 而且 /install 必须已经被关掉——这是上面那条断言真正要保护的东西。
	expectStatus(t,
		anon.call(http.MethodPost, "/install", map[string]string{"password": "Replacement9"}, nil),
		http.StatusConflict, "POST /install after the migrated start-up")

	// 旧密码在新版本里必须仍然能登录，且拿到的是管理员会话。
	admin := newAPI(t, server.URL)
	login := admin.call(http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": adminPassword}, nil)
	expectStatus(t, login, http.StatusOK, "POST /api/auth/login with the migrated password")

	var me meResponse
	decodeInto(t, login, &me)
	if me.Role != "admin" || me.Username != "admin" {
		t.Fatalf("the migrated administrator could not sign in as an administrator: %s", truncate(login.body))
	}

	// 库里的那一行必须真的是管理员，而不是一个恰好同名的访客。
	row, err := model.NewSQLiteAdminRepository(db).Get(context.Background())
	if err != nil {
		t.Fatalf("Get() the migrated administrator error = %v", err)
	}
	if !row.IsAdmin || row.Username != "admin" {
		t.Fatalf("the migrated row is not the administrator: %+v", row)
	}
}

/* -------------------------------------------------------------------------- */
/* Helpers                                                                     */
/* -------------------------------------------------------------------------- */

// newTestServer builds a server exactly the way main.go does, including the
// migration step. The ordering is the point: the administrator credentials must
// land in the users table *before* view.Register starts answering requests.
//
// It pre-installs nothing — the "install" subtest drives POST /install over HTTP —
// so the harness passes an empty config, and `withLegacyCredentials` covers the
// upgrade-from-settings.json path instead.
func newTestServer(t *testing.T) (*httptest.Server, *sql.DB, string) {
	t.Helper()
	return newTestServerWithConfig(t, func(*config.Config) {})
}

// newTestServerWithConfig is the same harness with a hook for seeding
// settings.json, standing in for a database that predates this refactor.
func newTestServerWithConfig(t *testing.T, seed func(*config.Config)) (*httptest.Server, *sql.DB, string) {
	t.Helper()

	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	db, err := database.Open(filepath.Join(dir, "blog.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := &config.Config{}
	cfg.ApplyDefaults()
	seed(cfg)

	// 图片必须在落盘前转成 WebP，转换器因此是硬性依赖——测试服务器也得有一个。
	// 默认注入一个「假 cwebp」（见 internal/testtool）；用例可以把 cfg.WebPTool
	// 改成别的路径来构造「这台机器上没有可用编码器」的处境，那时 Detect 会失败。
	//
	// 这里比生产路径**宽松**：main 在探测失败时直接退出，而这里把零值转换器交给
	// 路由，让请求照常走到 service 并拿到 500。差异是刻意的——要让「没有编码器时
	// 上传会怎样」可测，就不能把整个测试进程一起退出。
	if cfg.WebPTool == "" {
		cfg.WebPTool = testtool.Fake(t, testtool.ModeOK)
	}
	mediaConv, detectErr := mediaconv.Detect(cfg.WebPTool, cfg.WebMTool)
	if detectErr != nil {
		mediaConv = &mediaconv.Converter{}
	}

	// 与 main.go 完全相同的顺序：迁移 → 清空配置里的凭据 → 再注册路由。
	// 少了这一步，管理员就不在库里，而 /install_status 会报告「未安装」。
	if err := model.MigrateAdminCredentials(context.Background(), db, cfg); err != nil {
		t.Fatalf("migrate administrator credentials: %v", err)
	}
	cfg.ClearLegacyCredentials()

	e := echo.New()
	e.HideBanner = true
	// Search is not part of these flows, and a nil client keeps the test offline.
	if err := view.Register(e, cfg, db, settingsPath, nil, mediaConv); err != nil {
		t.Fatalf("register routes: %v", err)
	}

	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	return server, db, settingsPath
}

// seedVerificationCode stores a code the same way the service would, so the
// registration flow can be driven over HTTP without a reachable mail provider.
//
// 存储形式必须与服务端**完全一致**：验证码现在走 bcrypt 慢哈希
// （见 service.hashVerificationCode），所以这里复用导出出来的同一个函数。
// 用一个自己手写的哈希（例如曾经的 sha256）会让 fixture 与服务端悄悄漂移，
// 而症状是「测试全绿、真实注册却永远报验证码错误」。
func seedVerificationCode(t *testing.T, db *sql.DB, email, code string) {
	t.Helper()

	now := time.Now().Unix()
	repo := model.NewSQLiteEmailVerificationRepository(db)
	err := repo.Create(context.Background(), &model.EmailVerificationCode{
		Email:     email,
		CodeHash:  service.HashVerificationCodeForStorage(code),
		ExpiresAt: now + 600,
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("seed verification code: %v", err)
	}
}

// seedUserWithoutPassword inserts an account that never set a password, exactly
// as a commenter who never registered would leave behind.
func seedUserWithoutPassword(t *testing.T, db *sql.DB, email, username, nickname string) {
	t.Helper()

	now := time.Now().Unix()
	repo := model.NewSQLiteUserRepository(db)
	if err := repo.Create(context.Background(), &model.User{
		Email:     email,
		Username:  username,
		Nickname:  nickname,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
}

func visitorID(t *testing.T, list userList, email string) int64 {
	t.Helper()
	for _, item := range list.Items {
		if item.Email == email {
			return item.ID
		}
	}
	t.Fatalf("account %s is missing from the admin user list", email)
	return 0
}

func usernameOf(t *testing.T, list userList, email string) string {
	t.Helper()
	for _, item := range list.Items {
		if item.Email == email {
			return item.Username
		}
	}
	t.Fatalf("account %s is missing from the admin user list", email)
	return ""
}

// assertSessionCookies pins the cookie contract the SPA depends on: the session
// cookie is unreadable to scripts and the CSRF cookie is readable, which is what
// makes the double-submit check meaningful.
func assertSessionCookies(t *testing.T, res response) {
	t.Helper()

	var session, csrf *http.Cookie
	for _, c := range res.cookies {
		switch c.Name {
		case "tw_session":
			session = c
		case "tw_csrf":
			csrf = c
		}
	}

	if session == nil {
		t.Fatalf("login did not set tw_session (cookies: %v)", cookieNames(res.cookies))
	}
	if !session.HttpOnly {
		t.Fatalf("tw_session must be HttpOnly so scripts cannot read it")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("tw_session SameSite = %v, want Lax", session.SameSite)
	}
	if len(session.Value) != 64 || strings.Trim(session.Value, "0123456789abcdef") != "" {
		t.Fatalf("tw_session value is not a 64-char hex token: %q", session.Value)
	}
	if session.Path != "/" {
		t.Fatalf("tw_session Path = %q, want /", session.Path)
	}

	if csrf == nil {
		t.Fatalf("login did not set tw_csrf (cookies: %v)", cookieNames(res.cookies))
	}
	if csrf.HttpOnly {
		t.Fatalf("tw_csrf must stay readable so the SPA can echo it in a header")
	}
	if csrf.Value == session.Value {
		t.Fatalf("the CSRF token must differ from the session token")
	}
}

func cookieNames(cookies []*http.Cookie) []string {
	names := make([]string, 0, len(cookies))
	for _, c := range cookies {
		names = append(names, c.Name)
	}
	return names
}
