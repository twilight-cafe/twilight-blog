package view_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/model"
	"blog-engine/internal/session"
)

// 这个文件是「管理员凭据并入 users 表」这次重构的端到端守护。
//
// 为什么需要整整一个文件：这次改动把**一个隐含的授权条件**抽掉了。改动之前
// 管理员会话的 UserID 恒为 nil，于是 `sess.UserID == nil` 这一句恰好把管理员挡在
// 若干「仅访客」端点之外；管理员现在有真实账号行了，那个条件不再成立。
// 这类语义翻转不会报错、不会失败编译，只会静默改变授权结果——所以每一条都必须
// 有断言盯着，而且每一条都必须做过反证。
//
// 复用 auth_e2e_test.go 的 api / newAPI / newTestServer / expectStatus 等辅助函数。

// installFreshBlog 走真实的安装接口建好一个站点，返回管理员浏览器。
//
// 刻意走 HTTP 而不是直接插库：安装路径本身也是这次重构改过的部分
// （不再写 settings.json，改为插 is_admin = 1 的行）。
func installFreshBlog(t *testing.T, server *httptest.Server) *api {
	t.Helper()

	admin := newAPI(t, server.URL)
	res := admin.call(http.MethodPost, "/install", map[string]string{"password": adminPassword}, nil)
	expectStatus(t, res, http.StatusOK, "POST /install")

	login := admin.call(http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": adminPassword}, nil)
	expectStatus(t, login, http.StatusOK, "POST /api/auth/login as the administrator")
	return admin
}

// TestLoginFromTheAdminRowYieldsAnAdminSession pins invariant I3: the
// administrator signs in through the *same* single lookup every visitor uses,
// and the role/TTL still follow the account rather than the code path.
//
// 反证方式：把 Login 末尾的 `if user.IsAdmin` 改成恒走 startUserSession →
// role 变成 user，测试失败。这正是 T1/T2 之外第三个会「静默地把管理员降级」
// 的地方。
func TestLoginFromTheAdminRowYieldsAnAdminSession(t *testing.T) {
	server, db, _ := newTestServer(t)

	// 先装好站点（这一步会创建 is_admin = 1 的那一行）。
	expectStatus(t,
		newAPI(t, server.URL).call(http.MethodPost, "/install", map[string]string{"password": adminPassword}, nil),
		http.StatusOK, "POST /install")

	// 那一行必须真的是管理员，而不是一个恰好同名的访客。
	row, err := model.NewSQLiteAdminRepository(db).Get(context.Background())
	if err != nil {
		t.Fatalf("Get() the administrator row error = %v", err)
	}
	if !row.IsAdmin {
		t.Fatal("install created a row that is not marked as the administrator")
	}

	admin := newAPI(t, server.URL)
	login := admin.call(http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": adminPassword}, nil)
	expectStatus(t, login, http.StatusOK, "POST /api/auth/login")

	var me meResponse
	decodeInto(t, login, &me)
	if me.Role != "admin" {
		t.Fatalf("the administrator signed in with role=%q, want admin", me.Role)
	}
	if me.Username != "admin" || me.Name == "" {
		t.Fatalf("the session is missing the administrator identity: %s", truncate(login.body))
	}
	// 不变量 I2：admin 会话的响应体里**没有** user 对象。
	if me.User != nil {
		t.Fatalf("the administrator session leaked a user object: %s", truncate(login.body))
	}

	// 不变量 I3：会话行是 role=admin 且 user_id 指向真实账号行——user_id 不再是
	// NULL。第二点很关键：RevokeUserSessions(adminID) 靠它才能找到管理员的会话，
	// 否则「改密后其他设备下线」这条保证会静默失效。
	var role string
	var userID sql.NullInt64
	if err := db.QueryRow(`SELECT role, user_id FROM sessions ORDER BY created_at DESC LIMIT 1`).Scan(&role, &userID); err != nil {
		t.Fatalf("read the session row: %v", err)
	}
	if role != string(session.RoleAdmin) {
		t.Fatalf("the session row role = %q, want admin", role)
	}
	if !userID.Valid || userID.Int64 != row.ID {
		t.Fatalf("the administrator session row user_id = %v, want the real account id %d — "+
			"without it, revoking the administrator's sessions cannot work", userID, row.ID)
	}

	// TTL 由 sessions.expires_at 体现：必须落在 AdminTTL 附近，而不是 UserTTL。
	var expiresAt int64
	if err := db.QueryRow(`SELECT expires_at FROM sessions ORDER BY created_at DESC LIMIT 1`).Scan(&expiresAt); err != nil {
		t.Fatalf("read the session expiry: %v", err)
	}
	remaining := time.Until(time.Unix(expiresAt, 0))
	if remaining > session.AdminTTL+time.Minute {
		t.Fatalf("the administrator session outlives AdminTTL (%s remaining); it looks like a UserTTL session", remaining)
	}
	if remaining < session.AdminTTL-time.Hour {
		t.Fatalf("the administrator session is much shorter than AdminTTL: %s remaining", remaining)
	}

	// 会话确实能进管理员分组。
	expectStatus(t, admin.call(http.MethodGet, "/api/admin/users?page=1&page_size=12", nil, nil), http.StatusOK, "GET /api/admin/users as the administrator")
}

// TestAdminCanChangeOwnPassword is the guard for D-1=b and for T2.
//
// 它同时守两件事，而这两件事必须分开断言：
//  1. 管理员**能**改自己的密码（改动前这里恒为 403，前端那个表单因此永远失败）；
//  2. 改完之后本机会话**仍然是 role=admin**——若用 startUserSession 重新签发，
//     管理员会被当场降级成访客，而请求照样返回 204。这是最危险的一种：用户看到
//     成功，却再也进不了后台，且没有任何错误信息指向这里。
//
// 反证方式（两条，都要做）：
//   - 恢复 `sess.UserID == nil` 那个守卫 → 第一次调用返回 403，失败；
//   - 把 reissueSession 换回 startUserSession → 会话角色变成 user，失败。
func TestAdminCanChangeOwnPassword(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)

	oldToken := admin.cookie("tw_session")
	if oldToken == "" {
		t.Fatal("no administrator session to rotate")
	}

	// 当前密码错误 → 401，且密码不变。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/me/password", map[string]string{
			"current_password": "not-the-password",
			"new_password":     "AdminNewPass1",
		}, admin.unsafe(nil)),
		http.StatusUnauthorized, "PUT /api/me/password with a wrong current password")

	// 弱口令 → 400。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/me/password", map[string]string{
			"current_password": adminPassword,
			"new_password":     "weak",
		}, admin.unsafe(nil)),
		http.StatusBadRequest, "PUT /api/me/password with a weak new password")

	// ★ 唯一有意改变的响应：管理员从此拿到 204，而不是 403。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/me/password", map[string]string{
			"current_password": adminPassword,
			"new_password":     "AdminNewPass1",
		}, admin.unsafe(nil)),
		http.StatusNoContent, "PUT /api/me/password as the administrator")

	// 本机被重新登录，而且**仍然是管理员**。这一条就是 T2 的守卫。
	meRes := admin.call(http.MethodGet, "/api/me", nil, nil)
	expectStatus(t, meRes, http.StatusOK, "GET /api/me after the password change")
	var me meResponse
	decodeInto(t, meRes, &me)
	if me.Role != "admin" {
		t.Fatalf("the administrator was silently demoted to role=%q by their own password change: %s",
			me.Role, truncate(meRes.body))
	}
	if after := admin.cookie("tw_session"); after == "" || after == oldToken {
		t.Fatalf("the session token was not rotated (before=%q after=%q)", oldToken, after)
	}

	// 改完之后仍然进得去管理员分组。
	expectStatus(t, admin.call(http.MethodGet, "/api/admin/users?page=1&page_size=12", nil, nil), http.StatusOK, "GET /api/admin/users after the change")

	// 旧密码不再可用，新密码可用。这一对断言确保我们改的是**真的那一行**。
	expectStatus(t,
		newAPI(t, server.URL).call(http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": adminPassword}, nil),
		http.StatusUnauthorized, "POST /api/auth/login with the retired password")
	expectStatus(t,
		newAPI(t, server.URL).call(http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": "AdminNewPass1"}, nil),
		http.StatusOK, "POST /api/auth/login with the new password")

	// 库里的哈希确实换过了（而不是只换了会话）。
	row, err := model.NewSQLiteAdminRepository(db).Get(context.Background())
	if err != nil {
		t.Fatalf("Get() the administrator row error = %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(row.PasswordHash), []byte("AdminNewPass1")); err != nil {
		t.Fatalf("the stored hash does not match the new password: %v", err)
	}
	if row.ID == 0 {
		t.Fatal("the administrator row lost its id")
	}
}

// TestAdminPasswordChangeRevokesOtherAdminSessions covers the second half of the
// rotate: every other session of the account must die, or a stolen token survives
// the very action that was supposed to revoke it.
//
// 反证方式：把 ChangePassword 里的 RevokeUserSessions 调用去掉 → 第二个浏览器的
// 会话仍然有效，测试失败。
func TestAdminPasswordChangeRevokesOtherAdminSessions(t *testing.T) {
	server, _, _ := newTestServer(t)
	admin := installFreshBlog(t, server)

	// 第二个浏览器：同一个账号的另一个会话，模拟「另一台设备」。
	other := newAPI(t, server.URL)
	expectStatus(t,
		other.call(http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": adminPassword}, nil),
		http.StatusOK, "POST /api/auth/login from the second device")
	otherToken := other.cookie("tw_session")
	if otherToken == "" {
		t.Fatal("the second device got no session")
	}
	expectStatus(t, other.call(http.MethodGet, "/api/me", nil, nil), http.StatusOK, "GET /api/me on the second device")

	// 第一台设备改密码。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/me/password", map[string]string{
			"current_password": adminPassword,
			"new_password":     "AdminNewPass1",
		}, admin.unsafe(nil)),
		http.StatusNoContent, "PUT /api/me/password")

	// 另一台设备必须下线——包括直接重放旧 token 也不行。
	expectStatus(t, other.call(http.MethodGet, "/api/me", nil, nil), http.StatusUnauthorized, "GET /api/me on the second device after the change")
	expectStatus(t,
		withSession(t, server.URL, otherToken).call(http.MethodGet, "/api/me", nil, nil),
		http.StatusUnauthorized, "GET /api/me replaying the second device's revoked token")

	// 本机仍然可用，否则这次轮换就把自己也踢了出去。
	expectStatus(t, admin.call(http.MethodGet, "/api/me", nil, nil), http.StatusOK, "GET /api/me on the changing device")
}

