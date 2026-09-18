package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blog-engine/internal/database"
	"blog-engine/internal/pidfile"
)

// --- helpers ---------------------------------------------------------------

// restoreIsTerminal 让 isTerminal 在本次用例里恒返回 want。
//
// 真实终端没法在测试里伪造，而「非终端一律拒绝」「输入不是 yes 就取消」这两条
// 分支恰恰最该被钉住——它们就是「没人能回答不等于同意」这条规则的实现。
func restoreIsTerminal(t *testing.T, want bool) {
	t.Helper()
	previous := isTerminal
	isTerminal = func(io.Reader) bool { return want }
	t.Cleanup(func() { isTerminal = previous })
}

// withDB 打开当前工作目录里的 blog.db，把连接交给 fn 之后关闭。
//
// 直接开连接而不是走仓储层：这些断言看的是「库里有没有那一行」（会话有没有被删、
// 昵称回来了没有），中间多一层抽象只会让「实际发生了什么」更模糊。
func withDB(t *testing.T, fn func(db *sql.DB)) {
	t.Helper()

	db, err := database.Open(databasePath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()
	fn(db)
}

// seedSession 往 sessions 表里插一行，用来给「默认吊销会话」那条用例造对照。
func seedSession(t *testing.T, token string) {
	t.Helper()
	withDB(t, func(db *sql.DB) {
		if _, err := db.Exec(
			`INSERT INTO sessions (token_hash, role, user_id, csrf_hash, expires_at, created_at)
			 VALUES (?, 'admin', NULL, 'csrf', ?, ?)`,
			token, time.Now().Add(time.Hour).Unix(), time.Now().Unix(),
		); err != nil {
			t.Fatalf("插入会话失败: %v", err)
		}
	})
}

func countSessions(t *testing.T) int {
	t.Helper()
	total := 0
	withDB(t, func(db *sql.DB) {
		if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&total); err != nil {
			t.Fatalf("统计会话失败: %v", err)
		}
	})
	return total
}

func clearSessions(t *testing.T) {
	t.Helper()
	withDB(t, func(db *sql.DB) {
		if _, err := db.Exec(`DELETE FROM sessions`); err != nil {
			t.Fatalf("清空会话失败: %v", err)
		}
	})
}

// --- 参数解析 ---------------------------------------------------------------

// TestParseRestoreArgs 覆盖位置参数、全部开关与拒绝路径。
func TestParseRestoreArgs(t *testing.T) {
	t.Run("位置参数与全部开关", func(t *testing.T) {
		opts, err := parseRestoreArgs([]string{
			"site.tar.gz", "--yes", "--keep-settings", "--keep-sessions",
			"--no-safety-backup", "--dry-run", "--force",
		})
		if err != nil {
			t.Fatalf("parseRestoreArgs() 报错: %v", err)
		}
		if opts.archive != "site.tar.gz" || !opts.yes || !opts.keepSettings ||
			!opts.keepSessions || !opts.noSafetyBackup || !opts.dryRun || !opts.force {
			t.Fatalf("参数没有被正确记下来: %+v", opts)
		}
	})

	t.Run("短选项 -y", func(t *testing.T) {
		opts, err := parseRestoreArgs([]string{"-y", "site.tar.gz"})
		if err != nil {
			t.Fatalf("parseRestoreArgs() 报错: %v", err)
		}
		if !opts.yes {
			t.Fatal("-y 应当等同于 --yes")
		}
	})

	t.Run("归档出现在开关之后", func(t *testing.T) {
		opts, err := parseRestoreArgs([]string{"--dry-run", "site.tar.gz"})
		if err != nil {
			t.Fatalf("parseRestoreArgs() 报错: %v", err)
		}
		if opts.archive != "site.tar.gz" || !opts.dryRun {
			t.Fatalf("参数没有被正确记下来: %+v", opts)
		}
	})

	t.Run("- 是合法的输入而不是参数", func(t *testing.T) {
		opts, err := parseRestoreArgs([]string{"-"})
		if err != nil {
			t.Fatalf("parseRestoreArgs() 报错: %v", err)
		}
		if opts.archive != "-" {
			t.Fatalf("archive = %q, 期望 -", opts.archive)
		}
	})

	t.Run("拒绝", func(t *testing.T) {
		for _, args := range [][]string{
			nil,                      // 缺少归档
			{"a.tar.gz", "b.tar.gz"}, // 两个归档
			{"--nope", "a.tar.gz"},   // 未知参数
			{"-o", "a.tar.gz"},       // backup 的选项不该在这里被接受
		} {
			if _, err := parseRestoreArgs(args); err == nil {
				t.Fatalf("parseRestoreArgs(%v) 应当报错", args)
			}
		}
	})
}

