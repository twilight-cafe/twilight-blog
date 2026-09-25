package mediaconv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	xwebp "golang.org/x/image/webp"

	"blog-engine/internal/testtool"
)

// TestMain 让这个包也能用 testtool 的假转码器。
//
// 假转码器就是**这个测试二进制自己**，所以子进程起来后要先在这里被拦下、
// 按环境变量行动——而它必须在 flag 解析之前被拦，因为传进去的是 cwebp 的命令行。
func TestMain(m *testing.M) {
	os.Exit(testtool.Main(m))
}

/* -------------------------------------------------------------------------- */
/* Helpers                                                                     */
/* -------------------------------------------------------------------------- */

// pngBytes 生成一张真实的 PNG。
//
// 用编码器生成而不是硬编码一段字节：媒体与转码链路是按**内容**判断类型的，
// 一段抄错的字节会变成「上传被拒」或「转码器说这不是图片」，而症状看起来像是
// 校验逻辑坏了。
func pngBytes(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatalf("编码测试用的 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

// sourceImage 在临时目录里放一张 PNG，返回它的路径。
func sourceImage(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "input.png")
	if err := os.WriteFile(path, pngBytes(t), 0o600); err != nil {
		t.Fatalf("写测试图片失败: %v", err)
	}
	return path
}

// toolOnPath 在 dir 里放一个「名字像转码器」的文件，让 exec.LookPath 找得到它。
//
// 内容无关紧要：这些用例测的是**查找**（Detect 走 PATH 那一支），不是执行。
// 名字写两个（基名与 .exe）是为了让同一个辅助在两种平台语义下都成立——
// Unix 认可执行位，Windows 按 PATHEXT 补后缀。
func toolOnPath(t *testing.T, dir, base string) {
	t.Helper()

	for _, name := range toolNameCandidates(base, "windows") {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("放入假工具 %s 失败: %v", name, err)
		}
	}
}

// pathWith 把 PATH 换成一个只含 dir 的目录。
//
// 刻意切成「只有它」而不是往真实 PATH 后面追加：开发机上往往真的装着 cwebp
// （那是本项目的部署前提），留着真实 PATH 的话，「找不到编码器」这一类用例会时灵时不灵。
func pathWith(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

// converterFor 造一个「用假工具当转码器」的 Converter。
//
// kind 直接给定而不是让 Detect 去猜：假工具的文件名不会包含 cwebp/ffmpeg，
// 而用例要验的正是**按 kind 选出来的那条命令行**。
func converterFor(t *testing.T, mode, kind string) *Converter {
	t.Helper()

	return &Converter{
		WebPEnabled: true,
		WebPTool:    testtool.Fake(t, mode),
		WebPKind:    kind,
	}
}

// webMToolFor 同上，但用于音视频那一路。
func webMToolFor(t *testing.T, mode string) *Converter {
	t.Helper()

	return &Converter{
		WebMEnabled: true,
		WebMTool:    testtool.Fake(t, mode),
	}
}

// argLog 让假工具记录命令行，返回日志路径。
func argLog(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "args.log")
	testtool.RecordArgs(t, path)
	return path
}

// readArgLines 读回假工具记录下来的命令行（一次调用一行）。
func readArgLines(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读假工具的参数日志失败: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
}

// argLine 按假工具记录时的写法把参数连起来。
func argLine(args ...string) string { return strings.Join(args, " ") }

// findRealTool 在 PATH 里找一个真实的转码器；没有就返回空串。
func findRealTool(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

/* -------------------------------------------------------------------------- */
/* Detect                                                                      */
/* -------------------------------------------------------------------------- */

// TestDetectPrefersTheConfiguredPath 确认配置优先于 PATH。
//
// 这条不是形式主义：配置项存在的意义就是「这台机器上我想用哪一个」。
// 让 PATH 上也放一个**不同的** cwebp，才能把「配置赢了」与「PATH 恰好也是它」分开。
func TestDetectPrefersTheConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	toolOnPath(t, dir, "cwebp")
	pathWith(t, dir)

	configured := testtool.Self()
	converter, err := Detect(configured, "")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if !converter.WebPEnabled || converter.WebPTool != configured {
		t.Fatalf("配置的 webp_tool 没有被采用: %+v", converter)
	}
}

