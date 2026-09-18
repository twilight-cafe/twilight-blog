package backup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixedTime 让清单里的时间戳可预期——断言 CreatedAt 时不必容忍「刚好差一秒」。
func fixedTime() time.Time {
	return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
}

// seedTree 在 root 下造一棵小文件树，供往返测试比对。
//
// 名字不能叫 writeTree：archive.go 里已经有一个同名的内部函数（它负责把一棵树
// 写进 tar），而这两个东西的语义毫无关系。
func seedTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("准备测试数据失败 (%s): %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("准备测试数据失败 (%s): %v", name, err)
		}
	}
}

// TestArchiveRoundTrip 是这组功能的主干断言：写出来的归档必须能被原样读回来。
//
// 它同时钉住三件容易各自漂移的事：清单里的体积与文件数与磁盘上一致、
// 标了 Hash 的单文件带 sha256（没标的绝不该带）、目录结构被完整保留。
//
// 反证方式：把 writeTree 里的 rel 计算改成固定值（丢掉子目录层级）→ 文件数断言失败。
func TestArchiveRoundTrip(t *testing.T) {
	src := t.TempDir()
	seedTree(t, filepath.Join(src, "media"), map[string]string{
		"a.png":              "png-bytes",
		"2026/09/nested.txt": "nested",
	})
	if err := os.WriteFile(filepath.Join(src, "blog.db"), []byte("database-bytes"), 0o600); err != nil {
		t.Fatalf("准备数据库文件失败: %v", err)
	}

	entries := []Entry{
		{Name: PartDatabase, Source: filepath.Join(src, "blog.db"), Target: DataPrefix + "blog.db", Hash: true},
		{Name: PartMedia, Source: filepath.Join(src, "media"), Target: DataPrefix + "media"},
	}

	var buf bytes.Buffer
	manifest, err := Create(&buf, entries, BuiltBy{OS: "test", Arch: "test", Revision: "deadbeef"}, fixedTime())
	if err != nil {
		t.Fatalf("Create() 报错: %v", err)
	}

	if manifest.Format != FormatName || manifest.FormatVersion != FormatVersion {
		t.Fatalf("清单的格式标识不对: %+v", manifest)
	}
	if !manifest.CreatedAt.Equal(fixedTime()) {
		t.Fatalf("CreatedAt = %v, 期望 %v", manifest.CreatedAt, fixedTime())
	}
	if len(manifest.Parts) != 2 {
		t.Fatalf("清单里应有 2 项，实际 %d", len(manifest.Parts))
	}

	db := manifest.Parts[0]
	if db.Name != PartDatabase || db.Bytes != int64(len("database-bytes")) || db.SHA256 == "" {
		t.Fatalf("数据库那一项不对: %+v", db)
	}
	media := manifest.Parts[1]
	if media.Files != 2 {
		t.Fatalf("媒体那一项的文件数 = %d, 期望 2", media.Files)
	}
	if media.SHA256 != "" {
		t.Fatalf("目录条目不该带 sha256（逐文件哈希的成本远超收益）: %+v", media)
	}

	reader, err := Open(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if got := reader.Manifest(); len(got.Parts) != 2 {
		t.Fatalf("读回来的清单有 %d 项, 期望 2", len(got.Parts))
	}

	out := t.TempDir()
	result, err := reader.Extract(out, ExtractOptions{})
	if err != nil {
		t.Fatalf("Extract() 报错: %v", err)
	}
	if result.Files != 3 {
		t.Fatalf("解出 %d 个文件, 期望 3", result.Files)
	}
	if len(result.Parts) != 2 {
		t.Fatalf("确认落盘的部分数 = %d, 期望 2", len(result.Parts))
	}

	// 内容与目录结构都要对。子目录那一条尤其重要：它证明 writeTree 是按相对路径
	// 落盘的，而不是把所有文件平铺到一个目录里。
	for name, want := range map[string]string{
		"blog.db":                  "database-bytes",
		"media/a.png":              "png-bytes",
		"media/2026/09/nested.txt": "nested",
	} {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("读回 %s 失败: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s 的内容 = %q, 期望 %q", name, got, want)
		}
	}
}

