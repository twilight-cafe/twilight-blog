// Package mediaconv 调用外部二进制（cwebp / ffmpeg / avconv）做媒体转码。
//
// # WebP 编码器是硬性依赖
//
// 站点承诺「图片一律以 WebP 落盘」，而这个承诺只能靠真正的编码器兑现：Go 标准库
// 与 golang.org/x/image 都只有 WebP 解码器，没有编码器。所以 Detect 找不到 WebP
// 编码器时返回 ErrWebPUnavailable，调用方必须让**进程启动失败**（见 main.go）
// 而不是降级运行。
//
// 这条硬约束是有来历的：旧实现把「找不到 cwebp」当成可选能力缺失，于是上传退回
// 原格式落盘。当 settings.json 里钉的是另一台机器的绝对路径时，症状就是「上传
// 头图和头像后磁盘上还是 jpg/png」——而且只在有人去看磁盘时才会被发现。
//
// # WebM 能力目前未接线
//
// ConvertAudioToWebM 与它的探测都保留着，但**站点当前不使用**：音视频一律原样
// 落盘（见 service.StoreFile 里那段说明）。保留而非删除，是为了让重新启用只是
// 加回一个分支的事，而不是把这段实现重写一遍。
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

// ErrWebPUnavailable 表示这台机器上没有任何可用的 WebP 编码器。
//
// 它是一条**启动失败**，不是「可选能力没开」：见包注释里关于硬性依赖的说明。
var ErrWebPUnavailable = errors.New("no WebP encoder is available")

// Converter 描述有哪些外部转码工具可用。
// WebPKind 记录选中工具的类型（"cwebp" 或 "ffmpeg"），因为两者的命令行不同。
type Converter struct {
	WebPEnabled bool
	WebPTool    string
	WebPKind    string // "cwebp" or "ffmpeg"
	WebMEnabled bool
	WebMTool    string
}

// Detect 探测可用的转码工具：先看配置里指定的路径，再退回 PATH 查找。
//
// WebP 编码器是**硬性依赖**：一个都没找到就返回包装了 ErrWebPUnavailable 的错误，
// 调用方应当让进程启动失败。错误信息里带上该改哪个配置键、该装什么——这条路径上
// 唯一的读者是正在排障的运维，一句「找不到 cwebp」不足以让他动手。
//
// WebM 工具缺失不算错误：它只意味着 WebM 能力关闭（而站点目前也并未使用它，
// 见包注释「WebM 能力目前未接线」）。
func Detect(webpTool, webmTool string) (*Converter, error) {
	logging.Debugf("[mediaconv] detecting conversion tools webp_tool=%q webm_tool=%q", webpTool, webmTool)
	c := &Converter{}

	path, kind := detectWebP(webpTool)
	if path == "" {
		return nil, webPUnavailableError(webpTool)
	}
	c.WebPEnabled = true
	c.WebPTool = path
	c.WebPKind = kind
	logging.Debugf("[mediaconv] WebP tool detected: kind=%s path=%s", kind, path)

	if webmPath := detectWebM(webmTool); webmPath != "" {
		c.WebMEnabled = true
		c.WebMTool = webmPath
		logging.Debugf("[mediaconv] WebM tool detected: path=%s", webmPath)
	}

	return c, nil
}

// webPUnavailableError 组装一条能照着做的启动失败信息。
//
// 「配了路径、但那个文件不在」与「什么都没配、PATH 里也没有」是两种处境，处置方式
// 并不相同——前者多半是把另一台机器的 settings.json 拷了过来，改配置即可；后者
// 要装东西。所以分两种写法，而不是笼统一句「找不到 cwebp」。
func webPUnavailableError(configured string) error {
	const guidance = "install cwebp (recommended, from libwebp) or ffmpeg and make it reachable on PATH, " +
		"or point webp_tool in settings.json at the binary"
	if configured != "" {
		return fmt.Errorf("%w: the configured webp_tool %q does not exist and neither cwebp nor ffmpeg "+
			"was found on PATH; %s", ErrWebPUnavailable, configured, guidance)
	}
	return fmt.Errorf("%w: neither cwebp nor ffmpeg was found on PATH; %s", ErrWebPUnavailable, guidance)
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
func execNames(base string) []string {
	return toolNameCandidates(base, runtime.GOOS)
}

// toolNameCandidates 给出某个候选名在给定平台上要尝试的全部写法。
//
// 平台作为参数传进来、而不是在函数里直接读 runtime.GOOS，是为了让两个分支都能被测到：
// runtime.GOOS 是编译期常量，直接判断的话永远只有一个分支可达，而这段逻辑在两个平台
// 上**确实不同**，两边都该有人守着——只在你手上这台机器上成立的分支，等于另一台上没测。
//
// 严格说这个后缀列表不是必需的：Go 的 exec.LookPath 在 Windows 上本来就会按 PATHEXT
// 自动补后缀。显式列出只是让候选名一目了然，顺带兼容 PATHEXT 被改坏的环境。
func toolNameCandidates(base, goos string) []string {
	names := []string{base}
	if goos != "windows" {
		return names
	}
	return append(names, base+".exe", base+".bat", base+".cmd")
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
