package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"mime/multipart"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"blog-engine/internal/database"
	"blog-engine/internal/mediaconv"
	"blog-engine/internal/model"
	"blog-engine/internal/testtool"
)

// TestMain 让本包能把**测试二进制自己**当成外部转码器（见 internal/testtool）。
//
// 上传路径上几乎每一条分支都取决于「转码器成功 / 静默退出 / 失败」，而这些结局在
// 真实机器上不可控——取决于有没有装 cwebp、装的是哪个构建。把测试结果交给宿主机
// 环境，就等于让「转码失败时会不会留下半成品」这条最重要的逻辑永远没人验过。
func TestMain(m *testing.M) { os.Exit(testtool.Main(m)) }

// mediaTestRig 是一个媒体服务装置：真实的 SQLite 仓库 + 临时媒体目录 +
// 由调用方指定的转换器。
type mediaTestRig struct {
	svc *MediaService
	// repo 始终是真实的仓库，哪怕 svc.repo 被换成了会失败的替身——
	// 断言「库里那一行到底变了没有」只能在真库上做。
	repo *model.SQLiteMediaRepository
	dir  string
}

func newMediaTestRig(t *testing.T, conv *mediaconv.Converter) *mediaTestRig {
	t.Helper()

	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "media.db"))
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := model.NewSQLiteMediaRepository(db)
	dir := filepath.Join(root, "media")
	return &mediaTestRig{svc: NewMediaService(repo, dir, conv), repo: repo, dir: dir}
}

// fakeConverter 造一个把转码交给 testtool 的转换器。
//
// kind 显式传入而不是让它从文件名里推：kind 决定命令行形状，而两种形状都该有人守着
// （cwebp 是 `-quiet src -o dst`，ffmpeg 是 `-y -i src -c:v libwebp dst`）。假工具
// 总是写**最后一个**参数，所以同一个替身能同时冒充两者。
func fakeConverter(t *testing.T, mode, kind string) *mediaconv.Converter {
	t.Helper()
	return &mediaconv.Converter{
		WebPEnabled: true,
		WebPTool:    testtool.Fake(t, mode),
		WebPKind:    kind,
	}
}

// readForm 用真实的多部分解析器造一个 FileHeader。
//
// 不走 httptest 再 ParseMultipartForm：那条路会顺带带上路由、CSRF 与 body limit，
// 而这里要验的是服务层对 FileHeader 的处理，不是 HTTP 层。
//
// maxMemory 决定内容留在内存还是落到临时文件。后者只在需要构造「临时文件已经没了」
// 这种处境时才用得上（见 TestStoreFileReportsAnUnopenableUpload）。
func readForm(t *testing.T, name string, body []byte, maxMemory int64) (*multipart.Form, *multipart.FileHeader) {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("create the form file: %v", err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatalf("write the form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close the form writer: %v", err)
	}

	form, err := multipart.NewReader(&buf, writer.Boundary()).ReadForm(maxMemory)
	if err != nil {
		t.Fatalf("read the form: %v", err)
	}
	t.Cleanup(func() { _ = form.RemoveAll() })

	headers := form.File["file"]
	if len(headers) != 1 {
		t.Fatalf("the form carries %d file parts, want 1", len(headers))
	}
	return form, headers[0]
}

// uploadPart 造一个内容留在内存里的上传段。
func uploadPart(t *testing.T, name string, body []byte) *multipart.FileHeader {
	t.Helper()
	_, header := readForm(t, name, body, 1<<20)
	return header
}

// mediaFiles 列出媒体目录里的文件名（目录不存在时返回空）。
//
// 断言要打在磁盘上：「接口返回了错误」证明不了失败之后没留下文件，而这正是这一轮
// 最要紧的性质（不能出现无记录的孤儿，也不能出现记录指不到的产物）。
func mediaFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read the media directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// seedImage 在媒体目录里放一个文件、并写入对应的记录，返回那条记录。
func (rig *mediaTestRig) seedImage(t *testing.T, name string, body []byte, mimeType string) *model.Media {
	t.Helper()

	if err := os.MkdirAll(rig.dir, 0o700); err != nil {
		t.Fatalf("create the media directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rig.dir, name), body, 0o600); err != nil {
		t.Fatalf("seed the media file: %v", err)
	}

	media := &model.Media{
		StorageName:  name,
		OriginalName: name,
		MediaType:    model.MediaTypeImage,
		MimeType:     mimeType,
		Size:         int64(len(body)),
		Converted:    mimeType == "image/webp",
		CreatedAt:    1,
	}
	if err := rig.repo.Create(context.Background(), media); err != nil {
		t.Fatalf("seed the media record: %v", err)
	}
	return media
}

// failingMediaRepo 让指定的一个方法失败，其余照常走真实仓库。
//
// 用嵌入而不是把整个接口重写一遍：要验的是「这一处失败之后调用方做了什么」，
// 而该接口的其余方法在这里只是通道。
type failingMediaRepo struct {
	model.MediaRepository
	createErr error
	updateErr error
}

func (r failingMediaRepo) Create(ctx context.Context, media *model.Media) error {
	if r.createErr != nil {
		return r.createErr
	}
	return r.MediaRepository.Create(ctx, media)
}

func (r failingMediaRepo) UpdateStorage(ctx context.Context, id int64, storageName, mimeType string, size int64, converted bool) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	return r.MediaRepository.UpdateStorage(ctx, id, storageName, mimeType, size, converted)
}

// brokenReader 在任何一次读取上都失败，用来模拟上传流中途坏掉。
type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("the upload stream is broken") }

// breakEntropy 让 crypto/rand 失败。
//
// rand.Reader 是标准库导出的变量，替换它是官方支持的注入点——不需要在生产代码里
// 为「随机数取不到」留一个缝。它守护的是：storageName 生成失败时，不能带着一个
// 空名字继续走到落盘。
func breakEntropy(t *testing.T) {
	t.Helper()
	previous := rand.Reader
	rand.Reader = brokenReader{}
	t.Cleanup(func() { rand.Reader = previous })
}

// blockTempDir 让 os.MkdirTemp("", …) 失败：把临时目录指向一个**文件**。
//
// 三个变量都要设：Windows 读 TMP 再读 TEMP，其余平台读 TMPDIR。
func blockTempDir(t *testing.T) {
	t.Helper()

	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("create the file that blocks the temp directory: %v", err)
	}
	t.Setenv("TMPDIR", file)
	t.Setenv("TMP", file)
	t.Setenv("TEMP", file)
}

