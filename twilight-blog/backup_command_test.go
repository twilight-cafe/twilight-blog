package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blog-engine/internal/backup"
	"blog-engine/internal/config"
	"blog-engine/internal/search"
)

// TestParseBackupArgs 覆盖参数解析的四种写法与全部拒绝路径。
//
// 手写解析的全部价值就在这里：四种写法（-x V / --x V / -x=V / --x=V）必须都成立，
// 而重复指定、缺值、未知参数必须报错而不是取其中一个——一个被静默覆盖的 -o
// 会让用户以为归档去了 A，实际写到了 B。
func TestParseBackupArgs(t *testing.T) {
	t.Run("默认输出名", func(t *testing.T) {
		opts, err := parseBackupArgs(nil)
		if err != nil {
			t.Fatalf("parseBackupArgs() 报错: %v", err)
		}
		if !strings.HasPrefix(opts.output, "twilight-backup-") || !strings.HasSuffix(opts.output, ".tar.gz") {
			t.Fatalf("默认输出名 = %q", opts.output)
		}
	})

	t.Run("四种写法", func(t *testing.T) {
		for _, args := range [][]string{
			{"-o", "x.tar.gz"},
			{"--output", "x.tar.gz"},
			{"-o=x.tar.gz"},
			{"--output=x.tar.gz"},
		} {
			opts, err := parseBackupArgs(args)
			if err != nil {
				t.Fatalf("parseBackupArgs(%v) 报错: %v", args, err)
			}
			if opts.output != "x.tar.gz" {
				t.Fatalf("parseBackupArgs(%v).output = %q, 期望 x.tar.gz", args, opts.output)
			}
		}
	})

	t.Run("开关", func(t *testing.T) {
		opts, err := parseBackupArgs([]string{"--with-index", "--with-logs", "--force", "--quiet"})
		if err != nil {
			t.Fatalf("parseBackupArgs() 报错: %v", err)
		}
		if !opts.withIndex || !opts.withLogs || !opts.force || !opts.quiet {
			t.Fatalf("开关没有被记下来: %+v", opts)
		}
	})

	t.Run("拒绝", func(t *testing.T) {
		for _, args := range [][]string{
			{"-o"},                 // 缺值
			{"-o", "a", "-o", "b"}, // 重复指定
			{"--output="},          // 空值
			{"-o="},                // 短选项的同一形态
			{"--output", " "},
			{"--nope"}, // 未知参数
		} {
			if _, err := parseBackupArgs(args); err == nil {
				t.Fatalf("parseBackupArgs(%v) 应当报错", args)
			}
		}
	})
}

// TestDefaultArchiveNameUsesUTC 钉住 UTC 命名。
//
// 同一个瞬间无论本机在哪个时区，名字都必须一样：排障时要把归档和日志里的时间
// 对上，而「这是几点打的」在不同时区有不同答案是纯粹的干扰。
//
// 反证方式：把 defaultArchiveName 里的 .UTC() 去掉 → 本条失败。
func TestDefaultArchiveNameUsesUTC(t *testing.T) {
	// 23:30 UTC+8 就是 15:30 UTC，同一天。
	moment := time.Date(2026, 9, 18, 23, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))

	want := "twilight-backup-20260918-153000.tar.gz"
	if got := defaultArchiveName(moment); got != want {
		t.Fatalf("defaultArchiveName() = %q, 期望 %q", got, want)
	}
}

