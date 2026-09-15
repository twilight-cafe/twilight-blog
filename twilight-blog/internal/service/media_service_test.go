package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"sync"
	"testing"
)

// craftPNG 造一张「声明尺寸很大、实际字节极少」的 PNG。
//
// 它模拟的正是解压炸弹的形状：IDAT 里没有真实像素数据，但 IHDR 声明的宽高
// 足以让解码器分配 GB 级缓冲。载荷只有几百字节，因此任何基于**文件体积**的
// 闸门都拦不住它——只有解析头部声明的尺寸才行。
func craftPNG(width, height uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})

	writeChunk := func(typ string, data []byte) {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(data)))
		buf.Write(length[:])
		buf.WriteString(typ)
		buf.Write(data)
		crc := crc32.NewIEEE()
		crc.Write([]byte(typ))
		crc.Write(data)
		var sum [4]byte
		binary.BigEndian.PutUint32(sum[:], crc.Sum32())
		buf.Write(sum[:])
	}

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // colour type: truecolour
	writeChunk("IHDR", ihdr)
	// DecodeConfig 只读 IHDR，IDAT 的内容无关紧要。
	writeChunk("IDAT", []byte{0x78, 0x9c, 0x01, 0x00, 0x00, 0xff, 0xff, 0x00, 0x00, 0x00, 0x01})
	writeChunk("IEND", nil)
	return buf.Bytes()
}

// TestImageDimensionsReadsTheDeclaredSizeWithoutDecoding 是尺寸闸门的核心守护测试。
//
// 它一次性证明三件事：
//  1. image.DecodeConfig 能从**未解码**的头部读出真实宽高；
//  2. 一个几百字节的载荷可以声明白万级像素；
//  3. 这样的图片在进入解码器之前就会被 checkImageBudget 拒掉。
//
// 反证方式：删掉 StoreFile 里的 imageDimensions / checkImageBudget 调用，
// 或者把 maxImagePixels 放大到 3 亿以上，这条测试与
// TestCheckImageBudget 里的边界用例会失败。（后者在真机上还有一层证据：
// 上传这种文件会让 cwebp 分配约 1GB 并最终解码失败返回 500。）
func TestImageDimensionsReadsTheDeclaredSizeWithoutDecoding(t *testing.T) {
	bomb := craftPNG(16000, 16000)
	if len(bomb) > 1024 {
		t.Fatalf("the crafted payload is %d bytes; it is meant to be tiny", len(bomb))
	}
	// 1.6 万 ×1.6 万 = 2.56 亿像素，解码后约 0.95 GiB（2.56e8 × 4 字节）。
	if pixels := int64(16000) * 16000; pixels*4 < 1<<29 {
		t.Fatalf("the payload is not actually a bomb: %d pixels", pixels)
	}

	width, height, err := imageDimensions(bytes.NewReader(bomb))
	if err != nil {
		t.Fatalf("imageDimensions() error = %v", err)
	}
	if width != 16000 || height != 16000 {
		t.Fatalf("imageDimensions() = %dx%d, want 16000x16000", width, height)
	}

	if err := checkImageBudget(width, height); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("checkImageBudget(16000, 16000) error = %v, want ErrImageTooLarge", err)
	}
}

// TestCheckImageBudget pins the arithmetic, including the boundaries and the
// shapes that would slip through a naive `width*height > limit` written in int.
func TestCheckImageBudget(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		wantErr       bool
	}{
		{name: "ordinary photo", width: 4000, height: 3000, wantErr: false},
		{name: "8K screenshot", width: 7680, height: 4320, wantErr: false},
		{name: "exactly at the pixel budget", width: 8000, height: maxImagePixels / 8000, wantErr: false},
		{name: "one row over the pixel budget", width: 8000, height: maxImagePixels/8000 + 1, wantErr: true},
		{name: "long thin strip", width: 1, height: maxImagePixels + 1, wantErr: true},
		{name: "one side over the per-side limit", width: maxImageDimension + 1, height: 10, wantErr: true},
		{name: "both sides at the per-side limit", width: maxImageDimension, height: maxImageDimension, wantErr: true},
		{name: "both sides huge", width: 50000, height: 50000, wantErr: true},
		{name: "zero width", width: 0, height: 100, wantErr: true},
		{name: "negative height", width: 100, height: -1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkImageBudget(tt.width, tt.height)
			if tt.wantErr {
				if !errors.Is(err, ErrImageTooLarge) {
					t.Fatalf("checkImageBudget(%d, %d) error = %v, want ErrImageTooLarge", tt.width, tt.height, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkImageBudget(%d, %d) error = %v, want nil", tt.width, tt.height, err)
			}
		})
	}
}

