package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blog-engine/internal/backup"
	"blog-engine/internal/database"
	"blog-engine/internal/pidfile"
	"blog-engine/internal/search"
)

// 本文件收的是备份/还原里那些**失败路径**的用例。
//
// 主干用例（backup_command_test.go / restore_command_test.go）证明「正常时数据
// 真的被搬对了」；这里证明「不正常时它拒绝得干净」。两者缺一不可，而后者更容易
// 被漏——因为失败路径在手工验证时几乎不会被走到。
//
// 这里所有「让某个系统调用失败」的构造都只用**跨平台都成立**的手段：拿一个目录
// 当文件写、拿一个文件当目录用、把一个超长的名字交给 OS。不写平台分支，也不靠
// 权限位（在管理员账号下属主为空的测试跑不出差别）。

// --- 手工归档 ---------------------------------------------------------------

// archiveEntry 是一条要写进手工归档的条目（只支持常规文件）。
type archiveEntry struct {
	name string
	body string
}

// craftArchive 在 path 处造一份**手工搭建**的归档。
//
// 为什么需要它：performBackup 只会产出「本站自己写的那种」归档——部件齐全、
// 路径规整、摘要正确。而还原端真正要防的是**别人给的**归档：清单里声明了本程序
// 不认识的部件、一个部件都没有、或者声明了摘要却对不上。这些形态用真实备份造不
// 出来，只能自己拼一份。
//
// 清单是逐条给进去的（而不是调 backup.Create 生成），因为要测的恰恰是「清单与
// 内容之间的关系」，让被测代码自己写清单就把前提消掉了。
func craftArchive(t *testing.T, path string, parts []backup.Part, entries []archiveEntry) {
	t.Helper()

	manifest, err := json.Marshal(backup.Manifest{
		Format:        backup.FormatName,
		FormatVersion: backup.FormatVersion,
		CreatedAt:     time.Now().UTC(),
		BuiltBy:       backup.BuiltBy{OS: "test", Arch: "test"},
		Parts:         parts,
	})
	if err != nil {
		t.Fatalf("序列化清单失败: %v", err)
	}

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建归档失败: %v", err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)

	// 清单必须是第一个条目，Open 只认这一个位置。
	craftTarFile(t, tw, backup.ManifestName, string(manifest))
	for _, entry := range entries {
		craftTarFile(t, tw, entry.name, entry.body)
	}

	// 两层收尾都要走到，否则解包端读不下去——这本身也是它该有的样子。
	if err := tw.Close(); err != nil {
		t.Fatalf("收尾 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("收尾 gzip 失败: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("关闭归档失败: %v", err)
	}
}

// craftTarFile 往 tar 流里写一个常规文件。
func craftTarFile(t *testing.T, tw *tar.Writer, name, body string) {
	t.Helper()

	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Size:     int64(len(body)),
		Mode:     0o600,
		ModTime:  time.Now(),
	}); err != nil {
		t.Fatalf("写 %s 的头部失败: %v", name, err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("写 %s 的内容失败: %v", name, err)
	}
}

// unusableName 是一个**操作系统的文件系统拒绝处理**的名字。
//
// 用途是构造「Stat 失败但不是因为不存在」这条分支。它必须同时满足两点：
//   - 长到超过任何平台的上限（Windows 的路径规则、Linux/macOS 的 NAME_MAX=255），
//     于是拿到的是「名字非法」而不是「找不到」；
//   - 不含任何路径分隔符，于是它永远只是个名字，不会意外落在别的地方。
//
// 用权限位做不到这件事：测试经常以管理员身份运行，而管理员能穿透 ACL。
func unusableName() string {
	return strings.Repeat("a", 300)
}

// ignoredReader 是一个永远读不出东西、只报错的输入流。
type ignoredReader struct{}

func (ignoredReader) Read([]byte) (int, error) { return 0, errors.New("input is broken") }

// blocker 在当前工作目录里造一个**常规文件**，用来把某个本该是目录的位置占掉。
//
// 「拿文件当目录用」是这里构造故障的通用手法：它不依赖权限，也不依赖平台，
// 而且产生的错误正是生产环境里最常见的那类（路径被别的东西占了）。
func blocker(t *testing.T, path string) string {
	t.Helper()

	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
	if err := os.WriteFile(abs, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("创建 %s 失败: %v", path, err)
	}
	return abs
}

// --- backup 的失败路径 ------------------------------------------------------

// TestBackupFailsWhenTheDatabaseIsNotSQLite 钉住「快照失败就整条命令失败」。
//
// blog.db 被换成非数据库内容（半截写入、磁盘坏块、被别的程序覆盖）时，VACUUM
// INTO 会失败。此时**必须**报错而不是跳过数据库——产出一份不含数据库的归档看起来
// 是成功的，而它要到还原那天才暴露：站点能起来，只是变成空站。
func TestBackupFailsWhenTheDatabaseIsNotSQLite(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)

	// 先把旁文件清掉：一份合法的 -wal 会让 sqlite 先去重放它，那样测的就不是
	// 「库里是垃圾」而是「wal 与库对不上」了。
	_ = removeSQLiteSidecars(databasePath)
	if err := os.WriteFile(databasePath, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatalf("破坏数据库失败: %v", err)
	}

	if _, err := performBackup(backupOptions{output: "x.tar.gz"}); err == nil {
		t.Fatal("数据库不是 SQLite 库时应当报错")
	}
	if exists("x.tar.gz") {
		t.Fatal("失败的备份不该留下归档文件")
	}
}

