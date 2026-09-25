package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blog-engine/internal/model"
	"blog-engine/internal/testtool"
)

// 这个文件是第七轮渗透测试 R7-02 的守护测试。
//
// 背景：media.storage_name 由服务端生成，**通过 HTTP 注入不了**；但 restore 会把
// 归档里的 blog.db 整体换上，而 backup 包只校验归档的文件路径、不校验数据库内容。
// 于是一份恶意归档就能塞进一行 `storage_name='../../../../etc/passwd'`，让公开的
// GET /api/media/:id 变成任意文件读取，也让 DELETE /api/admin/media/:id 变成任意
// 文件删除。
//
// 单测打在 Path / Delete / UpgradeToWebP 三个真正碰文件系统的入口上：handler 只是
// 把它们的结果映射成 HTTP 状态码。

// TestPathRejectsUnsafeStorageNames 钉住「拼路径之前先判定它是不是一个普通文件名」。
func TestPathRejectsUnsafeStorageNames(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	sep := string(filepath.Separator)
	unsafe := map[string]string{
		"empty":           "",
		"blank":           "   ",
		"dot":             ".",
		"parent":          "..",
		"parent relative": ".." + sep + "secret.txt",
		"deep escape":     ".." + sep + ".." + sep + ".." + sep + ".." + sep + "etc" + sep + "passwd",
		"absolute":        filepath.Join(sep, "etc", "passwd"),
		"subdirectory":    "sub" + sep + "file.png",
		// 刻意手工拼接：filepath.Join 会把它 Clean 成 "b"，那样测的就不是这一条了。
		"internal dotdot": "a" + sep + ".." + sep + "b",
		"trailing slash":  "file.png" + sep,
	}
	for label, name := range unsafe {
		t.Run(label, func(t *testing.T) {
			media := &model.Media{StorageName: name}
			if _, err := rig.svc.Path(media); !errors.Is(err, ErrInvalidStorageName) {
				t.Fatalf("Path(%q) error = %v, want ErrInvalidStorageName", name, err)
			}
		})
	}

	if _, err := rig.svc.Path(nil); !errors.Is(err, ErrInvalidStorageName) {
		t.Fatalf("Path(nil) error = %v, want ErrInvalidStorageName", err)
	}
}

// TestContentTypeOnlyAllowsDisplayableMedia 钉住公开读接口的 MIME allowlist。
//
// 恶意归档可以把一行 media 的 mime_type 改成 text/html 或 application/javascript。
// handler 若原样回放，就会给出同源页面/脚本，形成存储型 XSS。因此 ContentType
// 只接受图片 allowlist、audio/ 与 video/ 前缀，其余一律拒绝。
func TestContentTypeOnlyAllowsDisplayableMedia(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	safe := map[string]model.Media{
		"image png":  {ID: 1, MediaType: model.MediaTypeImage, MimeType: "image/png"},
		"image webp": {ID: 2, MediaType: model.MediaTypeImage, MimeType: " IMAGE/WEBP "},
		"audio":      {ID: 3, MediaType: "audio", MimeType: "audio/mpeg"},
		"video":      {ID: 4, MediaType: "video", MimeType: "video/mp4"},
	}
	for name, media := range safe {
		t.Run(name, func(t *testing.T) {
			got, err := rig.svc.ContentType(&media)
			if err != nil {
				t.Fatalf("ContentType() error = %v", err)
			}
			if got != strings.ToLower(strings.TrimSpace(media.MimeType)) {
				t.Fatalf("ContentType() = %q, want normalized MIME", got)
			}
		})
	}

	unsafe := map[string]model.Media{
		"nil":                        {},
		"html":                       {ID: 5, MediaType: model.MediaTypeImage, MimeType: "text/html"},
		"javascript":                 {ID: 6, MediaType: model.MediaTypeImage, MimeType: "application/javascript"},
		"svg":                        {ID: 7, MediaType: model.MediaTypeImage, MimeType: "image/svg+xml"},
		"audio with scriptable mime": {ID: 8, MediaType: "audio", MimeType: "text/html"},
		"video with scriptable mime": {ID: 9, MediaType: "video", MimeType: "application/javascript"},
		"unknown media type":         {ID: 10, MediaType: "document", MimeType: "text/plain"},
	}
	for name, media := range unsafe {
		t.Run(name, func(t *testing.T) {
			if _, err := rig.svc.ContentType(&media); !errors.Is(err, ErrInvalidMediaMimeType) {
				t.Fatalf("ContentType() error = %v, want ErrInvalidMediaMimeType", err)
			}
		})
	}

	if _, err := rig.svc.ContentType(nil); !errors.Is(err, ErrInvalidMediaMimeType) {
		t.Fatalf("ContentType(nil) error = %v, want ErrInvalidMediaMimeType", err)
	}
}