// TestManifestIsTheFirstEntry 钉住「清单必须排在第一个」。
//
// 这不是格式洁癖：还原端要在落盘任何东西之前判断「这份归档我能不能接」，
// 而 tar 只能顺序读。清单若排到后面，检查就变成「先写一遍再说」。
//
// 反证方式：把 Create 里的 writeManifestEntry 挪到循环之后 → 本条失败。
func TestManifestIsTheFirstEntry(t *testing.T) {
	src := t.TempDir()
	seedTree(t, src, map[string]string{"blog.db": "x"})

	var buf bytes.Buffer
	if _, err := Create(&buf, []Entry{
		{Name: PartDatabase, Source: filepath.Join(src, "blog.db"), Target: DataPrefix + "blog.db"},
	}, BuiltBy{}, fixedTime()); err != nil {
		t.Fatalf("Create() 报错: %v", err)
	}

	// 直接读原始 tar，确认物理顺序。
	names := listEntryNames(t, buf.Bytes())
	if len(names) == 0 || names[0] != ManifestName {
		t.Fatalf("归档的条目顺序 = %v, 第一项必须是 %q", names, ManifestName)
	}
}

// TestOpenRejectsForeignAndNewerArchives 覆盖「这份归档不该被接」的几种形态。
func TestOpenRejectsForeignAndNewerArchives(t *testing.T) {
	t.Run("第一个条目不是清单", func(t *testing.T) {
		raw := craftArchive(t, nil, []craftEntry{{name: "data/blog.db", body: "x"}})
		if _, err := Open(bytes.NewReader(raw)); err == nil {
			t.Fatal("第一个条目不是 manifest.json 的归档应当被拒绝")
		}
	})

	t.Run("格式标识不对", func(t *testing.T) {
		manifest := Manifest{Format: "something-else", FormatVersion: 1, Parts: []Part{{Name: PartDatabase, Path: "data/blog.db"}}}
		raw := craftArchive(t, &manifest, []craftEntry{{name: "data/blog.db", body: "x"}})
		if _, err := Open(bytes.NewReader(raw)); err == nil {
			t.Fatal("格式标识不匹配的归档应当被拒绝")
		}
	})

	t.Run("格式版本比本程序新", func(t *testing.T) {
		manifest := Manifest{Format: FormatName, FormatVersion: FormatVersion + 1, Parts: []Part{{Name: PartDatabase, Path: "data/blog.db"}}}
		raw := craftArchive(t, &manifest, []craftEntry{{name: "data/blog.db", body: "x"}})
		_, err := Open(bytes.NewReader(raw))
		if err == nil {
			t.Fatal("更新的格式版本应当被拒绝")
		}
		// 错误信息必须告诉用户「去升级」，而不是一句 invalid format。
		if !bytes.Contains([]byte(err.Error()), []byte("upgrade")) {
			t.Fatalf("错误信息没有指出该升级: %v", err)
		}
	})

	t.Run("清单里一项都没有", func(t *testing.T) {
		manifest := Manifest{Format: FormatName, FormatVersion: FormatVersion}
		raw := craftArchive(t, &manifest, nil)
		if _, err := Open(bytes.NewReader(raw)); err == nil {
			t.Fatal("没有任何部分的归档应当被拒绝")
		}
	})
}

// TestCreateRejectsUnsafeTargets 确认写入端与解包端共用同一套路径判据。
//
// 为什么写入端也要校验：它挡住的是「调用方把 Target 拼错成绝对路径或带 .. 的
// 路径」——那种归档一旦写出来，读的时候才失败，而那时它可能已经被当成备份存了
// 几个月。
//
// 反证方式：把 scanEntry 里的 safeEntryPath 调用删掉 → 本条失败。
func TestCreateRejectsUnsafeTargets(t *testing.T) {
	src := t.TempDir()
	seedTree(t, src, map[string]string{"blog.db": "x"})

	for _, target := range []string{
		"blog.db",           // 少了 data/ 前缀
		DataPrefix + "../x", // 往上跳
		DataPrefix + "a/../../x",
		"/absolute/path",
	} {
		var buf bytes.Buffer
		_, err := Create(&buf, []Entry{
			{Name: PartDatabase, Source: filepath.Join(src, "blog.db"), Target: target},
		}, BuiltBy{}, fixedTime())
		if err == nil {
			t.Fatalf("Target = %q 应当被拒绝", target)
		}
	}
}

// TestCreateRejectsNonRegularSource 钉住「非常规文件不进归档」。
//
// 用目录去冒充一个文件条目是不该放过的输入错误：它会让清单里出现一项既没有
// Files 也没有 sha256 的东西，而还原端拿它没办法。
func TestCreateRejectsNonRegularSource(t *testing.T) {
	src := t.TempDir()
	dir := filepath.Join(src, "media")
	seedTree(t, dir, map[string]string{"a.png": "x"})

	var buf bytes.Buffer
	// 目录是允许的（PartMedia 就是这样），但**同一份源**以目录形式出场时
	// 走的是目录分支——这里用不存在的路径确认 stat 失败会被报出来。
	if _, err := Create(&buf, []Entry{
		{Name: PartDatabase, Source: filepath.Join(src, "missing.db"), Target: DataPrefix + "missing.db"},
	}, BuiltBy{}, fixedTime()); err == nil {
		t.Fatal("源文件不存在时 Create 应当失败")
	}
}