// TestBackupFailsWhenTheMediaDirCannotBeInspected 钉住那句 `!os.IsNotExist(err)`。
//
// 「媒体目录不存在」与「媒体目录查不了」必须区别对待：前者是全新站点，跳过就是
// 正确行为；后者说明 media_dir 配错了或磁盘有问题，跳过等于**静默产出一份没有
// 媒体的归档**——而媒体是硬删、没有回收站的，磁盘上那份就是唯一副本。
func TestBackupFailsWhenTheMediaDirCannotBeInspected(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)

	// 一个 OS 拒绝处理的目录名：Stat 拿到的是「名字非法」而不是「不存在」。
	body := `{"media_dir":"` + unusableName() + `"}`
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatalf("改写配置失败: %v", err)
	}

	_, err := performBackup(backupOptions{output: "x.tar.gz"})
	if err == nil {
		t.Fatal("媒体目录查不了时应当报错，而不是当成「没有媒体」跳过")
	}
	if !strings.Contains(err.Error(), "media") {
		t.Fatalf("错误信息应当指向媒体目录: %v", err)
	}
}

// TestBackupFailsWhenNoSnapshotDirectoryCanBeCreated 覆盖「连临时目录都建不出来」。
//
// 快照要落在一处中转位置，而系统临时目录可能不可用（被指到一个文件上、被清空、
// 挂载点掉了）。此时必须报错：继续走下去会写出一个没有数据库的归档。
func TestBackupFailsWhenNoSnapshotDirectoryCanBeCreated(t *testing.T) {
	dir := inTempWorkdir(t)
	seedSite(t, nil)

	// 把系统临时目录指到一个常规文件上，MkdirTemp 必定失败。
	occupied := blocker(t, filepath.Join(dir, "no-temp-here"))
	t.Setenv("TMP", occupied)
	t.Setenv("TEMP", occupied)
	t.Setenv("TMPDIR", occupied)

	_, err := performBackup(backupOptions{output: "x.tar.gz"})
	if err == nil {
		t.Fatal("建不出快照目录时应当报错")
	}
	if !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("错误信息应当说明是快照目录的问题: %v", err)
	}
}

// TestBackupSkipsNonRegularLogMatches 钉住 --with-logs 只收常规文件。
//
// 通配符 log.txt* 会顺手匹配上 log.txt.1 这类**目录**（有人把轮转日志挪去别处、
// 或者干脆建了个同名目录）。把它们当文件塞进归档会在写入阶段炸掉，而这里只是
// 备份日志——为它失败不值得。
func TestBackupSkipsNonRegularLogMatches(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	seedTree(t, ".", map[string]string{"log.txt": "now", "log.txt.2": "older"})
	if err := os.MkdirAll(filepath.Join("log.txt.1", "inner"), 0o700); err != nil {
		t.Fatalf("创建同名目录失败: %v", err)
	}

	manifest, err := performBackup(backupOptions{output: "logs.tar.gz", withLogs: true})
	if err != nil {
		t.Fatalf("performBackup() 报错: %v", err)
	}

	var paths []string
	for _, part := range manifest.Parts {
		if part.Name == backup.PartLogs {
			paths = append(paths, part.Path)
		}
	}
	if len(paths) != 2 {
		t.Fatalf("归档里应有 2 个日志文件, 实际 %d: %v", len(paths), paths)
	}
	for _, path := range paths {
		if strings.Contains(path, "log.txt.1") {
			t.Fatalf("目录 log.txt.1 不该被当成日志文件收进来: %v", paths)
		}
	}
}

// TestBackupFailsWhenTheOutputDirectoryIsUnusable 覆盖「建立临时归档失败」。
//
// 文件输出走的是「同目录临时文件 + rename」，所以先要在目标目录里建一个临时文件。
// 目标目录其实是个文件时（`-o notes.txt/backup.tar.gz` 这种笔误），失败必须现在
// 发生——否则用户会在几秒后收到一个「找不到输出文件」的迷惑报错。
func TestBackupFailsWhenTheOutputDirectoryIsUnusable(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	blocker(t, "notes.txt")

	_, err := performBackup(backupOptions{output: filepath.Join("notes.txt", "x.tar.gz")})
	if err == nil {
		t.Fatal("输出目录其实是个文件时应当报错")
	}
}

// TestBackupFailsWhenTheOutputIsAnExistingDirectory 覆盖「临时文件改名不过去」。
//
// rename 覆盖不了目录（Windows 与 POSIX 一致）。--force 也救不了这种情况：
// 它表示「允许覆盖一个文件」，而不是「允许删掉一个目录」。所以这里必须报错并
// **保留**那个目录——一条命令的 --force 不该顺手删掉一个和它同名的目录。
func TestBackupFailsWhenTheOutputIsAnExistingDirectory(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	if err := os.MkdirAll(filepath.Join("out.tar.gz", "inner"), 0o700); err != nil {
		t.Fatalf("创建同名目录失败: %v", err)
	}

	if _, err := performBackup(backupOptions{output: "out.tar.gz", force: true}); err == nil {
		t.Fatal("输出路径是个目录时应当报错")
	}
	if !exists(filepath.Join("out.tar.gz", "inner")) {
		t.Fatal("被拒绝的备份不该动那个同名目录")
	}
}

