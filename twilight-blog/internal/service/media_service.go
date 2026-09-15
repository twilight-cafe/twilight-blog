package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	// 只有被注册进来的格式，image.DecodeConfig 才认得。这里刻意只注册
	// **会被解码**的那几种（见 checkImageBudget 的说明）：多注册一个用不上的
	// 解析器就等于多一份攻击面。
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"

	"blog-engine/internal/logging"
	"blog-engine/internal/mediaconv"
	"blog-engine/internal/model"
)

// 媒体上传的哨兵错误，会被 handler 映射成 4xx：
// ErrUnsupportedMediaType（类型不受支持）、ErrEmptyFile（空文件）、
// ErrImageTooLarge（解码尺寸超预算）与 ErrBusy（转码槽位占满，可重试）。
var (
	ErrUnsupportedMediaType = errors.New("unsupported media type")
	ErrEmptyFile            = errors.New("uploaded file is empty")
	ErrImageTooLarge        = errors.New("image is too large to decode")
	ErrBusy                 = errors.New("media converter is busy")
)

// 解码资源闸门。这三个常量共同决定「一次上传最坏能吃掉多少内存」，改动任何一个
// 都要重新算一遍：maxImagePixels × 4 字节 × maxConcurrentTranscodes。
//
// **为什么按像素而不是按文件大小设限**：图片解压炸弹的载荷本身很小。一张
// 16000×16000 的纯色 PNG 压缩后可能只有几十 KB，远低于 max_upload_size 的 50MB，
// 但 libwebp 解码时要按 宽×高×4 分配缓冲区（约 1GB）。所以下调上传体积上限
// （BodyLimit）对这条路完全没有作用——唯一能挡住它的，是在**解码之前**读图片
// 头部里声明的尺寸。
//
// 注意 cwebp 自己也有单边上限（libwebp 的 WEBP_MAX_DIMENSION = 16383），但它
// 是在**分配完缓冲区之后**才检查的，挡不住内存峰值。
const (
	// maxImagePixels 是单张图片允许的解码后像素数。4000 万像素覆盖到 8K 截图
	// 与几乎所有消费级相机（含 4000 万像素级全画幅），而单次解码的峰值内存
	// 约 160MB。
	maxImagePixels = 40_000_000
	// maxImageDimension 是单边上限，与 libwebp 自身的 WEBP_MAX_DIMENSION 对齐：
	// 超过它的图 cwebp 本来也会拒绝，提前拒绝能给出更清楚的错误信息。
	maxImageDimension = 16383
	// maxConcurrentTranscodes 是同时进行的转码数量。
	//
	// 像素闸门管的是「单个文件最多解码多大」，这一项管的是「同时解码几个」，
	// 两者相乘才是峰值内存（默认配置下最坏约 320MB）。没有它，N 个并发上传
	// 就能把单文件的上限乘以 N。
	maxConcurrentTranscodes = 2
)

// imageExtensions / audioExtensions / videoExtensions 是三张「文件扩展名 → 内容类型」
// 对照表。它们只在「内容嗅探无法得出结论」时作为兜底使用（见 classify）。
var imageExtensions = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".bmp":  "image/bmp",
	".tif":  "image/tiff",
	".tiff": "image/tiff",
	".webp": "image/webp",
}

var supportedImageMimes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/bmp":  true,
	"image/tiff": true,
	"image/webp": true,
}

var audioExtensions = map[string]string{
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".opus": "audio/ogg",
	".flac": "audio/flac",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
}

var videoExtensions = map[string]string{
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".mov":  "video/quicktime",
	".qt":   "video/quicktime",
	".avi":  "video/x-msvideo",
	".mkv":  "video/x-matroska",
	".webm": "video/webm",
	".ogv":  "video/ogg",
	".wmv":  "video/x-ms-wmv",
	".flv":  "video/x-flv",
	".mpeg": "video/mpeg",
	".mpg":  "video/mpeg",
	".m2v":  "video/mpeg",
	".ts":   "video/mp2t",
	".mts":  "video/mp2t",
	".m2ts": "video/mp2t",
	".3gp":  "video/3gpp",
	".3g2":  "video/3gpp2",
}