// mp3Bytes 是一个能被内容嗅探认成 audio/mpeg 的最小载荷（ID3 头）。
func mp3Bytes() []byte { return append([]byte("ID3"), bytes.Repeat([]byte{0}, 64)...) }

// mp4Bytes 是一个带 ftyp box 的 mp4 头，嗅探器据此判出 video/mp4。
func mp4Bytes() []byte { return []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom") }

// webpBytes 是一个带 RIFF….WEBP 签名的载荷（嗅探器据此判出 image/webp）。
func webpBytes() []byte {
	return append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0}, 48)...)
}

// TestStoreFileConvertsImagesToWebP 是新上传路径的核心用例。
//
// 它一次性钉住四件事：图片被真的交给了转码器、落盘的是**转码产物**而不是原文件、
// 记录里写的是 WebP 与 converted=1、以及原始文件名只作为留档进库。
func TestStoreFileConvertsImagesToWebP(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	media, err := rig.svc.StoreFile(context.Background(), uploadPart(t, "holiday.png", craftPNG(1200, 800)))
	if err != nil {
		t.Fatalf("StoreFile() error = %v", err)
	}

	if media.MimeType != "image/webp" {
		t.Errorf("mime type = %q, want image/webp", media.MimeType)
	}
	if media.MediaType != "image" {
		t.Errorf("media type = %q, want image", media.MediaType)
	}
	if !media.Converted {
		t.Error("converted = false; the file went through an encoder and the record must say so")
	}
	if !strings.HasSuffix(media.StorageName, ".webp") {
		t.Errorf("storage name = %q, want a .webp suffix", media.StorageName)
	}
	if media.OriginalName != "holiday.png" {
		t.Errorf("original name = %q, want holiday.png", media.OriginalName)
	}
	if media.StorageName == media.OriginalName {
		t.Error("the storage name is the uploader's file name; it must be generated instead")
	}

	stored, err := os.ReadFile(filepath.Join(rig.dir, media.StorageName))
	if err != nil {
		t.Fatalf("read the stored file: %v", err)
	}
	if !bytes.Equal(stored, testtool.Payload()) {
		t.Errorf("stored %q; the encoder's output should have been written verbatim", stored)
	}
	if media.Size != int64(len(testtool.Payload())) {
		t.Errorf("size = %d, want %d", media.Size, len(testtool.Payload()))
	}

	fromDB, err := rig.repo.GetByID(context.Background(), media.ID)
	if err != nil {
		t.Fatalf("read the record back: %v", err)
	}
	if *fromDB != *media {
		t.Errorf("the record in the database is %+v, want %+v", *fromDB, *media)
	}
}

// TestStoreFileRecordsAlreadyWebPImagesWithoutReencoding 钉住那个跳过分支：
// 已经是 WebP 的图不该再过一遍编码器（重编码只会掉画质，且浪费一次解码预算）。
func TestStoreFileRecordsAlreadyWebPImagesWithoutReencoding(t *testing.T) {
	// 编码器故意配成失败：一旦代码走了转码那条路，这条用例就会以错误告终。
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeFail, "cwebp"))

	body := webpBytes()
	media, err := rig.svc.StoreFile(context.Background(), uploadPart(t, "already.webp", body))
	if err != nil {
		t.Fatalf("StoreFile() error = %v", err)
	}

	if media.MimeType != "image/webp" {
		t.Errorf("mime type = %q, want image/webp", media.MimeType)
	}
	if media.Converted {
		t.Error("converted = true; an image that was already WebP must not be re-encoded")
	}
	if !strings.HasSuffix(media.StorageName, ".webp") {
		t.Errorf("storage name = %q, want a .webp suffix", media.StorageName)
	}

	stored, err := os.ReadFile(filepath.Join(rig.dir, media.StorageName))
	if err != nil {
		t.Fatalf("read the stored file: %v", err)
	}
	if !bytes.Equal(stored, body) {
		t.Error("the file was altered; an already-WebP upload must be stored byte for byte")
	}
}

// TestStoreFileStoresAudioAndVideoVerbatim 是本轮「暂时关闭音视频转 WebM」的
// 回归守门人。
//
// 上一版把音频交给 ffmpeg 转 WebM。关掉之后**音频与视频必须逐字节原样落盘**，
// 而且不能出现任何转码痕迹（Converted 必须为 false）。把失败模式的转码器配进去，
// 是为了让「又走了转码」这件事立刻变成失败，而不是靠人去读代码。
func TestStoreFileStoresAudioAndVideoVerbatim(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		body     []byte
		wantMime string
		wantExt  string
	}{
		{name: "audio", filename: "song.mp3", body: mp3Bytes(), wantMime: "audio/mpeg", wantExt: ".mp3"},
		{name: "video", filename: "clip.mp4", body: mp4Bytes(), wantMime: "video/mp4", wantExt: ".mp4"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeFail, "cwebp"))

			media, err := rig.svc.StoreFile(context.Background(), uploadPart(t, tc.filename, tc.body))
			if err != nil {
				t.Fatalf("StoreFile() error = %v", err)
			}

			if media.MimeType != tc.wantMime {
				t.Errorf("mime type = %q, want %q", media.MimeType, tc.wantMime)
			}
			if media.MediaType != tc.name {
				t.Errorf("media type = %q, want %q", media.MediaType, tc.name)
			}
			if media.Converted {
				t.Error("converted = true; audio and video must be stored as-is")
			}
			if !strings.HasSuffix(media.StorageName, tc.wantExt) {
				t.Errorf("storage name = %q, want a %s suffix", media.StorageName, tc.wantExt)
			}

			stored, err := os.ReadFile(filepath.Join(rig.dir, media.StorageName))
			if err != nil {
				t.Fatalf("read the stored file: %v", err)
			}
			if !bytes.Equal(stored, tc.body) {
				t.Error("the file was altered; audio and video must be stored byte for byte")
			}
		})
	}
}

// TestStoreFileRefusesImagesWithoutAnEncoder 是这一轮修复的核心：**不能**再退化成
// 「原样落盘」。
//
// 旧实现在 WebP 编码器缺失时打一条 Warn 就把原文件存下去，于是站点对外声明的
// 「图片一律 WebP」变成一句空话，而症状只在有人去看磁盘时才出现。现在这条路径
// 必须是一条明确的拒绝，且**一点文件都不能留下**。
func TestStoreFileRefusesImagesWithoutAnEncoder(t *testing.T) {
	rig := newMediaTestRig(t, &mediaconv.Converter{})

	_, err := rig.svc.StoreFile(context.Background(), uploadPart(t, "holiday.png", craftPNG(120, 80)))
	if !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("StoreFile() error = %v, want ErrConversionUnavailable", err)
	}

	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the rejection left %v behind; a refused upload must not touch the disk", names)
	}
}