// TestPathAcceptsAPlainFileName 保证校验没有把正常名字一起挡掉。
func TestPathAcceptsAPlainFileName(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	media := &model.Media{StorageName: "0f1e2d3c4b5a69788796a5b4.webp"}

	path, err := rig.svc.Path(media)
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	if want := filepath.Join(rig.dir, media.StorageName); path != want {
		t.Fatalf("Path() = %q, want %q", path, want)
	}
}

// TestPathRejectsASymlinkedFile 覆盖纵深防御那一层：即便名字是一个合法的普通文件名，
// 只要它在磁盘上是一条指向别处的符号链接，也不能把它当成媒体文件读写。
//
// 解包端（internal/backup）本来就拒绝符号链接，所以这条路要先有人手工往 mediaDir 里
// 放链接才走得到——这正是它值得单独钉住的原因：它防的是「本机被动了手脚」。
func TestPathRejectsASymlinkedFile(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	if err := os.MkdirAll(rig.dir, 0o700); err != nil {
		t.Fatalf("create the media directory: %v", err)
	}

	target := filepath.Join(filepath.Dir(rig.dir), "outside.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write the link target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(rig.dir, "link.png")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	if _, err := rig.svc.Path(&model.Media{StorageName: "link.png"}); !errors.Is(err, ErrInvalidStorageName) {
		t.Fatalf("Path(link.png) error = %v, want ErrInvalidStorageName", err)
	}
}

// TestDeleteRefusesToTouchAPathOutsideTheMediaDir 是 R7-02 在删除路径上的守护：
// 一行被还原进来的坏记录可以（也应该）被删掉，但它的 storage_name 绝不能拿去
// 碰文件系统——否则管理员点一次删除就等于删掉任意一个文件。
func TestDeleteRefusesToTouchAPathOutsideTheMediaDir(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	secret := filepath.Join(filepath.Dir(rig.dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("do not delete me"), 0o600); err != nil {
		t.Fatalf("write the file the record points at: %v", err)
	}

	poisoned := &model.Media{
		StorageName:  filepath.Join("..", "secret.txt"),
		OriginalName: "passwd",
		MediaType:    model.MediaTypeImage,
		MimeType:     "image/png",
		Size:         3,
		CreatedAt:    1,
	}
	if err := rig.repo.Create(context.Background(), poisoned); err != nil {
		t.Fatalf("seed the poisoned record: %v", err)
	}

	if err := rig.svc.Delete(context.Background(), poisoned.ID); err != nil {
		t.Fatalf("Delete() error = %v; a record with a bad storage name must still be removable", err)
	}

	// 记录必须消失——否则这条坏数据永远留在库里。
	if _, err := rig.repo.GetByID(context.Background(), poisoned.ID); !errors.Is(err, model.ErrMediaNotFound) {
		t.Fatalf("the poisoned record is still there: %v", err)
	}
	// 而它指向的那个文件必须原封不动。
	if body, err := os.ReadFile(secret); err != nil || string(body) != "do not delete me" {
		t.Fatalf("the file outside the media directory was touched: body=%q err=%v", body, err)
	}
}

// TestUpgradeToWebPRefusesAnUnsafeStorageName 覆盖转码入口：它同样会读取磁盘文件，
// 所以同一套校验必须在它之前生效。
func TestUpgradeToWebPRefusesAnUnsafeStorageName(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	poisoned := &model.Media{
		ID:          1,
		StorageName: filepath.Join("..", "..", "etc", "passwd"),
		MediaType:   model.MediaTypeImage,
		MimeType:    "image/png",
	}

	if _, err := rig.svc.UpgradeToWebP(context.Background(), poisoned); !errors.Is(err, ErrInvalidStorageName) {
		t.Fatalf("UpgradeToWebP() error = %v, want ErrInvalidStorageName", err)
	}
}