// extensionInfo 表示某个文件名扩展名对上传内容的推断结果。
type extensionInfo struct {
	mediaType string
	mimeType  string
}

// extensionLookup 把上面三张表拍平成一张，使 classify 与 extensionFor 里的扩展名兜底
// 无需逐表试探。
//
// TestExtensionTablesDoNotOverlap 守护一条不变量：同一个扩展名不得同时出现在两类媒体下
// ——否则这里的「谁后写入谁生效」就是随机的，兜底结果会变得不确定。
var extensionLookup = func() map[string]extensionInfo {
	lookup := make(map[string]extensionInfo, len(imageExtensions)+len(audioExtensions)+len(videoExtensions))
	for mediaType, table := range map[string]map[string]string{
		"image": imageExtensions,
		"audio": audioExtensions,
		"video": videoExtensions,
	} {
		for ext, mimeType := range table {
			lookup[ext] = extensionInfo{mediaType: mediaType, mimeType: mimeType}
		}
	}
	return lookup
}()

// canonicalExtension 给出每种内容类型的标准扩展名，
// 用于「上传文件名里的扩展名与嗅探出的类型不一致或缺失」的情况。
var canonicalExtension = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/bmp":  ".bmp",
	"image/tiff": ".tiff",
	"image/webp": ".webp",
	"audio/mpeg": ".mp3",
	"audio/wav":  ".wav",
	// net/http 的内容嗅探对 WAV 返回的是 audio/wave（不是上面表里的 audio/wav）。
	// 两个名字都要有映射，否则 WAV 上传会掉进 extensionFor 的兜底分支——
	// 而那正是「落盘扩展名取自上传者」这个缺口的入口。
	"audio/wave":       ".wav",
	"audio/ogg":        ".ogg",
	"audio/flac":       ".flac",
	"audio/mp4":        ".m4a",
	"audio/aac":        ".aac",
	"audio/webm":       ".webm",
	"video/mp4":        ".mp4",
	"video/quicktime":  ".mov",
	"video/x-msvideo":  ".avi",
	"video/avi":        ".avi", // 同上：嗅探器对 AVI 返回 video/avi
	"video/x-matroska": ".mkv",
	"video/webm":       ".webm",
	"video/ogg":        ".ogv",
	"video/x-ms-wmv":   ".wmv",
	"video/x-flv":      ".flv",
	"video/mpeg":       ".mpeg",
	"video/mp2t":       ".ts",
	"video/3gpp":       ".3gp",
	"video/3gpp2":      ".3g2",
}

// transcoding 描述一次上传可能需要的转码动作。
type transcoding struct {
	// ext 与 mimeType 描述转码后实际落盘的结果。
	ext      string
	mimeType string
	// label 与 tool 只用于日志。
	label string
	tool  string
	// ext/mimeType 同时决定中间产物的扩展名——它现在落在私有临时目录里，
	// 不再需要单独的命名模式（见 StoreFile 的说明）。
	convert func(ctx context.Context, src, dst string) error
}

// MediaService 处理媒体上传与删除。
//
// 目录约定：所有文件都落在 mediaDir 下，文件名（StorageName）由服务层生成，
// 与用户上传时的原始名无关——OriginalName 只作为展示与留档使用。
//
// 存储策略：图片一律转为 WebP；音频在配置了转换器时转 WebM；
// 其余类型原样落盘。是否转换由 mediaconv.Converter 的能力开关决定。
type MediaService struct {
	repo     model.MediaRepository
	mediaDir string
	conv     *mediaconv.Converter
	// transcodeSlots 是转码的并发闸门，容量为 maxConcurrentTranscodes。
	// 它是一个有缓冲的 channel 而不是信号量库：Go 的 channel 本身就是
	// 「带阻塞的计数信号量」，够用且没有额外依赖。
	transcodeSlots chan struct{}
}

// NewMediaService 构造服务，并为两个可选依赖补上安全的默认值：
// mediaDir 为空时落到 "media"，conv 为 nil 时用零值转换器（即不启用任何转码）。
func NewMediaService(repo model.MediaRepository, mediaDir string, conv *mediaconv.Converter) *MediaService {
	if mediaDir == "" {
		mediaDir = "media"
	}
	if conv == nil {
		conv = &mediaconv.Converter{}
	}
	return &MediaService{
		repo:           repo,
		mediaDir:       mediaDir,
		conv:           conv,
		transcodeSlots: make(chan struct{}, maxConcurrentTranscodes),
	}
}

