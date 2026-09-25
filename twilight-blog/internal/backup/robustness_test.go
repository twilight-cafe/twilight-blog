package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本文件收的是归档读写里那些**失败路径**的用例。
//
// 它们和 backup_test.go / extract_test.go 的分工是：那两份证明「正常输入得到预期
// 结果」，这份证明「不正常的输入与环境都被拒绝，而且拒绝得干净」。备份/还原是唯一
// 会不可逆地搬运整站数据的代码，失败时**留下一个半成品**比直接失败糟得多——因为
// 半成品看起来和成功没有区别。

// --- 故障注入小工具 ---------------------------------------------------------

// failingWriter 在写出的字节超过 limit 之后开始报错。
//
// 它模拟的是「备份写到一半磁盘满了」——真实世界里最常见的备份失败原因，也是最
// 需要被处理成「明确失败」而不是「产出一份半截归档」的一种。
type failingWriter struct {
	limit int
	wrote int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.wrote+len(p) > w.limit {
		return 0, errors.New("no space left on device")
	}
	w.wrote += len(p)
	return len(p), nil
}

// brokenReader 永远读不出东西，只报错。
type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("input went away") }

// incompressible 造一段压不掉的字节。
//
// 用它而不是重复同一个字符：归档里全是重复字符时 gzip 能把它压到几十字节，
// 于是「写到第 N 个字节时失败」这类用例根本触发不到。
func incompressible(size int) string {
	body := make([]byte, size)
	// 线性同余，够随机也够确定——用例失败时能一模一样地复现。
	state := uint32(0x2545F491)
	for i := range body {
		state = state*1664525 + 1013904223
		body[i] = byte(state >> 16)
	}
	return string(body)
}

// --- archive.go -------------------------------------------------------------

// TestCreateReportsAWriterThatFails 钉住「写不出去就必须报错」。
//
// 备份的输出端可能是一个正在被写满的磁盘、一个断掉的管道、一个满了的远端。
// 每一种都必须让 Backup 返回错误：一份内容缺了一截的 .tar.gz 与一份完整的归档
// 在文件名、大小、退出码上都没有区别，它要到**还原那天**才暴露。
//
// 参数化的那几个 limit 覆盖失败发生的不同位置（gzip 头、清单、正文、收尾），
// 它们对应的分支不同，但要求是同一条：全部返回错误。
func TestCreateReportsAWriterThatFails(t *testing.T) {
	src := t.TempDir()
	files := map[string]string{
		"big.bin":   incompressible(200 << 10),
		"small.txt": "small",
	}
	seedTree(t, src, files)
	seedTree(t, filepath.Join(src, "nested"), map[string]string{"deep.txt": "deep"})

	entries := []Entry{
		{Name: PartDatabase, Source: filepath.Join(src, "big.bin"), Target: DataPrefix + "big.bin", Hash: true},
		{Name: PartSettings, Source: filepath.Join(src, "small.txt"), Target: DataPrefix + "small.txt", Hash: true},
		{Name: PartMedia, Source: filepath.Join(src, "nested"), Target: DataPrefix + "nested"},
	}

	// 这几个 limit 覆盖失败发生的不同位置：gzip 头、tar 头、正文、以及**收尾**
	// （tar 的块尾与 gzip 的 CRC 都只在 Close 时才写出去，而漏掉它们的归档看起来
	// 完全正常）。它们对应的分支不同，但要求是同一条：全部返回错误。
	for _, limit := range []int{0, 1, 8, 16, 24, 32, 48, 64, 128, 256, 1024, 16 << 10, 64 << 10} {
		writer := &failingWriter{limit: limit}
		if _, err := Create(writer, entries, BuiltBy{}, fixedTime()); err == nil {
			t.Fatalf("写到第 %d 个字节时失败，Create 却报告成功", limit)
		}
	}

	// 对照：limit 足够大时同一批输入必须成功，否则上面那些断言只是「它总是失败」。
	if _, err := Create(&failingWriter{limit: 4 << 20}, entries, BuiltBy{}, fixedTime()); err != nil {
		t.Fatalf("容量充足时 Create 不该失败: %v", err)
	}
}

