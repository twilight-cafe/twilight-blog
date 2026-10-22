package view_test

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"
)

// mediaPath 拼出公开读接口的路径（测试里到处要用，避免各写一遍）。
func mediaPath(id int64) string {
	return "/api/media/" + strconv.FormatInt(id, 10)
}

// mediaAdminPath 拼出管理端的单条媒体路径（改名与删除都挂在它下面）。
func mediaAdminPath(id int64) string {
	return "/api/admin/media/" + strconv.FormatInt(id, 10)
}

// TestMediaRenameOverHTTP 覆盖 PATCH /api/admin/media/:id 的完整边界：
// 成功改名、后台列表跟着变、公开读取照旧 200，以及四类失败（非法 id、空名、
// 非法字符、未知 id）与两道鉴权闸门（未登录 401、缺 CSRF 403）。
//
// 这里断言的是**响应形状**，也就是前端真正读的那几个键：original_name 用来显示，
// url 用来取图。前端只把 id 与名字交给这个端点，两者若漂移，界面上的名字会变而
// 图片地址跟着坏——正是这条用例要拦住的。
func TestMediaRenameOverHTTP(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	anon := newAPI(t, server.URL)

	mediaID := uploadMedia(t, admin, "sunset.png", pngBytes(t))

	// 名字里带空白与一个「错」的扩展名：服务端裁掉空白，并把后缀换成这条记录
	// 现有展示名的后缀——上传时叫 sunset.png，所以改完仍是 .png，而不是被换成
	// 磁盘上的 .webp（展示名是上传名的留档，真实格式由 mime_type 记录）。
	renamed := admin.call(http.MethodPatch, mediaAdminPath(mediaID),
		map[string]any{"original_name": "  海边日落.jpg  "}, admin.unsafe(nil))
	expectStatus(t, renamed, http.StatusOK, "PATCH /api/admin/media/:id")

	var body struct {
		ID           int64  `json:"id"`
		URL          string `json:"url"`
		OriginalName string `json:"original_name"`
		MimeType     string `json:"mime_type"`
	}
	decodeInto(t, renamed, &body)
	if body.OriginalName != "海边日落.png" {
		t.Fatalf("original_name = %q, want %q (the extension follows the existing display name)",
			body.OriginalName, "海边日落.png")
	}
	if body.ID != mediaID {
		t.Fatalf("id = %d after the rename, want %d", body.ID, mediaID)
	}
	if body.URL != mediaPath(mediaID) {
		t.Fatalf("url = %q after the rename, want %q", body.URL, mediaPath(mediaID))
	}
	if body.MimeType != "image/webp" {
		t.Fatalf("mime_type = %q after the rename, want it untouched", body.MimeType)
	}

	// 后台列表显示新名字（MediaAdmin 的列表读的就是它）。
	var page struct {
		Items []struct {
			ID           int64  `json:"id"`
			OriginalName string `json:"original_name"`
			URL          string `json:"url"`
		} `json:"items"`
	}
	decodeInto(t, admin.call(http.MethodGet, "/api/admin/media?page=1&page_size=12", nil, nil), &page)
	if len(page.Items) != 1 || page.Items[0].OriginalName != "海边日落.png" {
		t.Fatalf("admin media list = %+v, want the new display name", page.Items)
	}

	// 公开读取仍然可用：改名不碰文件，地址也不变。
	expectStatus(t, anon.call(http.MethodGet, mediaPath(mediaID), nil, nil),
		http.StatusOK, "GET /api/media/:id after the rename")

	t.Run("renaming to the very same name is a successful no-op", func(t *testing.T) {
		// 这条路径真实存在：前端不做「名字没变就别发请求」的判断，用户点开弹窗
		// 直接按确定就会走到这里。SQLite 对「值没变的 UPDATE」仍然报告 1 行受影响，
		// 所以它**不能**被当成「这一行不存在」而回 404——那会让用户看到
		// 「媒体不存在」，而图明明还在。
		again := admin.call(http.MethodPatch, mediaAdminPath(mediaID),
			map[string]any{"original_name": "海边日落.png"}, admin.unsafe(nil))
		expectStatus(t, again, http.StatusOK, "PATCH with the same name")

		var same struct {
			OriginalName string `json:"original_name"`
		}
		decodeInto(t, again, &same)
		if same.OriginalName != "海边日落.png" {
			t.Fatalf("original_name = %q after a same-name rename, want it unchanged", same.OriginalName)
		}
	})

	t.Run("a visitor session cannot rename", func(t *testing.T) {
		seedVerificationCode(t, db, "rename-visitor@example.com", seededCode)
		visitor := newAPI(t, server.URL)
		expectStatus(t, visitor.call(http.MethodPost, "/api/auth/register", map[string]string{
			"email": "rename-visitor@example.com", "code": seededCode,
			"username": "renamevisitor", "password": userPassword, "nickname": "Rename Visitor",
		}, nil), http.StatusCreated, "POST /api/auth/register")

		// 403 而不是 401：身份有效，但角色不够（RequireAdmin 挂在 /api/admin 组上）。
		expectStatus(t, visitor.call(http.MethodPatch, mediaAdminPath(mediaID),
			map[string]any{"original_name": "访客改的.png"}, visitor.unsafe(nil)),
			http.StatusForbidden, "PATCH as a visitor")
	})

	t.Run("invalid inputs are refused", func(t *testing.T) {
		expectStatus(t, admin.call(http.MethodPatch, "/api/admin/media/not-a-number",
			map[string]any{"original_name": "x"}, admin.unsafe(nil)),
			http.StatusBadRequest, "PATCH with a non-numeric id")
		// 请求体不是 JSON 对象：c.Bind 直接失败（400 invalid request），
		// 而不是把零值当成「空名字」继续往下走。
		expectStatus(t, admin.call(http.MethodPatch, mediaAdminPath(mediaID),
			"这只是一个字符串", admin.unsafe(nil)),
			http.StatusBadRequest, "PATCH with a body that is not a JSON object")
		expectStatus(t, admin.call(http.MethodPatch, mediaAdminPath(mediaID),
			map[string]any{"original_name": "   "}, admin.unsafe(nil)),
			http.StatusBadRequest, "PATCH with a blank name")
		expectStatus(t, admin.call(http.MethodPatch, mediaAdminPath(mediaID),
			map[string]any{"original_name": "a/b"}, admin.unsafe(nil)),
			http.StatusBadRequest, "PATCH with a path separator in the name")
		expectStatus(t, admin.call(http.MethodPatch, mediaAdminPath(999999),
			map[string]any{"original_name": "不存在.webp"}, admin.unsafe(nil)),
			http.StatusNotFound, "PATCH on an unknown media id")
	})

	t.Run("authentication and CSRF still apply", func(t *testing.T) {
		// 未登录：连会话都没有，RequireSession 先挡下（CSRF 中间件对无会话请求放行）。
		expectStatus(t, anon.call(http.MethodPatch, mediaAdminPath(mediaID),
			map[string]any{"original_name": "匿名改的.webp"}, nil),
			http.StatusUnauthorized, "PATCH without a session")
		// 已登录但没带 CSRF token：403。PATCH 不在任何「安全方法」白名单里，
		// 所以它与其他写端点受同一套保护。
		expectStatus(t, admin.call(http.MethodPatch, mediaAdminPath(mediaID),
			map[string]any{"original_name": "无令牌改的.webp"}, nil),
			http.StatusForbidden, "PATCH without a CSRF token")
	})

	// 两次被拒的改名不能留下痕迹。
	decodeInto(t, admin.call(http.MethodGet, "/api/admin/media?page=1&page_size=12", nil, nil), &page)
	if len(page.Items) != 1 || page.Items[0].OriginalName != "海边日落.png" {
		t.Fatalf("display name = %+v after rejected renames, want it unchanged", page.Items)
	}
}