// withTranscodeSlot 占用一个转码槽位执行 fn，槽位占满时不排队而是立即失败。
//
// 选择「快速失败」而不是「排队等待」：转码最长可以跑 2 分钟（见
// mediaconv.conversionTimeout），排队会让请求在服务端堆积成一层看不见的队列，
// 上传方看到的是一个悬而未决的连接。直接回一个可重试的错误，语义更清楚。
func (s *MediaService) withTranscodeSlot(ctx context.Context, fn func() error) error {
	select {
	case s.transcodeSlots <- struct{}{}:
		defer func() { <-s.transcodeSlots }()
		return fn()
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrBusy
	}
}

// StoreFile 校验并落盘一个上传文件，然后写入数据库记录。
//
// 处理顺序（每一步失败都会在返回前清理已产生的临时/落盘文件）：
//  1. 读前 512 字节做内容嗅探（http.DetectContentType），空文件直接拒绝；
//  2. classify 依据嗅探结果判定媒体类型，嗅探无结论时才回退到扩展名；
//  3. **图片再过一道解码尺寸闸门**（checkImageBudget）。必须排在解码之前，
//     因为解压炸弹的载荷很小、解码结果很大，按文件大小设限挡不住它；
//  4. 若需要转码，先转到临时文件、成功后再移动到最终位置——
//     这样「转码到一半」的结果永远不会以最终文件名出现在媒体目录里；
//  5. 落盘成功后写库；写库失败则把刚落的文件删掉，避免出现无记录的孤儿文件。
//
// 转码还受一个并发槽位约束（withTranscodeSlot）：像素闸门管单张图能解码多大，
// 槽位管同时解码几张，两者相乘才是进程的峰值内存。
//
// 注意写库与落盘不是原子的：进程若在两者之间被杀，可能留下一个有文件无记录的孤儿，
// 反之（有记录无文件）在这里被显式避免了。
func (s *MediaService) StoreFile(ctx context.Context, header *multipart.FileHeader) (*model.Media, error) {
	logging.Debugf("[media] storing upload filename=%q size=%d", header.Filename, header.Size)
	src, err := header.Open()
	if err != nil {
		logging.Errorf("[media] open uploaded file failed filename=%q: %v", header.Filename, err)
		return nil, fmt.Errorf("open uploaded file: %w", err)
	}
	defer src.Close()

	head := make([]byte, 512)
	n, err := io.ReadFull(src, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		logging.Errorf("[media] read uploaded file failed filename=%q: %v", header.Filename, err)
		return nil, fmt.Errorf("read uploaded file: %w", err)
	}
	if n == 0 {
		return nil, ErrEmptyFile
	}

	detectedMime := http.DetectContentType(head[:n])
	mediaType, mimeType := classify(header.Filename, detectedMime, head[:n])
	logging.Debugf("[media] detected filename=%q media_type=%s mime_type=%s", header.Filename, mediaType, mimeType)
	if mediaType == "" {
		logging.Warnf("[media] unsupported media type filename=%q detected_mime=%s", header.Filename, detectedMime)
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedMediaType, detectedMime)
	}

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		logging.Errorf("[media] rewind uploaded file failed filename=%q: %v", header.Filename, err)
		return nil, fmt.Errorf("rewind uploaded file: %w", err)
	}

	// 图片必须在**解码之前**过一道尺寸闸门（理由见 maxImagePixels 的说明）。
	// 只对「会被解码」的格式做：已经是 WebP 的图不转码、原样落盘，既没有解码
	// 放大，而 webp 头部解析的支持面（无损/动图变体）反而比这里需要的窄——
	// 把它拉进来只会制造误拒。
	if mediaType == "image" && !isWebP(mimeType) {
		width, height, err := imageDimensions(src)
		if err != nil {
			logging.Warnf("[media] cannot read image header filename=%q: %v", header.Filename, err)
			return nil, fmt.Errorf("%w: cannot read the image header", ErrUnsupportedMediaType)
		}
		if err := checkImageBudget(width, height); err != nil {
			logging.Warnf("[media] rejecting oversized image filename=%q dimensions=%dx%d: %v",
				header.Filename, width, height, err)
			return nil, err
		}
		// 头部解析消费掉了前若干字节，拷贝到最终位置之前要重新回到起点。
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			logging.Errorf("[media] rewind after the dimension check failed filename=%q: %v", header.Filename, err)
			return nil, fmt.Errorf("rewind uploaded file: %w", err)
		}
	}

	// 所有中间产物都放进一个**私有**临时目录，而不是直接落在系统临时目录里。
	//
	// 这不是洁癖，是一处真实的本地提权缺口（在 Linux 上尤其严重）：
	// 转码器需要「自己创建输出文件」，而 os.CreateTemp 只能给出一个已经存在的
	// 文件，于是旧实现是「创建 → 关闭 → **删除** → 把路径交给子进程重建」。
	// 在 /tmp（Linux 下是 1777，所有人可写且可列举）里，从删除到子进程 open()
	// 之间隔着一整个 fork+exec（毫秒级），本机任何其他用户都能用 inotify 盯住
	// 这个文件名，抢先用符号链接占位；转码器随后会**跟随符号链接**写入，
	// 于是内容落到了攻击者指定的路径上——以博客进程的身份。
	// 可写 settings.json / blog.db，或往 ~/.ssh/authorized_keys 里塞一行。
	//
	// os.MkdirTemp 建出来的目录是 0700 且属主是本进程，别人无法在其中创建任何
	// 条目，竞态窗口因此**不复存在**（不是变小，是没有了）。defer RemoveAll
	// 连清理都一并解决，比原先逐个 Remove 更不容易漏。
	tmpDir, err := os.MkdirTemp("", "media-upload-")
	if err != nil {
		return nil, fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpInputName := filepath.Join(tmpDir, "input"+safeExt(header.Filename))
	tmpInput, err := os.OpenFile(tmpInputName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create temp input: %w", err)
	}

	if _, err := io.Copy(tmpInput, src); err != nil {
		tmpInput.Close()
		return nil, fmt.Errorf("copy uploaded file to temp: %w", err)
	}
	if err := tmpInput.Close(); err != nil {
		return nil, fmt.Errorf("close temp input: %w", err)
	}

	storageBase, err := randomHex(16)
	if err != nil {
		return nil, fmt.Errorf("generate storage name: %w", err)
	}

	storageName := storageBase + extensionFor(mimeType, header.Filename)
	finalMime := mimeType
	converted := false

	// 图片一律转成 WebP；音频在配置了转换器时转成 WebM；
	// 其余类型原样落盘，不做任何处理。
	//
	// ⚠️ 转换器**不可用**时必须退化成「原样落盘」，而不是拒绝上传。
	// 旧实现在 !WebPEnabled 时直接返回 ErrUnsupportedMediaType（→ 400），
	// 这与本包与 mediaconv 的设计声明（「找不到工具只关闭对应能力，
	// 上传照常进行」/ router 里那句 "Images will be stored as-is"）**直接矛盾**。
	// 后果在 Linux 上尤其明显：发行版默认不装 cwebp/ffmpeg，于是**任何 JPG/PNG
	// 都传不上去**，博客没法配图。
	//
	// 代价是失去「重新编码以抹掉多态载荷」这层纵深防御——但内容嗅探
	// （classify，不信任客户端 Content-Type）、nosniff 与 CSP 都还在，
	// 而「完全不能传图」显然比这层加固更糟。
	var plan *transcoding
	switch {
	case mediaType == "image" && !isWebP(mimeType):
		if !s.conv.WebPEnabled {
			logging.Warnf("[media] no WebP converter available; storing %q in its original format "+
				"(install cwebp or ffmpeg to enable conversion)", header.Filename)
			break
		}
		plan = &transcoding{
			ext:      ".webp",
			mimeType: "image/webp",
			label:    "WebP",
			tool:     s.conv.WebPTool,
			convert:  s.conv.ConvertImageToWebP,
		}
	case mediaType == "audio" && s.conv.WebMEnabled && !isWebM(mimeType):
		plan = &transcoding{
			ext:      ".webm",
			mimeType: "audio/webm",
			label:    "WebM",
			tool:     s.conv.WebMTool,
			convert:  s.conv.ConvertAudioToWebM,
		}
	}

	if plan == nil {
		if err := s.saveFile(tmpInputName, storageName); err != nil {
			logging.Errorf("[media] save uploaded file failed filename=%q: %v", header.Filename, err)
			return nil, err
		}
	} else {
		storageName = storageBase + plan.ext
		finalMime = plan.mimeType
		converted = true
		// 转码要占一个并发槽位：像素闸门限住的是单张图能解码多大，
		// 这里限住的是同时能解码几张（见 maxConcurrentTranscodes）。
		if err := s.withTranscodeSlot(ctx, func() error {
			return s.applyTranscoding(ctx, header.Filename, tmpInputName, storageName, plan, tmpDir)
		}); err != nil {
			return nil, err
		}
	}

	finalPath := filepath.Join(s.mediaDir, storageName)
	info, err := os.Stat(finalPath)
	if err != nil {
		removeQuietly(finalPath)
		logging.Errorf("[media] stat stored file failed path=%s: %v", finalPath, err)
		return nil, fmt.Errorf("stat stored media: %w", err)
	}

	media := &model.Media{
		StorageName:  storageName,
		OriginalName: header.Filename,
		MediaType:    mediaType,
		MimeType:     finalMime,
		Size:         info.Size(),
		Converted:    converted,
		CreatedAt:    time.Now().Unix(),
	}

	if err := s.repo.Create(ctx, media); err != nil {
		removeQuietly(finalPath)
		logging.Errorf("[media] create media record failed filename=%q storage_name=%s: %v", header.Filename, storageName, err)
		return nil, err
	}

	logging.Infof("[media] stored id=%d filename=%q storage_name=%s type=%s mime=%s size=%d converted=%v", media.ID, header.Filename, media.StorageName, media.MediaType, media.MimeType, media.Size, media.Converted)
	return media, nil
}