// TestConfirmRestore 钉住「没人能回答不等于同意」。
//
// 这条规则唯一的实现就在这里：非终端输入直接拒绝。把它写成「读不到就当同意」，
// 一次管道调用（或 systemd 单元里的一条 ExecStart）就能在无人值守时替换掉整站数据。
//
// 反证方式：把 confirmRestore 开头的 isTerminal 判断删掉 → 「非终端一律拒绝」
// 子用例失败。
func TestConfirmRestore(t *testing.T) {
	t.Run("非终端一律拒绝", func(t *testing.T) {
		// 这里用真实的 isTerminal：strings.Reader 不是 *os.File。
		var out bytes.Buffer
		// 哪怕内容正好是 yes 也不能被当成同意——「没人回答」与「回答了 yes」
		// 是两件不同的事，而前者最常见。
		if err := confirmRestore(&out, strings.NewReader("yes\n")); err == nil {
			t.Fatal("非终端输入应当被拒绝，而不是把内容当成同意")
		}
	})

	t.Run("输入 yes 才继续", func(t *testing.T) {
		restoreIsTerminal(t, true)
		var out bytes.Buffer
		if err := confirmRestore(&out, strings.NewReader("yes\n")); err != nil {
			t.Fatalf("输入 yes 时应当继续: %v", err)
		}
		if !strings.Contains(out.String(), "REPLACE") {
			t.Fatalf("提示里应当明确这是一次替换: %q", out.String())
		}
	})

	t.Run("大小写与空白无关", func(t *testing.T) {
		restoreIsTerminal(t, true)
		var out bytes.Buffer
		if err := confirmRestore(&out, strings.NewReader("  YeS  \n")); err != nil {
			t.Fatalf("YES 应当被接受: %v", err)
		}
	})

	t.Run("其他输入一律取消", func(t *testing.T) {
		restoreIsTerminal(t, true)
		for _, answer := range []string{"n\n", "no\n", "y\n", "\n", "yes please\n"} {
			var out bytes.Buffer
			if err := confirmRestore(&out, strings.NewReader(answer)); err == nil {
				t.Fatalf("输入 %q 应当被当成取消", answer)
			}
		}
	})
}

// --- 文件换位原语 -----------------------------------------------------------