// TestTitleOnlyUpdateKeepsTheBody 是 R3-15 的端到端守护：走真实 HTTP，
// 复现当时的攻击/误操作路径——只带 title 的 PUT。
//
// 为什么必须在 HTTP 这一层也测一次：类型层面的指针语义（viewmodel 的单测）
// 与「handler 把请求字段透给 service」是两件事，中间任何一处漏传，
// 指针就会变成 nil 或（更早的版本里）空串。真正会伤到人的是这一整条路径。
func TestTitleOnlyUpdateKeepsTheBody(t *testing.T) {
	server, _, _ := newTestServer(t)
	admin := installFreshBlog(t, server)

	t.Run("article", func(t *testing.T) {
		const body = "端到端正文标记-不许消失"

		create := admin.call(http.MethodPost, "/api/admin/articles", map[string]any{
			"title": "原始标题", "content": body, "tags": []string{},
		}, admin.unsafe(nil))
		expectStatus(t, create, http.StatusCreated, "POST /api/admin/articles")
		var article struct {
			ID int64 `json:"id"`
		}
		decodeInto(t, create, &article)

		path := "/api/admin/articles/" + strconv.FormatInt(article.ID, 10)
		// 只传标题：这正是会清空正文的那次请求。
		expectStatus(t,
			admin.call(http.MethodPut, path, map[string]any{"title": "只改标题"}, admin.unsafe(nil)),
			http.StatusOK, "PUT with a title only")

		got := admin.call(http.MethodGet, "/api/articles/"+strconv.FormatInt(article.ID, 10), nil, nil)
		expectStatus(t, got, http.StatusOK, "GET the updated article")
		var stored struct {
			Title   string `json:"title"`
			Content string `json:"content"`
		}
		decodeInto(t, got, &stored)
		if stored.Title != "只改标题" {
			t.Fatalf("title = %q, want 只改标题", stored.Title)
		}
		if stored.Content != body {
			t.Fatalf("content = %q, want %q — a title-only update wiped the article body", stored.Content, body)
		}
	})

	t.Run("page", func(t *testing.T) {
		const body = "端到端页面正文-不许消失"

		create := admin.call(http.MethodPost, "/api/admin/pages", map[string]any{
			"title": "原始页面", "content": body,
		}, admin.unsafe(nil))
		expectStatus(t, create, http.StatusCreated, "POST /api/admin/pages")
		var page struct {
			ID int64 `json:"id"`
		}
		decodeInto(t, create, &page)

		path := "/api/admin/pages/" + strconv.FormatInt(page.ID, 10)
		expectStatus(t,
			admin.call(http.MethodPut, path, map[string]any{"title": "只改标题"}, admin.unsafe(nil)),
			http.StatusOK, "PUT with a title only")

		got := admin.call(http.MethodGet, "/api/pages/"+strconv.FormatInt(page.ID, 10), nil, nil)
		expectStatus(t, got, http.StatusOK, "GET the updated page")
		var stored struct {
			Title   string `json:"title"`
			Content string `json:"content"`
		}
		decodeInto(t, got, &stored)
		if stored.Content != body {
			t.Fatalf("content = %q, want %q — a title-only update wiped the page body", stored.Content, body)
		}
	})
}