func (s *MediaService) Get(ctx context.Context, id int64) (*model.Media, error) {
	logging.Debugf("[media] get id=%d", id)
	return s.repo.GetByID(ctx, id)
}

func (s *MediaService) List(ctx context.Context, p model.Pagination) ([]model.Media, int64, error) {
	logging.Debugf("[media] list page=%d page_size=%d", p.Page, p.PageSize)
	return s.repo.List(ctx, p)
}

// Delete 删除媒体记录与磁盘文件。
//
// 顺序是先删库、再删文件：库删除失败就直接返回（文件还留着，可重试）；
// 文件删除失败只记警告、仍然返回成功——此时记录已经没了，
// 磁盘上留下一个无人引用的文件，比让接口报错更有用（日志里可追）。
func (s *MediaService) Delete(ctx context.Context, id int64) error {
	logging.Debugf("[media] deleting id=%d", id)
	media, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logging.Errorf("[media] get for delete failed id=%d: %v", id, err)
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		logging.Errorf("[media] delete record failed id=%d: %v", id, err)
		return err
	}
	path := s.Path(media)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		logging.Warnf("[media] remove file after delete failed id=%d path=%s: %v", id, path, err)
	} else {
		logging.Debugf("[media] removed file path=%s", path)
	}
	logging.Infof("[media] deleted id=%d storage_name=%s", id, media.StorageName)
	return nil
}