// TestSwapIn 覆盖换位的三种形态：替换、保留旧的、目标本来就不存在。
//
// 反证方式：把 swapIn 里的 os.Rename(dst, parked) 换成直接 os.RemoveAll(dst)
// → 「keepOld 时把旧的挪到一边」子用例失败（旧数据已经没了）。
func TestSwapIn(t *testing.T) {
	t.Run("替换并删除旧的", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "staged.db")
		dst := filepath.Join(dir, "blog.db")
		seedTree(t, dir, map[string]string{"staged.db": "new", "blog.db": "old"})

		kept, err := swapIn(src, dst, false)
		if err != nil {
			t.Fatalf("swapIn() 报错: %v", err)
		}
		if kept != "" {
			t.Fatalf("keepOld=false 时不该留下旧数据: %q", kept)
		}
		if body, _ := os.ReadFile(dst); string(body) != "new" {
			t.Fatalf("替换后的内容 = %q, 期望 new", body)
		}
		if exists(src) {
			t.Fatal("源文件应当已经被移走，而不是被拷贝")
		}
		if leftovers := replacedLeftovers(t, dir); len(leftovers) != 0 {
			t.Fatalf("不该留下 .replaced- 残留: %v", leftovers)
		}
	})

	t.Run("keepOld 时把旧的挪到一边", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "staged.db")
		dst := filepath.Join(dir, "blog.db")
		seedTree(t, dir, map[string]string{"staged.db": "new", "blog.db": "old"})

		kept, err := swapIn(src, dst, true)
		if err != nil {
			t.Fatalf("swapIn() 报错: %v", err)
		}
		if kept == "" {
			t.Fatal("keepOld=true 时应当返回被保留的旧数据路径")
		}
		if body, _ := os.ReadFile(kept); string(body) != "old" {
			t.Fatalf("保留下来的内容 = %q, 期望 old", body)
		}
		if body, _ := os.ReadFile(dst); string(body) != "new" {
			t.Fatalf("替换后的内容 = %q, 期望 new", body)
		}
	})

	t.Run("目标不存在时直接放置", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "staged.db")
		seedTree(t, dir, map[string]string{"staged.db": "new"})

		kept, err := swapIn(src, filepath.Join(dir, "blog.db"), false)
		if err != nil {
			t.Fatalf("swapIn() 报错: %v", err)
		}
		if kept != "" {
			t.Fatalf("没有旧数据时不该报告保留路径: %q", kept)
		}
	})

	t.Run("源缺失时报错", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := swapIn(filepath.Join(dir, "missing"), filepath.Join(dir, "dst"), false); err == nil {
			t.Fatal("暂存的源不存在时应当报错")
		}
	})
}

// TestSwapInRollsBackWhenPlacingFails 钉住失败时的回滚。
//
// 一次失败的还原必须留下一个**能用的站点**，而不是一个「什么都没有」的目录——
// 后者比拒绝还原糟得多，因为数据听起来还在（「我有备份」），只是当下不可用。
//
// 构造：让目标落在源目录**内部**。这不是硬凑的边角情况——把媒体目录还原到它自己
// 的子目录里就会走到这条路径，而它必然失败（内核拒绝对目录做这样的 rename）。
//
// 反证方式：把 swapIn 里 `if parked != ""` 那段恢复逻辑删掉 → 本条失败。
func TestSwapInRollsBackWhenPlacingFails(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "media")    // 要放进去的东西（一个目录）
	dst := filepath.Join(src, "keep.txt") // 目标在源内部 → 放置必然失败

	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatalf("准备源目录失败: %v", err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatalf("准备旧数据失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatalf("准备新数据失败: %v", err)
	}

	if _, err := swapIn(src, dst, false); err == nil {
		t.Fatal("把目录放进它自己的子目录应当失败")
	}

	// 关键：旧数据必须回到原位且内容不变。
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("失败之后旧数据没有回到原位: %v", err)
	}
	if string(body) != "old" {
		t.Fatalf("回到原位的旧数据内容 = %q, 期望 old", body)
	}
}

// TestCopyTreeCopiesNestedContent 覆盖「跨卷时退化成拷贝」那条路走得通。
func TestCopyTreeCopiesNestedContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	seedTree(t, src, map[string]string{"a.txt": "A", "sub/b.txt": "B"})

	dst := filepath.Join(dir, "dst")
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree() 报错: %v", err)
	}

	for name, want := range map[string]string{"a.txt": "A", "sub/b.txt": "B"} {
		body, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		if string(body) != want {
			t.Fatalf("%s = %q, 期望 %q", name, body, want)
		}
	}
}