// TestDetectFallsBackToPath 覆盖「配的路径不存在」这一常见处境——把另一台机器的
// settings.json 拷过来就会这样——以及 PATH 里找到 cwebp。
func TestDetectFallsBackToPath(t *testing.T) {
	dir := t.TempDir()
	toolOnPath(t, dir, "cwebp")
	pathWith(t, dir)

	missing := filepath.Join(t.TempDir(), "cwebp-elsewhere.exe")
	converter, err := Detect(missing, "")
	if err != nil {
		t.Fatalf("配置的路径不存在时应当退回 PATH, 实际 error = %v", err)
	}
	if !converter.WebPEnabled || converter.WebPKind != "cwebp" {
		t.Fatalf("没有退回 PATH 里那个 cwebp: %+v", converter)
	}
	if filepath.Dir(converter.WebPTool) != dir {
		t.Fatalf("选中的工具路径 = %q, 期望位于 %q", converter.WebPTool, dir)
	}
}

// TestDetectFallsBackToFFmpeg 覆盖没有 cwebp、只有 ffmpeg 的机器
// （发行版里常见：ffmpeg 到处都有，libwebp 的工具不一定）。
func TestDetectFallsBackToFFmpeg(t *testing.T) {
	dir := t.TempDir()
	toolOnPath(t, dir, "ffmpeg")
	pathWith(t, dir)

	converter, err := Detect("", "")
	if err != nil {
		t.Fatalf("只有 ffmpeg 时应当可用, 实际 error = %v", err)
	}
	if !converter.WebPEnabled || converter.WebPKind != "ffmpeg" {
		t.Fatalf("没有采用 ffmpeg: %+v", converter)
	}
}

// TestDetectFallsBackToAvconv 覆盖更老的选择（avconv 与 ffmpeg 命令行同源，
// 所以 kind 记作 ffmpeg）。
func TestDetectFallsBackToAvconv(t *testing.T) {
	dir := t.TempDir()
	toolOnPath(t, dir, "avconv")
	pathWith(t, dir)

	converter, err := Detect("", "")
	if err != nil {
		t.Fatalf("只有 avconv 时应当可用, 实际 error = %v", err)
	}
	if !converter.WebPEnabled || converter.WebPKind != "ffmpeg" {
		t.Fatalf("avconv 应当按 ffmpeg 风格处理: %+v", converter)
	}
	if !strings.Contains(filepath.Base(converter.WebPTool), "avconv") {
		t.Fatalf("选中的工具 = %q, 期望是那个 avconv", converter.WebPTool)
	}
}

// TestDetectReportsTheWebMTool 确认 WebM 能力仍然被探测出来。
//
// 站点目前不使用它（音视频一律原样落盘，见包注释），但探测结果要如实反映这台机器
// 的现状：重新启用时第一个要问的就是「ffmpeg 在不在」。
func TestDetectReportsTheWebMTool(t *testing.T) {
	dir := t.TempDir()
	toolOnPath(t, dir, "cwebp")
	toolOnPath(t, dir, "ffmpeg")
	toolOnPath(t, dir, "avconv")
	pathWith(t, dir)

	converter, err := Detect("", "")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if !converter.WebMEnabled {
		t.Fatalf("ffmpeg 存在时 WebM 能力应当被标记为可用: %+v", converter)
	}
	if filepath.Dir(converter.WebMTool) != dir {
		t.Fatalf("WebM 工具路径 = %q, 期望位于 %q", converter.WebMTool, dir)
	}
}