// TestEmptyCredentialsCannotBlankTheLoginName 是端到端守护：
// 一个显式传空串的资料更新**不能**把账号的登录名清空。
//
// 这条链横跨三层：viewmodel 负责把「显式传空串」保留为指针（否则它会被当成
// 「没传」，service 连拒绝的机会都没有），service 的形状校验负责判定空值非法，
// handler 负责把校验错误映射成 400。任何一层松动，结果都是**账号被锁死**——
// 登录名被清空之后，那个账号再也登不进来，而唯一的补救手段是站长手工改库。
//
// 反证方式：让 service 的 normalizeAndValidateUsername 放行空值 → 本条失败。
func TestEmptyCredentialsCannotBlankTheLoginName(t *testing.T) {
	server, db, _ := newTestServer(t)
	// 这条链只涉及访客账号，管理员身份仅用于把站点装起来。
	installFreshBlog(t, server)

	seedVerificationCode(t, db, "blank@example.com", seededCode)
	visitor := newAPI(t, server.URL)
	expectStatus(t,
		visitor.call(http.MethodPost, "/api/auth/register", map[string]string{
			"email":    "blank@example.com",
			"code":     seededCode,
			"username": "blanktester",
			"password": userPassword,
			"nickname": "Blank Tester",
		}, nil),
		http.StatusCreated, "POST /api/auth/register")
	// 注册后该客户端已经持有会话，此后的写请求必须带 CSRF（unsafe 就是「补上它」）。
	expectStatus(t,
		visitor.call(http.MethodPost, "/api/auth/login", map[string]string{
			"username": "blanktester", "password": userPassword,
		}, visitor.unsafe(nil)),
		http.StatusOK, "POST /api/auth/login as the visitor")

	for _, body := range []map[string]any{
		{"username": ""},
		{"username": "   "},
		{"nickname": "   "},
	} {
		expectStatus(t,
			visitor.call(http.MethodPatch, "/api/me", body, visitor.unsafe(nil)),
			http.StatusBadRequest, "PATCH /api/me with a blank value")
	}

	// 真正的证据在库与接口里：登录名与昵称都还在原值上。
	//
	// 注意字段位置：MeResponse.Username 是**站长**的登录名，只有 role=admin 时
	// 才出现（omitempty）。访客账号的名字在 user 子对象里，读错位置会拿一个
	// 恒为空串的值去比较，让这条守护永远「失败」——或者更糟，如果写成
	// 「期望为空」，会永远「通过」而什么都没守住。
	me := visitor.call(http.MethodGet, "/api/me", nil, nil)
	expectStatus(t, me, http.StatusOK, "GET /api/me")
	var decoded meResponse
	decodeInto(t, me, &decoded)
	if decoded.User == nil {
		t.Fatalf("GET /api/me returned no user object for a signed-in visitor (body: %s)", truncate(me.body))
	}
	if decoded.User.Username != "blanktester" {
		t.Fatalf("username = %q, want blanktester — a blank update renamed the account", decoded.User.Username)
	}
	if decoded.User.Nickname != "Blank Tester" {
		t.Fatalf("nickname = %q, want Blank Tester", decoded.User.Nickname)
	}

	// 而且原来的凭据必须仍然能登录——这是「没被锁死」的直接证明。
	expectStatus(t,
		newAPI(t, server.URL).call(http.MethodPost, "/api/auth/login", map[string]string{
			"username": "blanktester", "password": userPassword,
		}, nil),
		http.StatusOK, "POST /api/auth/login after the refused blank updates")
}