// Path 返回该媒体在磁盘上的绝对或相对路径（由 mediaDir 与 StorageName 拼成）。
func (s *MediaService) Path(media *model.Media) string {
	return filepath.Join(s.mediaDir, media.StorageName)
}

// applyTranscoding 先把转码结果写到中间文件，成功后再移动到最终位置。
// 这样即使转码失败或只写了一半，媒体目录里也不会出现一个以最终文件名命名的坏文件。
//
// tmpDir 必须是调用方用 os.MkdirTemp 建出来的**私有**目录（0700、属主是本进程）。
// 不能改成系统临时目录下的一个固定/可预测路径：转码器需要自己创建输出文件，
// 而一个别人能往里放条目的目录就等于把写入目标交给了别人（符号链接跟随）。
// 详见 StoreFile 里那段说明。
func (s *MediaService) applyTranscoding(ctx context.Context, filename, srcPath, dstName string, plan *transcoding, tmpDir string) error {
	tmpOutput := filepath.Join(tmpDir, "output"+plan.ext)

	logging.Debugf("[media] converting to %s filename=%q tool=%s", plan.label, filename, plan.tool)
	if err := plan.convert(ctx, srcPath, tmpOutput); err != nil {
		logging.Errorf("[media] %s conversion failed filename=%q: %v", plan.label, filename, err)
		return err
	}
	if err := s.saveFile(tmpOutput, dstName); err != nil {
		logging.Errorf("[media] save %s output failed filename=%q: %v", plan.label, filename, err)
		return err
	}
	logging.Debugf("[media] converted filename=%q label=%s storage_name=%s", filename, plan.label, dstName)
	return nil
}