// TestDetectHonoursAConfiguredWebMTool 覆盖「webm_tool 配了、而且文件在」这一支。
//
// 站点目前不使用 WebM（音视频一律原样落盘），但探测结果仍要如实反映配置——
// 重新启用该功能时，用哪个二进制正是由它决定的。
func TestDetectHonoursAConfiguredWebMTool(t *testing.T) {
	dir := t.TempDir()
	toolOnPath(t, dir, "cwebp")
	pathWith(t, dir)

	configuredWebM := testtool.Self()
	converter, err := Detect("", configuredWebM)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if !converter.WebMEnabled || converter.WebMTool != configuredWebM {
		t.Fatalf("配置的 webm_tool 没有被采用: %+v", converter)
	}
}

// TestDetectFailsWithoutAWebPEncoder 是这次改动的核心断言：没有编码器就是**错误**，
// 不是「可选能力关闭」。
//
// 反证方式：把 Detect 里的 `return nil, webPUnavailableError(...)` 改回旧的
// 「把能力标记为关闭然后正常返回」，这条用例立刻红。那正是让图片悄悄以原格式落盘
// 的那段代码。
func TestDetectFailsWithoutAWebPEncoder(t *testing.T) {
	pathWith(t, t.TempDir()) // 一个什么都没有的目录

	_, err := Detect("", "")
	if !errors.Is(err, ErrWebPUnavailable) {
		t.Fatalf("没有编码器时 Detect() error = %v, 期望 ErrWebPUnavailable", err)
	}
	if strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("什么都没配置时不该说「配置的路径不存在」: %v", err)
	}
	if !strings.Contains(err.Error(), "webp_tool") {
		t.Fatalf("错误信息里应当点出该改哪个配置键: %v", err)
	}
}

// TestDetectNamesTheMissingConfiguredTool 覆盖另一种缺失：配了路径、文件不在。
//
// 提示里必须出现那个路径本身——运维需要一眼看出它是从别的机器抄过来的。
func TestDetectNamesTheMissingConfiguredTool(t *testing.T) {
	pathWith(t, t.TempDir())
	missing := filepath.Join(t.TempDir(), "cwebp-elsewhere.exe")

	_, err := Detect(missing, "")
	if !errors.Is(err, ErrWebPUnavailable) {
		t.Fatalf("Detect() error = %v, 期望 ErrWebPUnavailable", err)
	}
	// 用 %q 的形式比对：错误信息里路径是带引号转义的，直接 contains 会在
	// Windows 的反斜杠上失配。
	if !strings.Contains(err.Error(), fmt.Sprintf("%q", missing)) {
		t.Fatalf("错误信息里应当出现配置的那个路径 %q: %v", missing, err)
	}
}

/* -------------------------------------------------------------------------- */
/* Small helpers                                                               */
/* -------------------------------------------------------------------------- */

// TestToolKind 覆盖按名字判断命令行风格的四种情况。
//
// 猜错 kind 的后果是拿 ffmpeg 的参数去调 cwebp（或反过来），而两者都会以
// 「转码失败」告终，看不出是这里猜错了。
func TestToolKind(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/usr/bin/cwebp", "cwebp"},
		{`C:\tools\cwebp.exe`, "cwebp"},
		{`C:\Program Files\libwebp\bin\cwebp.bat`, "cwebp"},
		{"/usr/bin/ffmpeg", "ffmpeg"},
		{"/usr/bin/avconv", "ffmpeg"},
		{"/opt/some/custom-encoder", "ffmpeg"}, // 名字判断不出来 → 用更通用的那种风格
	}
	for _, tc := range cases {
		if got := toolKind(tc.path); got != tc.want {
			t.Fatalf("toolKind(%q) = %q, 期望 %q", tc.path, got, tc.want)
		}
	}
}