// TestCreateRejectsAnEmptyEntryList 钉住「什么都不备份」被拒绝。
//
// 它会产出一个人人都以为「有备份」的归档，而里面只有一个清单。
func TestCreateRejectsAnEmptyEntryList(t *testing.T) {
	if _, err := Create(&bytes.Buffer{}, nil, BuiltBy{}, fixedTime()); err == nil {
		t.Fatal("没有任何数据源时应当报错")
	}
}

// TestScanEntryRejectsANonRegularSource 钉住「只备份常规文件」。
//
// 字符设备、命名管道、套接字都能被 Stat 成功返回，但把它们的「内容」写进归档
// 要么立刻失败、要么读出一堆毫无意义的东西（DevNull 读出来是空的）。备份必须
// 在这一步就停下，而不是等到写入阶段。
func TestScanEntryRejectsANonRegularSource(t *testing.T) {
	_, _, err := scanEntry(Entry{
		Name:   PartDatabase,
		Source: os.DevNull,
		Target: DataPrefix + "devnull",
	})
	if err == nil {
		t.Fatalf("%s 不是常规文件，应当被拒绝", os.DevNull)
	}
	if !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("错误信息应当说明原因: %v", err)
	}
}

// TestHashFileReportsFailures 覆盖 HashFile 的两条失败路径与成功路径。
//
// 它是导出函数（还原命令用它核对刚落盘的归档），所以那两条失败路径必须能被外部
// 观察到：读不到文件时返回错误而不是一个空摘要——一个空摘要会被下游当成
// 「摘要不匹配」还是「没算」完全取决于调用方怎么猜。
func TestHashFileReportsFailures(t *testing.T) {
	dir := t.TempDir()

	if _, err := HashFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("文件不存在时应当报错")
	}
	// 目录能被打开，但读不出字节。
	if _, err := HashFile(dir); err == nil {
		t.Fatal("目标是目录时应当报错")
	}

	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	sum, err := HashFile(path)
	if err != nil {
		t.Fatalf("HashFile() 报错: %v", err)
	}
	if sum != sha256Hex("payload") {
		t.Fatalf("HashFile() = %s, 期望 %s", sum, sha256Hex("payload"))
	}
}

// TestWriteFileEntryReportsFailures 覆盖写单个文件的三个失败入口。
//
//   - tar 的头部写不出去（输出端已经坏了）；
//   - 源文件打不开（扫描之后被删掉、权限变了）；
//   - 正文拷到一半失败（源其实读不出字节，或者输出端坏了）。
//
// 第三条最要紧：它意味着 tar 流里已经写下了「这个文件有 N 字节」的头部，而正文
// 只有一半。此时必须让整次备份失败——tar 自己的长度校验会因此报错，而那份归档
// 从此再也读不出来（这正是我们想要的：失败要显式）。
func TestWriteFileEntryReportsFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat 失败: %v", err)
	}

	// 头部写不出去。
	if err := writeFileEntry(tar.NewWriter(&failingWriter{limit: 0}), "data/x", path, info); err == nil {
		t.Fatal("输出端已经坏了时应当报错")
	}
	// 源打不开。
	if err := writeFileEntry(tar.NewWriter(io.Discard), "data/x", filepath.Join(dir, "gone"), info); err == nil {
		t.Fatal("源文件打不开时应当报错")
	}
	// 源读不出字节：目录能被打开，但 io.Copy 立刻失败。
	if err := writeFileEntry(tar.NewWriter(io.Discard), "data/x", dir, info); err == nil {
		t.Fatal("源读不出字节时应当报错")
	}
}

