package testtool

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestMain 让这个包自己也能被当成假工具启动（本包自己的用例就要这么用它），
// 同时把 Main → dispatch 的正常分支走一遍。
//
// 开头先调一次 Self()，刻意把那个 once 缓存固化下来：下面的用例会临时替换
// absPath 去测退路，而缓存是全包共享的——先固化，后面怎么换都影响不到它，
// 用例之间也就不再有顺序依赖。
func TestMain(m *testing.M) {
	_ = Self()
	os.Exit(Main(m))
}

// TestRunFakeWritesToTheLastArgument 是假工具的成功形态。
//
// 输出路径取最后一个参数这件事必须钉住：cwebp 是 `-quiet src -o dst`，ffmpeg 是
// `… dst`，两种命令行都以它结尾——整个「一个假工具冒充两个转码器」的设计都建立在
// 这条约定上。它一旦不成立，mediaconv 的用例会得到「转码成功但没有产物」，
// 而那看起来像是被测代码的毛病。
func TestRunFakeWritesToTheLastArgument(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.webp")

	if code := dispatch(ModeOK, nil, []string{"-quiet", "src.png", "-o", out}); code != 0 {
		t.Fatalf("dispatch(ok) = %d, 期望 0", code)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("假工具没有产出文件: %v", err)
	}
	if string(data) != fakePayload {
		t.Fatalf("产物内容 = %q, 期望 %q", data, fakePayload)
	}
	// Payload() 是说给别的包听的同一件事。两处不一致的话，那些拿它做断言的
	// 用例会在一个完全正确的结果上失败——排障方向会被彻底带偏。
	if !bytes.Equal(Payload(), data) {
		t.Fatalf("Payload() = %q, 而假工具写下的是 %q", Payload(), data)
	}
}

// TestRunFakeWithoutAnOutputPath 覆盖「命令行里根本没有输出路径」这条自检。
//
// 它不是一个假想的处境：如果哪天 mediaconv 把输出路径挪到参数中间（或者改成了
// 短选项形式），假转码器会立刻以 64 退出，而不是安安静静地什么都不写——后者会让
// 用例以「上游说转码成功了、产物却不存在」的形式失败，方向完全指错。
func TestRunFakeWithoutAnOutputPath(t *testing.T) {
	if code := dispatch(ModeOK, nil, nil); code != 64 {
		t.Fatalf("没有输出路径时 dispatch(ok) = %d, 期望 64", code)
	}
}

// TestRunFakeCannotWriteItsOutput 覆盖写出失败（目录不存在）。
func TestRunFakeCannotWriteItsOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "no-such-dir", "out.webp")
	if code := dispatch(ModeOK, nil, []string{"-o", out}); code != 74 {
		t.Fatalf("输出路径不可写时 dispatch(ok) = %d, 期望 74", code)
	}
}

// TestRunFakeSilentProducesNoFile 覆盖「退出码 0 但没有产物」。
//
// 这是 mediaconv 里那条 exists() 检查存在的理由，也是假工具最有价值的一种形态：
// 真实的转码器确实会这样（静默失败），而只看退出码的实现会把它当成成功。
func TestRunFakeSilentProducesNoFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.webp")

	if code := dispatch(ModeSilent, nil, []string{"-o", out}); code != 0 {
		t.Fatalf("dispatch(silent) = %d, 期望 0", code)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("silent 模式不该产出文件, Stat 返回 %v", err)
	}
}

// TestRunFakeFails 覆盖转码器非零退出。
func TestRunFakeFails(t *testing.T) {
	if code := dispatch(ModeFail, nil, []string{"-o", "unused.webp"}); code != 7 {
		t.Fatalf("dispatch(fail) = %d, 期望 7", code)
	}
}

// TestRunFakeRejectsAnUnknownMode 覆盖拼错模式名的情况。
//
// 报错而不是「默认成功」：一个拼错的模式名会让所有用例都默默地走同一条路，
// 而它们看上去仍然全绿。
func TestRunFakeRejectsAnUnknownMode(t *testing.T) {
	if code := dispatch("nonsense", nil, nil); code != 78 {
		t.Fatalf("未知模式 dispatch = %d, 期望 78", code)
	}
}