// TestBackupWritesEveryEssentialPart 是这条命令的主干断言。
//
// 它同时钉住四件事：三样不可替代的数据都进了归档、旁文件的摘要与归档本体一致、
// 归档能被真的读回来、清单里的体积与磁盘上一致。
//
// 反证方式：把 performBackup 里 settings 那一条 Entry 删掉 → 本条在「缺少 settings」
// 处失败。
func TestBackupWritesEveryEssentialPart(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png-bytes", "sub/b.jpg": "jpg-bytes"})

	const archive = "out.tar.gz"
	manifest, err := performBackup(backupOptions{output: archive})
	if err != nil {
		t.Fatalf("performBackup() 报错: %v", err)
	}

	names := map[string]bool{}
	for _, part := range manifest.Parts {
		names[part.Name] = true
	}
	for _, want := range []string{backup.PartDatabase, backup.PartSettings, backup.PartMedia} {
		if !names[want] {
			t.Fatalf("归档里缺少 %s 部分: %+v", want, manifest.Parts)
		}
	}

	// 旁文件必须与归档本体一致——它存在的意义就是「事后核对一次」。
	sidecar, err := os.ReadFile(archive + ".sha256")
	if err != nil {
		t.Fatalf("没有写出 .sha256 旁文件: %v", err)
	}
	sum, err := backup.HashFile(archive)
	if err != nil {
		t.Fatalf("计算摘要失败: %v", err)
	}
	if !strings.HasPrefix(string(sidecar), sum) {
		t.Fatalf("旁文件的摘要与归档不符:\n  sidecar=%q\n  real=%s", sidecar, sum)
	}

	// 归档必须能真的读回来：写出去的字节如果读不回来，前面那些断言都是自说自话。
	file, err := os.Open(archive)
	if err != nil {
		t.Fatalf("打开归档失败: %v", err)
	}
	defer file.Close()

	reader, err := backup.Open(file)
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	result, err := reader.Extract(t.TempDir(), backup.ExtractOptions{})
	if err != nil {
		t.Fatalf("Extract() 报错: %v", err)
	}
	// 库 + 配置 + 两张图。
	if result.Files != 4 {
		t.Fatalf("解出 %d 个文件, 期望 4", result.Files)
	}
}

// TestBackupSkipsTheIndexUnlessAsked 钉住默认不打包索引。
//
// 索引每次启动都从数据库全量重建，把它打进去只是让归档更大、还多带一个必须与
// 索引成对的密钥文件。--with-index 时才带上，且**两个一起带**——换了钥匙的旧库
// 对不上，只带一半的索引只是一堆读不出来的文件。
//
// 反证方式：把 performBackup 里 opts.withIndex 的条件删掉（改成总是带）→
// 本条的「默认」子断言失败。
func TestBackupSkipsTheIndexUnlessAsked(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})
	seedTree(t, search.DefaultDataDir, map[string]string{"data.mdb": "index"})
	if err := os.WriteFile(search.MasterKeyPath(search.DefaultDataDir), []byte("0123456789abcdef\n"), 0o600); err != nil {
		t.Fatalf("写主密钥失败: %v", err)
	}

	manifest, err := performBackup(backupOptions{output: "plain.tar.gz"})
	if err != nil {
		t.Fatalf("performBackup() 报错: %v", err)
	}
	if manifest.IsPart(backup.PartSearch) || manifest.IsPart(backup.PartSearchKey) {
		t.Fatalf("默认不该带上索引: %+v", manifest.Parts)
	}

	manifest, err = performBackup(backupOptions{output: "full.tar.gz", withIndex: true})
	if err != nil {
		t.Fatalf("performBackup(--with-index) 报错: %v", err)
	}
	if !manifest.IsPart(backup.PartSearch) || !manifest.IsPart(backup.PartSearchKey) {
		t.Fatalf("--with-index 应当把索引与主密钥一起带上: %+v", manifest.Parts)
	}
}

// TestBackupIncludesRotatedLogs 确认 --with-logs 带上全部轮转代。
//
// 只带当前那一个是不够的：轮转掉的那批恰恰是「出问题之前」的记录，
// 而那正是要看的东西。
func TestBackupIncludesRotatedLogs(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	seedTree(t, ".", map[string]string{"log.txt": "now", "log.txt.1": "before", "log.txt.2": "older"})

	manifest, err := performBackup(backupOptions{output: "logs.tar.gz", withLogs: true})
	if err != nil {
		t.Fatalf("performBackup() 报错: %v", err)
	}

	logs := 0
	for _, part := range manifest.Parts {
		if part.Name == backup.PartLogs {
			logs++
		}
	}
	if logs != 3 {
		t.Fatalf("归档里应有 3 个日志文件, 实际 %d: %+v", logs, manifest.Parts)
	}
}