// TestStoreFileRejectsEmptyAndUnsupportedUploads 覆盖两个 4xx 来源。
func TestStoreFileRejectsEmptyAndUnsupportedUploads(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		body     []byte
		wantErr  error
	}{
		{name: "empty file", filename: "empty.png", body: nil, wantErr: ErrEmptyFile},
		{name: "plain text", filename: "notes.txt", body: []byte("just some prose, nothing to sniff"), wantErr: ErrUnsupportedMediaType},
		{name: "html pretending to be an image", filename: "payload.png", body: []byte("<html><body>hi</body></html>"), wantErr: ErrUnsupportedMediaType},
		{name: "unknown binary", filename: "blob", body: []byte("\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09"), wantErr: ErrUnsupportedMediaType},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

			_, err := rig.svc.StoreFile(context.Background(), uploadPart(t, tc.filename, tc.body))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("StoreFile() error = %v, want %v", err, tc.wantErr)
			}
			if names := mediaFiles(t, rig.dir); len(names) != 0 {
				t.Fatalf("the rejection left %v behind", names)
			}
		})
	}
}

// TestStoreFileRejectsAnUnreadableImageHeader 覆盖「嗅探说是图片、但读不出尺寸」
// 这一支。它必须在这里被拒：放过去就等于把一个尺寸未知的文件交给解码器，
// 而那正是解压炸弹要的。
func TestStoreFileRejectsAnUnreadableImageHeader(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	// 只有 8 字节的 PNG 签名：内容嗅探认得它，但 IHDR 根本不存在。
	_, err := rig.svc.StoreFile(context.Background(), uploadPart(t, "truncated.png", []byte("\x89PNG\r\n\x1a\n")))
	if !errors.Is(err, ErrUnsupportedMediaType) {
		t.Fatalf("StoreFile() error = %v, want ErrUnsupportedMediaType", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the rejection left %v behind", names)
	}
}

// TestStoreFileRejectsAnImageOverThePixelBudget 从服务层验证像素闸门确实接在
// 「解码之前」。
//
// 载荷只有几百字节，因此任何基于文件体积的闸门都会放它过去。
func TestStoreFileRejectsAnImageOverThePixelBudget(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	_, err := rig.svc.StoreFile(context.Background(), uploadPart(t, "bomb.png", craftPNG(16000, 16000)))
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("StoreFile() error = %v, want ErrImageTooLarge", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the rejection left %v behind", names)
	}
}

// TestStoreFileReportsBusyWhenEverySlotIsTaken 验证槽位占满时**立即失败**，
// 而不是排队——排队会让上传方看到一个悬而未决的连接。
func TestStoreFileReportsBusyWhenEverySlotIsTaken(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	for i := 0; i < maxConcurrentTranscodes; i++ {
		rig.svc.transcodeSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < maxConcurrentTranscodes; i++ {
			select {
			case <-rig.svc.transcodeSlots:
			default:
			}
		}
	})

	_, err := rig.svc.StoreFile(context.Background(), uploadPart(t, "holiday.png", craftPNG(120, 80)))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("StoreFile() error = %v, want ErrBusy", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the rejected upload left %v behind", names)
	}
}

// TestWithTranscodeSlotHonoursACanceledContext 覆盖等待槽位的那一支。
//
// 它与「槽位占满」是两条不同的出路：客户端先断开时，正确的结局是把取消传上去，
// 而不是回一个可重试的 503——那样上传方会立刻重试一个已经没人要的请求。
func TestWithTranscodeSlotHonoursACanceledContext(t *testing.T) {
	svc := NewMediaService(nil, "", nil)
	for i := 0; i < maxConcurrentTranscodes; i++ {
		svc.transcodeSlots <- struct{}{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// 槽位被占满，Done 通道已就绪：select 只能走取消这一支。
	err := svc.withTranscodeSlot(ctx, func() error {
		t.Error("the work ran even though the request was already canceled")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("withTranscodeSlot() error = %v, want context.Canceled", err)
	}
}

// TestStoreFileSurfacesAConversionFailureAndStoresNothing 覆盖转码器报错那条路。
//
// 这里最容易犯的错是「转码失败就用原文件顶上」——那会让拒绝策略在失败时失效。
func TestStoreFileSurfacesAConversionFailureAndStoresNothing(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeFail, "cwebp"))

	_, err := rig.svc.StoreFile(context.Background(), uploadPart(t, "holiday.png", craftPNG(120, 80)))
	if err == nil {
		t.Fatal("StoreFile() succeeded although the encoder failed")
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the failed conversion left %v behind; the media directory must hold no half-written output", names)
	}
}

// TestStoreFileSurfacesAMissingEncoderOutput 覆盖「转码器以 0 退出却没有产物」。
//
// 只看退出码的实现在这里会走到 os.Stat，进而以一个莫名其妙的错误收场；
// 正确的行为是把「没有产物」当成转码失败。
func TestStoreFileSurfacesAMissingEncoderOutput(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeSilent, "cwebp"))

	_, err := rig.svc.StoreFile(context.Background(), uploadPart(t, "holiday.png", craftPNG(120, 80)))
	if err == nil || !strings.Contains(err.Error(), "produced no output file") {
		t.Fatalf("StoreFile() error = %v, want a 'produced no output file' failure", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the failure left %v behind", names)
	}
}

// TestStoreFileRemovesTheStoredFileWhenTheRecordCannotBeWritten 覆盖「落盘成功、
// 写库失败」那一支。
//
// 这条路径存在的全部意义就是清理：记录没写成，那个文件就成了磁盘上没人能解释的
// 孤儿（而且它占着空间、又能被猜到路径）。所以断言必须打在磁盘上。
func TestStoreFileRemovesTheStoredFileWhenTheRecordCannotBeWritten(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	rig.svc.repo = failingMediaRepo{MediaRepository: rig.repo, createErr: errors.New("the database is gone")}

	if _, err := rig.svc.StoreFile(context.Background(), uploadPart(t, "holiday.png", craftPNG(120, 80))); err == nil {
		t.Fatal("StoreFile() succeeded although the record could not be written")
	}

	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the orphaned file(s) %v were left on disk", names)
	}
}