// createCommentTargetArticle 以管理员身份建一篇文章并返回它的 id。
//
// 存在的理由：评论服务现在要求目标内容真实存在（R3-10），而几乎所有
// 「发一条评论」的用例都只关心评论本身，不关心目标。把建目标这件事收成
// 一个 helper，用例里就只剩被测的那一步。
func createCommentTargetArticle(t *testing.T, admin *api) int64 {
	t.Helper()

	res := admin.call(http.MethodPost, "/api/admin/articles", map[string]any{
		"title":   "comment target",
		"content": "body",
		"tags":    []string{},
	}, admin.unsafe(nil))
	expectStatus(t, res, http.StatusCreated, "POST /api/admin/articles (comment target)")

	var article struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, res, &article)
	if article.ID == 0 {
		t.Fatalf("the seeded comment target has no id: %s", truncate(res.body))
	}
	return article.ID
}

// TestAdminCannotUseVisitorOnlyEndpoints is the §7 audit turned into assertions:
// the three endpoints that must stay closed to the administrator.
//
// 为什么这些必须在这次重构里显式加守卫：改动前它们靠「管理员的 UserID 是 nil」
// 挡住管理员，而那个前提已经不存在。少加任何一处，管理员都会**静默**获得一项
// 本来只属于访客的能力（改自己的用户名、给评论点赞），而这是范围外的行为变更。
//
// 反证方式：逐个移除 handler 里的 IsAdmin() 守卫 → 对应断言失败。
func TestAdminCannotUseVisitorOnlyEndpoints(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)

	// 评论必须挂在真实存在的内容下（CommentService 会校验目标，见 R3-10），
	// 所以先建一篇文章拿到它的 id。用真实接口而不是直接写库：这两条路径
	// 一旦有一处漏掉「目标存在」这个前提，这里就该失败。
	targetID := createCommentTargetArticle(t, admin)

	// 造一条评论，供点赞用例使用。管理员可以发表评论（署名走管理员分支），
	// 所以这里直接用管理员身份创建，省掉一个访客账号。
	commentRes := admin.call(http.MethodPost, "/api/comments", map[string]any{
		"target_type": "article",
		"target_id":   targetID,
		"content":     "a comment to like",
	}, admin.unsafe(nil))
	expectStatus(t, commentRes, http.StatusCreated, "POST /api/comments as the administrator")
	var comment struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, commentRes, &comment)
	if comment.ID == 0 {
		t.Fatalf("the created comment has no id: %s", truncate(commentRes.body))
	}
	commentPath := "/api/comments/" + strconv.FormatInt(comment.ID, 10)

	t.Run("PATCH /api/me stays closed", func(t *testing.T) {
		res := admin.call(http.MethodPatch, "/api/me", map[string]string{"nickname": "Renamed"}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusForbidden, "PATCH /api/me as the administrator")
		if !strings.Contains(string(res.body), "only visitor accounts") {
			t.Fatalf("the refusal did not name the visitor-only rule: %s", truncate(res.body))
		}

		// 被拒绝的请求不得留下任何痕迹：管理员的用户名与昵称必须原样。
		me := admin.call(http.MethodGet, "/api/me", nil, nil)
		var decoded meResponse
		decodeInto(t, me, &decoded)
		if decoded.Username != "admin" {
			t.Fatalf("a refused PATCH /api/me still moved the login name to %q", decoded.Username)
		}
		if decoded.Name != "admin" {
			t.Fatalf("a refused PATCH /api/me still changed the display name to %q", decoded.Name)
		}
	})

	t.Run("liking stays closed", func(t *testing.T) {
		expectStatus(t,
			admin.call(http.MethodPost, commentPath+"/like", nil, admin.unsafe(nil)),
			http.StatusForbidden, "POST /api/comments/:id/like as the administrator")
		expectStatus(t,
			admin.call(http.MethodDelete, commentPath+"/like", nil, admin.unsafe(nil)),
			http.StatusForbidden, "DELETE /api/comments/:id/like as the administrator")
	})

	t.Run("the like count did not move", func(t *testing.T) {
		// 403 只是第一层；真正的证据是 comment_likes 里没有多出一行指向管理员的记录。
		var likes int
		if err := db.QueryRow(`SELECT COUNT(*) FROM comment_likes`).Scan(&likes); err != nil {
			t.Fatalf("count comment_likes: %v", err)
		}
		if likes != 0 {
			t.Fatalf("a refused like still wrote %d row(s) into comment_likes", likes)
		}
	})

	t.Run("a visitor can still do both", func(t *testing.T) {
		// 对照组：这些端点必须对访客照常工作，否则上面的 403 可能只是「全都坏了」。
		seedVerificationCode(t, db, "liker@example.com", seededCode)
		visitor := newAPI(t, server.URL)
		expectStatus(t,
			visitor.call(http.MethodPost, "/api/auth/register", map[string]string{
				"email":    "liker@example.com",
				"code":     seededCode,
				"username": "liker",
				"password": userPassword,
				"nickname": "Liker",
			}, nil),
			http.StatusCreated, "POST /api/auth/register")

		expectStatus(t,
			visitor.call(http.MethodPost, commentPath+"/like", nil, visitor.unsafe(nil)),
			http.StatusOK, "POST /api/comments/:id/like as a visitor")
		expectStatus(t,
			visitor.call(http.MethodPatch, "/api/me", map[string]string{"nickname": "Liker Renamed"}, visitor.unsafe(nil)),
			http.StatusOK, "PATCH /api/me as a visitor")
	})
}