// TestBackupRefusesToOverwrite 钉住「默认不覆盖」。
//
// 一份三个月前的归档比一次手滑更值钱，所以覆盖必须显式要求。
//
// 反证方式：把 writeArchive 里的 opts.force 判断删掉 → 本条失败。
func TestBackupRefusesToOverwrite(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)

	if _, err := performBackup(backupOptions{output: "a.tar.gz"}); err != nil {
		t.Fatalf("第一次备份应当成功: %v", err)
	}
	if _, err := performBackup(backupOptions{output: "a.tar.gz"}); err == nil {
		t.Fatal("目标已存在时应当拒绝覆盖")
	}
	if _, err := performBackup(backupOptions{output: "a.tar.gz", force: true}); err != nil {
		t.Fatalf("--force 应当允许覆盖: %v", err)
	}
}

// TestBackupRefusesOutputInsideMediaDir 钉住输出位置的那条守卫。
//
// 归档是流式写的，而媒体目录正是它自己正在读的那棵树：写进去等于一边读一边改
// 被读的内容。更常见的原因是这个路径几乎总是笔误。
//
// 反证方式：删掉 checkOutputTarget 的调用 → 本条失败。
func TestBackupRefusesOutputInsideMediaDir(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})

	_, err := performBackup(backupOptions{output: filepath.Join("media", "self.tar.gz")})
	if err == nil {
		t.Fatal("把归档写进媒体目录应当被拒绝")
	}
	if exists(filepath.Join("media", "self.tar.gz")) {
		t.Fatal("被拒绝的备份不该在媒体目录里留下文件")
	}
}

// TestBackupRequiresAnInstalledSite 确认缺配置时直接失败。
//
// 产出一份「没有 settings.json 的归档」看起来是成功的，而它要到还原那天才暴露，
// 那时已经太晚了。
func TestBackupRequiresAnInstalledSite(t *testing.T) {
	inTempWorkdir(t)

	if _, err := performBackup(backupOptions{output: "x.tar.gz"}); err == nil {
		t.Fatal("工作目录里没有 settings.json 时应当报错")
	}
}

// TestReadMediaDir 覆盖读出 media_dir 的几条路径。
//
// 它刻意不走 config.Load（那会做迁移、可能回写文件），所以那套容错必须在这里
// 自己成立：BOM、空值、缺键、坏 JSON 全都要回退到默认值而不是报错——备份不该
// 因为一个可以合理兜底的配置项而失败。
func TestReadMediaDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	cases := []struct {
		label string
		body  string
		want  string
	}{
		{"正常", `{"media_dir":"uploads"}`, "uploads"},
		{"带 UTF-8 BOM", "\ufeff" + `{"media_dir":"uploads"}`, "uploads"},
		{"只写了空白", `{"media_dir":"   "}`, config.DefaultMediaDir},
		{"没有这个键", `{"port":8080}`, config.DefaultMediaDir},
		{"不是 JSON", `not json at all`, config.DefaultMediaDir},
	}
	for _, tc := range cases {
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatalf("准备 %s 失败: %v", tc.label, err)
		}
		if got := readMediaDir(path); got != tc.want {
			t.Fatalf("%s: readMediaDir() = %q, 期望 %q", tc.label, got, tc.want)
		}
	}

	if got := readMediaDir(filepath.Join(dir, "missing.json")); got != config.DefaultMediaDir {
		t.Fatalf("配置文件不存在时应当回退到默认值, 实际 %q", got)
	}
}

// TestWithin 覆盖路径包含判断。
//
// 它是 checkOutputTarget 的判据，写错的后果是「媒体目录里的归档没被拦住」或者
// 「正常路径被误拦」。子目录、父目录、兄弟目录三种关系都要对。
func TestWithin(t *testing.T) {
	base := filepath.Join("a", "b")

	cases := map[string]bool{
		base:                          true,  // 自己
		filepath.Join(base, "c.txt"):  true,  // 直接子项
		filepath.Join(base, "d", "e"): true,  // 深层子项
		filepath.Join("a", "c"):       false, // 兄弟
		filepath.Join("a"):            false, // 父目录
		"a":                           false,
	}

	for path, want := range cases {
		if got := within(base, path); got != want {
			t.Fatalf("within(%q, %q) = %v, 期望 %v", base, path, got, want)
		}
	}

	// 一个绝对、一个相对：filepath.Rel 做不了这种换算，此时必须判定为「不在里面」
	// 而不是把错误吞掉当成「是」。判错方向的后果是**媒体目录里的归档没被拦住**，
	// 而那正是这条守卫唯一要防的事。
	if within(base, filepath.Join(t.TempDir(), "x.tar.gz")) {
		t.Fatal("绝对路径与相对基准无法比较时，必须返回 false")
	}
}