// TestWriteTreeReportsFailures 覆盖遍历目录时的失败。
//
// 目录在扫描之后消失（有人一边备份一边清理，或者挂载点掉了）必须报错：跳过它
// 会让归档里**少一整个目录**，而清单里那个部件的体积还是扫描时的数字。
func TestWriteTreeReportsFailures(t *testing.T) {
	dir := t.TempDir()

	if err := writeTree(tar.NewWriter(io.Discard), DataPrefix+"missing", filepath.Join(dir, "gone")); err == nil {
		t.Fatal("目录不存在时应当报错")
	}
}

// TestTreeTotalsReportsFailures 覆盖目录统计的失败。
//
// 它与 writeTree 是同一棵树的两次遍历，但两者报错的时机不同：统计发生在**写出
// 任何字节之前**，所以这里的失败能让备份在不留下任何痕迹的情况下中止。
func TestTreeTotalsReportsFailures(t *testing.T) {
	dir := t.TempDir()

	if _, _, err := treeTotals(filepath.Join(dir, "gone")); err == nil {
		t.Fatal("目录不存在时应当报错")
	}

	seedTree(t, filepath.Join(dir, "media"), map[string]string{"a.txt": "A", "sub/b.txt": "B"})
	files, size, err := treeTotals(filepath.Join(dir, "media"))
	if err != nil {
		t.Fatalf("treeTotals() 报错: %v", err)
	}
	if files != 2 || size != 2 {
		t.Fatalf("treeTotals() = (%d, %d), 期望 (2, 2)", files, size)
	}
}

// TestWriteManifestEntryReportsAWriteFailure 覆盖「清单正文写不出去」。
//
// 它需要一份**足够大**的清单才能触发：小清单被 gzip 压在内存里，直到 Close 才
// 落到输出端，于是错误会从收尾那里冒出来，看起来像是「收尾失败」。而这里的失败
// 发生在写入过程中——它必须同样被报出来，因为一个只写了半截的清单会让解包端
// 读到一份被截断的 JSON。
func TestWriteManifestEntryReportsAWriteFailure(t *testing.T) {
	parts := make([]Part, 4000)
	for i := range parts {
		parts[i] = Part{Name: PartDatabase, Path: DataPrefix + "part" + strconv.Itoa(i), Bytes: int64(i)}
	}
	manifest := &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		CreatedAt:     fixedTime(),
		Parts:         parts,
	}

	for _, limit := range []int{64, 256, 1024} {
		tw := tar.NewWriter(&failingWriter{limit: limit})
		if err := writeManifestEntry(tw, manifest); err == nil {
			t.Fatalf("写到第 %d 个字节时失败，写清单却报告成功", limit)
		}
	}

	// 对照：容量充足时同一份清单必须写得出去。
	if err := writeManifestEntry(tar.NewWriter(io.Discard), manifest); err != nil {
		t.Fatalf("容量充足时不该失败: %v", err)
	}
}

// TestValidateRejectsAZeroFormatVersion 钉住「版本号本身要合法」。
//
// 0 或负数说明这份清单不是本程序写的（我们的写入端恒写 >= 1）。它与「版本太新」
// 是两回事，所以报错也必须分开——一句笼统的「格式不对」会让排障的人去查版本
// 兼容性，而真正的问题是这份文件根本不是清单。
func TestValidateRejectsAZeroFormatVersion(t *testing.T) {
	for _, version := range []int{0, -1} {
		manifests := []Manifest{{
			Format:        FormatName,
			FormatVersion: version,
			Parts:         []Part{{Name: PartDatabase, Path: DataPrefix + "blog.db"}},
		}}
		if err := manifests[0].validate(); err == nil {
			t.Fatalf("format_version=%d 应当被拒绝", version)
		}
	}
}

// failAtCall 从第 n 次 Write **调用**开始报错（与字节数无关）。
//
// 用途是够到收尾那两个分支：tar 的块尾与 gzip 的 CRC 只在 Close 时才写出去，
// 而 gzip 会把小块数据攒在内存里——按字节数控制的失败总是落在更早的某次写入上。
// 按调用次数控制才能把失败点正好推到 Close。
type failAtCall struct {
	n     int
	calls int
}