// TestAdminRenameKeepsTheOldNameFree covers the rename path now that the
// administrator name lives in the same unique index as visitor usernames.
//
// 这一次不需要任何预留名单：改名之后旧名字自然被释放，新名字自然被占用，
// 全部由 idx_users_username 一个索引决定。旧版要靠 settings.json 里的字符串
// 加一套「预留判定」来模拟这件事，而那份名单必须与数据库保持同步——那正是
// 这次重构要消灭的推理负担。
//
// 反证方式：删掉 idx_users_username（只剩单管理员索引）→ 「访客可以注册旧名字
// admin」这一条会在管理员仍然叫 admin 时也通过，测试失败。
func TestAdminRenameKeepsTheOldNameFree(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)

	// 改名 admin → root（同时改展示名）。
	rename := admin.call(http.MethodPut, "/api/admin/profile", map[string]string{
		"name":     "站长",
		"username": "root",
	}, admin.unsafe(nil))
	expectStatus(t, rename, http.StatusOK, "PUT /api/admin/profile")

	var me meResponse
	decodeInto(t, rename, &me)
	if me.Username != "root" || me.Name != "站长" {
		t.Fatalf("the rename did not take effect: %s", truncate(rename.body))
	}

	// 新名字能登录。
	expectStatus(t,
		newAPI(t, server.URL).call(http.MethodPost, "/api/auth/login", map[string]string{"username": "root", "password": adminPassword}, nil),
		http.StatusOK, "POST /api/auth/login as root")

	// 旧名字不能再登录。注意登录限流是 3/min，所以这里的调用次数要克制。
	expectStatus(t,
		newAPI(t, server.URL).call(http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": adminPassword}, nil),
		http.StatusUnauthorized, "POST /api/auth/login with the vacated name")

	// 旧名字已经被释放：访客现在可以注册它。
	seedVerificationCode(t, db, "recycler@example.com", seededCode)
	recycler := newAPI(t, server.URL)
	expectStatus(t,
		recycler.call(http.MethodPost, "/api/auth/register", map[string]string{
			"email":    "recycler@example.com",
			"code":     seededCode,
			"username": "admin",
			"password": userPassword,
			"nickname": "Recycler",
		}, nil),
		http.StatusCreated, "POST /api/auth/register with the vacated administrator name")

	// 反过来：改名撞上访客已有的用户名 → 409，而且名字不变。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/admin/profile", map[string]string{
			"name":     "站长",
			"username": "admin",
		}, admin.unsafe(nil)),
		http.StatusConflict, "PUT /api/admin/profile onto a visitor's username")

	current := admin.call(http.MethodGet, "/api/me", nil, nil)
	var decoded meResponse
	decodeInto(t, current, &decoded)
	if decoded.Username != "root" {
		t.Fatalf("a refused rename moved the administrator login name to %q", decoded.Username)
	}
}