// TestStoreFileSurfacesAnUnwritableMediaDirectory 覆盖保存失败那一支（不转码的
// 类型也会走到这里）。媒体目录指向一个**文件**时，MkdirAll 必然失败。
func TestStoreFileSurfacesAnUnwritableMediaDirectory(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	blocker := filepath.Join(filepath.Dir(rig.dir), "media-is-a-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("create the file that stands in for the media directory: %v", err)
	}
	rig.svc.mediaDir = blocker

	_, err := rig.svc.StoreFile(context.Background(), uploadPart(t, "song.mp3", mp3Bytes()))
	if err == nil || !strings.Contains(err.Error(), "create media dir") {
		t.Fatalf("StoreFile() error = %v, want a 'create media dir' failure", err)
	}
}

// TestStoreFileReportsAnUnusableTempDirectory 覆盖建临时目录失败那一支。
//
// 这一支不是洁癖：临时目录不可用时必须**在读到任何字节之前**就失败，
// 否则一个 GB 级上传会被完整读进来再丢掉。
func TestStoreFileReportsAnUnusableTempDirectory(t *testing.T) {
	// 顺序要紧：装置自己要用 t.TempDir()，而 t.TempDir() 也走临时目录。
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	header := uploadPart(t, "holiday.png", craftPNG(120, 80))
	blockTempDir(t)

	_, err := rig.svc.StoreFile(context.Background(), header)
	if err == nil || !strings.Contains(err.Error(), "create temp directory") {
		t.Fatalf("StoreFile() error = %v, want a 'create temp directory' failure", err)
	}
}

// TestStoreFileReportsAnUnopenableUpload 覆盖「上传流打不开」那一支。
//
// 构造方式：让内容落到临时文件上（ReadForm 的内存额度给到最小，超过 10MB 的部分
// 必定落盘），解析完成之后再 RemoveAll——FileHeader 里记的那个路径就指不到了。
// 这正是客户端中途断开、net/http 已经把临时文件收走时的形状。
func TestStoreFileReportsAnUnopenableUpload(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	// 11 MiB：multipart 只在超过 maxMemory+10MiB 时才落盘，取 11MiB 刚好越界，
	// 而内容是什么无关紧要——它在被读到之前就已经不在了。
	form, header := readForm(t, "holiday.png", make([]byte, 11<<20), 1)
	if err := form.RemoveAll(); err != nil {
		t.Fatalf("remove the parsed form: %v", err)
	}

	_, err := rig.svc.StoreFile(context.Background(), header)
	if err == nil || !strings.Contains(err.Error(), "open uploaded file") {
		t.Fatalf("StoreFile() error = %v, want an 'open uploaded file' failure", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the failure left %v behind", names)
	}
}

// TestStoreFileSurfacesAnUnwritableMediaDirectoryAfterTranscoding 覆盖 applyTranscoding
// 里保存失败那一支。
//
// 与上一条的区别在于顺序：这条走的是「转码已经成功、产物也已经生成」，失败发生在
// 把它搬进媒体目录那一步。这一步失败必须让整次上传失败——把已经转好的中间产物留在
// 私有临时目录里、却照样写一条记录，会让数据库指向一个不存在的文件。
func TestStoreFileSurfacesAnUnwritableMediaDirectoryAfterTranscoding(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	blocker := filepath.Join(filepath.Dir(rig.dir), "media-is-a-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("create the file that stands in for the media directory: %v", err)
	}
	rig.svc.mediaDir = blocker

	_, err := rig.svc.StoreFile(context.Background(), uploadPart(t, "holiday.png", craftPNG(120, 80)))
	if err == nil || !strings.Contains(err.Error(), "create media dir") {
		t.Fatalf("StoreFile() error = %v, want a 'create media dir' failure", err)
	}
}

// TestStoreFileReportsAnUnreadableUpload 覆盖「上传流读到一半坏掉」那一支。
//
// 构造方式是把 multipart 落盘的那个临时文件换成一个**目录**：os.Open 对目录会成功
// （所以走得到读取那一步），而读取必然失败。这正是磁盘读到坏块、或上传方在传输途中断开
// 时的形状——它必须让这次上传失败，而不是拿一个不完整的文件往下走。
func TestStoreFileReportsAnUnreadableUpload(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	// 内容必须大到落盘：multipart 只在超过 maxMemory+10MiB 时才建临时文件。
	before := tempDirEntries(t)
	_, header := readForm(t, "holiday.png", make([]byte, 11<<20), 1)
	created := theNewTempFile(t, before)

	if err := os.Remove(created); err != nil {
		t.Fatalf("remove the parsed temp file: %v", err)
	}
	if err := os.Mkdir(created, 0o700); err != nil {
		t.Fatalf("put a directory where the temp file was: %v", err)
	}

	_, err := rig.svc.StoreFile(context.Background(), header)
	if err == nil || !strings.Contains(err.Error(), "read uploaded file") {
		t.Fatalf("StoreFile() error = %v, want a 'read uploaded file' failure", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the failure left %v behind", names)
	}
}

// tempDirEntries 记录临时目录当前的内容，供 theNewTempFile 做差集。
func tempDirEntries(t *testing.T) map[string]bool {
	t.Helper()

	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read the temp directory: %v", err)
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name()] = true
	}
	return seen
}

// theNewTempFile 找出临时目录里新出现的那个文件，也就是 multipart 落盘的那一个。
//
// 差集而不是照 "multipart-" 前缀去猜：前缀是 mime/multipart 的实现细节，而这里要的
// 只是「刚才多出来的那个文件」。多出来的不止一个就跳过——那说明有别的进程同时在写
// 临时目录，此时任何猜测都不可靠，而这条用例守的只是一条错误路径。
func theNewTempFile(t *testing.T, before map[string]bool) string {
	t.Helper()

	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read the temp directory: %v", err)
	}
	var fresh []string
	for _, entry := range entries {
		if !before[entry.Name()] {
			fresh = append(fresh, filepath.Join(os.TempDir(), entry.Name()))
		}
	}
	if len(fresh) != 1 {
		t.Skipf("the temp directory gained %d entries; cannot tell which one belongs to this test", len(fresh))
	}
	return fresh[0]
}

