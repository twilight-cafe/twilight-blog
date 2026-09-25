package view_test

import (
	"bytes"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"blog-engine/internal/config"
)

// 这个文件是媒体端点的端到端守护，重点是第七轮渗透测试 R7-02。
//
// 为什么必须在 HTTP 这一层再测一遍（service 层已经有单测）：storage_name 的来源
// 是**数据库**，而唯一能让攻击者控制数据库内容的路口是 restore。这里直接往
// media 表插一行带 `..` 的记录，走真实的公开路由 GET /api/media/:id，验证它
// 不会把工作目录之外的文件吐出来。
//
// 顺带覆盖管理端的列表 / 删除：媒体端点此前没有任何 HTTP 层用例。

// mediaPage 是本文件关心的分页信封形状（只取断言要用的字段）。
type mediaPage struct {
	Items []struct {
		ID          int64  `json:"id"`
		StorageName string `json:"storage_name"`
		MimeType    string `json:"mime_type"`
	} `json:"items"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// seedMediaRow 直接往库里插一行媒体记录，返回它的 id。
//
// 走 SQL 而不是上传接口：要造的是「上传接口生成不出来的 storage_name」。
func seedMediaRow(t *testing.T, db *sql.DB, storageName string) int64 {
	t.Helper()
	return seedMediaRowWithMime(t, db, storageName, "image", "image/png", 3)
}

// seedMediaRowWithMime 允许测试构造“上传接口生成不出来”的 media_type / mime_type 组合。
func seedMediaRowWithMime(t *testing.T, db *sql.DB, storageName, mediaType, mimeType string, size int64) int64 {
	t.Helper()

	res, err := db.Exec(`
INSERT INTO media (storage_name, original_name, media_type, mime_type, size, converted, created_at)
VALUES (?, ?, ?, ?, ?, 0, 1);
`, storageName, filepath.Base(storageName), mediaType, mimeType, size)
	if err != nil {
		t.Fatalf("seed media row %q: %v", storageName, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("read the seeded media id: %v", err)
	}
	return id
}

// TestMediaPublicReadRefusesATraversalStorageName 是 R7-02 的端到端守护。
//
// 反证方式：把 MediaService.Path 里的校验删掉 → 匿名 GET 会拿到 200 与文件内容。
func TestMediaPublicReadRefusesATraversalStorageName(t *testing.T) {
	mediaDir := filepath.Join(t.TempDir(), "media")
	server, db, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.MediaDir = mediaDir
	})
	installFreshBlog(t, server)

	// 媒体目录的**父目录**里放一份「机密」，记录用 `..` 指过去。
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		t.Fatalf("create the media directory: %v", err)
	}
	secret := filepath.Join(filepath.Dir(mediaDir), "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-CONTENT"), 0o600); err != nil {
		t.Fatalf("write the file outside the media directory: %v", err)
	}

	id := seedMediaRow(t, db, filepath.Join("..", "secret.txt"))

	res := newAPI(t, server.URL).call(http.MethodGet, "/api/media/"+strconv.FormatInt(id, 10), nil, nil)
	if res.status == http.StatusOK {
		t.Fatalf("anonymous GET of a traversal storage_name returned 200 with %d bytes", len(res.body))
	}
	if bytes.Contains(res.body, []byte("TOP-SECRET-CONTENT")) {
		t.Fatal("the response leaked the file outside the media directory")
	}
	// 500 而不是 404：这是库里的坏数据，静默 404 会让它永远不为人知。
	expectStatus(t, res, http.StatusInternalServerError, "GET /api/media/:id with a traversal storage_name")
}

// TestMediaPublicReadRefusesAScriptableMimeType 是本轮 R8-03 的端到端守护。
//
// 恶意归档可以还原一行 media_type=image、mime_type=text/html 的记录，并把文件内容
// 写成 <script src="/api/media/2">。如果公开读接口直接回放数据库里的 MIME，就会在
// 站点同源上下文里交付 HTML/JS，形成存储型 XSS。这里验证坏记录返回 500，且响应体
// 不包含恶意标记。
func TestMediaPublicReadRefusesAScriptableMimeType(t *testing.T) {
	mediaDir := filepath.Join(t.TempDir(), "media")
	server, db, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.MediaDir = mediaDir
	})
	installFreshBlog(t, server)

	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		t.Fatalf("create the media directory: %v", err)
	}
	payload := []byte(`<script src="/api/media/2"></script>`)
	if err := os.WriteFile(filepath.Join(mediaDir, "xss.html"), payload, 0o600); err != nil {
		t.Fatalf("write the malicious media file: %v", err)
	}
	id := seedMediaRowWithMime(t, db, "xss.html", "image", "text/html", int64(len(payload)))

	res := newAPI(t, server.URL).call(http.MethodGet, "/api/media/"+strconv.FormatInt(id, 10), nil, nil)
	expectStatus(t, res, http.StatusInternalServerError, "GET /api/media/:id with a scriptable mime_type")
	if bytes.Contains(res.body, []byte("<script")) {
		t.Fatal("the response leaked the malicious HTML body")
	}
}

// TestMediaAdminDeleteRefusesToTouchAPathOutsideTheMediaDir 是 R7-02 在删除路径上的
// 端到端守护：管理员删掉那条坏记录是允许的，但绝不能顺着它删掉目录外的文件。
func TestMediaAdminDeleteRefusesToTouchAPathOutsideTheMediaDir(t *testing.T) {
	mediaDir := filepath.Join(t.TempDir(), "media")
	server, db, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.MediaDir = mediaDir
	})
	admin := installFreshBlog(t, server)

	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		t.Fatalf("create the media directory: %v", err)
	}
	secret := filepath.Join(filepath.Dir(mediaDir), "keep-me.txt")
	if err := os.WriteFile(secret, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write the file outside the media directory: %v", err)
	}

	id := seedMediaRow(t, db, filepath.Join("..", "keep-me.txt"))

	res := admin.call(http.MethodDelete, "/api/admin/media/"+strconv.FormatInt(id, 10), nil, admin.unsafe(nil))
	expectStatus(t, res, http.StatusNoContent, "DELETE /api/admin/media/:id with a traversal storage_name")

	if body, err := os.ReadFile(secret); err != nil || string(body) != "keep me" {
		t.Fatalf("the file outside the media directory was touched: body=%q err=%v", body, err)
	}
}

// TestMediaEndpointRoundTrip 覆盖媒体端点正常的读、列、删，以及几条错误映射。
//
// 媒体是唯一没有回收站的资源（记录与文件一起消失），所以「删完再读」必须变成 404，
// 这条断言同时证明删除真的落到了磁盘。
func TestMediaEndpointRoundTrip(t *testing.T) {
	server, mediaDir := newCoverServerWithDir(t)
	admin := installFreshBlog(t, server)

	id := uploadMedia(t, admin, "photo.png", pngBytes(t))
	path := "/api/media/" + strconv.FormatInt(id, 10)

	t.Run("an uploaded image is readable by anyone", func(t *testing.T) {
		res := newAPI(t, server.URL).call(http.MethodGet, path, nil, nil)
		expectStatus(t, res, http.StatusOK, "GET "+path)
		if got := res.header.Get("Content-Type"); got != "image/webp" {
			t.Fatalf("content type = %q, want image/webp", got)
		}
		if got := res.header.Get("Cache-Control"); got != "public, max-age=86400" {
			t.Fatalf("cache control = %q, want a long public cache", got)
		}
	})

	t.Run("the admin listing reports it", func(t *testing.T) {
		res := admin.call(http.MethodGet, "/api/admin/media?page=1&page_size=10", nil, nil)
		expectStatus(t, res, http.StatusOK, "GET /api/admin/media")
		var page mediaPage
		decodeInto(t, res, &page)
		if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != id {
			t.Fatalf("media page = %+v, want exactly the uploaded item", page)
		}
	})

	t.Run("invalid pagination is a 400", func(t *testing.T) {
		res := admin.call(http.MethodGet, "/api/admin/media?page=0&page_size=10", nil, nil)
		expectStatus(t, res, http.StatusBadRequest, "GET /api/admin/media with page=0")
	})

	t.Run("a non-numeric id is a 400", func(t *testing.T) {
		res := admin.call(http.MethodGet, "/api/media/not-a-number", nil, nil)
		expectStatus(t, res, http.StatusBadRequest, "GET /api/media/not-a-number")
	})

	t.Run("an unknown id is a 404", func(t *testing.T) {
		res := admin.call(http.MethodGet, "/api/media/999999", nil, nil)
		expectStatus(t, res, http.StatusNotFound, "GET an unknown media id")
	})

	t.Run("a record whose file vanished is a 404", func(t *testing.T) {
		names := mediaFiles(t, mediaDir)
		if len(names) != 1 {
			t.Fatalf("media directory holds %v, want exactly one file", names)
		}
		if err := os.Remove(filepath.Join(mediaDir, names[0])); err != nil {
			t.Fatalf("remove the stored file: %v", err)
		}
		res := admin.call(http.MethodGet, path, nil, nil)
		expectStatus(t, res, http.StatusNotFound, "GET a media record whose file is gone")
	})

	t.Run("deleting it removes record and response", func(t *testing.T) {
		res := admin.call(http.MethodDelete, "/api/admin/media/"+strconv.FormatInt(id, 10), nil, admin.unsafe(nil))
		expectStatus(t, res, http.StatusNoContent, "DELETE "+path)

		res = admin.call(http.MethodGet, path, nil, nil)
		expectStatus(t, res, http.StatusNotFound, "GET after DELETE")
	})

	t.Run("deleting an unknown id is a 404", func(t *testing.T) {
		res := admin.call(http.MethodDelete, "/api/admin/media/999999", nil, admin.unsafe(nil))
		expectStatus(t, res, http.StatusNotFound, "DELETE an unknown media id")
	})

	t.Run("a non-numeric delete id is a 400", func(t *testing.T) {
		res := admin.call(http.MethodDelete, "/api/admin/media/not-a-number", nil, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "DELETE /api/admin/media/not-a-number")
	})
}
