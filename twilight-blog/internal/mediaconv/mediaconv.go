// Package mediaconv 调用外部二进制（cwebp / ffmpeg / avconv）做媒体转码。
//
// 定位：这是**可选增强**而不是必需依赖。Detect 找不到工具不算错误，
// 只是把对应能力关掉，上传的文件保持原格式（router 会为此打一条 Warn）。
// 因此本包所有 Convert* 方法在能力未启用时返回错误，调用方必须能接受
// 「不转码、原样存储」这条路径。
package mediaconv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"blog-engine/internal/childenv"
	"blog-engine/internal/logging"
)

// conversionTimeout 限制单次转码调用的时长。
// 没有它，一个病态的输入文件能让子进程和它的 goroutine 一直活着，
// 只要客户端不主动断开连接。
const conversionTimeout = 2 * time.Minute

// maxCapturedOutput 限制为构造错误信息而保留的子进程输出量。
// ffmpeg 处理大文件时输出极其啰嗦，直接 CombinedOutput 会把它们全缓冲在内存里。
const maxCapturedOutput = 16 << 10

// Converter 描述有哪些外部转码工具可用。
// WebPKind 记录选中工具的类型（"cwebp" 或 "ffmpeg"），因为两者的命令行不同。
type Converter struct {
	WebPEnabled bool
	WebPTool    string
	WebPKind    string // "cwebp" or "ffmpeg"
	WebMEnabled bool
	WebMTool    string
}

// Detect 探测 WebP 与 WebM 转码工具：先看配置里指定的路径，再退回 PATH 查找。
// 工具缺失不算错误——它只意味着对应转码能力关闭，上传仍然照常进行。
func Detect(webpTool, webmTool string) *Converter {
	logging.Debugf("[mediaconv] detecting conversion tools webp_tool=%q webm_tool=%q", webpTool, webmTool)
	c := &Converter{}

	if path, kind := detectWebP(webpTool); path != "" {
		c.WebPEnabled = true
		c.WebPTool = path
		c.WebPKind = kind
		logging.Debugf("[mediaconv] WebP tool detected: kind=%s path=%s", kind, path)
	}

	if path := detectWebM(webmTool); path != "" {
		c.WebMEnabled = true
		c.WebMTool = path
		logging.Debugf("[mediaconv] WebM tool detected: path=%s", path)
	}

	return c
}

// ConvertImageToWebP 把图片转成 WebP，按 WebPKind 选择 cwebp 或 ffmpeg 的命令行。
//
// 结束后还会确认输出文件真的存在：转码器有时会以 0 退出却没有产出文件，
// 只看退出码会把这种情况误判为成功。
func (c *Converter) ConvertImageToWebP(ctx context.Context, src, dst string) error {
	if !c.WebPEnabled {
		return errors.New("webp conversion is not enabled")
	}
	logging.Debugf("[mediaconv] converting image to webp src=%s dst=%s kind=%s", src, dst, c.WebPKind)

	ctx, cancel := context.WithTimeout(ctx, conversionTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if c.WebPKind == "cwebp" {
		cmd = exec.CommandContext(ctx, c.WebPTool, "-quiet", src, "-o", dst)
	} else {
		cmd = exec.CommandContext(ctx, c.WebPTool, "-y", "-i", src, "-c:v", "libwebp", dst)
	}

	if out, err := runCommand(cmd); err != nil {
		return conversionError(ctx, "webp", err, out)
	}
	if !exists(dst) {
		return errors.New("webp conversion produced no output file")
	}
	logging.Debugf("[mediaconv] webp conversion complete src=%s dst=%s", src, dst)
	return nil
}

// ConvertAudioToWebM 把音频转成 WebM，带一次编码器回退：先试 libopus，
// 失败再试 libvorbis。某些 ffmpeg 构建缺少 libopus，回退能让它们仍然可用。
//
// 两个错误都会出现在最终错误信息里，避免只看到回退失败而误判原因。
func (c *Converter) ConvertAudioToWebM(ctx context.Context, src, dst string) error {
	if !c.WebMEnabled {
		return errors.New("webm conversion is not enabled")
	}
	logging.Debugf("[mediaconv] converting audio to webm src=%s dst=%s", src, dst)

	if err := c.runFFmpeg(ctx, src, dst, "libopus", "-b:a", "128k"); err == nil {
		if exists(dst) {
			logging.Debugf("[mediaconv] webm conversion complete src=%s dst=%s codec=libopus", src, dst)
			return nil
		}
		return errors.New("webm conversion produced no output file")
	} else if err2 := c.runFFmpeg(ctx, src, dst, "libvorbis", "-q:a", "5"); err2 != nil {
		return fmt.Errorf("webm conversion failed: %v; fallback also failed: %w", err, err2)
	}

	if !exists(dst) {
		return errors.New("webm conversion produced no output file")
	}
	logging.Debugf("[mediaconv] webm conversion complete src=%s dst=%s codec=libvorbis", src, dst)
	return nil
}

// runFFmpeg 用指定音频编码器调用 ffmpeg 转出 WebM。
//
// 每次尝试都用**独立**的超时 context：否则第一次编码器超时之后，
// 回退的那次会拿着一个已经耗尽的预算立刻失败，回退等于没做。
// -vn 明确丢弃视频流，只保留音频。
func (c *Converter) runFFmpeg(ctx context.Context, src, dst, audioCodec, codecArg, codecValue string) error {
	logging.Debugf("[mediaconv] running ffmpeg codec=%s src=%s dst=%s", audioCodec, src, dst)

	// Each attempt gets its own deadline so a timed-out first codec does not
	// immediately doom the fallback codec.
	ctx, cancel := context.WithTimeout(ctx, conversionTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.WebMTool,
		"-y", "-i", src,
		"-vn",
		"-c:a", audioCodec, codecArg, codecValue,
		"-f", "webm", dst,
	)
	out, err := runCommand(cmd)
	if err != nil {
		return conversionError(ctx, audioCodec, err, out)
	}
	return nil
}

// runCommand 执行命令并返回合并后的、有大小上限的输出。
// 上限由有限的 Writer 保证：子进程看到的始终是「写成功」，
// 因此不会因为管道缓冲写满而卡死。
//
// 环境变量在这里统一清理：转码器是第三方二进制，只需要读一个文件、写一个文件，
// 没有任何理由把本站的凭据（BLOG_MAIL_API_KEY）交给它。原先这里没有设 cmd.Env，
// 于是 cwebp / ffmpeg 会原样继承整个父环境——子进程崩溃转储、/proc/<pid>/environ
// 以及它自身的信息泄露问题都会顺带带上那枚能冒充站点发信的密钥。
func runCommand(cmd *exec.Cmd) (string, error) {
	cmd.Env = childenv.Sanitized()

	var buf bytes.Buffer
	capture := &limitedWriter{writer: &buf, limit: maxCapturedOutput}
	cmd.Stdout = capture
	cmd.Stderr = capture

	err := cmd.Run()
	return buf.String(), err
}

// conversionError 把 exec 的失败转成能区分「超时」「被取消」与「转码器本身失败」
// 的错误。这个区分很有用：超时通常意味着输入过大或工具卡住，
// 而普通失败需要去看 ffmpeg 自己的输出。
func conversionError(ctx context.Context, codec string, err error, output string) error {
	detail := strings.TrimSpace(output)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s conversion timed out after %s", codec, conversionTimeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("%s conversion canceled: client disconnected", codec)
	}
	return fmt.Errorf("%s conversion failed: %w: %s", codec, err, detail)
}