func (w *failAtCall) Write(p []byte) (int, error) {
	w.calls++
	if w.calls >= w.n {
		return 0, errors.New("the pipe was closed")
	}
	return len(p), nil
}

// TestCreateReportsFailuresWhileClosingTheStream 钉住收尾失败也要报错。
//
// tar 与 gzip 是两层流：tar 写它的结束块，gzip 写它的 CRC 与长度。漏掉任一层，
// 归档都读不出来，而**文件名、大小、退出码全都正常**——只有真正去解包时才发现。
// 所以这两层的错误必须一路传回调用方。
func TestCreateReportsFailuresWhileClosingTheStream(t *testing.T) {
	src := t.TempDir()
	seedTree(t, src, map[string]string{"small.txt": "small enough to stay buffered"})
	entries := []Entry{{
		Name:   PartSettings,
		Source: filepath.Join(src, "small.txt"),
		Target: DataPrefix + "small.txt",
		Hash:   true,
	}}

	// 调用 1 是 gzip 头；之后所有写入都被 flate 攒在内存里，直到 Close 才落盘。
	// 所以从调用 2 起报错，逼出来的就是收尾那一层的错误。
	for _, n := range []int{2, 3, 4} {
		writer := &failAtCall{n: n}
		if _, err := Create(writer, entries, BuiltBy{}, fixedTime()); err == nil {
			t.Fatalf("第 %d 次写入起失败，Create 却报告成功", n)
		}
	}
}

// --- extract.go -------------------------------------------------------------

// TestOpenReportsACorruptTarStream 覆盖「gzip 能解，但里面的 tar 不是 tar」。
//
// 这种情况在真实世界里的成因是「文件被另一个程序改写成了 gzip」或者「归档的前
// 512 字节被覆盖过」。gzip 的校验和管不到头部，所以它一定会解压成功，判据只能是
// tar 自己的头部校验。
func TestOpenReportsACorruptTarStream(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	// 0xff 的块既不是全零（那会被当成 tar 的结束标记），也过不了头部校验。
	if _, err := gz.Write(bytes.Repeat([]byte{0xff}, 1024)); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}

	if _, err := Open(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("tar 头损坏时应当报错")
	}
}

// TestOpenRejectsAnImplausibleManifestSize 钉住清单的体积上限。
//
// 清单是唯一会被**整个读进内存**的东西，所以它的长度必须由头部先声明、先检查。
// 少了这条判断，一个 8 GiB 的「清单」会让还原在读取阶段就把内存吃光——而它只是
// 一个压缩后几十 KB 的归档。
func TestOpenRejectsAnImplausibleManifestSize(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// 只写头部、不写正文：长度检查发生在读正文之前，所以这份流足够触发它。
	// Close 会因为「少写了内容」报错，这里刻意忽略——要的正是一个畸形流。
	_ = tw.WriteHeader(&tar.Header{
		Name:     ManifestName,
		Typeflag: tar.TypeReg,
		Size:     maxManifestBytes + 1,
		Mode:     0o600,
	})
	_ = tw.Close()
	_ = gz.Close()

	if _, err := Open(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("清单声明了不可能的体积时应当报错")
	}
}