// removeQuietly 删除一个「已知不再需要」的文件：转换器的中间产物，
// 或是一条数据库记录没能写成功、因而失去了归属的落盘文件。
//
// 失败只上报不上抛。调用它时要么正在展开一个更重要的错误，要么只是收尾清理，
// 调用方即便拿到删除失败也无从处理。记一条日志能让残留文件可被追查，
// 总好过让清理悄无声息地半途而废。
func removeQuietly(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		logging.Warnf("[media] cleanup failed path=%s: %v", path, err)
	}
}

// saveFile 把 srcName 复制到媒体目录下的 dstName（必要时先建目录）。
// 复制或关闭失败时会删掉写到一半的目标文件，不留残缺产物。
//
// 目录 0700、文件 0600：媒体目录里放的是**尚未发布**的素材（先传图再写文章是
// 常规流程），而同机其他用户没有任何理由读得到它们。此前是 0755/0644，
// 与 settings.json（0600）、Meilisearch 数据目录（0700）的口径不一致。
//
// MkdirAll 对**已存在**的目录不做任何改动，所以还要显式 Chmod 一次——
// 否则升级只对新部署生效，老部署的 0755 目录会一直留到有人手工改。
// 沿用 database.Open 与 search.manager 的同一条做法：失败只告警，不影响上传。
func (s *MediaService) saveFile(srcName, dstName string) error {
	if err := os.MkdirAll(s.mediaDir, 0o700); err != nil {
		return fmt.Errorf("create media dir: %w", err)
	}
	if err := os.Chmod(s.mediaDir, 0o700); err != nil {
		logging.Warnf("[media] tightening media dir permissions failed dir=%s: %v", s.mediaDir, err)
	}

	src, err := os.Open(srcName)
	if err != nil {
		return fmt.Errorf("open temp output: %w", err)
	}
	defer src.Close()

	dstPath := filepath.Join(s.mediaDir, dstName)
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create stored media: %w", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		removeQuietly(dstPath)
		return fmt.Errorf("copy stored media: %w", err)
	}
	if err := dst.Close(); err != nil {
		removeQuietly(dstPath)
		return fmt.Errorf("close stored media: %w", err)
	}
	return nil
}

// scriptableMimes 是浏览器会当作「可执行内容」解释的类型。
//
// 安全要点：只要上传内容嗅探出这些类型之一，就绝不允许再依据文件名把它改判成
// 图片或媒体资源。否则一个精心构造的文件就能以「同源资源」的身份被浏览器执行脚本
// ——这是存储型 XSS 的经典路径。
var scriptableMimes = map[string]bool{
	"text/html":              true,
	"text/xml":               true,
	"text/ecmascript":        true,
	"text/javascript":        true,
	"application/xhtml+xml":  true,
	"application/xml":        true,
	"application/javascript": true,
	"application/ecmascript": true,
	"image/svg+xml":          true,
}