// TestStoreFileSurfacesAnEntropyFailure 覆盖 storageName 生成失败那一支。
//
// 取不到随机数时，落盘文件名就没有了唯一性的来源；此时必须失败，而不是拿一个
// 空名字（或可预测的名字）去写。crypto/rand.Reader 是标准库导出的变量，
// 替换它即可构造这个处境。
func TestStoreFileSurfacesAnEntropyFailure(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	// 上传段必须先造好：multipart 的 Writer 自己也要用 crypto/rand 生成分隔串，
	// 先掐掉熵源会让这条用例在准备阶段就 panic（那就变成在测 mime/multipart 了）。
	header := uploadPart(t, "holiday.png", craftPNG(120, 80))
	breakEntropy(t)

	_, err := rig.svc.StoreFile(context.Background(), header)
	if err == nil || !strings.Contains(err.Error(), "generate storage name") {
		t.Fatalf("StoreFile() error = %v, want a 'generate storage name' failure", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the failure left %v behind", names)
	}
}

// TestSaveFileRefusesAnUnreadableSource 覆盖 saveFile 的源文件打不开那一支。
func TestSaveFileRefusesAnUnreadableSource(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	err := rig.svc.saveFile(filepath.Join(rig.dir, "does-not-exist"), "target.webp")
	if err == nil || !strings.Contains(err.Error(), "open temp output") {
		t.Fatalf("saveFile() error = %v, want an 'open temp output' failure", err)
	}
}

// TestSaveFileRefusesADestinationInAMissingDirectory 覆盖目标文件建不出来那一支。
//
// 目标目录不存在时 os.OpenFile 会失败，而**不能**替它把子目录建出来：落盘文件名由
// 服务层生成，永远是一个裸文件名，出现路径分隔符就说明上游被人改坏了。
func TestSaveFileRefusesADestinationInAMissingDirectory(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	source := filepath.Join(filepath.Dir(rig.dir), "source.bin")
	if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
		t.Fatalf("create the source file: %v", err)
	}

	err := rig.svc.saveFile(source, filepath.Join("nested", "target.webp"))
	if err == nil || !strings.Contains(err.Error(), "create stored media") {
		t.Fatalf("saveFile() error = %v, want a 'create stored media' failure", err)
	}
}

// TestSaveFileCleansUpAHalfWrittenTarget 覆盖复制失败那一支。
//
// 源是一个**目录**：os.Open 会成功，读取则会失败。于是 io.Copy 走到一半报错，
// 而写了一半的目标文件必须被清掉——否则重启之后没人知道它是什么。
func TestSaveFileCleansUpAHalfWrittenTarget(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	source := filepath.Join(filepath.Dir(rig.dir), "a-directory")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("create the source directory: %v", err)
	}

	err := rig.svc.saveFile(source, "target.webp")
	if err == nil || !strings.Contains(err.Error(), "copy stored media") {
		t.Fatalf("saveFile() error = %v, want a 'copy stored media' failure", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the half-written file %v was left behind", names)
	}
}

// TestUpgradeToWebPReplacesTheFileAndKeepsTheID 是 upgrade-media 的执行体用例。
//
// 最要紧的断言是 **id 不变**：articles.cover_media_id、users.avatar_media_id 与
// moment_media.media_id 指的都是 id，只要它不变，所有引用就自动继续有效。
// 其次才是「磁盘上换了文件」与「记录里换了名字」。
func TestUpgradeToWebPReplacesTheFileAndKeepsTheID(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	original := rig.seedImage(t, "old.png", craftPNG(600, 400), "image/png")

	upgraded, err := rig.svc.UpgradeToWebP(context.Background(), original)
	if err != nil {
		t.Fatalf("UpgradeToWebP() error = %v", err)
	}

	if upgraded.ID != original.ID {
		t.Errorf("id = %d, want %d — every reference points at the id", upgraded.ID, original.ID)
	}
	if upgraded.MimeType != "image/webp" || !upgraded.Converted {
		t.Errorf("mime type = %q converted = %v, want image/webp and true", upgraded.MimeType, upgraded.Converted)
	}
	if upgraded.StorageName == original.StorageName {
		t.Error("the storage name did not change; the re-encoded file needs its own name")
	}
	if !strings.HasSuffix(upgraded.StorageName, ".webp") {
		t.Errorf("storage name = %q, want a .webp suffix", upgraded.StorageName)
	}

	stored, err := os.ReadFile(filepath.Join(rig.dir, upgraded.StorageName))
	if err != nil {
		t.Fatalf("read the converted file: %v", err)
	}
	if !bytes.Equal(stored, testtool.Payload()) {
		t.Error("the converted file is not the encoder's output")
	}
	if _, err := os.Stat(filepath.Join(rig.dir, original.StorageName)); !os.IsNotExist(err) {
		t.Errorf("the original file is still there (stat error = %v); it has no record pointing at it", err)
	}

	fromDB, err := rig.repo.GetByID(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("read the record back: %v", err)
	}
	if fromDB.StorageName != upgraded.StorageName || fromDB.MimeType != "image/webp" || !fromDB.Converted {
		t.Errorf("the stored record is %+v, want the upgrade to have been persisted", *fromDB)
	}
}