// TestToolNameCandidates 覆盖两个平台上的候选名，并确认 execNames 与之一致。
//
// 平台是参数，所以两个分支在本机上都能测到——若直接读 runtime.GOOS，
// 另一条分支就永远是「没测过」。
func TestToolNameCandidates(t *testing.T) {
	unix := toolNameCandidates("cwebp", "linux")
	if len(unix) != 1 || unix[0] != "cwebp" {
		t.Fatalf("非 Windows 上应当只用基名, 实际 %v", unix)
	}

	windows := toolNameCandidates("cwebp", "windows")
	want := []string{"cwebp", "cwebp.exe", "cwebp.bat", "cwebp.cmd"}
	if argLine(windows...) != argLine(want...) {
		t.Fatalf("Windows 上的候选名 = %v, 期望 %v", windows, want)
	}

	// execNames 是生产路径用的入口，它必须在当前平台上给出与上表一致的答案。
	if argLine(execNames("cwebp")...) != argLine(toolNameCandidates("cwebp", runtime.GOOS)...) {
		t.Fatalf("execNames 与 toolNameCandidates 不一致: %v", execNames("cwebp"))
	}
}

// TestExists 覆盖「算不算一个存在的文件」的四条判定。
//
// 目录不算、空路径不算——这条判断守着 detectWebP 里「配置的路径是不是可用工具」，
// 把一个目录当成工具会让 exec 以「拒绝访问」告终，而错误信息完全指不到配置上。
func TestExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "cwebp.exe")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	if exists("") {
		t.Fatal("空路径不算存在")
	}
	if exists(filepath.Join(dir, "missing")) {
		t.Fatal("不存在的路径不算存在")
	}
	if exists(dir) {
		t.Fatal("目录不算存在（它不是一个可执行的工具）")
	}
	if !exists(file) {
		t.Fatal("存在的文件应当算存在")
	}
}

/* -------------------------------------------------------------------------- */
/* ConvertImageToWebP                                                          */
/* -------------------------------------------------------------------------- */

// TestConvertImageToWebPRequiresAnEncoder 覆盖能力未启用时的短路。
func TestConvertImageToWebPRequiresAnEncoder(t *testing.T) {
	converter := &Converter{}
	err := converter.ConvertImageToWebP(context.Background(), "in.png", "out.webp")
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("没有编码器时 error = %v, 期望提到能力未启用", err)
	}
}

// TestConvertImageToWebPUsesTheCwebpCommandLine 钉住 cwebp 风格的命令行。
//
// 断言打在假工具记下来的 argv 上：产物存在与否与命令行无关（假工具总是写最后一个
// 参数），而真实环境里少一个或者错一个开关，症状就是「转码一直失败」。
func TestConvertImageToWebPUsesTheCwebpCommandLine(t *testing.T) {
	log := argLog(t)
	src := sourceImage(t)
	dst := filepath.Join(t.TempDir(), "out.webp")

	converter := converterFor(t, testtool.ModeOK, "cwebp")
	if err := converter.ConvertImageToWebP(context.Background(), src, dst); err != nil {
		t.Fatalf("ConvertImageToWebP() error = %v", err)
	}

	lines := readArgLines(t, log)
	want := argLine("-quiet", src, "-o", dst)
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("cwebp 命令行 = %v, 期望 [%s]", lines, want)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("转码产物不存在: %v", err)
	}
}

// TestConvertImageToWebPUsesTheFFmpegCommandLine 钉住 ffmpeg 风格的命令行分支。
func TestConvertImageToWebPUsesTheFFmpegCommandLine(t *testing.T) {
	log := argLog(t)
	src := sourceImage(t)
	dst := filepath.Join(t.TempDir(), "out.webp")

	converter := converterFor(t, testtool.ModeOK, "ffmpeg")
	if err := converter.ConvertImageToWebP(context.Background(), src, dst); err != nil {
		t.Fatalf("ConvertImageToWebP() error = %v", err)
	}

	lines := readArgLines(t, log)
	want := argLine("-y", "-i", src, "-c:v", "libwebp", dst)
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("ffmpeg 命令行 = %v, 期望 [%s]", lines, want)
	}
}