// TestRunFakeFailOnOpus 覆盖「首编码器不可用、回退可用」的两半。
//
// 两个方向都要走：含 libopus 的那次必须失败（否则「回退」这条路径根本不会被触发，
// 用例会以一种看起来通过的方式什么都没测到）。
func TestRunFakeFailOnOpus(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.webm")

	opusArgs := []string{"-i", "in.mp3", "-c:a", opusMarker, "-b:a", "128k", "-f", "webm", out}
	if code := dispatch(ModeFailOnOpus, nil, opusArgs); code != 7 {
		t.Fatalf("libopus 那次 dispatch = %d, 期望 7", code)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("libopus 失败的那次不该产出文件, Stat 返回 %v", err)
	}

	vorbisArgs := []string{"-i", "in.mp3", "-c:a", "libvorbis", "-q:a", "5", "-f", "webm", out}
	if code := dispatch(ModeFailOnOpus, nil, vorbisArgs); code != 0 {
		t.Fatalf("回退那次 dispatch = %d, 期望 0", code)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("回退那次应当产出文件: %v", err)
	}
}

// TestRunFakeSilentVorbis 覆盖「回退那次也没有产物」。
//
// 这条与「首编码器没有产物」是两处独立的检查，落在 ConvertAudioToWebM 的不同分支上，
// 所以需要一种「首编码器失败、回退成功退出但不写文件」的形态。
func TestRunFakeSilentVorbis(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.webm")

	opusArgs := []string{"-c:a", opusMarker, "-f", "webm", out}
	if code := dispatch(ModeSilentVorbis, nil, opusArgs); code != 7 {
		t.Fatalf("libopus 那次 dispatch = %d, 期望 7", code)
	}

	vorbisArgs := []string{"-c:a", "libvorbis", "-f", "webm", out}
	if code := dispatch(ModeSilentVorbis, nil, vorbisArgs); code != 0 {
		t.Fatalf("回退那次 dispatch = %d, 期望 0", code)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("回退那次不该产出文件, Stat 返回 %v", err)
	}
}

// TestHasArg 覆盖子串匹配，含「一个都没有」这条。
func TestHasArg(t *testing.T) {
	args := []string{"-c:a", "libvorbis", "-q:a", "5"}
	if !hasArg(args, "libvorbis") {
		t.Fatal("hasArg 应当认出 libvorbis")
	}
	if hasArg(args, "libopus") {
		t.Fatal("hasArg 不该把 libvorbis 当成 libopus")
	}
	if hasArg(nil, "libopus") {
		t.Fatal("空参数表里不该找到任何东西")
	}
}

// TestRecordArgsCapturesTheCommandLine 覆盖参数记录的两种情况。
//
// 这条能力本身就是被测代码 argv 的唯一证词：假工具总是写最后一个参数，
// 所以「有没有产物」的断言对命令行的形状一无所知。
func TestRecordArgsCapturesTheCommandLine(t *testing.T) {
	tmp := t.TempDir()
	log := filepath.Join(tmp, "args.log")
	// 产物路径必须落在临时目录里：假工具是照 argv 的**最后一个参数**写的，
	// 给它一个相对名就会把文件丢在当前工作目录——也就是这个包的源码目录里，
	// 而一个测试跑完在源码树里留下垃圾，只会在下一次 git status 时被人发现。
	args := []string{"-quiet", "a.png", "-o", filepath.Join(tmp, "out.webp")}

	if code := dispatch(ModeOK, nil, args); code != 0 {
		t.Fatalf("未开日志时 dispatch = %d, 期望 0", code)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("没开日志时不该产生文件, Stat 返回 %v", err)
	}

	RecordArgs(t, log)
	if code := dispatch(ModeOK, nil, args); code != 0 {
		t.Fatalf("开启日志后 dispatch = %d, 期望 0", code)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("读参数日志失败: %v", err)
	}
	// 逐字比对整条命令行：日志存在的意义就是「argv 长什么样」这件事有证词，
	// 而传错一个开关（把 -o 写成 -output）在测试里只会表现为全绿。
	if got, want := strings.TrimSpace(string(data)), strings.Join(args, " "); got != want {
		t.Fatalf("参数日志 = %q, 期望 %q", got, want)
	}
}