// TestUpgradeToWebPRefusesNonImages 钉住「只升级图片」这条边界。
func TestUpgradeToWebPRefusesNonImages(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	_, err := rig.svc.UpgradeToWebP(context.Background(), &model.Media{ID: 7, MediaType: "audio"})
	if !errors.Is(err, ErrUnsupportedMediaType) {
		t.Fatalf("UpgradeToWebP() error = %v, want ErrUnsupportedMediaType", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("the refusal left %v behind", names)
	}
}

// TestUpgradeToWebPRefusesWithoutAnEncoder 同样要在这里兜住：升级命令在改动任何
// 一个字节之前就确认编码器可用，而服务层自己也不能成为绕过这道门的后门。
func TestUpgradeToWebPRefusesWithoutAnEncoder(t *testing.T) {
	rig := newMediaTestRig(t, &mediaconv.Converter{})
	original := rig.seedImage(t, "old.png", craftPNG(120, 80), "image/png")

	_, err := rig.svc.UpgradeToWebP(context.Background(), original)
	if !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("UpgradeToWebP() error = %v, want ErrConversionUnavailable", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 1 || names[0] != "old.png" {
		t.Fatalf("media directory = %v, want exactly the untouched old.png", names)
	}
}

// TestUpgradeToWebPReportsAMissingFile 覆盖「记录在、文件没了」——库里指向一个
// 不存在的文件时，命令要如实报错并计入失败，而不是静默跳过。
func TestUpgradeToWebPReportsAMissingFile(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	if err := os.MkdirAll(rig.dir, 0o700); err != nil {
		t.Fatalf("create the media directory: %v", err)
	}
	missing := &model.Media{
		StorageName: "gone.png", OriginalName: "gone.png", MediaType: model.MediaTypeImage,
		MimeType: "image/png", Size: 10, CreatedAt: 1,
	}
	if err := rig.repo.Create(context.Background(), missing); err != nil {
		t.Fatalf("seed the record: %v", err)
	}

	if _, err := rig.svc.UpgradeToWebP(context.Background(), missing); err == nil {
		t.Fatal("UpgradeToWebP() succeeded although the file is missing")
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("media directory = %v, want it empty", names)
	}
}

// TestUpgradeToWebPRefusesADirectory 覆盖「那个路径是个目录」这一支。
//
// os.Remove 对一个非空目录会失败，而 os.Stat 又不会报错——所以必须显式判 IsDir，
// 否则这段代码会在「删掉原文件」那一步留下一个删不掉的目录。
func TestUpgradeToWebPRefusesADirectory(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	if err := os.MkdirAll(filepath.Join(rig.dir, "old.png"), 0o700); err != nil {
		t.Fatalf("create the directory: %v", err)
	}
	media := &model.Media{
		StorageName: "old.png", OriginalName: "old.png", MediaType: model.MediaTypeImage,
		MimeType: "image/png", Size: 0, CreatedAt: 1,
	}
	if err := rig.repo.Create(context.Background(), media); err != nil {
		t.Fatalf("seed the record: %v", err)
	}

	_, err := rig.svc.UpgradeToWebP(context.Background(), media)
	if err == nil || !strings.Contains(err.Error(), "it is a directory") {
		t.Fatalf("UpgradeToWebP() error = %v, want a 'it is a directory' failure", err)
	}
}

// TestUpgradeToWebPKeepsTheOriginalWhenConversionFails 覆盖转码失败那一支：
// 原文件与原来的记录都必须**原封不动**，重跑一遍即可。
func TestUpgradeToWebPKeepsTheOriginalWhenConversionFails(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeFail, "cwebp"))
	original := rig.seedImage(t, "old.png", craftPNG(120, 80), "image/png")

	if _, err := rig.svc.UpgradeToWebP(context.Background(), original); err == nil {
		t.Fatal("UpgradeToWebP() succeeded although the encoder failed")
	}

	if names := mediaFiles(t, rig.dir); len(names) != 1 || names[0] != "old.png" {
		t.Fatalf("media directory = %v, want exactly the untouched old.png", names)
	}
	fromDB, err := rig.repo.GetByID(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("read the record back: %v", err)
	}
	if fromDB.StorageName != "old.png" || fromDB.MimeType != "image/png" || fromDB.Converted {
		t.Errorf("the record was touched: %+v", *fromDB)
	}
}

// TestUpgradeToWebPRemovesTheNewFileWhenTheRecordCannotBeUpdated 是本文件里
// 最容易写错的那条断言：落盘顺序是「新文件 → 改库 → 删旧文件」，而改库失败时
// 必须回到「磁盘上每个文件都能被一条记录解释」这条性质上。
func TestUpgradeToWebPRemovesTheNewFileWhenTheRecordCannotBeUpdated(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	original := rig.seedImage(t, "old.png", craftPNG(120, 80), "image/png")
	rig.svc.repo = failingMediaRepo{MediaRepository: rig.repo, updateErr: errors.New("the database is gone")}

	if _, err := rig.svc.UpgradeToWebP(context.Background(), original); err == nil {
		t.Fatal("UpgradeToWebP() succeeded although the record could not be updated")
	}

	// 只剩原来那个文件：新产物被清掉了，旧文件与旧记录全程没被动过。
	if names := mediaFiles(t, rig.dir); len(names) != 1 || names[0] != "old.png" {
		t.Fatalf("media directory = %v, want exactly the untouched old.png", names)
	}
	fromDB, err := rig.repo.GetByID(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("read the record back: %v", err)
	}
	if fromDB.StorageName != "old.png" {
		t.Errorf("storage name = %q, want old.png", fromDB.StorageName)
	}
}

