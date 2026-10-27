package view_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"blog-engine/internal/config"
	"blog-engine/internal/testtool"
)

// multipartFile 是上传用例里的一件文件。
type multipartFile struct {
	field    string
	filename string
	content  []byte
}

// postMultipart 发一次原始 multipart 请求。字段名显式给出，因为「files」与
// 兼容旧调用的「file」两条分支都要被走到。
func postMultipart(t *testing.T, admin *api, files []multipartFile, textField bool) response {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if textField {
		if err := writer.WriteField("note", "not a file"); err != nil {
			t.Fatalf("write text field: %v", err)
		}
	}
	for _, file := range files {
		part, err := writer.CreateFormFile(file.field, file.filename)
		if err != nil {
			t.Fatalf("create form file %s: %v", file.filename, err)
		}
		if _, err := part.Write(file.content); err != nil {
			t.Fatalf("write form file %s: %v", file.filename, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, admin.base+"/api/admin/media", &buf)
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-CSRF-Token", admin.csrf())
	resp, err := admin.client.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read upload response: %v", err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: body, cookies: resp.Cookies()}
}

// oversizedPNG 造一张只有 IHDR 的 PNG：解码器读到声明的尺寸就足以判定了，
// 而这一点点字节正好说明「按文件大小设限挡不住解压炸弹」。
func oversizedPNG(t *testing.T, width, height uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})

	data := make([]byte, 13)
	binary.BigEndian.PutUint32(data[0:4], width)
	binary.BigEndian.PutUint32(data[4:8], height)
	data[8] = 8  // bit depth
	data[9] = 6  // truecolour with alpha
	data[10] = 0 // compression
	data[11] = 0 // filter
	data[12] = 0 // interlace

	chunk := append([]byte("IHDR"), data...)
	_ = binary.Write(&buf, binary.BigEndian, uint32(13))
	buf.WriteString("IHDR")
	buf.Write(data)
	_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return buf.Bytes()
}

// TestMediaUploadRejectsBadRequests covers the multipart envelope itself: a
// non-multipart POST, a multipart body with no file field at all, and the
// singular "file" alias that OLD clients still send.
func TestMediaUploadRejectsBadRequests(t *testing.T) {
	server, _ := newCoverServerWithDir(t)
	admin := installFreshBlog(t, server)

	t.Run("not multipart", func(t *testing.T) {
		res := admin.call(http.MethodPost, "/api/admin/media", map[string]string{"note": "x"}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "POST /api/admin/media as JSON")
	})

	t.Run("multipart without files", func(t *testing.T) {
		res := postMultipart(t, admin, nil, true)
		expectStatus(t, res, http.StatusBadRequest, "POST /api/admin/media without a file part")
	})

	t.Run("the singular file field still works", func(t *testing.T) {
		res := postMultipart(t, admin, []multipartFile{
			{field: "file", filename: "single.png", content: pngBytes(t)},
		}, false)
		expectStatus(t, res, http.StatusCreated, "POST /api/admin/media with the file field")
	})
}

// TestMediaUploadErrorMapping covers the four distinct answers the service can
// give. Each one tells the client something different: retry won't help (400),
// the image is too big (413), the server is momentarily busy (503), or the
// storage failed (500).
func TestMediaUploadErrorMapping(t *testing.T) {
	t.Run("unsupported type is a 400", func(t *testing.T) {
		server, _ := newCoverServerWithDir(t)
		admin := installFreshBlog(t, server)
		res := postMultipart(t, admin, []multipartFile{
			{field: "files", filename: "notes.txt", content: []byte("plain text")},
		}, false)
		expectStatus(t, res, http.StatusBadRequest, "uploading a text file")
	})

	t.Run("empty file is a 400", func(t *testing.T) {
		server, _ := newCoverServerWithDir(t)
		admin := installFreshBlog(t, server)
		res := postMultipart(t, admin, []multipartFile{
			{field: "files", filename: "empty.png", content: nil},
		}, false)
		expectStatus(t, res, http.StatusBadRequest, "uploading an empty file")
	})

	t.Run("a decompression bomb is a 413", func(t *testing.T) {
		server, _ := newCoverServerWithDir(t)
		admin := installFreshBlog(t, server)
		res := postMultipart(t, admin, []multipartFile{
			{field: "files", filename: "huge.png", content: oversizedPNG(t, 20000, 20000)},
		}, false)
		expectStatus(t, res, http.StatusRequestEntityTooLarge, "uploading an oversized image")
	})

}

// TestMediaUploadRollsBackEarlierFiles covers the compensation path: the first
// file is stored, the second is rejected, and the first must disappear again.
// Without it a failed batch leaves orphaned media that no UI ever shows.
func TestMediaUploadRollsBackEarlierFiles(t *testing.T) {
	server, mediaDir := newCoverServerWithDir(t)
	admin := installFreshBlog(t, server)

	res := postMultipart(t, admin, []multipartFile{
		{field: "files", filename: "good.png", content: pngBytes(t)},
		{field: "files", filename: "bad.txt", content: []byte("not media")},
	}, false)
	expectStatus(t, res, http.StatusBadRequest, "a batch whose second file is unsupported")

	if names := mediaFiles(t, mediaDir); len(names) != 0 {
		t.Fatalf("the rollback left %v in the media directory", names)
	}

	list := admin.call(http.MethodGet, "/api/admin/media?page=1&page_size=12", nil, nil)
	expectStatus(t, list, http.StatusOK, "GET /api/admin/media after a rolled-back batch")
	var page mediaPage
	decodeInto(t, list, &page)
	if page.Total != 0 {
		t.Fatalf("media rows after the rollback = %d, want 0", page.Total)
	}
}

