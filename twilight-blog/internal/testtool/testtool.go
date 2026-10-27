// Package testtool 给「调用外部二进制」的代码提供一个可编程的假工具。
//
// 谁需要它：mediaconv 会 exec cwebp / ffmpeg 这类第三方二进制。要覆盖它的每一条
// 分支，就必须能确定性地造出「成功」「非零退出」「退出码 0 却不产出文件」这几种
// 结果。依赖宿主机装没装 cwebp，既会让测试结果随机器漂移，也让失败分支根本覆盖
// 不到——而失败分支恰恰是「上传时转码失败该怎么办」这条逻辑的全部内容。
//
// 手法是把**测试二进制自己**当成那个外部工具：子进程起来后先看 BLOG_TESTTOOL_MODE，
// 命中就按模式行动并退出。这个判断必须发生在 flag 解析之前——传进来的参数是 cwebp
// 的命令行（-quiet src -o dst），原样交给 testing 包会直接以用法错误退出。
//
// 环境变量之所以到得了子进程，靠的是生产路径本身：mediaconv 用
// childenv.Sanitized() 构造子进程环境，而它只摘掉凭据、其余照传（见
// internal/childenv）。因此这条测试链路与生产链路走的是同一个环境构造点，
// 不是一条专供测试的旁路。
//
// 用法：需要它的测试包各写一次 TestMain，然后在用例里取假工具路径。
//
//	func TestMain(m *testing.M) { os.Exit(testtool.Main(m)) }
//
//	tool := testtool.Fake(t, testtool.ModeFail)
package testtool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ModeEnv 是「本次进程要扮演哪种假工具」的环境变量名。
const ModeEnv = "BLOG_TESTTOOL_MODE"

// 假工具能扮演的行为。这些都是转码路径上真实会遇到的结局。
const (
	// ModeOK 把产物写到最后一个命令行参数指向的路径，退出码 0。
	ModeOK = "ok"
	// ModeSilent 退出码 0，但**不产出任何文件**。
	// 它对应 mediaconv 里那条「只看退出码会把这种情况误判为成功」的检查。
	ModeSilent = "silent"
	// ModeFail 非零退出，并向 stderr 写一段话（转码器失败时的典型表现）。
	ModeFail = "fail"
	// ModeFailOnOpus 参数里含 libopus 就失败，否则产出文件。
	// 它对应「首编码器不可用、回退编码器可用」：某些 ffmpeg 构建不带 libopus，
	// 而那正是 ConvertAudioToWebM 做一次回退的全部理由。
	ModeFailOnOpus = "fail-on-opus"
	// ModeSilentVorbis 参数里含 libopus 就失败，否则退出码 0 且不产出文件。
	// 它对应「回退那次也没有产物」——那条检查与首编码器那次是两处独立的代码。
	ModeSilentVorbis = "silent-vorbis"
	// ModeSleep 先落一个标记文件、停一会儿，再产出结果。
	//
	// 存在的理由只有一个：转码槽位是并发闸门，要证明「占满时第三个请求立刻
	// 得到 503」，就必须让前两个真的停在转码里。真实的假工具结束得太快，
	// 那个窗口靠时序碰运气；标记文件让测试知道子进程确实起来了。
	ModeSleep = "sleep"
)

// MarkerEnv 指向一个目录时，ModeSleep 会在里面建一个以自身 PID 命名的文件。
const MarkerEnv = "BLOG_TESTTOOL_MARKER"

// fakePayload 是假工具产出的文件内容。
//
// 它刻意不是合法的 WebP：转码产物此后不再被本站解码（媒体是按数据库里的
// mime_type 原样回放的），所以内容是否真的是 WebP 不影响这些用例要验的东西。
// 「产物确实是 WebP」由那条用真 cwebp 的用例负责，见 mediaconv 的测试。
const fakePayload = "FAKE-WEBP-PAYLOAD"

// selfPath 缓存假工具的可执行文件路径（即当前测试二进制）。
//
// 用 sync.Once 而不是包级变量：解析要在**第一次用到时**发生。此后再缓存，
// 因为 cwd 可能被改掉——CLI 测试就是用切换工作目录来模拟真实站点的，
// 那时再解析一个相对形式的 argv[0] 会指向别的文件。
var (
	selfOnce sync.Once
	selfPath string
)

// absPath 是 filepath.Abs 的间接层。
//
// 存在的唯一理由是让「读不出当前工作目录」那条退路可测：要触发它就得让
// os.Getwd 失败，而在 Windows 上删掉自己所在的目录会被系统拒绝（cwd 被占用），
// 那条退路于是无法用真实处境构造。而它恰恰不能靠「反正不会发生」来交代——
// 退路的取值错了，exec 就会去找一个别的可执行文件。
var absPath = filepath.Abs

// resolveSelf 把 argv[0] 规范成绝对路径。
//
// 失败时退回原值而不是返回空串：空串会让 exec 去 PATH 里找一个空名字，
// 那是最坏的结局；而 argv[0] 即便是个相对路径，至少在调用方当时的 cwd 下是它。
func resolveSelf(argv0 string) string {
	path, err := absPath(argv0)
	if err != nil {
		return argv0
	}
	return path
}

// Self 返回假工具的可执行文件路径（即当前测试二进制），不影响子进程的行为模式。
func Self() string {
	selfOnce.Do(func() { selfPath = resolveSelf(os.Args[0]) })
	return selfPath
}

// Payload 返回假工具产出的文件内容。
//
// 导出它是为了让调用方能把断言打在「磁盘上那份确实是转码产物」上。让每个用到它的
// 包各自抄一份期望值，就多了一个会与这里漂移的事实来源。
func Payload() []byte { return []byte(fakePayload) }