// TestCopyTreeRefusesToCopyIntoItself 钉住那条防无限递归的守卫。
//
// 没有它，「目标在源目录内部」会让遍历每拷一层就多出一层要拷的东西——表现是
// 进程卡死并把磁盘写满，而不是一个错误。
//
// 反证方式：删掉 copyTree 开头的那段判断 → 本条会挂住（而不是失败），
// 足以说明它为什么必须存在。
func TestCopyTreeRefusesToCopyIntoItself(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	seedTree(t, src, map[string]string{"a.txt": "A"})

	if err := copyTree(src, filepath.Join(src, "inner")); err == nil {
		t.Fatal("把目录拷进它自己的子目录应当被拒绝")
	}
	if err := copyTree(src, src); err == nil {
		t.Fatal("把目录拷到它自己身上应当被拒绝")
	}
}

// TestRemoveSQLiteSidecars 钉住「覆盖数据库之前先清掉三个旁文件」。
//
// 陈旧的 WAL 属于还原**之前**那一份库。让它与刚放下的库共存，最好的情况是被判定
// 无效后丢弃，最坏的情况是那些帧被回放进新库——写进去的是备份之后的修改，
// 或者干脆把库弄坏。三种结果都不值得赌。
//
// 反证方式：把 swapIn 调用前的那句 removeSQLiteSidecars 删掉 → 端到端用例里
// 「归档之后的写入必须消失」会变得不可靠（这正是它要防的东西）。
func TestRemoveSQLiteSidecars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blog.db")

	if err := os.WriteFile(path, []byte("db"), 0o600); err != nil {
		t.Fatalf("准备数据库失败: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.WriteFile(path+suffix, []byte("stale"), 0o600); err != nil {
			t.Fatalf("准备 %s 失败: %v", suffix, err)
		}
	}

	if err := removeSQLiteSidecars(path); err != nil {
		t.Fatalf("removeSQLiteSidecars() 报错: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if exists(path + suffix) {
			t.Fatalf("%s 没有被删掉", suffix)
		}
	}
	// 主库不归它管，必须原样留着。
	if body, err := os.ReadFile(path); err != nil || string(body) != "db" {
		t.Fatalf("主库被误删或改动了: %q %v", body, err)
	}

	// 不存在时不该报错：这三个文件本来就只在写入期间存在。
	if err := removeSQLiteSidecars(path); err != nil {
		t.Fatalf("没有旁文件时不该报错: %v", err)
	}
}

// --- 端到端 -----------------------------------------------------------------

// TestPerformRestoreEndToEnd 是这一轮功能的主干断言：
// 造站点 → 备份 → 破坏 → 还原 → 逐项确认数据回来了。
//
// 断言打在**副作用**上：数据库里的内容、磁盘上的文件与文件数。只看「还原命令
// 返回了 0」是不够的——一个把空目录放回去的还原同样返回 0。
func TestPerformRestoreEndToEnd(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png-one", "sub/b.jpg": "jpg-two"})

	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 破坏：改库里的内容、删一张图、多出一张图、改配置。
	setAdminNickname(t, "被改坏了")
	if err := os.Remove(filepath.Join("media", "a.png")); err != nil {
		t.Fatalf("删除媒体文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join("media", "leak.png"), []byte("should-be-gone"), 0o600); err != nil {
		t.Fatalf("写入多余媒体失败: %v", err)
	}
	if err := os.WriteFile(settingsPath,
		[]byte(`{"media_dir":"media","site_description":"被改坏了"}`), 0o600); err != nil {
		t.Fatalf("改写配置失败: %v", err)
	}

	captureStreams(t, "")
	if err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true}); err != nil {
		t.Fatalf("还原失败: %v", err)
	}

	// 1. 数据库内容回来了（不只是「文件在」）。
	if got := adminNickname(t); got != "站长" {
		t.Fatalf("还原后管理员昵称 = %q, 期望「站长」", got)
	}
	// 2. 被删掉的图回来了，且内容与备份时一致。
	if body, err := os.ReadFile(filepath.Join("media", "a.png")); err != nil || string(body) != "png-one" {
		t.Fatalf("被删掉的那张图没有正确回来: body=%q err=%v", body, err)
	}
	// 3. 子目录结构保住了。
	if _, err := os.Stat(filepath.Join("media", "sub", "b.jpg")); err != nil {
		t.Fatalf("子目录里的图没有回来: %v", err)
	}
	// 4. 备份之后新增的文件必须消失——媒体目录是整体替换的。
	if exists(filepath.Join("media", "leak.png")) {
		t.Fatal("备份之后新增的文件不该留下来")
	}
	// 5. 文件数回到备份那一刻（2 张图）。
	if got := countFiles(t, "media"); got != 2 {
		t.Fatalf("媒体目录里应有 2 个文件, 实际 %d", got)
	}
	// 6. 配置也回来了。
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("读配置失败: %v", err)
	}
	if !strings.Contains(string(raw), "测试站点") {
		t.Fatalf("配置没有被还原: %s", raw)
	}
}