// TestMediaUploadRollbackFailureIsOnlyAWarning pins the compensation's own
// failure mode. When the delete fails there is nothing more the handler can do,
// but it must still answer with the original reason for the batch failure and
// must not pretend the rollback worked.
func TestMediaUploadRollbackFailureIsOnlyAWarning(t *testing.T) {
	mediaDir := filepath.Join(t.TempDir(), "media")
	server, db, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.MediaDir = mediaDir
	})
	admin := installFreshBlog(t, server)
	failDeletes(t, db, "media")

	res := postMultipart(t, admin, []multipartFile{
		{field: "files", filename: "kept.png", content: pngBytes(t)},
		{field: "files", filename: "bad.txt", content: []byte("not media")},
	}, false)
	expectStatus(t, res, http.StatusBadRequest, "a batch whose rollback also fails")

	// 补偿失败时文件留在磁盘上，这是「至少把数据留在可解释的位置」的取舍；
	// 关键是响应仍然是原始错误，而不是变成 500。
	if names := mediaFiles(t, mediaDir); len(names) != 1 {
		t.Fatalf("media directory = %v, want the one file whose compensation failed", names)
	}
}

// TestMediaUploadReportsABusyConverter fills both transcode slots and then asks
// for a third upload. The service intentionally fails fast instead of queueing,
// so the answer is a retryable 503 rather than a hanging connection.
func TestMediaUploadReportsABusyConverter(t *testing.T) {
	markers := t.TempDir()
	t.Setenv(testtool.ModeEnv, testtool.ModeSleep)
	t.Setenv(testtool.MarkerEnv, markers)

	// 直接把 WebPTool 指向假工具：newCoverServerWithDir 会调用 testtool.Fake
	// 把模式改回 ModeOK，那会让进程立刻结束，槽位根本占不住。
	mediaDir := filepath.Join(t.TempDir(), "media")
	server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.MediaDir = mediaDir
		cfg.WebPTool = testtool.Self()
	})
	admin := installFreshBlog(t, server)

	// 前两张图会真的停在子进程里；等到两个标记文件出现，槽位就已经被占满。
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := postMultipart(t, admin, []multipartFile{
				{field: "files", filename: "slow.png", content: pngBytes(t)},
			}, false)
			statuses <- res.status
		}()
	}
	waitForMarkers(t, markers, 2)

	res := postMultipart(t, admin, []multipartFile{
		{field: "files", filename: "third.png", content: pngBytes(t)},
	}, false)
	if res.status != http.StatusServiceUnavailable {
		t.Fatalf("the third upload status = %d, want 503 (body: %s)", res.status, truncate(res.body))
	}

	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusCreated {
			t.Fatalf("one of the two slot holders failed: %d", status)
		}
	}
}

// waitForMarkers waits until the fake converter processes have started, so the
// 503 test does not depend on a sleep long enough to win a race.
func waitForMarkers(t *testing.T, dir string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	entries, _ := os.ReadDir(dir)
	t.Fatalf("only %d of %d converter processes started", len(entries), want)
}

// TestMediaUploadReportsAStorageFailureViaDB pins the write that happens after
// the file is already on disk: the row cannot be inserted, so the file must be
// removed again and the client gets a 500.
func TestMediaUploadReportsAStorageFailureViaDB(t *testing.T) {
	mediaDir := filepath.Join(t.TempDir(), "media")
	server, db, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.MediaDir = mediaDir
	})
	admin := installFreshBlog(t, server)
	failWrites(t, db, "media")

	res := postMultipart(t, admin, []multipartFile{
		{field: "files", filename: "stored.png", content: pngBytes(t)},
	}, false)
	expectStatus(t, res, http.StatusInternalServerError, "an upload whose insert fails")

	// 落盘后的补偿必须把文件删掉：磁盘上不该留下一条没有记录的孤儿。
	if names := mediaFiles(t, mediaDir); len(names) != 0 {
		t.Fatalf("the failed insert left %v behind", names)
	}
}

// TestMediaUploadOversizedPNGIsLegible guards the fixture itself: an
// unrecognised header would be rejected as unsupported instead of 413, and the
// test above would then assert the wrong error for the right reason.
func TestMediaUploadOversizedPNGIsLegible(t *testing.T) {
	header := oversizedPNG(t, 20000, 20000)
	if !bytes.HasPrefix(header, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatal("the fixture is not a PNG")
	}
	if !strings.Contains(string(header[12:16]), "IHDR") {
		t.Fatalf("the fixture has no IHDR chunk: %q", header)
	}
}