// TestRecordArgsKeepsGoingWhenTheLogCannotBeOpened 覆盖日志路径不可写。
//
// 关键性质是「假工具照常工作」：日志是诊断辅助，它写不进去不该把一次成功的转码
// 变成失败——那会让被测代码收到一个假故障，方向完全指错。
func TestRecordArgsKeepsGoingWhenTheLogCannotBeOpened(t *testing.T) {
	unwritable := filepath.Join(t.TempDir(), "no-such-dir", "args.log")
	RecordArgs(t, unwritable)

	out := filepath.Join(t.TempDir(), "out.webp")
	if code := dispatch(ModeOK, nil, []string{"-o", out}); code != 0 {
		t.Fatalf("日志写不进去时 dispatch = %d, 期望 0", code)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("日志写不进去不该影响产物: %v", err)
	}
}

// TestMarkStarted covers the signal used by the transcode-slot tests. Without a
// marker directory it must be a no-op; with one it must create the file; and an
// unwritable directory must not make the fake tool fail.
func TestMarkStarted(t *testing.T) {
	t.Run("no marker directory", func(t *testing.T) {
		t.Setenv(MarkerEnv, "")
		markStarted()
	})

	t.Run("marker directory", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(MarkerEnv, dir)
		markStarted()
		if _, err := os.Stat(filepath.Join(dir, "pid-"+strconv.Itoa(os.Getpid()))); err != nil {
			t.Fatalf("the marker was not written: %v", err)
		}
	})

	t.Run("unwritable marker directory", func(t *testing.T) {
		t.Setenv(MarkerEnv, filepath.Join(t.TempDir(), "missing"))
		markStarted() // must not panic or exit
	})
}

// TestFakeConfiguresTheProcess 确认 Fake 同时做了两件事：把模式写进本进程环境
// （子进程会原样继承），并把测试二进制自己的路径交出去。
func TestFakeConfiguresTheProcess(t *testing.T) {
	path := Fake(t, ModeFail)

	if got := os.Getenv(ModeEnv); got != ModeFail {
		t.Fatalf("%s = %q, 期望 %q", ModeEnv, got, ModeFail)
	}
	if path != Self() {
		t.Fatalf("Fake() = %q, 期望与 Self() 一致 (%q)", path, Self())
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("假工具路径应当是绝对路径, 实际 %q", path)
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		t.Fatalf("假工具路径必须指向一个真实文件: Stat(%q) = %v", path, err)
	}
}

// TestResolveSelfKeepsTheRawValueWhenTheWorkingDirectoryIsUnreadable 覆盖那条退路。
//
// 替换 absPath 而不是真的删掉 cwd：Windows 上删不掉自己所在的目录（被系统占用），
// 而 Linux 上删得掉——一条只在某个平台上测得到的退路，等于另一个平台上没测。
func TestResolveSelfKeepsTheRawValueWhenTheWorkingDirectoryIsUnreadable(t *testing.T) {
	original := absPath
	absPath = func(string) (string, error) { return "", errors.New("current directory is gone") }
	got := resolveSelf("relative.tool")
	absPath = original // 立刻换回来，别让这个替换活过本用例

	if got != "relative.tool" {
		t.Fatalf("resolveSelf() = %q, 期望原样返回 %q", got, "relative.tool")
	}
}

// TestResolveSelfReturnsAnAbsolutePath 是上面那条的反面：正常情况下必须规范化。
func TestResolveSelfReturnsAnAbsolutePath(t *testing.T) {
	if got := resolveSelf(os.Args[0]); !filepath.IsAbs(got) {
		t.Fatalf("resolveSelf(%q) = %q, 期望绝对路径", os.Args[0], got)
	}
}