// TestWithTranscodeSlotSaturatesAndReleases asserts the two properties the
// concurrency gate has to have: it refuses work while saturated (rather than
// queueing it), and it always gives the slot back.
//
// 第二点同样重要：槽位泄漏会让媒体上传永久变成 503，而且症状（「刚开始好好的，
// 后来所有上传都失败」）很难联想到一个 channel 忘了读。
func TestWithTranscodeSlotSaturatesAndReleases(t *testing.T) {
	svc := NewMediaService(nil, "", nil)
	ctx := context.Background()

	release := make(chan struct{})
	started := make(chan struct{}, maxConcurrentTranscodes)
	var wg sync.WaitGroup

	for i := 0; i < maxConcurrentTranscodes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.withTranscodeSlot(ctx, func() error {
				started <- struct{}{}
				<-release
				return nil
			})
		}()
	}
	for i := 0; i < maxConcurrentTranscodes; i++ {
		<-started
	}

	if err := svc.withTranscodeSlot(ctx, func() error { return nil }); !errors.Is(err, ErrBusy) {
		t.Fatalf("withTranscodeSlot() while saturated error = %v, want ErrBusy", err)
	}

	close(release)
	wg.Wait()
	if err := svc.withTranscodeSlot(ctx, func() error { return nil }); err != nil {
		t.Fatalf("withTranscodeSlot() after the slots were released error = %v, want nil", err)
	}
}

func TestClassifyVideo(t *testing.T) {
	tests := []struct {
		name         string
		filename     string
		detectedMime string
		wantType     string
		wantMime     string
	}{
		{
			name:         "mp4 detected",
			filename:     "clip.mp4",
			detectedMime: "video/mp4",
			wantType:     "video",
			wantMime:     "video/mp4",
		},
		{
			name:         "mkv extension fallback",
			filename:     "movie.mkv",
			detectedMime: "application/octet-stream",
			wantType:     "video",
			wantMime:     "video/x-matroska",
		},
		{
			name:         "webm detected as video",
			filename:     "clip.webm",
			detectedMime: "video/webm",
			wantType:     "video",
			wantMime:     "video/webm",
		},
		{
			name:         "mov extension fallback",
			filename:     "movie.mov",
			detectedMime: "application/octet-stream",
			wantType:     "video",
			wantMime:     "video/quicktime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mediaType, mimeType := classify(tt.filename, tt.detectedMime, nil)
			if mediaType != tt.wantType || mimeType != tt.wantMime {
				t.Fatalf("classify(%q, %q) = (%q, %q), want (%q, %q)",
					tt.filename, tt.detectedMime, mediaType, mimeType, tt.wantType, tt.wantMime)
			}
		})
	}
}

func TestClassifyStillSupportsImagesAndAudio(t *testing.T) {
	tests := []struct {
		filename     string
		detectedMime string
		wantType     string
		wantMime     string
	}{
		{filename: "photo.jpg", detectedMime: "image/jpeg", wantType: "image", wantMime: "image/jpeg"},
		{filename: "audio.mp3", detectedMime: "audio/mpeg", wantType: "audio", wantMime: "audio/mpeg"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			mediaType, mimeType := classify(tt.filename, tt.detectedMime, nil)
			if mediaType != tt.wantType || mimeType != tt.wantMime {
				t.Fatalf("classify(%q, %q) = (%q, %q), want (%q, %q)",
					tt.filename, tt.detectedMime, mediaType, mimeType, tt.wantType, tt.wantMime)
			}
		})
	}
}

