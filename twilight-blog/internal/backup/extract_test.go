package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// databaseOnly 造一份「只声明了数据库」的清单。
func databaseOnly() *Manifest {
	return &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		Parts:         []Part{{Name: PartDatabase, Path: "data/blog.db"}},
	}
}

// TestExtractRejectsUnsafeEntries 是解包守卫的核心断言。
//
// 归档是不可信输入（下载、U 盘、别人的机器），所以路径要逐条校验，而不是
// 「反正我们自己写出来的归档都是干净的」。zip-slip 的每一个经典变体都在这里。
//
// 反证方式：删掉 safeEntryPath 里的 filepath.IsLocal 判断 → 「往上跳一层」
// 与「跳到更上层」两条子用例立刻失败（文件被写到 root 之外）。
func TestExtractRejectsUnsafeEntries(t *testing.T) {
	cases := []struct {
		label    string
		entry    string
		typeflag byte
	}{
		{"缺少 data 前缀", "evil.txt", 0},
		{"往上跳一层", "data/../escape.txt", 0},
		{"跳到更上层", "data/a/../../escape.txt", 0},
		// 目录条目才允许以斜杠结尾（tar 的约定），而它的相对路径剥完是空串。
		{"空路径", "data/", tar.TypeDir},
		{"绝对路径", "/etc/passwd", 0},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			raw := craftArchive(t, databaseOnly(), []craftEntry{
				{name: "data/blog.db", body: "ok"},
				{name: tc.entry, body: "evil", typeflag: tc.typeflag},
			})

			root := t.TempDir()
			reader, err := Open(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("Open() 报错: %v", err)
			}
			if _, err := reader.Extract(root, ExtractOptions{}); err == nil {
				t.Fatalf("条目 %q 应当被拒绝", tc.entry)
			}

			// 拒绝还不够：要确认它**什么都没写出去**。
			// 目标目录的兄弟路径是 zip-slip 最常见的落点。
			sibling := filepath.Join(filepath.Dir(root), "escape.txt")
			if _, err := os.Stat(sibling); err == nil {
				t.Fatalf("解包把文件写到了目标目录之外: %s", sibling)
			}
		})
	}
}

// TestExtractRejectsUnsupportedTypes 确认符号链接/硬链接/设备文件被**拒绝**而不是跳过。
//
// 为什么是拒绝而不是跳过：跳过会让归档的实际内容与清单对不上，而那种不一致没有
// 任何地方会报出来——用户以为还原好了，其实少了一部分。符号链接尤其危险，
// 它是「解包时顺着链接写到别处」的经典载体。
//
// 反证方式：把 Extract 的 default 分支改成 `continue` → 本条三个子用例全部失败。
func TestExtractRejectsUnsupportedTypes(t *testing.T) {
	cases := map[string]craftEntry{
		"符号链接": {name: "data/link", typeflag: tar.TypeSymlink, link: "../../etc/passwd"},
		"硬链接":  {name: "data/hard", typeflag: tar.TypeLink, link: "data/blog.db"},
		"字符设备": {name: "data/dev", typeflag: tar.TypeChar},
	}

	for label, entry := range cases {
		t.Run(label, func(t *testing.T) {
			raw := craftArchive(t, databaseOnly(), []craftEntry{
				{name: "data/blog.db", body: "ok"},
				entry,
			})

			reader, err := Open(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("Open() 报错: %v", err)
			}
			if _, err := reader.Extract(t.TempDir(), ExtractOptions{}); err == nil {
				t.Fatalf("%s 条目应当被拒绝", label)
			}
		})
	}
}

// TestExtractEnforcesResourceLimits 覆盖 gzip 炸弹的两道闸。
//
// 一个几十 KB 的归档可以解出几十 GB，也可以只含海量零字节条目把 inode 用光。
// 两种都不需要多大的技术门槛，所以上限必须存在且必须真的生效。
//
// 反证方式：删掉 Extract 里 result.Bytes 的累加判断 → 体积子用例失败；
// 删掉条目计数 → 条目数子用例失败。
func TestExtractEnforcesResourceLimits(t *testing.T) {
	t.Run("总体积", func(t *testing.T) {
		raw := craftArchive(t, databaseOnly(), []craftEntry{
			{name: "data/blog.db", body: strings.Repeat("A", 64)},
		})

		reader, err := Open(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("Open() 报错: %v", err)
		}
		if _, err := reader.Extract(t.TempDir(), ExtractOptions{MaxBytes: 16}); err == nil {
			t.Fatal("超过体积上限的归档应当被拒绝")
		}
	})

	t.Run("条目数", func(t *testing.T) {
		raw := craftArchive(t, databaseOnly(), []craftEntry{
			{name: "data/blog.db", body: "ok"},
			{name: "data/extra.txt", body: "ok"},
		})

		reader, err := Open(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("Open() 报错: %v", err)
		}
		if _, err := reader.Extract(t.TempDir(), ExtractOptions{MaxEntries: 1}); err == nil {
			t.Fatal("超过条目上限的归档应当被拒绝")
		}
	})
}