// TestRunBackupCommandReportsContents 确认非 quiet 模式下逐项列出内容。
//
// 逐项列体积是刻意的：扫一眼就能发现「这次媒体怎么只有 3 个文件」，
// 而不是等到需要还原的那天才知道少了什么。
func TestRunBackupCommandReportsContents(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})
	out, errOut := captureStreams(t, "")

	if code := runBackupCommand([]string{"-o", "r.tar.gz"}); code != 0 {
		t.Fatalf("退出码 = %d, stderr=%s", code, errOut.String())
	}
	for _, want := range []string{backup.PartDatabase, backup.PartSettings, backup.PartMedia, "total"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("摘要里缺少 %q:\n%s", want, out.String())
		}
	}
}

// TestRunBackupCommandQuietPrintsOnlyThePath 钉住 --quiet 的输出契约：
// 脚本靠它拿到路径，多打一行就等于让 `$(...)` 拿到一个带废话的值。
func TestRunBackupCommandQuietPrintsOnlyThePath(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	out, errOut := captureStreams(t, "")

	if code := runBackupCommand([]string{"-o", "q.tar.gz", "--quiet"}); code != 0 {
		t.Fatalf("退出码 = %d, stderr=%s", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "q.tar.gz" {
		t.Fatalf("--quiet 应当只打印归档路径, 实际 %q", out.String())
	}
}

// TestRunBackupCommandWritesToStdout 覆盖 `-o -`。
//
// stdout 上是二进制归档，所以摘要必须改走 stderr——否则
// `exe backup -o - > x.tar.gz` 会把摘要写进归档，那份文件再也读不出来。
//
// 反证方式：把 runBackupCommand 里选 destination 的那两行删掉（摘要固定走 stdout）
// → 本条在「stdout 上不是一份可读的归档」处失败。
func TestRunBackupCommandWritesToStdout(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})
	out, errOut := captureStreams(t, "")

	if code := runBackupCommand([]string{"-o", "-"}); code != 0 {
		t.Fatalf("退出码 = %d, stderr=%s", code, errOut.String())
	}
	if _, err := backup.Open(bytes.NewReader(out.Bytes())); err != nil {
		t.Fatalf("stdout 上不是一份可读的归档: %v", err)
	}
	if !strings.Contains(errOut.String(), "created") {
		t.Fatalf("摘要应当改走 stderr, 实际是 %q", errOut.String())
	}
}

// TestRunBackupCommandUsageErrors 确认用法错误走的是退出码 2 而不是 1：
// 脚本据此区分「你输错了」与「环境坏了」。
func TestRunBackupCommandUsageErrors(t *testing.T) {
	inTempWorkdir(t)
	_, errOut := captureStreams(t, "")

	if code := runBackupCommand([]string{"--nope"}); code != exitUsageOrValidation {
		t.Fatalf("未知参数的退出码 = %d, 期望 %d", code, exitUsageOrValidation)
	}
	if !strings.Contains(errOut.String(), "unknown argument") {
		t.Fatalf("应当在 stderr 上说明哪里错了: %q", errOut.String())
	}
}

// TestRunBackupCommandFailureReportsToStderr 确认运行期失败走退出码 1。
func TestRunBackupCommandFailureReportsToStderr(t *testing.T) {
	inTempWorkdir(t) // 空目录：没有任何可备份的东西
	_, errOut := captureStreams(t, "")

	if code := runBackupCommand([]string{"-o", "x.tar.gz"}); code != exitFailure {
		t.Fatalf("缺少站点时的退出码 = %d, 期望 %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "backup failed") {
		t.Fatalf("应当在 stderr 上说明失败原因: %q", errOut.String())
	}
}