// TestConvertImageToWebPDetectsAMissingOutput 覆盖「退出码 0 但没有产物」。
//
// 只看退出码会把这种情况当成成功，然后媒体目录里出现一条指向不存在文件的记录。
func TestConvertImageToWebPDetectsAMissingOutput(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out.webp")

	converter := converterFor(t, testtool.ModeSilent, "cwebp")
	err := converter.ConvertImageToWebP(context.Background(), sourceImage(t), dst)
	if err == nil || !strings.Contains(err.Error(), "produced no output file") {
		t.Fatalf("退出码 0 但没有产物时 error = %v", err)
	}
}

// TestConvertImageToWebPReportsTheFailure 覆盖非零退出：错误里要带上转码器自己的输出。
//
// 不带那段的后果是排障时只剩一句「转换失败」，而真正的原因（编解码器不存在、
// 输入损坏）就在被丢掉的那几行里。
func TestConvertImageToWebPReportsTheFailure(t *testing.T) {
	converter := converterFor(t, testtool.ModeFail, "cwebp")
	err := converter.ConvertImageToWebP(context.Background(), sourceImage(t), filepath.Join(t.TempDir(), "out.webp"))

	if err == nil {
		t.Fatal("转码器非零退出时应当报错")
	}
	if !errors.Is(err, context.DeadlineExceeded) && strings.Contains(err.Error(), "timed out") {
		t.Fatalf("普通失败不该被报成超时: %v", err)
	}
	if !strings.Contains(err.Error(), "webp conversion failed") {
		t.Fatalf("错误信息应当说明是 webp 转换失败: %v", err)
	}
	if !strings.Contains(err.Error(), "deliberate") {
		t.Fatalf("错误信息应当带上转码器自己的输出: %v", err)
	}
}

// TestConvertImageToWebPWritesARealWebP 用真实的 cwebp 走一遍，并**解码产物**。
//
// 前面几条用的是假转码器：它们证明本站的调用逻辑（argv、产物检查、错误映射）正确，
// 却证明不了「cwebp 收到这些参数真的能编出 WebP」。这条补上另一半，也是唯一能
// 证明「磁盘上的文件确实是 WebP」的用例。
//
// cwebp 不存在时跳过：它确实是本项目的部署前提（启动时会拦），但测试机不保证装了。
func TestConvertImageToWebPWritesARealWebP(t *testing.T) {
	cwebp := findRealTool("cwebp")
	if cwebp == "" {
		t.Skip("这台机器上没有 cwebp；真实产物由部署前的冒烟测试覆盖")
	}

	src := sourceImage(t)
	dst := filepath.Join(t.TempDir(), "out.webp")

	converter := &Converter{WebPEnabled: true, WebPTool: cwebp, WebPKind: "cwebp"}
	if err := converter.ConvertImageToWebP(context.Background(), src, dst); err != nil {
		t.Fatalf("真实 cwebp 转码失败: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("读转码产物失败: %v", err)
	}
	cfg, err := xwebp.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("产物不是可解码的 WebP: %v", err)
	}
	if cfg.Width != 8 || cfg.Height != 8 {
		t.Fatalf("产物尺寸 = %dx%d, 期望 8x8", cfg.Width, cfg.Height)
	}
}

/* -------------------------------------------------------------------------- */
/* ConvertAudioToWebM                                                          */
/* -------------------------------------------------------------------------- */

// 音视频转 WebM 目前**没有接线**（站点把音视频原样落盘，见包注释），但这些实现与
// 它的分支都保留着，重新启用时只需要加回一个分支。既然留着，就得有人守着——
// 否则「暂时关闭」会悄悄变成「已经坏掉」。

// TestConvertAudioToWebMRequiresAnEncoder 覆盖能力未启用时的短路。
func TestConvertAudioToWebMRequiresAnEncoder(t *testing.T) {
	converter := &Converter{}
	err := converter.ConvertAudioToWebM(context.Background(), "in.mp3", "out.webm")
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("没有 ffmpeg 时 error = %v, 期望提到能力未启用", err)
	}
}