// TestBackupSurvivesASidecarThatCannotBeWritten 钉住「旁文件失败不算备份失败」。
//
// 侧写的 .sha256 是事后核对的便利品，不是归档的一部分。写不出来（几百 MB 的
// 归档正好把盘写满、或者那个名字被一个目录占了）时只该告警——把一份已经完整落盘
// 的归档判为「备份失败」，会让人以为没有可用的备份而重跑，甚至去删掉它。
func TestBackupSurvivesASidecarThatCannotBeWritten(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	if err := os.MkdirAll("keep.tar.gz.sha256", 0o700); err != nil {
		t.Fatalf("占住旁文件的名字失败: %v", err)
	}

	manifest, err := performBackup(backupOptions{output: "keep.tar.gz"})
	if err != nil {
		t.Fatalf("旁文件写不出来不该让备份失败: %v", err)
	}
	if manifest == nil {
		t.Fatal("应当返回清单")
	}
	if !exists("keep.tar.gz") {
		t.Fatal("归档本体必须已经落盘")
	}
	// 那个目录也不该被动过——它只是占了名字。
	if info, err := os.Stat("keep.tar.gz.sha256"); err != nil || !info.IsDir() {
		t.Fatalf("旁文件位置上原有的目录被改动了: %v", err)
	}
}

// --- restore 的失败路径 -----------------------------------------------------

// TestRunRestoreCommandSucceeds 走完整条命令行路径并断言退出码 0。
//
// 其余用例大多直接调 performRestore（跳过壳），所以「壳本身能不能成功返回」
// 反而没有别的地方证明——而壳里还有日志开关与错误包装两层。
func TestRunRestoreCommandSucceeds(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if err := os.Remove(filepath.Join("media", "a.png")); err != nil {
		t.Fatalf("删除媒体失败: %v", err)
	}

	out, errOut := captureStreams(t, "")
	if code := runRestoreCommand([]string{"site.tar.gz", "--yes"}); code != 0 {
		t.Fatalf("退出码 = %d, stderr=%s", code, errOut.String())
	}
	if !exists(filepath.Join("media", "a.png")) {
		t.Fatal("命令行还原没有把文件放回来")
	}
	if !strings.Contains(out.String(), "Restore complete.") {
		t.Fatalf("摘要里应当报告完成: %s", out.String())
	}
}

// TestRestoreRejectsSomethingThatIsNotAnArchive 覆盖「连 gzip 头都读不出来」。
//
// 最常见的现场是「把 .zip 或 .sql 当成了备份」以及「归档传输途中被截断」。
// 错误信息必须点出「我们期待的是一个 .tar.gz」，否则用户只会看到一句
// 「invalid header」而无从下手。
func TestRestoreRejectsSomethingThatIsNotAnArchive(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	if err := os.WriteFile("junk.tar.gz", []byte("PK\x03\x04 definitely not a tarball"), 0o600); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	_, errOut := captureStreams(t, "")
	if code := runRestoreCommand([]string{"junk.tar.gz", "--yes"}); code != exitFailure {
		t.Fatalf("退出码 = %d, 期望 %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "tar.gz") {
		t.Fatalf("错误信息应当说明期待的是什么格式: %q", errOut.String())
	}
}

// TestRestoreRejectsAnArchiveWhoseChecksumDoesNotMatch 端到端钉住摘要对账。
//
// 这是整个还原链路上唯一「内容坏掉也能被发现」的地方：tar 与 gzip 自带的和校验
// 只覆盖压缩流本身，改掉解压后的字节它们照样通过。清单里记的 sha256 才是针对
// 数据库与配置文件内容的判据——少了它，一次字节级的损坏会被原样还原进去。
//
// 反证方式：把 Extract 里对 expected 的比较删掉 → 本条失败。
func TestRestoreRejectsAnArchiveWhoseChecksumDoesNotMatch(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)

	craftArchive(t, "forged.tar.gz",
		[]backup.Part{{
			Name:   backup.PartSettings,
			Path:   backup.DataPrefix + settingsPath,
			SHA256: strings.Repeat("0", 64), // 与下面的内容必然不符
		}},
		[]archiveEntry{{name: backup.DataPrefix + settingsPath, body: `{"media_dir":"media"}`}},
	)

	captureStreams(t, "")
	err := performRestore(restoreOptions{archive: "forged.tar.gz", yes: true})
	if err == nil {
		t.Fatal("摘要对不上时应当拒绝还原")
	}
	if !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("错误信息应当说明是内容损坏: %v", err)
	}
}