// TestExtractDetectsCorruption 钉住 sha256 对账。
//
// 摘要是「归档没坏」的唯一证据：tar 与 gzip 的结构校验只能证明「字节流是自洽的」，
// 证明不了「内容还是当初写下去的那份」。媒体还有「图打不开」这种即时反馈，
// 数据库坏掉则可能一直到某次查询才暴露。
//
// 反证方式：让 extractFile 返回一个固定摘要（不真算）→ 篡改子用例失败。
func TestExtractDetectsCorruption(t *testing.T) {
	body := "the real content"
	manifest := &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		Parts:         []Part{{Name: PartDatabase, Path: "data/blog.db", SHA256: sha256Hex(body)}},
	}

	t.Run("内容未变时通过", func(t *testing.T) {
		raw := craftArchive(t, manifest, []craftEntry{{name: "data/blog.db", body: body}})
		reader, err := Open(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("Open() 报错: %v", err)
		}
		if _, err := reader.Extract(t.TempDir(), ExtractOptions{}); err != nil {
			t.Fatalf("完好的归档应当解包成功: %v", err)
		}
	})

	t.Run("正文被换掉时报错", func(t *testing.T) {
		// 摘要还是原来那个，正文换成了别的内容。
		raw := craftArchive(t, manifest, []craftEntry{{name: "data/blog.db", body: "tampered content"}})
		reader, err := Open(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("Open() 报错: %v", err)
		}
		if _, err := reader.Extract(t.TempDir(), ExtractOptions{}); err == nil {
			t.Fatal("内容与摘要不符的归档应当被拒绝")
		}
	})
}

// TestExtractRequiresEveryDeclaredPart 是「静默灾难」的防线。
//
// 少了数据库的归档仍然能被解包、仍然能让站点起来——只是变成一个空站，而操作者
// 以为数据回来了。必须在这里、在落盘任何东西之前把它挡住。
//
// 反证方式：删掉 Extract 结尾那段对账循环 → 本条失败。
func TestExtractRequiresEveryDeclaredPart(t *testing.T) {
	manifest := &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		Parts: []Part{
			{Name: PartDatabase, Path: "data/blog.db"},
			{Name: PartMedia, Path: "data/media"},
		},
	}

	// 清单声明了两个部分，归档里只有数据库。
	raw := craftArchive(t, manifest, []craftEntry{{name: "data/blog.db", body: "x"}})

	reader, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	_, err = reader.Extract(t.TempDir(), ExtractOptions{})
	if err == nil {
		t.Fatal("声明了但缺失的部分应当被报出来")
	}
	if !strings.Contains(err.Error(), PartMedia) {
		t.Fatalf("错误信息应当点名缺了哪一部分: %v", err)
	}
}

// TestExtractRequiresAnEmptyTarget 钉住「目标目录必须为空」。
//
// 这条前提支撑着解包路径安全的另一半：root 里没有预先埋好的符号链接。
// 顺带它也保证了一次被拒绝的解包不会碰目标目录里已有的任何东西。
//
// 反证方式：删掉 prepareRoot 里的空目录检查 → 本条失败。
func TestExtractRequiresAnEmptyTarget(t *testing.T) {
	raw := roundTripArchive(t, map[string]string{"blog.db": "x"})

	root := t.TempDir()
	existing := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(existing, []byte("hi"), 0o600); err != nil {
		t.Fatalf("准备目标目录失败: %v", err)
	}

	reader, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if _, err := reader.Extract(root, ExtractOptions{}); err == nil {
		t.Fatal("非空的目标目录应当被拒绝")
	}
	if _, err := os.Stat(existing); err != nil {
		t.Fatalf("被拒绝的解包不该动目标目录里已有的文件: %v", err)
	}
}

// TestExtractRejectsDuplicateEntries 是 O_EXCL 的语义。
//
// 同一个路径在归档里出现两次不该被当成「后者覆盖前者」：一份归档里出现两条同路径
// 的内容，本身就说明它是被拼出来的，而哪一份才是真的无从判断。停下来是唯一安全
// 的处理。
func TestExtractRejectsDuplicateEntries(t *testing.T) {
	raw := craftArchive(t, databaseOnly(), []craftEntry{
		{name: "data/blog.db", body: "first"},
		{name: "data/blog.db", body: "second"},
	})

	reader, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if _, err := reader.Extract(t.TempDir(), ExtractOptions{}); err == nil {
		t.Fatal("重复路径应当被拒绝")
	}
}