// limitedWriter 最多转发 limit 字节，其余静默丢弃，
// 但**始终报告完整的写入长度**，这样子进程不会看到短写而误以为管道出错。
type limitedWriter struct {
	writer  io.Writer
	limit   int
	written int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	total := len(p)
	if l.written >= l.limit {
		return total, nil
	}

	remaining := l.limit - l.written
	chunk := p
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
	}

	n, err := l.writer.Write(chunk)
	l.written += n
	if err != nil {
		return n, err
	}
	return total, nil
}

// detectWebP 返回可用的 WebP 工具路径与类型；找不到则返回两个空串。
// 配置了路径但文件不存在时，会继续去 PATH 里找，而不是直接判定不可用。
// 优先级为 cwebp → ffmpeg → avconv（cwebp 是专用工具，质量与可控性更好）。
func detectWebP(configured string) (string, string) {
	if configured != "" && exists(configured) {
		return configured, toolKind(configured)
	}

	for _, name := range execNames("cwebp") {
		if path, err := exec.LookPath(name); err == nil {
			return path, "cwebp"
		}
	}
	for _, name := range execNames("ffmpeg") {
		if path, err := exec.LookPath(name); err == nil {
			return path, "ffmpeg"
		}
	}
	for _, name := range execNames("avconv") {
		if path, err := exec.LookPath(name); err == nil {
			return path, "ffmpeg"
		}
	}
	return "", ""
}

// detectWebM 返回可用的 WebM 工具路径；只接受 ffmpeg 或 avconv
// （cwebp 只能编图片，编不了 WebM）。
func detectWebM(configured string) string {
	if configured != "" && exists(configured) {
		return configured
	}

	for _, name := range execNames("ffmpeg") {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	for _, name := range execNames("avconv") {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

// execNames 在 Windows 上额外列出 .exe/.bat/.cmd 变体。
//
// 严格说这不是必需的：Go 的 exec.LookPath 在 Windows 上本来就会按 PATHEXT
// 自动补后缀。显式列出只是让候选名一目了然，顺带兼容 PATHEXT 被改坏的环境。
func execNames(base string) []string {
	names := []string{base}
	if runtime.GOOS == "windows" {
		names = append(names, base+".exe", base+".bat", base+".cmd")
	}
	return names
}

// exists 报告路径是否是一个存在的**文件**（目录不算，空路径也不算）。
func exists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func toolKind(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(base, "cwebp"):
		return "cwebp"
	case strings.Contains(base, "ffmpeg"), strings.Contains(base, "avconv"):
		return "ffmpeg"
	default:
		// 用户配置了一个自定义的二进制名，无法从名字判断类型，
		// 按 ffmpeg 风格的命令行处理（两种风格里更通用的那种）。
		return "ffmpeg"
	}
}