// TestMediaRenameReportsAServerFailure 覆盖「库坏了」那一支：媒体表被删掉之后，
// 改名必须回答 500，而不是 400/404——后两者会让调用方以为是自己写错了名字或
// 改错了 id，从而永远不去看服务端日志。
func TestMediaRenameReportsAServerFailure(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	mediaID := uploadMedia(t, admin, "sunset.png", pngBytes(t))

	if _, err := db.Exec(`DROP TABLE media`); err != nil {
		t.Fatalf("drop the media table: %v", err)
	}

	expectStatus(t, admin.call(http.MethodPatch, mediaAdminPath(mediaID),
		map[string]any{"original_name": "随便改"}, admin.unsafe(nil)),
		http.StatusInternalServerError, "PATCH when the media table is gone")
}

// TestRenamingAMediaKeepsEveryReferencePointingAtIt 钉住「改名不用改引用」这句话。
//
// 头图（articles.cover_media_id）、微语配图（moment_media.media_id）与站长头像
// （users.avatar_media_id）指向的都是 media.id，而前台取图走 /api/media/<id>；
// 展示名只出现在界面、alt 与灯箱里。所以一次改名之后：
//
//   - 三个引用必须仍然指向同一个 id，前台拿到的图片地址一字不变；
//   - 公开读接口必须仍然 200，且**字节完全相同**（磁盘文件没被重命名或改写）。
//
// 反证方式：把服务层的 Rename 改成「改 storage_name」或「新建一条记录再改引用」，
// 前两条断言会立刻失败——而这两种写法在界面上都看不出来，只有图裂了才知道。
func TestRenamingAMediaKeepsEveryReferencePointingAtIt(t *testing.T) {
	server, _, _ := newTestServer(t)
	admin := installFreshBlog(t, server)

	mediaID := uploadMedia(t, admin, "cover.png", pngBytes(t))

	// ① 文章头图
	created := admin.call(http.MethodPost, "/api/admin/articles", map[string]any{
		"title": "带图的文章", "content": "正文", "cover_media_id": mediaID,
	}, admin.unsafe(nil))
	expectStatus(t, created, http.StatusCreated, "POST /api/admin/articles with a cover")
	var article struct {
		ID int64 `json:"id"`
	}
	decodeInto(t, created, &article)

	// ② 微语配图
	expectStatus(t, admin.call(http.MethodPost, "/api/admin/moments", map[string]any{
		"content": "带图的微语", "media_ids": []int64{mediaID},
	}, admin.unsafe(nil)), http.StatusCreated, "POST /api/admin/moments with media")

	// ③ 站长头像
	expectStatus(t, admin.call(http.MethodPut, "/api/admin/profile", map[string]any{
		"name": "站长", "avatar_media_id": mediaID,
	}, admin.unsafe(nil)), http.StatusOK, "PUT /api/admin/profile with an avatar")

	before := admin.call(http.MethodGet, mediaPath(mediaID), nil, nil)
	expectStatus(t, before, http.StatusOK, "GET /api/media/:id before the rename")

	expectStatus(t, admin.call(http.MethodPatch, mediaAdminPath(mediaID),
		map[string]any{"original_name": "改名之后的图"}, admin.unsafe(nil)),
		http.StatusOK, "PATCH /api/admin/media/:id")

	// ① 头图仍指向同一张图。
	if got := getArticle(t, admin, article.ID).CoverMediaID; got == nil || *got != mediaID {
		t.Fatalf("cover_media_id = %v after the rename, want it to still point at %d", got, mediaID)
	}

	// ② 公开微语列表里的 media_items 仍是同一张图、同一个地址。
	var feed struct {
		Items []struct {
			MediaItems []struct {
				ID  int64  `json:"id"`
				URL string `json:"url"`
			} `json:"media_items"`
		} `json:"items"`
	}
	decodeInto(t, admin.call(http.MethodGet, "/api/moments?page=1&page_size=12", nil, nil), &feed)
	if len(feed.Items) != 1 || len(feed.Items[0].MediaItems) != 1 {
		t.Fatalf("public moments feed = %+v, want one moment with one media item", feed.Items)
	}
	if got := feed.Items[0].MediaItems[0]; got.ID != mediaID || got.URL != mediaPath(mediaID) {
		t.Fatalf("moment media item = %+v after the rename, want id %d at %s", got, mediaID, mediaPath(mediaID))
	}

	// ③ 站长头像地址不变（站点信息是公开读的，前台侧栏作者卡用它）。
	var site struct {
		AvatarURL string `json:"avatar_url"`
	}
	decodeInto(t, admin.call(http.MethodGet, "/api/site", nil, nil), &site)
	if site.AvatarURL != mediaPath(mediaID) {
		t.Fatalf("avatar_url = %q after the rename, want %q", site.AvatarURL, mediaPath(mediaID))
	}

	// 图片本体逐字节相同：改名只动了展示名那一列。
	after := admin.call(http.MethodGet, mediaPath(mediaID), nil, nil)
	expectStatus(t, after, http.StatusOK, "GET /api/media/:id after the rename")
	if !bytes.Equal(before.body, after.body) {
		t.Fatalf("the served bytes changed across the rename: %d bytes before, %d after",
			len(before.body), len(after.body))
	}
}
