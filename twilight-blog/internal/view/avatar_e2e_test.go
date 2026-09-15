package view_test

import (
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"blog-engine/internal/config"
)

// 这个文件是「站长头像」功能在 HTTP 层的端到端守护，与 article_cover_e2e_test.go
// 是同一思路的姊妹篇：头像与头图一样要穿过 viewmodel 的三态解析（OptionalID）、
// handler 的字段拆分、service 的可用性校验（媒体必须存在且必须是图片），最后落进
// users.avatar_media_id 这条带外键的列。单独测任何一层都看不出
// 「前端传了 null，后端当成没传」这类三层透传问题。
//
// 与头图的关键差异：头图挂在文章上（作者拥有很多篇文章），头像挂在唯一的站长
// 身份上——所以这里的公开读取口是 GET /api/site 而不是文章接口，且「媒体被删」
// 这条路不需要在这里测：schema 用 ON DELETE SET NULL 兜底，service 侧
// usableAvatarID 又会在写入前拦截，各自已有测试。
//
// 上传媒体会把文件写进 cfg.MediaDir，因此 server 必须把 MediaDir 指到临时目录
// （复用 newCoverServer 的原因），否则测试会往仓库里写文件。

// siteResponse 是本文件关心的 /api/site 形状。AvatarURL 不用指针：
// 该字段恒输出（不带 omitempty），空串就是「没设头像」的合法取值。
type siteResponse struct {
	Description string `json:"description"`
	AvatarURL   string `json:"avatar_url"`
}

// getSite 读公开的站点设置。断言的是**读者能看到的那一份**，而不是写请求的响应。
func getSite(t *testing.T, admin *api) siteResponse {
	t.Helper()

	res := admin.call(http.MethodGet, "/api/site", nil, nil)
	expectStatus(t, res, http.StatusOK, "GET /api/site")

	var site siteResponse
	decodeInto(t, res, &site)
	return site
}

// updateProfile 走真实的 PUT /api/admin/profile，返回原始响应供各用例断言状态码。
func updateProfile(t *testing.T, admin *api, body map[string]any) response {
	t.Helper()

	res := admin.call(http.MethodPut, "/api/admin/profile", body, admin.unsafe(nil))
	expectStatus(t, res, http.StatusOK, "PUT /api/admin/profile")
	return res
}

// TestAdminAvatarEndToEnd 覆盖头像在管理端与公开端之间的完整往返。
func TestAdminAvatarEndToEnd(t *testing.T) {
	server := newCoverServer(t)
	admin := installFreshBlog(t, server)

	avatar := uploadMedia(t, admin, "avatar.png", pngBytes(t))
	replacement := uploadMedia(t, admin, "replacement.png", pngBytes(t))

	t.Run("a fresh site has no avatar", func(t *testing.T) {
		if site := getSite(t, admin); site.AvatarURL != "" {
			t.Fatalf("avatar_url on a fresh site = %q, want it empty", site.AvatarURL)
		}
	})

	t.Run("setting an avatar publishes it on the public endpoint", func(t *testing.T) {
		updateProfile(t, admin, map[string]any{"name": "站长", "avatar_media_id": avatar})

		if site := getSite(t, admin); site.AvatarURL != "/api/media/"+strconv.FormatInt(avatar, 10) {
			t.Fatalf("avatar_url = %q, want /api/media/%d", site.AvatarURL, avatar)
		}
		// 头像必须是媒体库里的真实一条，公开 URL 打得开。
		expectStatus(t, admin.call(http.MethodGet, "/api/media/"+strconv.FormatInt(avatar, 10), nil, nil),
			http.StatusOK, "GET /api/media/:id of the avatar")
	})

	t.Run("a name-only update leaves the avatar alone", func(t *testing.T) {
		updateProfile(t, admin, map[string]any{"name": "改名不改头像"})

		if site := getSite(t, admin); site.AvatarURL != "/api/media/"+strconv.FormatInt(avatar, 10) {
			t.Fatalf("avatar_url = %q after a name-only PUT; the field must be optional, not absent", site.AvatarURL)
		}
	})

	t.Run("an explicit null clears the avatar but keeps the media", func(t *testing.T) {
		updateProfile(t, admin, map[string]any{"name": "站长", "avatar_media_id": nil})

		if site := getSite(t, admin); site.AvatarURL != "" {
			t.Fatalf("avatar_url = %q after an explicit null, want it cleared", site.AvatarURL)
		}
		// 摘掉引用不等于删媒体：这张图必须还在库里（用户可能只是反悔）。
		expectStatus(t, admin.call(http.MethodGet, "/api/media/"+strconv.FormatInt(avatar, 10), nil, nil),
			http.StatusOK, "GET /api/media/:id after clearing the avatar")
	})

	t.Run("a provided id replaces the avatar", func(t *testing.T) {
		updateProfile(t, admin, map[string]any{"name": "站长", "avatar_media_id": replacement})

		if site := getSite(t, admin); site.AvatarURL != "/api/media/"+strconv.FormatInt(replacement, 10) {
			t.Fatalf("avatar_url = %q, want the replacement /api/media/%d", site.AvatarURL, replacement)
		}
	})
}