// TestClassifyRejectsImageExtensionWithoutImageContent 是本轮新增的守护测试（R3-05）。
//
// extensionLookup 支持的每一种图片格式，Go 的内容嗅探本来都认得出来。所以当嗅探
// 返回「不认识」（application/octet-stream）而文件名说是图片时，两者必有一假，
// 而假的只可能是文件名。改动前这一支会照着扩展名放行，于是
// `payload.webp`（700 字节随机二进制）能拿到 201 与 mime_type=image/webp，
// 并被逐字节原样回放——站点成了任意文件托管点，同时绕掉了「图片一律重新编码」。
func TestClassifyRejectsImageExtensionWithoutImageContent(t *testing.T) {
	notAnImage := []byte("this is definitely not an image, it is just text padding")

	cases := []struct {
		name     string
		filename string
		head     []byte
	}{
		{"webp extension, arbitrary bytes", "payload.webp", notAnImage},
		{"png extension, arbitrary bytes", "payload.png", notAnImage},
		{"jpg extension, arbitrary bytes", "payload.jpg", notAnImage},
		{"gif extension, arbitrary bytes", "payload.gif", notAnImage},
		{"bmp extension, arbitrary bytes", "payload.bmp", notAnImage},
		{"tiff extension, arbitrary bytes", "payload.tiff", notAnImage},
		{"webp extension, truncated head", "payload.webp", []byte("RIFF")},
		{"zip disguised as an image", "payload.png", []byte("PK\x03\x04\x14\x00\x00\x00")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mediaType, mimeType := classify(tc.filename, "application/octet-stream", tc.head)
			if mediaType != "" || mimeType != "" {
				t.Fatalf("classify(%q) = (%q, %q), want rejection: the content does not match the extension",
					tc.filename, mediaType, mimeType)
			}
		})
	}
}

// TestClassifyAcceptsImageExtensionWithMatchingSignature 是上一条的反面：
// 带正确魔数时兜底必须照常放行——否则合法上传会被误拒。
func TestClassifyAcceptsImageExtensionWithMatchingSignature(t *testing.T) {
	cases := []struct {
		filename string
		head     []byte
		wantMime string
	}{
		{"photo.webp", []byte("RIFF\x24\x00\x00\x00WEBPVP8 "), "image/webp"},
		{"photo.png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), "image/png"},
		{"photo.jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}, "image/jpeg"},
		{"photo.gif", []byte("GIF89a\x01\x00\x01\x00"), "image/gif"},
		{"photo.bmp", []byte("BM\x36\x00\x00\x00"), "image/bmp"},
		{"photo.tiff", []byte("II\x2a\x00\x08\x00\x00\x00"), "image/tiff"},
		{"photo.tif", []byte("MM\x00\x2a\x00\x00\x00\x08"), "image/tiff"},
	}

	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			mediaType, mimeType := classify(tc.filename, "application/octet-stream", tc.head)
			if mediaType != "image" || mimeType != tc.wantMime {
				t.Fatalf("classify(%q) = (%q, %q), want (image, %q)",
					tc.filename, mediaType, mimeType, tc.wantMime)
			}
		})
	}
}

// TestClassifyStillFallsBackForAudioAndVideo 钉住「只收紧图片、不动音视频」这条边界。
// 容器格式太多、嗅探认不全正是兜底存在的理由，而伪造一个 .mkv 的后果只是播不出来。
func TestClassifyStillFallsBackForAudioAndVideo(t *testing.T) {
	cases := []struct {
		filename string
		wantType string
		wantMime string
	}{
		{"song.flac", "audio", "audio/flac"},
		{"movie.mkv", "video", "video/x-matroska"},
		{"clip.avi", "video", "video/x-msvideo"},
	}

	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			mediaType, mimeType := classify(tc.filename, "application/octet-stream", []byte("\x00\x01\x02\x03"))
			if mediaType != tc.wantType || mimeType != tc.wantMime {
				t.Fatalf("classify(%q) = (%q, %q), want (%q, %q)",
					tc.filename, mediaType, mimeType, tc.wantType, tc.wantMime)
			}
		})
	}
}

func TestExtensionForVideo(t *testing.T) {
	tests := []struct {
		mimeType string
		filename string
		want     string
	}{
		{mimeType: "video/mp4", filename: "clip.mp4", want: ".mp4"},
		{mimeType: "video/webm", filename: "clip.webm", want: ".webm"},
		{mimeType: "video/x-matroska", filename: "", want: ".mkv"},
		{mimeType: "video/quicktime", filename: "movie.mov", want: ".mov"},
		{mimeType: "video/ogg", filename: "", want: ".ogv"},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			got := extensionFor(tt.mimeType, tt.filename)
			if got != tt.want {
				t.Fatalf("extensionFor(%q, %q) = %q, want %q", tt.mimeType, tt.filename, got, tt.want)
			}
		})
	}
}