// TestAdminDisplayNameFollowsTheDatabaseRow covers §18 开放问题 1: the display
// name used for comment attribution must come from the administrator row, not
// from a cached copy.
//
// 反证方式：让 AuthHandler.AdminName 返回缓存/常量 → 改名后新评论仍署旧名，失败。
func TestAdminDisplayNameFollowsTheDatabaseRow(t *testing.T) {
	server, _, _ := newTestServer(t)
	admin := installFreshBlog(t, server)

	// 评论目标必须真实存在，理由同 TestAdminCannotUseVisitorOnlyEndpoints。
	targetID := createCommentTargetArticle(t, admin)

	// 改展示名。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/admin/profile", map[string]string{"name": "夜行者"}, admin.unsafe(nil)),
		http.StatusOK, "PUT /api/admin/profile")

	// 新评论的署名必须立刻跟着变——评论写入频率很低，每次查一次库是可接受的代价，
	// 换来的是「改了昵称前台没变」这类 bug 不会出现。
	res := admin.call(http.MethodPost, "/api/comments", map[string]any{
		"target_type": "article",
		"target_id":   targetID,
		"content":     "signed with the new display name",
	}, admin.unsafe(nil))
	expectStatus(t, res, http.StatusCreated, "POST /api/comments")

	var comment struct {
		Author struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"author"`
	}
	decodeInto(t, res, &comment)
	if comment.Author.Name != "夜行者" {
		t.Fatalf("the comment was signed %q, want the database display name 夜行者 (body: %s)",
			comment.Author.Name, truncate(res.body))
	}
	// 署名类型仍然是管理员：改名不该把作者类型也改掉。
	if comment.Author.Type != "admin" {
		t.Fatalf("the comment author type = %q, want admin", comment.Author.Type)
	}
}