// classify 依据嗅探出的内容类型判定上传的媒体类型，
// 只有在嗅探无法得出结论时才回退到文件扩展名。
//
// 为什么需要扩展名兜底：http.DetectContentType 只认得少数几种容器格式，
// 像 flac、mkv 这类合法格式会被它判成通用二进制，不给兜底就没法上传。
// 但兜底绝不能触及「字节已经明确是文本或脚本」的情况——那一分支在上面提前返回。
//
// head 是文件开头若干字节，只用于兜底路径的图片魔数复核（见下方的说明）。
func classify(filename, detectedMime string, head []byte) (mediaType, mimeType string) {
	detectedMime = normalizeMime(detectedMime)

	if scriptableMimes[detectedMime] || strings.HasPrefix(detectedMime, "text/") {
		logging.Warnf("[media] rejecting upload with scriptable content type filename=%q detected_mime=%s", filename, detectedMime)
		return "", ""
	}

	if strings.HasPrefix(detectedMime, "image/") && supportedImageMimes[detectedMime] {
		return "image", detectedMime
	}
	if strings.HasPrefix(detectedMime, "video/") {
		return "video", detectedMime
	}
	if strings.HasPrefix(detectedMime, "audio/") {
		return "audio", detectedMime
	}

	ext := strings.ToLower(filepath.Ext(filename))
	if info, ok := extensionLookup[ext]; ok {
		// 图片的兜底必须再验一次魔数。
		//
		// 原因：extensionLookup 支持的**每一种图片格式，Go 的内容嗅探本来就能认出来**
		// （png/jpeg/gif/bmp/tiff/webp 全在 net/http 的签名表里）。所以走到兜底
		// 这一支时嗅探说的是「我不认识这些字节」——此时再用扩展名把它判成图片，
		// 等于让上传者用**文件名**指定内容类型。实测后果：700 字节的随机二进制
		// 命名 payload.webp 会得到 201 与 mime_type=image/webp，并被逐字节原样回放
		// （第三轮渗透测试 R3-05）。它同时绕掉了「图片一律重新编码」这条防线
		// ——那条防线正是用来消除 polyglot 之类夹带内容的手段。
		//
		// 不升级为 XSS：响应带 X-Content-Type-Options: nosniff，且 MIME 只能取自
		// 固定表（image/audio/video），text/html 与 SVG 在更前面就被拒了。
		// 但「站点可以托管任意二进制」本身就该被堵住。
		//
		// 只对图片做这项复核，不动音视频：那两类才是兜底存在的理由
		// （容器格式太多，嗅探认不全），而伪造一个 .mkv 的后果只是「播不出来」。
		if supportedImageMimes[info.mimeType] && !hasImageSignature(info.mimeType, head) {
			logging.Warnf("[media] rejecting upload whose content does not match its image extension filename=%q claimed_mime=%s", filename, info.mimeType)
			return "", ""
		}
		return info.mediaType, info.mimeType
	}
	return "", ""
}

// hasImageSignature 报告 head 是否带有 mimeType 对应格式的魔数。
//
// 纯字节比对，不解码：这几种格式的签名都位于固定偏移，长度也固定，
// 因此这里不可能被「构造一个恰好解析得过又夹带别的内容」的文件绕过
// ——那正是要靠重新编码来防的东西，而这条路径恰恰跳过了编码。
func hasImageSignature(mimeType string, head []byte) bool {
	has := func(offset int, signature string) bool {
		return len(head) >= offset+len(signature) && string(head[offset:offset+len(signature)]) == signature
	}

	switch mimeType {
	case "image/jpeg":
		return len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF
	case "image/png":
		return has(0, "\x89PNG\r\n\x1a\n")
	case "image/gif":
		return has(0, "GIF87a") || has(0, "GIF89a")
	case "image/bmp":
		return has(0, "BM")
	case "image/tiff":
		return has(0, "II\x2a\x00") || has(0, "MM\x00\x2a")
	case "image/webp":
		// RIFF????WEBP：中间四字节是长度字段，不参与判断。
		return has(0, "RIFF") && has(8, "WEBP")
	default:
		return false
	}
}