// TestAdminAvatarValidationOverHTTP 覆盖 handler 的错误映射与「先校验后写入」的
// 顺序：头像校验失败时，同一个请求里的显示名也**不能**被写进去——否则一次被拒的
// 换图会顺手把名字改掉，用户看到的是两条结果混在一起的一次提交。
//
// 非图片那条直接往库里的 media 表插一行 video（与头图的测试同理：上传接口造不出
// 真实可嗅探的音视频，而这里要验的是 service 会拒绝非图片）。
func TestAdminAvatarValidationOverHTTP(t *testing.T) {
	server, db, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.MediaDir = filepath.Join(t.TempDir(), "media")
	})
	admin := installFreshBlog(t, server)

	if _, err := db.Exec(`
INSERT INTO media (id, storage_name, original_name, media_type, mime_type, size, created_at)
VALUES (77, 'clip.mp4', 'clip.mp4', 'video', 'video/mp4', 10, 1);
`); err != nil {
		t.Fatalf("seed a non-image media row: %v", err)
	}

	t.Run("an unknown avatar is refused and nothing is written", func(t *testing.T) {
		res := admin.call(http.MethodPut, "/api/admin/profile",
			map[string]any{"name": "不该生效的名字", "avatar_media_id": 999999}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "PUT with an avatar that does not exist")

		// 先校验后写入：名字也不该变。
		var me struct {
			Name string `json:"name"`
		}
		decodeInto(t, admin.call(http.MethodGet, "/api/me", nil, nil), &me)
		if me.Name == "不该生效的名字" {
			t.Fatal("a refused avatar update still wrote the display name")
		}
		if site := getSite(t, admin); site.AvatarURL != "" {
			t.Fatalf("avatar_url = %v after a refused update, want it untouched", site.AvatarURL)
		}
	})

	t.Run("a non-image avatar is refused", func(t *testing.T) {
		res := admin.call(http.MethodPut, "/api/admin/profile",
			map[string]any{"name": "站长", "avatar_media_id": 77}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "PUT with a video as the avatar")
	})

	t.Run("a zero avatar is refused", func(t *testing.T) {
		res := admin.call(http.MethodPut, "/api/admin/profile",
			map[string]any{"name": "站长", "avatar_media_id": 0}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "PUT with avatar_media_id = 0")
	})

	t.Run("only admins can set an avatar", func(t *testing.T) {
		// 注册要走真实的邮箱验证码（见 auth_e2e_test.go 的注册用例），
		// 这里照搬同一套播种方式造一个访客会话。
		seedVerificationCode(t, db, "avatar-visitor@example.com", seededCode)
		visitor := newAPI(t, server.URL)
		reg := visitor.call(http.MethodPost, "/api/auth/register", map[string]string{
			"email":    "avatar-visitor@example.com",
			"code":     seededCode,
			"username": "avatarvisitor",
			"password": userPassword,
			"nickname": "路过的",
		}, nil)
		expectStatus(t, reg, http.StatusCreated, "POST /api/auth/register")

		res := visitor.call(http.MethodPut, "/api/admin/profile",
			map[string]any{"name": "冒充站长", "avatar_media_id": nil}, visitor.unsafe(nil))
		expectStatus(t, res, http.StatusForbidden, "PUT /api/admin/profile as a visitor")

		if site := getSite(t, admin); site.AvatarURL != "" {
			t.Fatalf("avatar_url = %q after a visitor's PUT, want it untouched", site.AvatarURL)
		}
	})
}