// TestUpgradeToWebPReportsAnUnusableTempDirectory 覆盖建临时目录失败那一支。
func TestUpgradeToWebPReportsAnUnusableTempDirectory(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	original := rig.seedImage(t, "old.png", craftPNG(120, 80), "image/png")
	blockTempDir(t)

	_, err := rig.svc.UpgradeToWebP(context.Background(), original)
	if err == nil || !strings.Contains(err.Error(), "create temp directory") {
		t.Fatalf("UpgradeToWebP() error = %v, want a 'create temp directory' failure", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 1 || names[0] != "old.png" {
		t.Fatalf("media directory = %v, want exactly the untouched old.png", names)
	}
}

// TestUpgradeToWebPSurfacesAnEntropyFailure 覆盖 storageName 生成失败那一支。
func TestUpgradeToWebPSurfacesAnEntropyFailure(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	original := rig.seedImage(t, "old.png", craftPNG(120, 80), "image/png")
	breakEntropy(t)

	_, err := rig.svc.UpgradeToWebP(context.Background(), original)
	if err == nil || !strings.Contains(err.Error(), "generate storage name") {
		t.Fatalf("UpgradeToWebP() error = %v, want a 'generate storage name' failure", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 1 || names[0] != "old.png" {
		t.Fatalf("media directory = %v, want exactly the untouched old.png", names)
	}
}

// TestUpgradeToWebPReportsAnUnwritableMediaDirectory 覆盖 saveFile 失败那一支。
//
// 构造走 mkdirAll 钩子：R7-02 之后，原先「mediaDir 指向一个文件、再用 ../ 把
// storage_name 挪到目录之外」的做法会被 Path 直接拒绝——那种 storage_name 本来
// 就只是为了让源文件在 stat 时存在。钩子让这条分支与那个不安全的构造解耦。
func TestUpgradeToWebPReportsAnUnwritableMediaDirectory(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	original := rig.seedImage(t, "old.png", craftPNG(120, 80), "image/png")

	failMkdirFor(t, rig.dir)

	_, err := rig.svc.UpgradeToWebP(context.Background(), original)
	if err == nil || !strings.Contains(err.Error(), "create media dir") {
		t.Fatalf("UpgradeToWebP() error = %v, want a 'create media dir' failure", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 1 || names[0] != "old.png" {
		t.Fatalf("media directory = %v, want exactly the untouched old.png", names)
	}
}

// failMkdirFor 让 mkdirAll 在创建 dir 时失败，其余调用照常转发。恢复由 t.Cleanup
// 负责——mkdirAll 是包级变量，泄漏会污染同包的其他用例。
func failMkdirFor(t *testing.T, dir string) {
	t.Helper()

	previous := mkdirAll
	mkdirAll = func(path string, perm os.FileMode) error {
		if path == dir {
			return errors.New("simulated read-only parent")
		}
		return previous(path, perm)
	}
	t.Cleanup(func() { mkdirAll = previous })
}

// TestUpgradeToWebPWarnsWhenTheOriginalFileCannotBeRemoved 覆盖最后那一步的告警。
//
// 构造走 removeFile 钩子而不是文件系统：这是唯一稳定的办法。曾经的做法是把
// mediaDir 改成 0500 让 unlink 因权限被拒，但 saveFile 每次都会把 mediaDir
// chmod 回 0700（见它的说明），于是那个前提被悄悄抵消——这条用例因此长期红着，
// 而它守的告警路径实际上从未被验证过（第七轮渗透测试报告 §4.2）。
func TestUpgradeToWebPWarnsWhenTheOriginalFileCannotBeRemoved(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	original := rig.seedImage(t, "old.png", craftPNG(120, 80), "image/png")

	failRemovalsOf(t, "old.png")

	upgraded, err := rig.svc.UpgradeToWebP(context.Background(), original)
	if err != nil {
		t.Fatalf("UpgradeToWebP() error = %v; a file that cannot be removed is not a failure", err)
	}
	if upgraded.MimeType != "image/webp" {
		t.Errorf("mime type = %q, want image/webp", upgraded.MimeType)
	}
	// 升级本身完成了；只是原文件还在，等着被人发现。
	if names := mediaFiles(t, rig.dir); len(names) != 2 {
		t.Fatalf("media directory = %v, want the leftover original next to the new file", names)
	}
}

// failRemovalsOf 让 removeFile 在删除名为 name 的文件时失败，其余调用照常转发。
// 恢复由 t.Cleanup 负责——removeFile 是包级变量，泄漏会污染同包的其他用例。
func failRemovalsOf(t *testing.T, name string) {
	t.Helper()

	previous := removeFile
	removeFile = func(path string) error {
		if filepath.Base(path) == name {
			return errors.New("simulated removal failure")
		}
		return previous(path)
	}
	t.Cleanup(func() { removeFile = previous })
}

// TestDeleteRemovesTheRecordAndTheFile 覆盖硬删的正常路径。
func TestDeleteRemovesTheRecordAndTheFile(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	media := rig.seedImage(t, "doomed.png", craftPNG(60, 40), "image/png")

	if err := rig.svc.Delete(context.Background(), media.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if names := mediaFiles(t, rig.dir); len(names) != 0 {
		t.Fatalf("media directory = %v, want it empty", names)
	}
	if _, err := rig.repo.GetByID(context.Background(), media.ID); !errors.Is(err, model.ErrMediaNotFound) {
		t.Errorf("GetByID() error = %v, want ErrMediaNotFound", err)
	}
}

// TestDeleteSurfacesAMissingRecord 覆盖「先查再删」里查询失败那一支：
// 查不到就直接返回，不能对一条不存在的记录报成功。
func TestDeleteSurfacesAMissingRecord(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	if err := rig.svc.Delete(context.Background(), 404); !errors.Is(err, model.ErrMediaNotFound) {
		t.Fatalf("Delete() error = %v, want ErrMediaNotFound", err)
	}
}

// TestDeleteSurfacesARepositoryFailure 覆盖仓库删除失败那一支：
// 记录没删掉就返回，文件还留着，重试即可。
func TestDeleteSurfacesARepositoryFailure(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	media := rig.seedImage(t, "doomed.png", craftPNG(60, 40), "image/png")
	rig.svc.repo = failingDeleteRepo{MediaRepository: rig.repo, err: errors.New("the database is gone")}

	if err := rig.svc.Delete(context.Background(), media.ID); err == nil {
		t.Fatal("Delete() succeeded although the record could not be deleted")
	}
	if names := mediaFiles(t, rig.dir); len(names) != 1 {
		t.Fatalf("media directory = %v, want the file to be left alone", names)
	}
}

// TestDeleteKeepsGoingWhenTheFileCannotBeRemoved 覆盖「记录已经没了、文件删不掉」
// 那一支：此时应当返回成功（记录才是真相），只留一条告警。
func TestDeleteKeepsGoingWhenTheFileCannotBeRemoved(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	media := rig.seedImage(t, "doomed.png", craftPNG(60, 40), "image/png")

	unlock := makeFileUndeletable(t, filepath.Join(rig.dir, "doomed.png"), rig.dir)
	defer unlock()

	if err := rig.svc.Delete(context.Background(), media.ID); err != nil {
		t.Fatalf("Delete() error = %v; a file that cannot be removed must not fail the request", err)
	}
	if _, err := rig.repo.GetByID(context.Background(), media.ID); !errors.Is(err, model.ErrMediaNotFound) {
		t.Errorf("the record should be gone regardless of the file, GetByID() error = %v", err)
	}
}

// failingDeleteRepo 让删除失败，其余照常。
type failingDeleteRepo struct {
	model.MediaRepository
	err error
}

func (r failingDeleteRepo) Delete(context.Context, int64) error { return r.err }

// makeFileUndeletable 让 path 在一段时间内删不掉，返回恢复用的函数。
//
// 平台差异见 TestUpgradeToWebPWarnsWhenTheOriginalFileCannotBeRemoved 的说明。
// 无法构造时返回一个空操作与 false 表示跳过。
func makeFileUndeletable(t *testing.T, path, dir string) func() {
	t.Helper()

	if runtime.GOOS == "windows" {
		// 保持一个打开句柄：Go 在 Windows 上打开文件时不带 FILE_SHARE_DELETE，
		// 于是在它关闭之前，DeleteFile 会以共享冲突被拒绝。
		handle, err := os.Open(path)
		if err != nil {
			t.Fatalf("open the file to lock it: %v", err)
		}
		return func() { _ = handle.Close() }
	}

	// 其余平台：把目录改成不可写，unlink 会因为权限被拒。
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make %s read-only: %v", dir, err)
	}
	return func() { _ = os.Chmod(dir, 0o700) }
}

// TestMediaServiceReadPaths 覆盖 Get / List / Path 三个透传。
func TestMediaServiceReadPaths(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	first := rig.seedImage(t, "a.png", craftPNG(60, 40), "image/png")
	rig.seedImage(t, "b.png", craftPNG(60, 40), "image/png")

	got, err := rig.svc.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != first.ID {
		t.Errorf("Get() id = %d, want %d", got.ID, first.ID)
	}

	list, total, err := rig.svc.List(context.Background(), model.Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Errorf("List() = %d rows, total %d, want 2 and 2", len(list), total)
	}

	path, err := rig.svc.Path(first)
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	if want := filepath.Join(rig.dir, first.StorageName); path != want {
		t.Errorf("Path() = %q, want %q", path, want)
	}
}

// TestRemoveQuietlyNeverReportsAnError 覆盖清理辅助的两条路：删不掉时只留一条
// 告警、路径本来就不在时连告警都不打，两者都不上抛。
//
// 它是拿不到返回值来断言的（签名就没有返回值），所以这里验的是「调用它不会 panic、
// 也不会改变调用方看到的结局」。真正的价值在别处：它被调用时，调用方手上往往
// 已经有一个更要紧的错误。
func TestRemoveQuietlyNeverReportsAnError(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))

	removeQuietly(filepath.Join(rig.dir, "never-existed.png"))

	// 非空目录：两个平台上 os.Remove 都会失败，于是走告警那一支。
	dir := filepath.Join(filepath.Dir(rig.dir), "non-empty")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create the directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("create the child file: %v", err)
	}
	removeQuietly(dir)
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("removeQuietly removed a non-empty directory: %v", err)
	}
}

// TestIsWebP 钉住那个单行判断——它决定「要不要过编码器」，而写反了会让所有图片
// 都被重编码一遍（画质下降），或者干脆不转码。
func TestIsWebP(t *testing.T) {
	if !isWebP("image/webp") {
		t.Error("isWebP(image/webp) = false")
	}
	for _, other := range []string{"image/png", "image/jpeg", "video/webm", "audio/webm", ""} {
		if isWebP(other) {
			t.Errorf("isWebP(%q) = true", other)
		}
	}
}

// TestRandomHexProducesUniqueNames 覆盖随机名生成：长度、字符集，以及「两次不同」。
func TestRandomHexProducesUniqueNames(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 64; i++ {
		name, err := randomHex(16)
		if err != nil {
			t.Fatalf("randomHex() error = %v", err)
		}
		if len(name) != 32 {
			t.Fatalf("randomHex(16) = %q (%d chars), want 32", name, len(name))
		}
		for _, r := range name {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				t.Fatalf("randomHex(16) = %q, which is not hexadecimal", name)
			}
		}
		if seen[name] {
			t.Fatalf("randomHex(16) returned %q twice in 64 tries", name)
		}
		seen[name] = true
	}

	if _, err := randomHex(0); err != nil {
		t.Fatalf("randomHex(0) error = %v", err)
	}
}

// TestRandomHexSurfacesAnEntropyFailure 覆盖取随机数失败那一支。
func TestRandomHexSurfacesAnEntropyFailure(t *testing.T) {
	breakEntropy(t)

	if _, err := randomHex(16); err == nil {
		t.Fatal("randomHex() succeeded with no entropy source")
	}
}

// TestHasImageSignatureRejectsEverythingElse 覆盖魔数表的兜底分支。
//
// 从 classify 走不到那里（只有 supportedImageMimes 里的类型会被问），所以这条
// 用例只能直接问函数。它守的是「将来有人加了一种图片格式、却忘了加签名」：
// 那时 hasImageSignature 会静默返回 false，而兜底的图片路径会因此拒绝一切。
func TestHasImageSignatureRejectsEverythingElse(t *testing.T) {
	head := []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")

	if hasImageSignature("totally/unknown", head) {
		t.Error("hasImageSignature(unknown type) = true; an unlisted format has no signature check")
	}
	// 短到放不下签名时也必须为假，而不是越界读。
	if hasImageSignature("image/webp", []byte("RIFF")) {
		t.Error("hasImageSignature(webp, 4 bytes) = true")
	}
	if hasImageSignature("image/jpeg", []byte{0xFF, 0xD8}) {
		t.Error("hasImageSignature(jpeg, 2 bytes) = true")
	}
	// 签名被放在错误的偏移上（RIFF 后面不是 WEBP）也算不匹配。
	if hasImageSignature("image/webp", []byte("RIFF\x24\x00\x00\x00XXXXVP8 ")) {
		t.Error("hasImageSignature(webp, wrong bytes at offset 8) = true")
	}
}

// TestNormalizeMimeStripsParameters 订住那个会影响后续比较的归一化。
func TestNormalizeMimeStripsParameters(t *testing.T) {
	cases := map[string]string{
		"text/plain; charset=utf-8": "text/plain",
		"IMAGE/PNG":                 "image/png",
		"  audio/mpeg  ":            "audio/mpeg",
		"application/octet-stream":  "application/octet-stream",
		"":                          "",
	}
	for input, want := range cases {
		if got := normalizeMime(input); got != want {
			t.Errorf("normalizeMime(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestSafeExtOnlyAcceptsBoringExtensions 覆盖扩展名白名单的三条出路。
func TestSafeExtOnlyAcceptsBoringExtensions(t *testing.T) {
	cases := map[string]string{
		"photo.PNG":         ".png", // 统一小写
		"archive.tar.gz":    ".gz",
		"no-extension":      "",
		"trailing.":         "",
		"weird.p-h":         "",
		"long.extremely-lo": "",
	}
	for input, want := range cases {
		if got := safeExt(input); got != want {
			t.Errorf("safeExt(%q) = %q, want %q", input, got, want)
		}
	}
	// 超过 10 个字符的扩展名一律拒绝，避免把用户输入拼进路径。
	if got := safeExt("file.abcdefghijk"); got != "" {
		t.Errorf("safeExt(11-char extension) = %q, want an empty string", got)
	}
}

// TestImageDimensionsSurfacesADecodeFailure 覆盖头部读不出来那一支。
func TestImageDimensionsSurfacesADecodeFailure(t *testing.T) {
	if _, _, err := imageDimensions(bytes.NewReader([]byte("not an image at all"))); err == nil {
		t.Fatal("imageDimensions() succeeded on garbage")
	}
}