// TestConvertAudioToWebMUsesOpus 覆盖首选编码器一次成功。
func TestConvertAudioToWebMUsesOpus(t *testing.T) {
	log := argLog(t)
	src := filepath.Join(t.TempDir(), "in.mp3")
	dst := filepath.Join(t.TempDir(), "out.webm")

	converter := webMToolFor(t, testtool.ModeOK)
	if err := converter.ConvertAudioToWebM(context.Background(), src, dst); err != nil {
		t.Fatalf("ConvertAudioToWebM() error = %v", err)
	}

	lines := readArgLines(t, log)
	want := argLine("-y", "-i", src, "-vn", "-c:a", "libopus", "-b:a", "128k", "-f", "webm", dst)
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("首编码器命令行 = %v, 期望 [%s]", lines, want)
	}
}

// TestConvertAudioToWebMDetectsAMissingFirstOutput 覆盖「首选编码器退出 0 却没产物」。
func TestConvertAudioToWebMDetectsAMissingFirstOutput(t *testing.T) {
	converter := webMToolFor(t, testtool.ModeSilent)
	err := converter.ConvertAudioToWebM(context.Background(),
		filepath.Join(t.TempDir(), "in.mp3"), filepath.Join(t.TempDir(), "out.webm"))

	if err == nil || !strings.Contains(err.Error(), "produced no output file") {
		t.Fatalf("首选编码器没有产物时 error = %v", err)
	}
}