// craftTrailingGarbage 造一份「清单正常、后面跟着垃圾」的归档。
//
// 手工拼而不是复用 craftArchive：tar 是单遍流，要在两个条目之间插入一段不属于
// 任何条目的字节，只能自己控制写入顺序。gz 是个普通 io.Writer，所以只要先把
// 当前条目 Flush 掉，后面写进去的字节就会出现在解压结果里。
func craftTrailingGarbage(t *testing.T, manifest Manifest) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("序列化清单失败: %v", err)
	}
	data = append(data, '\n')
	if err := tw.WriteHeader(&tar.Header{
		Name: ManifestName, Typeflag: tar.TypeReg, Size: int64(len(data)), Mode: 0o600,
	}); err != nil {
		t.Fatalf("写清单头失败: %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("写清单失败: %v", err)
	}
	if err := tw.Flush(); err != nil {
		t.Fatalf("刷新 tar 失败: %v", err)
	}
	if _, err := gz.Write(bytes.Repeat([]byte{0xff}, 512)); err != nil {
		t.Fatalf("写垃圾失败: %v", err)
	}

	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// TestExtractReportsACorruptTarStream 覆盖「清单读得出来，后面的条目坏了」。
//
// 这一条比 Open 那条更要紧：还原此时**已经知道这是一份本站的归档**了，用户也
// 已经看到「归档：xxx，包含 database/settings/media」。接下来失败必须把这份
// 已经建立起来的信任推翻，而不是把能读的部分留下。
func TestExtractReportsACorruptTarStream(t *testing.T) {
	raw := craftTrailingGarbage(t, Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		CreatedAt:     fixedTime(),
		Parts:         []Part{{Name: PartDatabase, Path: DataPrefix + "blog.db"}},
	})

	reader, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if _, err := reader.Extract(t.TempDir(), ExtractOptions{}); err == nil {
		t.Fatal("条目损坏时 Extract 应当报错")
	}
}

// TestExtractRejectsADirectoryThatCollidesWithAFile 钉住目录条目的冲突。
//
// 归档里可以有两条同名的条目（一条文件、一条目录），而 root 里那个名字已经被
// 文件占了。MkdirAll 会失败，而失败必须传出来：忽略它，后面所有落在那个「目录」
// 下的条目都会以同样的方式静默丢掉。
func TestExtractRejectsADirectoryThatCollidesWithAFile(t *testing.T) {
	raw := craftArchive(t, &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		CreatedAt:     fixedTime(),
		Parts:         []Part{{Name: PartMedia, Path: DataPrefix + "collide"}},
	}, []craftEntry{
		{name: DataPrefix + "collide", body: "i am a file"},
		{name: DataPrefix + "collide/", typeflag: tar.TypeDir},
	})

	reader, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	root := t.TempDir()
	if _, err := reader.Extract(root, ExtractOptions{}); err == nil {
		t.Fatal("目录与文件同名时应当报错")
	}
	// 已经写下的那一条不该被回滚掉一半——文件内容是原样落盘的。
	if body, err := os.ReadFile(filepath.Join(root, "collide")); err != nil || string(body) != "i am a file" {
		t.Fatalf("先落盘的文件被改动了: body=%q err=%v", body, err)
	}
}

// TestExtractRejectsAFileWhoseParentIsAFile 钉住父目录的冲突。
//
// 归档里先有一条叫 sub 的文件，后面又有一条 sub/f。第二条的目标目录建不出来，
// 必须报错而不是跳过——跳过会让「归档里有六个文件、还原出五个」这种半对半错的
// 状态悄无声息地成立。
func TestExtractRejectsAFileWhoseParentIsAFile(t *testing.T) {
	raw := craftArchive(t, &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		CreatedAt:     fixedTime(),
		Parts: []Part{
			{Name: PartMedia, Path: DataPrefix + "sub"},
			{Name: PartLogs, Path: DataPrefix + "sub/f"},
		},
	}, []craftEntry{
		{name: DataPrefix + "sub", body: "i am a file"},
		{name: DataPrefix + "sub/f", body: "child"},
	})

	reader, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if _, err := reader.Extract(t.TempDir(), ExtractOptions{}); err == nil {
		t.Fatal("父目录是个文件时应当报错")
	}
}

// TestExtractValidatesDeclaredPathsRegardlessOfEntries 钉住清单里的路径也要校验。
//
// 条目循环校验的是**归档里实际出现的**路径，而清单是另一份输入。它只有在
// 「清单声明了一个归档里根本没有（或路径非法）的部件」时才会和条目对不上——而
// 那正是伪造归档最省力的形态：正文一条都不写，只把清单里的路径指到 root 之外。
func TestExtractValidatesDeclaredPathsRegardlessOfEntries(t *testing.T) {
	raw := craftArchive(t, &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		CreatedAt:     fixedTime(),
		// 没有 data/ 前缀，且往上跳。
		Parts: []Part{{Name: PartDatabase, Path: "../escape"}},
	}, nil)

	reader, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if _, err := reader.Extract(t.TempDir(), ExtractOptions{}); err == nil {
		t.Fatal("清单里越界的路径应当被拒绝")
	}
}