// TestRestoreKeepsSettingsWhenAsked 覆盖 --keep-settings。
//
// 它是「把 A 站的数据还原到 B 站」时的开关：本机的端口、代理、媒体目录布局通常
// 要保留，只换内容。而媒体该放到哪正是由这个选项决定的——保留本机配置就必须按
// 本机的 media_dir 落位，否则会出现「文章都在、图全挂了」。
//
// 反证方式：把 applyRestore 里 opts.keepSettings 的分支删掉 → 本条失败。
func TestRestoreKeepsSettingsWhenAsked(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})

	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 本机此后改用另一个媒体目录，并改掉站点描述——两处都必须保留。
	if err := os.WriteFile(settingsPath,
		[]byte(`{"media_dir":"photos","site_description":"本机的描述"}`), 0o600); err != nil {
		t.Fatalf("改写配置失败: %v", err)
	}

	captureStreams(t, "")
	if err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true, keepSettings: true}); err != nil {
		t.Fatalf("还原失败: %v", err)
	}

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("读配置失败: %v", err)
	}
	if !strings.Contains(string(raw), "本机的描述") || !strings.Contains(string(raw), "photos") {
		t.Fatalf("--keep-settings 时不该覆盖 settings.json: %s", raw)
	}
	if got := countFiles(t, "photos"); got != 1 {
		t.Fatalf("媒体应当落到本机的 media_dir (photos) 里, 实际有 %d 个文件", got)
	}
}

// TestRestoreRevokesSessionsByDefault 钉住「默认吊销全部会话」。
//
// 还原会把 sessions 表回滚到备份那一刻，于是**备份之后被吊销的 token 会复活**。
// 一个已经被踢下线的设备重新拿到有效凭证，而它发生时没有任何提示。
//
// 两段断言缺一不可：只测「默认之后为 0」无法区分「主动吊销了」与「归档里本来
// 就没有」，所以第二段用 --keep-sessions 做对照——它证明归档里确实有一条会话，
// 而默认路径是把它删掉了。
//
// 反证方式：把 applyRestore 里 opts.keepSessions 的判断改成恒真 → 第一条断言失败。
func TestRestoreRevokesSessionsByDefault(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	seedSession(t, "archived-token")
	if got := countSessions(t); got != 1 {
		t.Fatalf("准备数据失败: 会话数 = %d, 期望 1", got)
	}

	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	// 备份之后这个会话被吊销了。
	clearSessions(t)

	captureStreams(t, "")
	if err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true}); err != nil {
		t.Fatalf("还原失败: %v", err)
	}
	if got := countSessions(t); got != 0 {
		t.Fatalf("默认还原之后会话数 = %d, 期望 0：备份之后被吊销的 token 不能复活", got)
	}

	// 对照：归档里确实有那一条。
	if err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true, keepSessions: true}); err != nil {
		t.Fatalf("还原失败: %v", err)
	}
	if got := countSessions(t); got != 1 {
		t.Fatalf("--keep-sessions 应当保留归档里的会话, 实际 %d", got)
	}
}