// TestRestoreRejectsAnUnknownPart 钉住「清单里有本程序不认识的部件」。
//
// 一份来自更新版本的归档可能多带一个部件。忽略它会让「还原完成」这句话变得
// 不准确——用户以为整站都回来了，而那个部件的数据一个字节都没落地。
func TestRestoreRejectsAnUnknownPart(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)

	const unknown = "future-feature"
	craftArchive(t, "newer.tar.gz",
		[]backup.Part{
			{Name: backup.PartSettings, Path: backup.DataPrefix + settingsPath},
			{Name: unknown, Path: backup.DataPrefix + "whatever.bin"},
		},
		[]archiveEntry{
			{name: backup.DataPrefix + settingsPath, body: `{"media_dir":"media"}`},
			{name: backup.DataPrefix + "whatever.bin", body: "opaque"},
		},
	)

	_, errOut := captureStreams(t, "")
	if code := runRestoreCommand([]string{"newer.tar.gz", "--yes"}); code != exitFailure {
		t.Fatalf("退出码 = %d, 期望 %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), unknown) {
		t.Fatalf("错误信息应当指出是哪个部件不认识: %q", errOut.String())
	}
}

// TestRestoreRejectsAnArchiveWithNothingToRestore 钉住「一个可还原的部件都没有」。
//
// 只装了日志的归档是合法归档（--with-logs 单独用），但日志是**不还原**的——
// 盖回去会冲掉当前的审计记录，包括这次还原刚写下的那些行。于是这份归档什么都
// 没换。此时必须报错：返回 0 会让人以为数据已经换过了。
func TestRestoreRejectsAnArchiveWithNothingToRestore(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)

	craftArchive(t, "logs-only.tar.gz",
		[]backup.Part{{Name: backup.PartLogs, Path: backup.DataPrefix + "log.txt"}},
		[]archiveEntry{{name: backup.DataPrefix + "log.txt", body: "old log"}},
	)

	captureStreams(t, "")
	err := performRestore(restoreOptions{archive: "logs-only.tar.gz", yes: true})
	if err == nil {
		t.Fatal("没有任何可还原部件的归档应当被拒绝")
	}
	if !strings.Contains(err.Error(), "nothing this build knows how to restore") {
		t.Fatalf("错误信息应当说明没有任何东西可还原: %v", err)
	}
}

// TestRestoreFailsWhenTheRestoredDatabaseIsUnusable 覆盖还原后的库打不开。
//
// 走到这一步说明归档的路径、类型、体积、摘要全部通过了，但里面的数据库是坏的
// （比如归档来自一次断电时的备份，而它当时就没写完整）。此时站点数据已经被换掉，
// 所以必须报错而不是当作成功——用户需要立刻知道「现在这个站点是坏的，请用安全
// 备份回滚」，而不是下次启动时才发现。
func TestRestoreFailsWhenTheRestoredDatabaseIsUnusable(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)

	craftArchive(t, "broken-db.tar.gz",
		[]backup.Part{{Name: backup.PartDatabase, Path: backup.DataPrefix + databasePath}},
		[]archiveEntry{{name: backup.DataPrefix + databasePath, body: "not a sqlite database"}},
	)

	captureStreams(t, "")
	err := performRestore(restoreOptions{archive: "broken-db.tar.gz", yes: true, noSafetyBackup: true})
	if err == nil {
		t.Fatal("还原进来的库打不开时应当报错")
	}
	if !strings.Contains(err.Error(), "database") {
		t.Fatalf("错误信息应当指向还原进来的数据库: %v", err)
	}
}

// TestRestoreFailsWhenTheSafetyBackupCannotBeTaken 钉住「安全备份失败就不动数据」。
//
// 安全备份是还原唯一的回滚点。它建不出来（本机数据库已经坏了、磁盘满了）时继续
// 走下去，等于把「这次操作可能是错的」这条退路一起替换掉了——而那正是这条命令
// 最需要保险的时刻。
//
// 反证方式：把 performRestore 里 createSafetyBackup 的错误分支改成只告警 →
// 本条失败。
func TestRestoreFailsWhenTheSafetyBackupCannotBeTaken(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png-one"})
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 本机的库坏了：还原自己的数据没问题，但「把当前状态打一份」做不到。
	_ = removeSQLiteSidecars(databasePath)
	if err := os.WriteFile(databasePath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatalf("破坏本机数据库失败: %v", err)
	}

	captureStreams(t, "")
	err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true})
	if err == nil {
		t.Fatal("建不出安全备份时应当拒绝还原")
	}
	if !strings.Contains(err.Error(), "safety backup") {
		t.Fatalf("错误信息应当说明是安全备份的问题: %v", err)
	}
	// 媒体没有被换掉：还原根本没走到那一步。
	if body, err := os.ReadFile(filepath.Join("media", "a.png")); err != nil || string(body) != "png-one" {
		t.Fatalf("被拒绝的还原不该改动媒体: body=%q err=%v", body, err)
	}
}