// LogEnv 指向一个文件时，假工具会把每次收到的命令行追加进那个文件。
//
// 存在的理由：假工具对「有没有产物」的验证与命令行长什么样**无关**（它总是写最后一个
// 参数），于是命令行的构造就成了一个没有任何断言覆盖的地方——而传错一个开关
// （比如把 -o 写成 -output）在真实环境里会立刻变成「转码一直失败」，
// 在测试里却全绿。记录下来的命令行让被测代码构造的 argv 可以被逐字比对。
const LogEnv = "BLOG_TESTTOOL_LOG"

// RecordArgs 让假工具把每次收到的命令行追加进 path。
func RecordArgs(t *testing.T, path string) {
	t.Helper()
	t.Setenv(LogEnv, path)
}

// recordArgs 把一条命令行写成一行（空格分隔）。
//
// 写失败只报不拦：这是诊断辅助，它自己出问题不该改变假工具的行为——
// 否则一个写错的日志路径会让被测代码收到一个「转码失败」，方向完全指错。
func recordArgs(path string, args []string) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testtool: open the argument log: %v\n", err)
		return
	}
	defer file.Close()
	fmt.Fprintln(file, strings.Join(args, " "))
}

// Main 是各测试包 TestMain 的实现，用法：os.Exit(testtool.Main(m))。
func Main(m *testing.M) int {
	return dispatch(os.Getenv(ModeEnv), m, os.Args[1:])
}

// dispatch 把「扮演假工具」与「正常跑测试」分开，是为了让两条路都能被直接测到：
// 假工具那条只在子进程里走，而子进程的覆盖率不进本进程的 profile，因此它必须
// 能从用例里直接调用，否则就是一段永远测不到的代码。
//
// mode 非空时 m 不会被解引用，因此给假工具那条路传 nil 是安全的。
func dispatch(mode string, m *testing.M, args []string) int {
	if mode != "" {
		return runFake(mode, args)
	}
	return m.Run()
}

// runFake 是假工具本体。输出路径取**最后一个参数**：cwebp（-quiet src -o dst）
// 与 ffmpeg（… dst）两种命令行都以输出路径结尾，所以同一个假工具能同时冒充两者，
// 不必各自解析一遍参数表——而解析得越像真的，测试就越容易与真实命令行漂移。
func runFake(mode string, args []string) int {
	if logPath := os.Getenv(LogEnv); logPath != "" {
		recordArgs(logPath, args)
	}

	switch mode {
	case ModeOK:
		return writeOutput(args)
	case ModeSilent:
		return 0
	case ModeFail:
		fmt.Fprintln(os.Stderr, "testtool: this failure is deliberate")
		return 7
	case ModeFailOnOpus:
		if hasArg(args, opusMarker) {
			return reportMissingOpus()
		}
		return writeOutput(args)
	case ModeSilentVorbis:
		if hasArg(args, opusMarker) {
			return reportMissingOpus()
		}
		return 0
	case ModeSleep:
		markStarted()
		time.Sleep(500 * time.Millisecond)
		return writeOutput(args)
	default:
		fmt.Fprintf(os.Stderr, "testtool: unknown mode %q\n", mode)
		return 78
	}
}

// opusMarker 是 ffmpeg 命令里用来识别「这次是首编码器」的片段。
const opusMarker = "libopus"

// markStarted 在 MarkerEnv 指向的目录里留下一个以本进程 PID 命名的空文件。
// 写失败只报不拦：这是给测试用的信号，不该改变假工具的行为。
func markStarted() {
	dir := os.Getenv(MarkerEnv)
	if dir == "" {
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("pid-%d", os.Getpid()))
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "testtool: write the start marker: %v\n", err)
	}
}

// reportMissingOpus 模拟一个不带 libopus 的 ffmpeg 构建。
func reportMissingOpus() int {
	fmt.Fprintln(os.Stderr, "testtool: unknown encoder 'libopus' in this build")
	return 7
}

// writeOutput 把产物写到最后一个参数指向的路径。
//
// 没有参数时返回 64 而不是静默成功：那说明命令行构造与这里的约定不一致
// （见 runFake 的说明），静默成功会让上层以为「转码成功但产物不见了」。
func writeOutput(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "testtool: no output path was passed")
		return 64
	}
	if err := os.WriteFile(args[len(args)-1], []byte(fakePayload), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "testtool: write the output file: %v\n", err)
		return 74
	}
	return 0
}

// hasArg 报告参数里有没有出现 needle。
//
// 子串匹配而不是解析参数表：这里只需要回答「这次调用用的是不是 libopus」，
// 而把 ffmpeg 的参数表在测试替身里再实现一遍，只会多出一个会与真实命令行漂移的地方。
func hasArg(args []string, needle string) bool {
	for _, arg := range args {
		if strings.Contains(arg, needle) {
			return true
		}
	}
	return false
}

// Fake 把当前测试二进制配置成假工具并返回它的路径。
//
// 用 t.Setenv 而不是自己在 exec.Cmd 上挂环境：被测代码在内部构造子进程环境
// （childenv.Sanitized），调用方碰不到那个 cmd。t.Setenv 改的是本进程的环境，
// 而它会原样传给子进程——这正好与生产链路一致。
//
// t.Setenv 会拒绝与 t.Parallel 并用，这里正合适：模式是进程级状态，
// 并行用例互相覆盖会让假工具的行为变得不可预测。
func Fake(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv(ModeEnv, mode)
	return Self()
}