// TestRestoreLeavesPreviousDataAsideWithoutASafetyBackup 覆盖 --no-safety-backup 的
// 配套行为：没有回滚点时就**不删**旧数据，而是把它挪到一边并报告出来。
//
// 反证方式：把 applyRestore 里的 keepOld 参数改成恒 false → 本条失败。
func TestRestoreLeavesPreviousDataAsideWithoutASafetyBackup(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})

	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join("media", "a.png"), []byte("changed"), 0o600); err != nil {
		t.Fatalf("改写媒体失败: %v", err)
	}

	out, _ := captureStreams(t, "")
	if err := performRestore(restoreOptions{
		archive: "site.tar.gz", yes: true, noSafetyBackup: true,
	}); err != nil {
		t.Fatalf("还原失败: %v", err)
	}

	leftovers := replacedLeftovers(t, ".")
	if len(leftovers) == 0 {
		t.Fatal("没有安全备份兜底时应当把旧数据挪到一边，而不是删掉")
	}
	if !strings.Contains(out.String(), "kept aside") {
		t.Fatalf("摘要里应当说明旧数据被保留了: %s", out.String())
	}
}

// TestRestoreRefusesWhileAServiceIsRunning 钉住「服务在跑就拒止还原」。
//
// 正在运行的进程持有 blog.db 与媒体目录的旧句柄：它随后写下的东西会落到已经被
// 替换掉的 inode 上，而这一切不报错。所以必须先停服务，--force 才放行。
//
// 反证方式：把 refuseIfRunning 的调用删掉 → 本条第一条断言失败。
func TestRestoreRefusesWhileAServiceIsRunning(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 锁文件指向本进程——本进程确实活着。
	handle, err := pidfile.Acquire(pidfile.FileName)
	if err != nil {
		t.Fatalf("写锁文件失败: %v", err)
	}
	defer handle.Release()

	captureStreams(t, "")
	err = performRestore(restoreOptions{archive: "site.tar.gz", yes: true})
	if err == nil {
		t.Fatal("服务在跑时必须拒绝还原")
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Fatalf("错误信息应当说明原因: %v", err)
	}

	if err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true, force: true}); err != nil {
		t.Fatalf("--force 应当越过这条拒止: %v", err)
	}
}

// TestRestoreIgnoresAStaleLock 确认陈旧锁放行而不是拒止。
//
// 「活着的进程」与「上次没退干净」对应的动作完全相反。把两者混为一谈，用户会被
// 永久挡在门外，而唯一的出路是手动去删一个自己不知道的文件。
//
// 反证方式：让 processAlive 恒返回 true → 本条失败。
func TestRestoreIgnoresAStaleLock(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 指向一个不存在的进程，模拟崩溃后留下的锁文件。
	stale, err := json.Marshal(map[string]any{"pid": 1 << 29, "started_at": time.Now().UTC()})
	if err != nil {
		t.Fatalf("序列化锁文件失败: %v", err)
	}
	if err := os.WriteFile(pidfile.FileName, stale, 0o600); err != nil {
		t.Fatalf("写锁文件失败: %v", err)
	}

	out, _ := captureStreams(t, "")
	if err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true}); err != nil {
		t.Fatalf("陈旧锁不该挡住还原: %v", err)
	}
	if !strings.Contains(out.String(), "no longer running") {
		t.Fatalf("应当提示这是一份陈旧锁: %s", out.String())
	}
}