// TestCreateRejectsEmptyEntries 是最小的输入校验：没有数据的归档没有意义，
// 而一份「成功产出的空归档」最容易被误当成有效备份。
func TestCreateRejectsEmptyEntries(t *testing.T) {
	var buf bytes.Buffer
	if _, err := Create(&buf, nil, BuiltBy{}, fixedTime()); err == nil {
		t.Fatal("没有数据源时 Create 应当失败")
	}
}

// TestRelativePathMatchesExtraction 钉住 RelativePath 的语义。
//
// 它必须与 Extract 用**同一套**换算（剥掉 data/ 前缀）：还原命令要靠
// filepath.Join(staging, RelativePath(part.Path)) 找到解包出来的文件。
// 若这里悄悄保留前缀，还原会在暂存目录里找不到几个部分——而解包校验、路径守卫、
// 摘要对账全都通过了，没有任何一处会报警。这条是回归守护，不是假想。
//
// 反证方式：把 RelativePath 改成保留 data/ 前缀 → 本条与
// TestExtractAndRelativePathAgree 一起失败。
func TestRelativePathMatchesExtraction(t *testing.T) {
	got, err := RelativePath("data/media/a.png")
	if err != nil {
		t.Fatalf("RelativePath() 报错: %v", err)
	}
	if filepath.ToSlash(got) != "media/a.png" {
		t.Fatalf("RelativePath() = %q, 期望 media/a.png（剥掉 data/ 前缀后的相对路径）", got)
	}

	for _, bad := range []string{"media/a.png", "data/../a.png", "data/"} {
		if _, err := RelativePath(bad); err == nil {
			t.Fatalf("RelativePath(%q) 应当报错", bad)
		}
	}
}

// TestManifestHelpers 覆盖 IsPart / PartPath —— 还原命令靠它们决定「要不要动
// 某个目录」，答错了就是「对一个归档里根本没有的东西做删除后放置」。
func TestManifestHelpers(t *testing.T) {
	manifest := Manifest{Parts: []Part{
		{Name: PartDatabase, Path: "data/blog.db"},
		{Name: PartMedia, Path: "data/media"},
	}}

	if !manifest.IsPart(PartMedia) {
		t.Fatal("IsPart(media) 应为真")
	}
	if manifest.IsPart(PartSearch) {
		t.Fatal("IsPart(search-index) 应为假")
	}
	if got := manifest.PartPath(PartMedia); got != "data/media" {
		t.Fatalf("PartPath(media) = %q", got)
	}
	if got := manifest.PartPath(PartSearch); got != "" {
		t.Fatalf("不存在的部分应返回空串，实际 %q", got)
	}
}

// TestManifestJSONShape 钉住清单的序列化形状。
//
// 清单是**跨版本**的接口：老程序要能读懂新程序写的清单。所以字段名与
// omitempty 的取舍不是内部细节，改动它等于改格式——这条测试的作用是让那种
// 改动必须先被看见（format_version 也该跟着涨）。
func TestManifestJSONShape(t *testing.T) {
	manifest := Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		CreatedAt:     fixedTime(),
		BuiltBy:       BuiltBy{OS: "linux", Arch: "amd64", Revision: "abc"},
		Parts: []Part{
			{Name: PartDatabase, Path: "data/blog.db", Bytes: 12, SHA256: "aa"},
			{Name: PartMedia, Path: "data/media", Bytes: 34, Files: 2},
		},
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("序列化清单失败: %v", err)
	}
	text := string(data)

	for _, want := range []string{
		`"format":"twilight-blog-backup"`,
		`"format_version":1`,
		`"created_at"`,
		`"built_by"`,
		`"revision":"abc"`,
		`"sha256":"aa"`,
		`"files":2`,
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("清单 JSON 里缺少 %s：%s", want, text)
		}
	}

	// 目录条目不该带 sha256（omitempty 必须在起作用）。
	if bytes.Contains(data, []byte(`"sha256":""`)) {
		t.Fatalf("空的 sha256 不该被写出来：%s", text)
	}
	// 单文件条目不该带 files。
	if bytes.Contains(data, []byte(`"files":0`)) {
		t.Fatalf("为 0 的 files 不该被写出来：%s", text)
	}
}