// normalizeMime 去掉 http.DetectContentType 为文本类型附加的参数（如 "; charset=utf-8"），
// 使后面的比较不会因为多出的参数而失配。
func normalizeMime(value string) string {
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = value[:index]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

// imageDimensions 只解析图片头部，返回其中声明的宽高。
//
// image.DecodeConfig 不解码像素数据，因此这一步的开销与图片实际有多大**无关**
// ——正是这个性质让它能当闸门用。能解析的格式由本文件的空白 import 决定
// （png / jpeg / gif / bmp / tiff），与 supportedImageMimes 里会被解码的集合一致。
func imageDimensions(r io.Reader) (int, int, error) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// checkImageBudget 判断一张已声明尺寸的图片是否在解码预算之内。
//
// 宽高来自文件内容，所以这里是**不可信输入**，两处都要防：
//   - 非正数：既没有意义，也会让后面的乘法得出奇怪的结果；
//   - 乘法溢出：用 int64 计算。32 位平台上 int 相乘可能溢出成负数，
//     从而让「超大图」通过 `> maxImagePixels` 的比较。
func checkImageBudget(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("%w: the image declares a non-positive size (%dx%d)", ErrImageTooLarge, width, height)
	}
	if width > maxImageDimension || height > maxImageDimension {
		return fmt.Errorf("%w: %dx%d exceeds the %d pixel per-side limit",
			ErrImageTooLarge, width, height, maxImageDimension)
	}
	if pixels := int64(width) * int64(height); pixels > maxImagePixels {
		return fmt.Errorf("%w: %.1f megapixels exceeds the %d megapixel limit",
			ErrImageTooLarge, float64(pixels)/1e6, maxImagePixels)
	}
	return nil
}

// safeExt 取出一个「可安全用于拼文件名」的扩展名：只允许小写字母与数字、
// 长度不超过 10，否则返回空串。这既避免了把用户输入直接拼进路径，
// 也避免构造出带路径分隔符或 .. 的名字。
func safeExt(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" || len(ext) > 10 {
		return ""
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}

// extensionFor 决定文件落盘时使用哪个扩展名：
// 原文件名的扩展名若与判定出的内容类型一致就沿用（保留 .jpeg/.tiff 这类写法），
// 否则改用该内容类型的标准扩展名。
// extensionFor 决定文件落盘时使用哪个扩展名：
//  1. 上传文件名里的扩展名，**前提是它就在我们自己的对照表里、且与内容类型一致**；
//  2. 否则用内容类型对应的标准扩展名；
//  3. 再没有就退到 .bin。
//
// ⚠️ 第 3 步曾经是 `return ext`——直接把**上传者提供的**扩展名写进落盘文件名。
// 当嗅探出的内容类型不在 canonicalExtension 里时（例如 WAV 的 audio/wave、
// AVI 的 video/avi：这两个是 net/http 嗅探器用的名字，与表里的 audio/wav、
// video/x-msvideo 并不相同），这个分支就会生效，于是
// `song.php`（内容是合法的 WAV 头）+ 伪造扩展名 = 磁盘上出现 media/<32hex>.php
// （第三轮渗透测试 R3-05 的第二条）。
//
// 本应用不把 media/ 暴露给 HTTP（web.go 只服务内嵌前端产物），所以它不是直接的
// RCE；但任何「顺手把 /media 映射出去」的部署都会立刻变成 RCE 或同源存储型 XSS，
// 而落盘扩展名本来就没有任何功能作用——文件是按数据库里的 mime_type 回放的。
// 因此这里不再接受上传者的取值：拿不准就用惰性的 .bin。
func extensionFor(mimeType, filename string) string {
	ext := safeExt(filename)
	if info, ok := extensionLookup[ext]; ok && info.mimeType == mimeType {
		return ext
	}
	if canonical, ok := canonicalExtension[mimeType]; ok {
		return canonical
	}
	logging.Warnf("[media] no canonical extension for content type %q; storing with .bin instead of the uploader-supplied %q", mimeType, ext)
	return ".bin"
}

// isWebP / isWebM 用于判断「是否已经就是目标格式」，从而跳过不必要的转码。
func isWebP(mimeType string) bool {
	return mimeType == "image/webp"
}

func isWebM(mimeType string) bool {
	return mimeType == "audio/webm" || mimeType == "video/webm"
}

// randomHex 生成指定字节数的随机十六进制串，用作落盘文件名的主体——
// 与原始文件名解耦，既避免重名覆盖，也避免暴露用户上传时的文件名。
func randomHex(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