// TestExtensionForNeverUsesTheUploaderExtension 是本轮新增的守护测试（R3-05 第二条）。
//
// 改动前 extensionFor 的最后一个分支是 `return ext`——把**上传者提供的**扩展名
// 直接写进落盘文件名。只要内容类型不在 canonicalExtension 里就会走到那里，
// 而 net/http 的嗅探结果恰好有两个这样的名字：WAV 是 audio/wave、AVI 是 video/avi
// （表里写的是 audio/wav、video/x-msvideo）。于是「合法 WAV 内容 + 文件名 song.php」
// 会在磁盘上产生 media/<32hex>.php。
//
// 本应用不把 media/ 暴露给 HTTP，所以这不是直接的 RCE；但落盘扩展名本来就没有
// 功能作用（文件按数据库里的 mime_type 回放），因此没有任何理由接受上传者的取值。
func TestExtensionForNeverUsesTheUploaderExtension(t *testing.T) {
	hostile := []string{"song.php", "payload.html", "x.js", "run.exe", "shell.sh", "a.svg", "no-extension"}

	for _, mimeType := range []string{"audio/wave", "video/avi", "application/octet-stream", "totally/unknown"} {
		for _, filename := range hostile {
			got := extensionFor(mimeType, filename)
			if got != ".bin" && got != ".wav" && got != ".avi" {
				t.Fatalf("extensionFor(%q, %q) = %q; the uploader-supplied extension must never reach disk",
					mimeType, filename, got)
			}
			for _, bad := range []string{".php", ".html", ".js", ".exe", ".sh", ".svg", ".htm"} {
				if got == bad {
					t.Fatalf("extensionFor(%q, %q) = %q — an executable/scriptable extension was written to disk",
						mimeType, filename, got)
				}
			}
		}
	}

	// 明确钉住两个别名映射：它们正是原先会掉进兜底分支的那两个。
	if got := extensionFor("audio/wave", "song.php"); got != ".wav" {
		t.Fatalf("extensionFor(\"audio/wave\", \"song.php\") = %q, want .wav", got)
	}
	if got := extensionFor("video/avi", "movie.php"); got != ".avi" {
		t.Fatalf("extensionFor(\"video/avi\", \"movie.php\") = %q, want .avi", got)
	}
	// 完全未知的内容类型：拿不准就用惰性的 .bin。
	if got := extensionFor("totally/unknown", "payload.php"); got != ".bin" {
		t.Fatalf("extensionFor(unknown) = %q, want .bin", got)
	}
}

// extensionTables lists the extension maps that extensionLookup is built from.
func extensionTables() map[string]map[string]string {
	return map[string]map[string]string{
		"image": imageExtensions,
		"audio": audioExtensions,
		"video": videoExtensions,
	}
}

// TestExtensionTablesDoNotOverlap guards the flattened extensionLookup map.
//
// If one extension were listed under two media types, the map would keep
// whichever entry the iteration happened to write last, so classify and
// extensionFor could resolve that extension differently between runs.
func TestExtensionTablesDoNotOverlap(t *testing.T) {
	owner := make(map[string]string)
	for mediaType, table := range extensionTables() {
		for ext := range table {
			if other, ok := owner[ext]; ok {
				t.Errorf("extension %q is listed under both %q and %q", ext, other, mediaType)
				continue
			}
			owner[ext] = mediaType
		}
	}

	if len(extensionLookup) != len(owner) {
		t.Errorf("len(extensionLookup) = %d, want %d", len(extensionLookup), len(owner))
	}
}

// TestEveryKnownMimeTypeHasACanonicalExtension asserts the invariant
// extensionFor relies on. Without an entry, an upload whose filename carries no
// usable extension would be stored with no extension at all.
func TestEveryKnownMimeTypeHasACanonicalExtension(t *testing.T) {
	for ext, info := range extensionLookup {
		if _, ok := canonicalExtension[info.mimeType]; !ok {
			t.Errorf("content type %q (from extension %q) has no canonical extension", info.mimeType, ext)
		}
	}
}