// TestExtractFileReportsABrokenReader 覆盖写出条目时的读失败。
//
// 输入流在写到一半时坏掉（网络归档、被同时写入的文件），必须报错并让整次解包
// 失败：留下一个只写了一半的文件，与「还原成功」在磁盘上没有任何区别。
func TestExtractFileReportsABrokenReader(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out.bin")
	if _, err := extractFile(dest, brokenReader{}); err == nil {
		t.Fatal("输入流坏掉时应当报错")
	}
}

// TestPrepareRootRejectsUnusableTargets 覆盖解包目录的三条拒绝路径。
//
//   - 目标位于一个常规文件之下 → 建不出来；
//   - 目标名非法（OS 拒绝处理）→ 连查都查不了；
//   - 目标存在但不是目录 → 往里写等于把归档数据撒到一个文件旁边。
//
// 第三条是真实会遇到的：有人把 `backup -o media` 里的路径写成了已有文件。
func TestPrepareRootRejectsUnusableTargets(t *testing.T) {
	dir := t.TempDir()
	occupied := filepath.Join(dir, "occupied.txt")
	if err := os.WriteFile(occupied, []byte("x"), 0o600); err != nil {
		t.Fatalf("准备占位文件失败: %v", err)
	}

	if err := prepareRoot(filepath.Join(occupied, "sub")); err == nil {
		t.Fatal("目标位于常规文件之下时应当报错")
	}
	if err := prepareRoot(strings.Repeat("a", 300)); err == nil {
		t.Fatal("目标名非法时应当报错")
	}
	if err := prepareRoot(occupied); err == nil {
		t.Fatal("目标存在但不是目录时应当报错")
	}

	// 不存在的目标会被创建出来——这是正常路径。
	fresh := filepath.Join(dir, "fresh")
	if err := prepareRoot(fresh); err != nil {
		t.Fatalf("不存在的目标应当被创建: %v", err)
	}
	if info, err := os.Stat(fresh); err != nil || !info.IsDir() {
		t.Fatalf("目标没有被创建成目录: %v", err)
	}
}

// TestExtractHonoursResourceLimitsOnDirectoriesOnly 是一组边界检查：
// 目录条目不计入体积，但计入条目数。
//
// 这条区分不是细节：把目录也算进体积，一个「全是空目录」的归档会被误判成超大；
// 而把目录排除在条目数之外，一个「只有空目录」的归档就能用极小的体积换到海量
// inode——那正是条目数上限要防的东西。
func TestExtractHonoursResourceLimitsOnDirectoriesOnly(t *testing.T) {
	var entries []craftEntry
	parts := make([]Part, 0, 30)
	for i := 0; i < 30; i++ {
		name := DataPrefix + "dir" + strconv.Itoa(i)
		entries = append(entries, craftEntry{name: name + "/", typeflag: tar.TypeDir})
		parts = append(parts, Part{Name: PartLogs, Path: name})
	}
	raw := craftArchive(t, &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		CreatedAt:     fixedTime(),
		Parts:         parts,
	}, entries)

	reader, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if _, err := reader.Extract(t.TempDir(), ExtractOptions{MaxEntries: 5}); err == nil {
		t.Fatal("目录条目也必须计入条目数上限")
	}

	reader, err = Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	// 一条正文都没有 → 体积为 0，再小的 MaxBytes 也该过得去。
	result, err := reader.Extract(t.TempDir(), ExtractOptions{MaxBytes: 1})
	if err != nil {
		t.Fatalf("空目录不该占用体积额度: %v", err)
	}
	if result.Bytes != 0 || result.Files != 0 {
		t.Fatalf("目录条目不该被算成文件或体积: %+v", result)
	}
}