// TestRestoreFailsWhenAStaleWalCannotBeRemoved 钉住「清不掉旁文件就别换库」。
//
// 陈旧的 -wal 属于还原**之前**那一份库。它删不掉时（被别的进程占着、或者这个
// 名字被一个非空目录占了）必须停下来：继续把新的 blog.db 放下去，最好的结果是
// 那份 WAL 被判定无效后丢弃，最坏的结果是旧帧被回放进新库。
//
// 这里刻意带 --no-safety-backup：占住 -wal 的名字之后，连「把当前状态打一份」
// 都做不到（sqlite 打不开这个库），于是安全备份会先一步失败，测不到要测的那条
// 分支。而实际现场里，还原一份**带索引的**归档时同样会走到 applyRestore。
func TestRestoreFailsWhenAStaleWalCannotBeRemoved(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 一个非空目录顶着 -wal 的名字：os.Remove 删不掉它。
	blocked := databasePath + "-wal"
	_ = os.Remove(blocked)
	if err := os.MkdirAll(filepath.Join(blocked, "inner"), 0o700); err != nil {
		t.Fatalf("占住 -wal 的名字失败: %v", err)
	}

	captureStreams(t, "")
	err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true, noSafetyBackup: true})
	if err == nil {
		t.Fatal("清不掉陈旧的 -wal 时应当拒绝还原")
	}
	if !strings.Contains(err.Error(), "-wal") {
		t.Fatalf("错误信息应当指出是哪个旁文件: %v", err)
	}
}

// TestRestoreBringsBackTheSearchIndex 覆盖索引与主密钥的还原。
//
// 两者必须**成对**处理：主密钥被记进 Meilisearch 自己的鉴权库，换一把钥匙的旧库
// 对不上。所以这条用例断言的是「两个文件都按各自的位置落好了」，而不只是
// 「search-index 这个部件被处理过」。
//
// 顺带钉住那句「索引会在下次启动时重新同步」——索引跟着数据库走，还原回来的库
// 与旧索引不是一回事，说清楚才免得有人以为索引也精准还原了。
func TestRestoreBringsBackTheSearchIndex(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	seedTree(t, search.DefaultDataDir, map[string]string{"data.mdb": "index-bytes"})
	keyPath := search.MasterKeyPath(search.DefaultDataDir)
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef\n"), 0o600); err != nil {
		t.Fatalf("写主密钥失败: %v", err)
	}

	if _, err := performBackup(backupOptions{output: "site.tar.gz", withIndex: true}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if err := os.RemoveAll(search.DefaultDataDir); err != nil {
		t.Fatalf("删掉索引失败: %v", err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("删掉主密钥失败: %v", err)
	}

	out, _ := captureStreams(t, "")
	if err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true}); err != nil {
		t.Fatalf("还原失败: %v", err)
	}

	if body, err := os.ReadFile(filepath.Join(search.DefaultDataDir, "data.mdb")); err != nil || string(body) != "index-bytes" {
		t.Fatalf("索引没有被还原: body=%q err=%v", body, err)
	}
	if body, err := os.ReadFile(keyPath); err != nil || !strings.HasPrefix(string(body), "0123456789") {
		t.Fatalf("主密钥没有被还原: body=%q err=%v", body, err)
	}
	if !strings.Contains(out.String(), "re-synced") {
		t.Fatalf("应当说明索引会在下次启动时重新同步: %s", out.String())
	}
}

// TestRestoreLeavesLogsAlone 钉住「日志不还原」。
//
// 日志是诊断物，不属于运行数据。把归档里的日志盖回去，只会冲掉当前的审计记录
// ——包括这次还原自己刚写下的那些行，而那恰恰是事后最需要看的东西。
func TestRestoreLeavesLogsAlone(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	seedTree(t, ".", map[string]string{"log.txt": "written before the backup"})
	if _, err := performBackup(backupOptions{output: "site.tar.gz", withLogs: true}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 备份之后又写了一些日志：它必须原样留着。
	const current = "written after the backup"
	if err := os.WriteFile("log.txt", []byte(current), 0o600); err != nil {
		t.Fatalf("改写日志失败: %v", err)
	}

	out, _ := captureStreams(t, "")
	if err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true}); err != nil {
		t.Fatalf("还原失败: %v", err)
	}
	if body, err := os.ReadFile("log.txt"); err != nil || string(body) != current {
		t.Fatalf("当前的日志被盖掉了: body=%q err=%v", body, err)
	}
	if !strings.Contains(out.String(), "log files are not restored") {
		t.Fatalf("应当说明日志被跳过了: %s", out.String())
	}
}

// TestDryRunNotesARunningInstance 覆盖 dry-run 对「服务在跑」的提示。
//
// 真正的还原会在这条情况下拒止，而 dry-run 不该拒止——它的用途正是在动手之前
// 先看一眼。但提示必须给出：否则用户会以为「dry-run 通过了，那就可以直接跑」，
// 然后在真正的还原那里撞上一堵墙。
func TestDryRunNotesARunningInstance(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	handle, err := pidfile.Acquire(pidfile.FileName)
	if err != nil {
		t.Fatalf("写锁文件失败: %v", err)
	}
	defer handle.Release()

	out, _ := captureStreams(t, "")
	if err := performRestore(restoreOptions{archive: "site.tar.gz", dryRun: true}); err != nil {
		t.Fatalf("dry-run 不该因为服务在跑而失败: %v", err)
	}
	if !strings.Contains(out.String(), "is running right now") {
		t.Fatalf("应当提示服务正在运行: %s", out.String())
	}
}