// TestConvertAudioToWebMFallsBackToVorbis 覆盖回退：某些 ffmpeg 构建不带 libopus。
//
// 关键的一条是「回退那次必须真的被执行」——只看返回值的话，一个把回退写成空操作的
// 实现同样会让这条用例通过。所以断言打在 argv 日志上：必须出现两行。
func TestConvertAudioToWebMFallsBackToVorbis(t *testing.T) {
	log := argLog(t)
	src := filepath.Join(t.TempDir(), "in.mp3")
	dst := filepath.Join(t.TempDir(), "out.webm")

	converter := webMToolFor(t, testtool.ModeFailOnOpus)
	if err := converter.ConvertAudioToWebM(context.Background(), src, dst); err != nil {
		t.Fatalf("回退应当成功, 实际 error = %v", err)
	}

	lines := readArgLines(t, log)
	if len(lines) != 2 {
		t.Fatalf("期望两次调用（opus 失败、vorbis 成功）, 实际 %d 次: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "libopus") {
		t.Fatalf("第一次调用应当是 libopus: %s", lines[0])
	}
	wantFallback := argLine("-y", "-i", src, "-vn", "-c:a", "libvorbis", "-q:a", "5", "-f", "webm", dst)
	if lines[1] != wantFallback {
		t.Fatalf("回退命令行 = %q, 期望 %q", lines[1], wantFallback)
	}
}

// TestConvertAudioToWebMReportsBothFailures 覆盖两次都失败。
//
// 两个原因都要出现在最终错误里：只留下回退失败的那一句，会让人以为问题出在
// libvorbis 上，而真正缺的可能是 libopus。
func TestConvertAudioToWebMReportsBothFailures(t *testing.T) {
	converter := webMToolFor(t, testtool.ModeFail)
	err := converter.ConvertAudioToWebM(context.Background(),
		filepath.Join(t.TempDir(), "in.mp3"), filepath.Join(t.TempDir(), "out.webm"))

	if err == nil {
		t.Fatal("两次都失败时应当报错")
	}
	if !strings.Contains(err.Error(), "fallback also failed") {
		t.Fatalf("错误信息应当说明回退也失败了: %v", err)
	}
	if !strings.Contains(err.Error(), "libopus") || !strings.Contains(err.Error(), "libvorbis") {
		t.Fatalf("错误信息应当同时包含两次失败的原因: %v", err)
	}
}

// TestConvertAudioToWebMDetectsAMissingFallbackOutput 覆盖回退那次也没有产物。
//
// 它与「首选编码器没有产物」是两处独立的检查，落在不同的分支上。
func TestConvertAudioToWebMDetectsAMissingFallbackOutput(t *testing.T) {
	converter := webMToolFor(t, testtool.ModeSilentVorbis)
	err := converter.ConvertAudioToWebM(context.Background(),
		filepath.Join(t.TempDir(), "in.mp3"), filepath.Join(t.TempDir(), "out.webm"))

	if err == nil || !strings.Contains(err.Error(), "produced no output file") {
		t.Fatalf("回退也没有产物时 error = %v", err)
	}
}

/* -------------------------------------------------------------------------- */
/* conversionError / limitedWriter                                             */
/* -------------------------------------------------------------------------- */

// TestConversionErrorNamesTheReason 覆盖三种失败被区分开。
//
// 超时通常意味着输入过大或工具卡住，普通失败要去看转码器自己的输出，而取消是客户端
// 断了——三种的下一步动作完全不同，所以必须分开。这里直接构造已耗尽的 context 与
// 已取消的 context，而不是等两分钟去触发真的超时。
func TestConversionErrorNamesTheReason(t *testing.T) {
	expired, cancelExpired := context.WithTimeout(context.Background(), -time.Second)
	defer cancelExpired()
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()

	if got := conversionError(expired, "webp", errors.New("killed"), "out"); !strings.Contains(got.Error(), "timed out") {
		t.Fatalf("已耗尽的 context 应当报超时: %v", got)
	}
	if got := conversionError(canceled, "libopus", errors.New("killed"), "out"); !strings.Contains(got.Error(), "canceled") {
		t.Fatalf("已取消的 context 应当报取消: %v", got)
	}

	plain := conversionError(context.Background(), "webp", errors.New("exit status 7"), "  some stderr  ")
	if !strings.Contains(plain.Error(), "exit status 7") {
		t.Fatalf("普通失败应当带上底层错误: %v", plain)
	}
	if !strings.Contains(plain.Error(), "some stderr") {
		t.Fatalf("普通失败应当带上转码器的输出: %v", plain)
	}
}

// TestLimitedWriterCapsTheCapturedOutput 覆盖限额的三种状态：未到、正好切掉、
// 已经到顶。
//
// 无论哪种，**返回的写入长度都必须是完整的**：子进程看到短写会以为管道坏了，
// 于是转码器自己先报错，而真正的原因（这段输出太长）就再也看不出来了。
func TestLimitedWriterCapsTheCapturedOutput(t *testing.T) {
	var buf bytes.Buffer
	writer := &limitedWriter{writer: &buf, limit: 4}

	n, err := writer.Write([]byte("ab"))
	if err != nil || n != 2 {
		t.Fatalf("未到限额时 Write() = (%d, %v), 期望 (2, nil)", n, err)
	}

	n, err = writer.Write([]byte("cdefgh"))
	if err != nil || n != 6 {
		t.Fatalf("超限写入必须报告完整长度: (%d, %v), 期望 (6, nil)", n, err)
	}

	n, err = writer.Write([]byte("more"))
	if err != nil || n != 4 {
		t.Fatalf("已到限额时仍须报告完整长度: (%d, %v), 期望 (4, nil)", n, err)
	}

	if buf.String() != "abcd" {
		t.Fatalf("捕获到的输出 = %q, 期望 %q", buf.String(), "abcd")
	}
}

// failingWriter 模拟一个写不进去的目标（管道破裂、磁盘写满）。
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// TestLimitedWriterReportsTheUnderlyingError 覆盖底层写入失败。
//
// 把它吞掉会让转码器的输出**看起来**被完整捕获了，而实际上什么都没留下。
func TestLimitedWriterReportsTheUnderlyingError(t *testing.T) {
	writer := &limitedWriter{writer: failingWriter{}, limit: 10}

	if _, err := writer.Write([]byte("abcd")); err == nil {
		t.Fatal("底层写入失败时应当返回错误")
	}
}
