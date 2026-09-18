package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"sort"
	"testing"
)

// craftEntry 描述一个要手工塞进归档的条目。typeflag 为 0 表示常规文件。
type craftEntry struct {
	name     string
	body     string
	typeflag byte
	link     string
}

// craftArchive 手工拼一份归档。
//
// 为什么不用 Create 来造这些输入：解包端的用例测的全是「正常写入端不可能产出的
// 东西」——越界路径、符号链接、重复条目、与正文不符的摘要。拿 Create 去造等于用
// 被测对象生产测试输入，而它自己就把这些形态挡住了。
//
// manifest 为 nil 时第一个条目是一个普通文件，用来测「这不是本站的归档」。
func craftArchive(t *testing.T, manifest *Manifest, entries []craftEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	if manifest == nil {
		body := []byte("this is not a manifest")
		if err := tw.WriteHeader(&tar.Header{Name: "random.txt", Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o600}); err != nil {
			t.Fatalf("写归档头失败: %v", err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("写归档正文失败: %v", err)
		}
	} else {
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatalf("序列化清单失败: %v", err)
		}
		data = append(data, '\n')
		if err := tw.WriteHeader(&tar.Header{Name: ManifestName, Typeflag: tar.TypeReg, Size: int64(len(data)), Mode: 0o600}); err != nil {
			t.Fatalf("写清单头失败: %v", err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("写清单失败: %v", err)
		}
	}

	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}

		hdr := &tar.Header{Name: entry.name, Typeflag: typeflag, Mode: 0o600}
		if typeflag == tar.TypeReg {
			hdr.Size = int64(len(entry.body))
		}
		if entry.link != "" {
			hdr.Linkname = entry.link
		}

		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("写条目 %s 失败: %v", entry.name, err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				t.Fatalf("写条目正文 %s 失败: %v", entry.name, err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("收尾归档失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("收尾归档失败: %v", err)
	}
	return buf.Bytes()
}

// listEntryNames 读原始 tar 的条目顺序（不解 gzip 之后的语义，只看物理顺序）。
func listEntryNames(t *testing.T, raw []byte) []string {
	t.Helper()

	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("读归档失败: %v", err)
	}
	tr := tar.NewReader(gz)

	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("读归档条目失败: %v", err)
		}
		names = append(names, hdr.Name)
	}
	return names
}

// sha256Hex 与 extractFile 用同一套编码算摘要，供「篡改正文」那类用例对账。
func sha256Hex(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// roundTripArchive 用真实的 Create 造一份归档，供那些不关心归档构造方式的用例复用。
//
// 它刻意用 Create 而不是 craftArchive：这样「写入端能产出什么」与「解包端接受
// 什么」之间的契约由一条真实路径串起来，而不是两边各自拼一个恰好能对上来的东西。
func roundTripArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	src := t.TempDir()
	seedTree(t, src, files)

	entries := make([]Entry, 0, len(files))
	for name := range files {
		entries = append(entries, Entry{
			Name:   PartDatabase,
			Source: filepath.Join(src, filepath.FromSlash(name)),
			Target: DataPrefix + name,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Target < entries[j].Target })

	var buf bytes.Buffer
	if _, err := Create(&buf, entries, BuiltBy{}, fixedTime()); err != nil {
		t.Fatalf("Create() 报错: %v", err)
	}
	return buf.Bytes()
}