// TestRestoreCancelledAtThePrompt 钉住「输入不是 yes 就取消」。
//
// 提示要的是一句明确的 yes，而 "y"、"ok"、空行都不算。这里连**已经解包并校验
// 完毕**的归档也不放行：解包成功只说明这份归档是好的，与「现在就该换掉整站」
// 是两个决定。
//
// 反证方式：把 confirmRestore 里 `!= "yes"` 改成 `== ""` → 本条失败。
func TestRestoreCancelledAtThePrompt(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png-before"})
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join("media", "a.png"), []byte("png-after"), 0o600); err != nil {
		t.Fatalf("改写媒体失败: %v", err)
	}

	restoreIsTerminal(t, true)
	captureStreams(t, "y\n")
	err := performRestore(restoreOptions{archive: "site.tar.gz"})
	if err == nil {
		t.Fatal("输入 y（而不是 yes）时应当取消")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("错误信息应当说明这是取消: %v", err)
	}
	if body, _ := os.ReadFile(filepath.Join("media", "a.png")); string(body) != "png-after" {
		t.Fatalf("取消之后媒体不该被改动: %q", body)
	}
}

// TestRestorePromptCannotBeRead 覆盖「确认读不出来（而不是读到别的）」。
//
// 输入流报错与输入流给出空行是两件事：前者说明终端或管道出了问题，把它一律
// 当成「用户没说 yes」会掩盖真正的故障；这里要求把 IO 错误原样报出来。
func TestRestorePromptCannotBeRead(t *testing.T) {
	restoreIsTerminal(t, true)

	var out bytes.Buffer
	err := confirmRestore(&out, ignoredReader{})
	if err == nil {
		t.Fatal("读确认输入失败时应当报错")
	}
	if !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("错误信息应当指向这次读取: %v", err)
	}
}

// TestIsTerminal 钉住真实的终端判据。
//
// 它是那条安全规则（「没人能回答不等于同意」）唯一的执行点，所以在替换它之前，
// 先用真实实现把两个方向都验一遍：不是文件、以及文件已关闭 → 一律 false；
// 真正的字符设备（os.DevNull 在两种平台上都是）→ true。
func TestIsTerminal(t *testing.T) {
	if isTerminal(strings.NewReader("yes")) {
		t.Fatal("非 *os.File 的输入不该被当成终端")
	}

	closed, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("打开 %s 失败: %v", os.DevNull, err)
	}
	closed.Close()
	if isTerminal(closed) {
		t.Fatal("Stat 失败的句柄不该被当成终端")
	}

	device, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("打开 %s 失败: %v", os.DevNull, err)
	}
	defer device.Close()
	if !isTerminal(device) {
		t.Fatalf("%s 是字符设备，应当被判定为终端", os.DevNull)
	}
}