// TestDryRunChangesNothing 钉住 --dry-run 的承诺。
//
// 它仍然要**完整解包一遍**（否则「能还原」这个结论是空的），但不能动任何既有
// 数据。所以断言是两个：数据库内容没变、文件数没变。
//
// 反证方式：把 performRestore 里 dryRun 的提前返回删掉 → 本条失败。
func TestDryRunChangesNothing(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	setAdminNickname(t, "改坏了")
	if err := os.Remove(filepath.Join("media", "a.png")); err != nil {
		t.Fatalf("删除媒体失败: %v", err)
	}
	before := countFiles(t, ".")

	out, _ := captureStreams(t, "")
	if err := performRestore(restoreOptions{archive: "site.tar.gz", dryRun: true}); err != nil {
		t.Fatalf("dry-run 失败: %v", err)
	}

	if got := adminNickname(t); got != "改坏了" {
		t.Fatalf("dry-run 不该改动数据库, 昵称变成了 %q", got)
	}
	if exists(filepath.Join("media", "a.png")) {
		t.Fatal("dry-run 不该把被删的文件放回来")
	}
	if after := countFiles(t, "."); after != before {
		t.Fatalf("dry-run 不该增减文件: %d → %d", before, after)
	}
	if !strings.Contains(out.String(), "Dry run") {
		t.Fatalf("应当明确说明这是 dry run: %s", out.String())
	}
}

// TestRestoreReadsTheArchiveFromStdin 覆盖 `restore -`。
//
// 它让 `ssh host 'exe backup -o -' | exe restore -` 成立：归档不必先在目标机器上
// 落一份中间文件——而那正是「磁盘刚坏掉、没有空间放临时归档」时唯一的办法。
func TestRestoreReadsTheArchiveFromStdin(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	archive, err := os.ReadFile("site.tar.gz")
	if err != nil {
		t.Fatalf("读归档失败: %v", err)
	}
	if err := os.Remove(filepath.Join("media", "a.png")); err != nil {
		t.Fatalf("删除媒体失败: %v", err)
	}

	// 二进制内容原样喂进 stdin：strings.NewReader 不做任何转码。
	captureStreams(t, string(archive))
	if err := performRestore(restoreOptions{archive: "-", yes: true}); err != nil {
		t.Fatalf("从 stdin 还原失败: %v", err)
	}
	if !exists(filepath.Join("media", "a.png")) {
		t.Fatal("从 stdin 还原没有把文件放回来")
	}
}

// TestRunRestoreCommandUsageErrors 确认用法错误走退出码 2。
func TestRunRestoreCommandUsageErrors(t *testing.T) {
	inTempWorkdir(t)
	_, errOut := captureStreams(t, "")

	if code := runRestoreCommand(nil); code != exitUsageOrValidation {
		t.Fatalf("缺少归档参数时的退出码 = %d, 期望 %d", code, exitUsageOrValidation)
	}
	if !strings.Contains(errOut.String(), "missing the archive") {
		t.Fatalf("应当在 stderr 上说明缺了什么: %q", errOut.String())
	}
}

// TestRunRestoreCommandFailureReportsToStderr 确认运行期失败走退出码 1，
// 且错误信息里带上具体原因（而不是一句「还原失败」）。
func TestRunRestoreCommandFailureReportsToStderr(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	_, errOut := captureStreams(t, "")

	if code := runRestoreCommand([]string{"nope.tar.gz", "--yes"}); code != exitFailure {
		t.Fatalf("归档不存在时的退出码 = %d, 期望 %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "restore failed") {
		t.Fatalf("应当在 stderr 上说明失败原因: %q", errOut.String())
	}
}

// replacedLeftovers 列出 root 之下所有 .replaced- 残留——「旧数据被挪开」
// 这件事在磁盘上的痕迹。
func replacedLeftovers(t *testing.T, root string) []string {
	t.Helper()

	children, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", root, err)
	}
	var found []string
	for _, child := range children {
		if strings.Contains(child.Name(), ".replaced-") {
			found = append(found, child.Name())
		}
	}
	return found
}