// TestExtractHandlesDirectories 确认目录条目只建目录、不写文件，
// 且后续同目录下的文件仍然能落进去（顺序依赖真实存在）。
func TestExtractHandlesDirectories(t *testing.T) {
	manifest := &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		Parts:         []Part{{Name: PartMedia, Path: "data/media"}},
	}
	raw := craftArchive(t, manifest, []craftEntry{
		{name: "data/media/", typeflag: tar.TypeDir},
		{name: "data/media/a.png", body: "png"},
	})

	root := t.TempDir()
	reader, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	result, err := reader.Extract(root, ExtractOptions{})
	if err != nil {
		t.Fatalf("Extract() 报错: %v", err)
	}
	if result.Files != 1 {
		t.Fatalf("解出的文件数 = %d, 期望 1（目录条目不该被算成文件）", result.Files)
	}

	// 落盘位置是剥掉 data/ 之后的相对路径：root 之下直接就是 media/a.png。
	body, err := os.ReadFile(filepath.Join(root, "media", "a.png"))
	if err != nil {
		t.Fatalf("读回文件失败: %v", err)
	}
	if string(body) != "png" {
		t.Fatalf("内容 = %q, 期望 png", body)
	}
}

// TestExtractCreatesMissingRoot 确认目标目录不存在时会被创建。
//
// 调用方（还原命令）总是先 MkdirTemp，但「不存在就建」让这个函数可以独立使用，
// 也让上面那条「必须为空」的规则不会被误解成「必须已存在」。
func TestExtractCreatesMissingRoot(t *testing.T) {
	raw := roundTripArchive(t, map[string]string{"blog.db": "x"})

	root := filepath.Join(t.TempDir(), "nested", "extract-here")
	reader, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if _, err := reader.Extract(root, ExtractOptions{}); err != nil {
		t.Fatalf("Extract() 报错: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "blog.db")); err != nil {
		t.Fatalf("目标目录没有被创建或文件没落进去: %v", err)
	}
}

// TestExtractAndRelativePathAgree 钉住「解包落点」与「还原命令定位文件的方式」一致。
//
// 这两处一旦漂移，症状是还原命令在暂存目录里找不到某几个部分——而归档校验、路径
// 守卫、摘要对账全都通过了，没有任何一处会报警。它实际发生过一次（Extract 剥掉了
// data/ 前缀而 RelativePath 没有），所以这是一条回归守护，不是假想。
//
// 反证方式：把 RelativePath 改成保留 data/ 前缀 → 本条失败。
func TestExtractAndRelativePathAgree(t *testing.T) {
	manifest := &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		Parts: []Part{
			{Name: PartDatabase, Path: "data/blog.db"},
			{Name: PartMedia, Path: "data/media"},
		},
	}
	raw := craftArchive(t, manifest, []craftEntry{
		{name: "data/blog.db", body: "db"},
		{name: "data/media/", typeflag: tar.TypeDir},
		{name: "data/media/a.png", body: "png"},
	})

	root := t.TempDir()
	reader, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Open() 报错: %v", err)
	}
	if _, err := reader.Extract(root, ExtractOptions{}); err != nil {
		t.Fatalf("Extract() 报错: %v", err)
	}

	// 清单里提到的每一个部分，都必须能从解包结果里按 RelativePath 找到。
	for _, part := range manifest.Parts {
		rel, err := RelativePath(part.Path)
		if err != nil {
			t.Fatalf("RelativePath(%s) 报错: %v", part.Path, err)
		}
		if _, err := os.Lstat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("%s 在解包结果里找不到（RelativePath 与 Extract 的换算不一致）: %v", part.Name, err)
		}
	}
}

// TestOpenRejectsTruncatedArchive 覆盖「归档被截断」。
//
// 这是很现实的形态：备份写了一半断电、传输中断、U 盘拔得太早。
// gzip 的 CRC 让它能被发现，而这里要确认错误真的被报出来而不是静默通过。
func TestOpenRejectsTruncatedArchive(t *testing.T) {
	raw := roundTripArchive(t, map[string]string{"blog.db": strings.Repeat("x", 4096)})
	if len(raw) < 100 {
		t.Fatalf("测试数据太小，无法截断: %d 字节", len(raw))
	}

	truncated := raw[:len(raw)/2]
	reader, err := Open(bytes.NewReader(truncated))
	if err != nil {
		// 头部就坏了也算通过——重要的是没有 panic，且调用方拿到错误。
		return
	}
	if _, err := reader.Extract(t.TempDir(), ExtractOptions{}); err == nil {
		t.Fatal("被截断的归档应当报错")
	}
}

// TestOpenRejectsGarbage 确认「随便一个文件」不会被当成归档接受，
// 且错误信息会提示期望的格式。
func TestOpenRejectsGarbage(t *testing.T) {
	_, err := Open(strings.NewReader("这不是一个 gzip 文件"))
	if err == nil {
		t.Fatal("非 gzip 内容应当被拒绝")
	}
	if !strings.Contains(err.Error(), "tar.gz") {
		t.Fatalf("错误信息应当提示期望的格式: %v", err)
	}
}

// TestExtractEmptyStreamIsRejected 用只有 gzip 头、没有任何条目的流确认
// 「空归档」在 Open 阶段就被挡住。
func TestExtractEmptyStreamIsRejected(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}

	if _, err := Open(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("空归档应当被拒绝")
	}
}