// TestRestoreFailsWhenSessionsCannotBeRevoked 钉住「吊销不成功就不算还原成功」。
//
// 默认吊销全部会话是一条**安全承诺**：还原会把 sessions 表回滚到备份那一刻，
// 于是备份之后被踢下线的设备重新拿到有效凭证。删除失败却返回 0，用户看到的是
// 「还原完成」，而那条承诺没有兑现——没有任何提示，也没有痕迹。
//
// 构造：把一份**合法但被冻结**的库放进归档（sessions 上挂一个 RAISE(ABORT) 的
// 删除触发器）。它证明这条失败路径真的会走到，而不是理论上的可能性。
func TestRestoreFailsWhenSessionsCannotBeRevoked(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)

	decoy := filepath.Join(t.TempDir(), "decoy.db")
	db, err := database.Open(decoy)
	if err != nil {
		t.Fatalf("构造冻结的库失败: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER freeze_sessions BEFORE DELETE ON sessions
		BEGIN SELECT RAISE(ABORT, 'sessions are frozen'); END;`); err != nil {
		db.Close()
		t.Fatalf("创建触发器失败: %v", err)
	}
	// 触发器是**按行**触发的：表里没有行时 DELETE 什么都不匹配，也就不会报错。
	// 所以必须先放一行进去，这条用例测的才是「删不掉」而不是「没得删」。
	if _, err := db.Exec(
		`INSERT INTO sessions (token_hash, role, user_id, csrf_hash, expires_at, created_at)
		 VALUES ('frozen-token', 'admin', NULL, 'csrf', ?, ?)`,
		time.Now().Add(time.Hour).Unix(), time.Now().Unix(),
	); err != nil {
		db.Close()
		t.Fatalf("写入会话失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭冻结的库失败: %v", err)
	}
	body, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatalf("读取冻结的库失败: %v", err)
	}

	craftArchive(t, "frozen.tar.gz",
		[]backup.Part{{Name: backup.PartDatabase, Path: backup.DataPrefix + databasePath}},
		[]archiveEntry{{name: backup.DataPrefix + databasePath, body: string(body)}},
	)

	captureStreams(t, "")
	err = performRestore(restoreOptions{archive: "frozen.tar.gz", yes: true, noSafetyBackup: true})
	if err == nil {
		t.Fatal("会话删不掉时不该报告还原成功")
	}
	if !strings.Contains(err.Error(), "revoke") {
		t.Fatalf("错误信息应当说明是吊销会话失败: %v", err)
	}
}

// TestRestoreFailsWhenTheMediaCannotBePlaced 覆盖媒体换位失败。
//
// 场景是真实存在的：--keep-settings 表示「保留本机的布局」，于是媒体要落到本机
// settings.json 说的那个位置。而那个位置可能根本用不了（media_dir 指在一个文件
// 底下、或者那块盘已经掉了）。此时必须报错——静默跳过会让站点看起来还原成功，
// 实际是「文章都在、图全挂了」，而那种状态几乎无从排查。
//
// 反证方式：把 applyRestore 里 PartMedia 那条 swap 的错误吞掉 → 本条失败。
func TestRestoreFailsWhenTheMediaCannotBePlaced(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 本机改用一个落在常规文件底下的媒体目录，同时要求保留本机配置。
	blocker(t, "occupied")
	if err := os.WriteFile(settingsPath,
		[]byte(`{"media_dir":"occupied/media"}`), 0o600); err != nil {
		t.Fatalf("改写配置失败: %v", err)
	}

	captureStreams(t, "")
	err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true, keepSettings: true, noSafetyBackup: true})
	if err == nil {
		t.Fatal("媒体放不下去时应当报错")
	}
	if !strings.Contains(err.Error(), "media") {
		t.Fatalf("错误信息应当指向媒体目录: %v", err)
	}
}

// TestRestoreRefusesToSwapAnIndexThatIsInUse 钉住「旧的挪不开就别换」。
//
// 「把旧数据挪到一边」是换位的第一步，而它在 Windows 上会因为目录里有别的进程
// 握着的句柄而失败——这正是真实场景：上一次退出的 meilisearch 还没完全释放，
// 或者有人用 --force 起了第二份实例。此时必须拒绝还原：硬换过去会让那个进程继续
// 往已经被替换掉的 inode 里写，数据看起来正常地在丢。
//
// 反证方式：把 swapIn 里 `os.Rename(dst, parked)` 的错误分支改成忽略 → 本条失败。
func TestRestoreRefusesToSwapAnIndexThatIsInUse(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	seedTree(t, search.DefaultDataDir, map[string]string{"data.mdb": "index-bytes"})
	if _, err := performBackup(backupOptions{output: "site.tar.gz", withIndex: true}); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	held, err := os.Open(filepath.Join(search.DefaultDataDir, "data.mdb"))
	if err != nil {
		t.Fatalf("打开索引文件失败: %v", err)
	}
	defer held.Close()

	captureStreams(t, "")
	err = performRestore(restoreOptions{archive: "site.tar.gz", yes: true, noSafetyBackup: true})
	if err == nil {
		t.Fatal("索引目录被占着时应当拒绝还原")
	}
	if !strings.Contains(err.Error(), search.DefaultDataDir) {
		t.Fatalf("错误信息应当指出是哪个目录: %v", err)
	}
}

// --- 文件原语的失败路径 -----------------------------------------------------// TestPlaceFallsBackToCopying 覆盖「rename 不成时退化成拷贝」。
//
// rename 只在同一卷内成功，而媒体目录完全可能挂在另一块盘上。跨卷时它失败，
// 于是 place 退化成「拷贝 + 删源」。这里用一个**已经存在的非空目录**当目标来
// 触发同一分支：rename 覆盖不了目录，两种情形走的是同一段代码。
//
// 断言必须落在内容上，而不是「没报错」：退化路径若只拷了一半就返回成功，表现是
// 还原后少几张图，而命令自己的输出一切正常。
func TestPlaceFallsBackToCopying(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	seedTree(t, src, map[string]string{"a.txt": "A", "sub/b.txt": "B"})

	dst := filepath.Join(dir, "dst")
	seedTree(t, dst, map[string]string{"already-here.txt": "keep"})

	if err := place(src, dst); err != nil {
		t.Fatalf("place() 报错: %v", err)
	}

	for name, want := range map[string]string{"a.txt": "A", "sub/b.txt": "B", "already-here.txt": "keep"} {
		body, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		if string(body) != want {
			t.Fatalf("%s = %q, 期望 %q", name, body, want)
		}
	}
	// 拷贝成功之后源必须消失：留着它等于在暂存目录里留一份重复的完整数据。
	if exists(src) {
		t.Fatal("退化路径成功之后应当删掉源")
	}
}

// TestCopyTreeRejectsWhatItCannotRead 覆盖 copyTree 入口处的失败。
//
// 两个方向：源不存在、源是个常规文件（此时应当直接走 copyFile，而不是试图把它
// 当目录遍历）。后者是真实会遇到的——被还原的「目录」在归档里可能就是一个文件。
func TestCopyTreeRejectsWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()

	if err := copyTree(filepath.Join(dir, "missing"), filepath.Join(dir, "out")); err == nil {
		t.Fatal("源不存在时应当报错")
	}

	src := filepath.Join(dir, "single.txt")
	if err := os.WriteFile(src, []byte("only one"), 0o600); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	dst := filepath.Join(dir, "copied.txt")
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("单文件应当被直接拷过去: %v", err)
	}
	if body, err := os.ReadFile(dst); err != nil || string(body) != "only one" {
		t.Fatalf("拷贝结果 = %q %v", body, err)
	}
}

// TestCopyTreeFailsWhenTheDestinationIsUnusable 覆盖建立目标目录失败。
//
// 目标是「常规文件底下的路径」时，MkdirAll 必定失败。这条分支必须把错误报出来：
// 静默继续会让遍历一个都没拷成，而调用方以为拷贝完成了。
func TestCopyTreeFailsWhenTheDestinationIsUnusable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	seedTree(t, src, map[string]string{"a.txt": "A"})

	occupied := filepath.Join(dir, "occupied.txt")
	if err := os.WriteFile(occupied, []byte("x"), 0o600); err != nil {
		t.Fatalf("准备占位文件失败: %v", err)
	}

	if err := copyTree(src, filepath.Join(occupied, "dst")); err == nil {
		t.Fatal("目标建不出来时应当报错")
	}
}

// TestCopyTreeReportsNestedFailures 覆盖「子树里某个文件拷不过去」。
//
// 构造：目标里已经有一个**同名的目录**，而源里同位置是一个文件。copyFile 会
// 在打开目标时失败，错误必须一路冒到顶层——中间任何一层吞掉它，结果都是
// 「还原成功」但少了一个文件，而媒体是硬删的，那份内容只在归档里。
func TestCopyTreeReportsNestedFailures(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	seedTree(t, src, map[string]string{"sub/a.txt": "A"})

	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(filepath.Join(dst, "sub", "a.txt"), 0o700); err != nil {
		t.Fatalf("占住目标文件名失败: %v", err)
	}

	if err := copyTree(src, dst); err == nil {
		t.Fatal("子文件拷不过去时应当报错，而不是静默跳过")
	}
}

// TestCopyFileReportsIOFailures 覆盖 copyFile 的两个失败入口。
//
//   - 源打不开（读之前就失败）；
//   - 目的建不出来（目标是目录）；
//   - 拷贝中途失败（源是个目录：能打开，但读不出字节）——这一条最容易被漏掉，
//     它证明 copyFile 不会把「一个字节都没读出来」当成拷贝成功。
func TestCopyFileReportsIOFailures(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("准备源失败: %v", err)
	}

	if err := copyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "out"), 0o600); err == nil {
		t.Fatal("源打不开时应当报错")
	}

	target := filepath.Join(dir, "target.txt")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("占住目标名字失败: %v", err)
	}
	if err := copyFile(src, target, 0o600); err == nil {
		t.Fatal("目标是目录时应当报错")
	}

	if err := copyFile(target, filepath.Join(dir, "from-dir"), 0o600); err == nil {
		t.Fatal("源是目录（读不出字节）时应当报错")
	}
}

// TestRemoveSQLiteSidecarsReportsBlockedRemoval 钉住「删不掉就报错」。
//
// 正常情况下这三个文件只在一段短暂的窗口里存在，删不掉几乎总是因为有个东西
// 顶着它们的名字（非空目录）或者被别的进程占着。此时返回 nil 会让调用方以为
// 环境已经干净，接着把新的 blog.db 放到那份陈旧 -wal 旁边。
func TestRemoveSQLiteSidecarsReportsBlockedRemoval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blog.db")

	blocked := path + "-wal"
	if err := os.MkdirAll(filepath.Join(blocked, "inner"), 0o700); err != nil {
		t.Fatalf("占住 -wal 名字失败: %v", err)
	}

	err := removeSQLiteSidecars(path)
	if err == nil {
		t.Fatal("旁文件删不掉时应当报错")
	}
	if !strings.Contains(err.Error(), "-wal") {
		t.Fatalf("错误信息应当指出是哪个旁文件: %v", err)
	}
	if !exists(blocked) {
		t.Fatal("报错之后不该已经动过它")
	}
}

// TestRunBackupAndRestoreConsumeTheirOwnInput 是一组最小的自洽检查：
// `backup -o -` 落到 stdout 的字节必须能原样喂给 `restore -`。
//
// 这一条单独立出来的理由是它跨过了两个进程边界：归档不落盘、只经内存与管道。
// 别的用例都是「文件 → 文件」，而 ssh 直传那条用法（磁盘上放不下中间归档时的
// 唯一办法）走的正是这里。
func TestRunBackupAndRestoreConsumeTheirOwnInput(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})

	streamed, _ := captureStreams(t, "")
	if code := runBackupCommand([]string{"-o", "-"}); code != 0 {
		t.Fatalf("导出到 stdout 失败, 退出码 %d", code)
	}
	archive := streamed.Bytes()
	if len(archive) == 0 {
		t.Fatal("stdout 上没有内容")
	}
	if _, err := backup.Open(bytes.NewReader(archive)); err != nil {
		t.Fatalf("stdout 上的内容不是一份可读归档: %v", err)
	}

	if err := os.Remove(filepath.Join("media", "a.png")); err != nil {
		t.Fatalf("删除媒体失败: %v", err)
	}

	captureStreams(t, string(archive))
	if err := performRestore(restoreOptions{archive: "-", yes: true}); err != nil {
		t.Fatalf("从标准输入还原失败: %v", err)
	}
	if !exists(filepath.Join("media", "a.png")) {
		t.Fatal("管道还原没有把文件放回来")
	}
}
