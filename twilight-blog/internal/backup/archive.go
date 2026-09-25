// Package backup 实现站点数据的归档与还原。
//
// 它只做两件事，别的都不管：
//
//   - 把若干「数据源」流式写成一个 tar.gz，并在**最前面**写一份 manifest.json；
//   - 把归档解包到一个目录，全程对路径与体积设防。
//
// 还原的编排（什么时候停服务、先不先打安全备份、换位之后做什么）不在这个包里，
// 因为那些决定依赖工作目录、settings.json 与命令行选项——它们是 CLI 的知识。
//
// 两条贯穿本包的前提：
//
//   - 归档是**不可信输入**。它可能来自下载、U 盘、别人的机器，所以解包路径必须
//     逐条校验（见 safeEntryPath），体积必须封顶（防 gzip 炸弹）。
//   - 归档是**敏感文件**。settings.json 里已经没有密码哈希了（凭据住在 users 表），
//     但 blog.db 里有全部账号的 bcrypt 哈希、会话 token 摘要与回收站内容，
//     媒体目录则是那些图片的**唯一副本**（媒体是硬删、没有回收站）。
//     所以写出的一切都按 0600 落盘，文档里也按敏感件提醒。
package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Hooks for deterministic failure-path tests. The real implementations are the
// default; tests replace them only to exercise branches that depend on
// filesystem races or inode-level failures.
var (
	walkDir        = filepath.WalkDir
	dirEntryInfo   = func(d fs.DirEntry) (fs.FileInfo, error) { return d.Info() }
	hashFileFn     = hashFile
	closeTarWriter = func(tw *tar.Writer) error { return tw.Close() }
)

// FormatName 是归档的格式标识，写在清单里。
// 改它意味着「老版本一定读不了」，所以只在破坏性变更时动。
const FormatName = "twilight-blog-backup"

// FormatVersion 是清单格式的版本。
//
// 递增它的唯一理由是「新归档里有旧程序必须拒绝的东西」。还原端只有一条判断：
// 清单版本高于本程序认识的版本 → 拒绝。所以这个数字本身就是兼容性契约——
// 读到更高的版本说明该换更新的程序，而不是硬着头皮解包。
const FormatVersion = 1

// ManifestName 是清单在归档里的固定名字，必须是**第一个**条目。
//
// 为什么抢第一个位置：tar 是顺序流，读到清单才知道后面有没有数据库、这份归档
// 能不能接。若它排在最后，还原端就得先把整卷读完（甚至已经落盘了一部分）才发现
// 版本不兼容。
const ManifestName = "manifest.json"

// DataPrefix 是归档里所有数据条目的路径前缀。
//
// 有它才能把「站点数据」与「归档自身的元数据」（manifest.json）分开，
// 也才有一个稳定的锚点去判断解包路径是否安全：只接受这个前缀之下的条目。
const DataPrefix = "data/"

// 各部分的名字。它们只影响清单的可读性与还原时的报告，不参与任何路径判断。
const (
	PartDatabase  = "database"
	PartSettings  = "settings"
	PartMedia     = "media"
	PartSearch    = "search-index"
	PartSearchKey = "search-master-key"
	PartLogs      = "logs"
)

// BuiltBy 记录归档是谁写的。排障时第一个要问的就是「这份归档来自哪个版本」。
type BuiltBy struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Revision string `json:"revision"`
}

// Part 是清单里的一项。
type Part struct {
	// Name 是部分名（database / settings / media / …）。
	Name string `json:"name"`
	// Path 是它在归档内的路径，必定落在 DataPrefix 之下。
	Path string `json:"path"`
	// Bytes 是这一部分的原始体积（未压缩）。
	Bytes int64 `json:"bytes"`
	// Files 只在 Path 指向目录时出现，是该目录里的常规文件数。
	Files int `json:"files,omitempty"`
	// SHA256 只在单文件条目上出现，十六进制小写。
	//
	// 为什么只给数据库与配置文件算：它们坏掉的代价最高（凭据与站点配置），
	// 而且都足够小。媒体动辄上万个文件，逐文件哈希会让备份多读一整遍全量数据，
	// 收益不抵成本——媒体损坏通常表现为某张图打不开，一眼就能发现。
	SHA256 string `json:"sha256,omitempty"`
}

// Manifest 是归档的目录：它说明归档里有什么、多大、由谁在何时写下。
type Manifest struct {
	Format        string    `json:"format"`
	FormatVersion int       `json:"format_version"`
	CreatedAt     time.Time `json:"created_at"`
	BuiltBy       BuiltBy   `json:"built_by"`
	Parts         []Part    `json:"parts"`
}

// validate 判断这份清单描述的归档本程序能不能接。
func (m *Manifest) validate() error {
	if m.Format != FormatName {
		return fmt.Errorf("unrecognised archive format %q (want %q)", m.Format, FormatName)
	}
	if m.FormatVersion < 1 {
		return fmt.Errorf("the archive declares an invalid format version %d", m.FormatVersion)
	}
	if m.FormatVersion > FormatVersion {
		return fmt.Errorf("this archive was written by a newer version of the blog engine "+
			"(format %d, this build understands up to %d): upgrade before restoring it",
			m.FormatVersion, FormatVersion)
	}
	if len(m.Parts) == 0 {
		return fmt.Errorf("the archive contains no data")
	}

	// 单例部分（settings / database / media / search-index / search-master-key）
	// 在归档里必须各只有一项。原实现只让 applyRestore 按第一个同名 part 做校验，
	// 却会循环把每个同名 part 都换到目标位置；因此“第一项安全、第二项恶意”的
	// 重复 settings part 可以绕过配置安全检查。这里在读取清单时就拒绝重复，
	// 使 PartPath 的“取第一个”语义不再有安全含义。
	//
	// logs 是刻意的例外：一次归档可以为每个 log.txt.* 生成一个同名 part。
	seen := make(map[string]struct{}, len(m.Parts))
	for _, part := range m.Parts {
		switch part.Name {
		case PartDatabase, PartSettings, PartMedia, PartSearch, PartSearchKey:
			if _, exists := seen[part.Name]; exists {
				return fmt.Errorf("the archive declares more than one %q part; each of database, settings, media, search-index and search-master-key may appear at most once", part.Name)
			}
			seen[part.Name] = struct{}{}
		}
	}

	return nil
}

// Entry 是一个要写进归档的数据源。
type Entry struct {
	// Name 是部分名，见 Part.Name。
	Name string
	// Source 是磁盘上的文件或目录路径。
	Source string
	// Target 是它在归档内的路径，必须以 DataPrefix 开头。
	//
	// 由调用方显式给出而不是从 Source 推：备份端与还原端必须对同一个位置达成
	// 一致，而「推导规则」是两份代码里最容易悄悄漂移的东西。
	Target string
	// Hash 为真时在清单里记录 sha256（只对单个文件有意义）。
	Hash bool
}

// scanItem 是一次预扫描的结果：清单里的那一项，加上 stat 出来的信息
// （后者留着给写入阶段用，免得再 stat 一次、多一个 TOCTOU 窗口）。
type scanItem struct {
	entry Entry
	part  Part
	info  fs.FileInfo
}

// Create 把 entries 写成一个 tar.gz。
//
// 顺序是刻意的：**先扫描全部数据源算出清单，再写**。tar 是单遍流，清单占着
// 第一个条目，所以它的内容必须先确定下来。扫描只读元数据（目录是 WalkDir 计数），
// 唯一的例外是 Hash 打开的单文件——那需要完整读一遍，但只有几百 KB 的
// blog.db 与 settings.json 会走这条路。
//
// 任何一步失败都不会留下「看上去完整」的归档：要么调用方拿到错误，要么 tar 与
// gzip 的收尾（它们各自写 trailer）没跑完，解包端一定读不下去。
func Create(w io.Writer, entries []Entry, builtBy BuiltBy, createdAt time.Time) (*Manifest, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("nothing to back up")
	}

	manifest := &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		CreatedAt:     createdAt.UTC(),
		BuiltBy:       builtBy,
	}

	items := make([]scanItem, 0, len(entries))
	for _, entry := range entries {
		part, info, err := scanEntry(entry)
		if err != nil {
			return nil, err
		}
		items = append(items, scanItem{entry: entry, part: part, info: info})
		manifest.Parts = append(manifest.Parts, part)
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	if err := writeManifestEntry(tw, manifest); err != nil {
		return nil, err
	}
	for _, item := range items {
		if err := writeSource(tw, item); err != nil {
			return nil, err
		}
	}

	// 两级收尾都要走到：tar 写它的块尾，gzip 写它的 CRC 与长度。
	// 漏掉任一层的直接后果是归档损坏，而它看起来一切正常。
	if err := closeTarWriter(tw); err != nil {
		return nil, fmt.Errorf("finish the archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("finish the archive: %w", err)
	}
	return manifest, nil
}

// scanEntry 把一份数据源读成清单里的一项。
func scanEntry(entry Entry) (Part, fs.FileInfo, error) {
	// 目标路径先过一遍**与解包端完全相同**的校验。放在这里是为了在写出任何
	// 字节之前失败——否则会留下半卷归档，而它看上去是完整的。
	if _, err := safeEntryPath(entry.Target); err != nil {
		return Part{}, nil, fmt.Errorf("backup part %s: %w", entry.Name, err)
	}

	info, err := os.Stat(entry.Source)
	if err != nil {
		return Part{}, nil, fmt.Errorf("backup %s: %w", entry.Source, err)
	}

	part := Part{Name: entry.Name, Path: entry.Target}

	if info.IsDir() {
		files, bytes, err := treeTotals(entry.Source)
		if err != nil {
			return Part{}, nil, fmt.Errorf("backup %s: %w", entry.Source, err)
		}
		part.Files, part.Bytes = files, bytes
		return part, info, nil
	}

	if !info.Mode().IsRegular() {
		return Part{}, nil, fmt.Errorf("backup %s: not a regular file", entry.Source)
	}

	part.Bytes = info.Size()
	if entry.Hash {
		sum, err := hashFileFn(entry.Source)
		if err != nil {
			return Part{}, nil, err
		}
		part.SHA256 = sum
	}
	return part, info, nil
}

// writeSource 把一份数据源写进归档。
func writeSource(tw *tar.Writer, item scanItem) error {
	if !item.info.IsDir() {
		return writeFileEntry(tw, item.entry.Target, item.entry.Source, item.info)
	}
	return writeTree(tw, item.entry.Target, item.entry.Source)
}

// writeTree 写一个目录，连同它的目录结构。
//
// 符号链接、命名管道、设备文件一律跳过，且不报错：它们备份出来没有意义
// （链接可能指向这台机器之外），还原时更是一类额外的安全问题
// （解包端只接受常规文件与目录，所以它们无论如何也回不去）。
func writeTree(tw *tar.Writer, target, srcDir string) error {
	return walkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("read %s: %w", path, walkErr)
		}

		rel, _ := filepath.Rel(srcDir, path)
		name := target
		if rel != "." {
			name = target + "/" + filepath.ToSlash(rel)
		}

		info, err := dirEntryInfo(d)
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}

		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     name + "/",
				Mode:     0o700,
				ModTime:  info.ModTime(),
			})
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return writeFileEntry(tw, name, path, info)
	})
}

// writeFileEntry 写一个常规文件。
//
// 权限固定 0600 而**不照抄源文件**：归档内容含凭据，还原出来的文件不该继承
// 源目录里那些 0644。ModTime 反而要照抄，它在排障时有用。
func writeFileEntry(tw *tar.Writer, name, src string, info fs.FileInfo) error {
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Size:     info.Size(),
		Mode:     0o600,
		ModTime:  info.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("archive %s: %w", src, err)
	}

	file, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer file.Close()

	// 源文件在扫描之后被改写时，这里会写出与 Size 不符的字节数，tar 会立刻
	// 报错（写多了 "write too long"，写少了收尾时 "missed writing"）。
	// 与其静默产出一份内容错位的归档，不如让备份失败。
	if _, err := io.Copy(tw, file); err != nil {
		return fmt.Errorf("archive %s: %w", src, err)
	}
	return nil
}

// writeManifestEntry 把清单写成归档的第一个条目。
func writeManifestEntry(tw *tar.Writer, manifest *Manifest) error {
	data, _ := json.MarshalIndent(manifest, "", "  ")
	data = append(data, '\n')

	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     ManifestName,
		Size:     int64(len(data)),
		Mode:     0o600,
		ModTime:  manifest.CreatedAt,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write the manifest: %w", err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write the manifest: %w", err)
	}
	return nil
}

// treeTotals 数一个目录里的常规文件数与总字节数。
func treeTotals(root string) (files int, bytes int64, err error) {
	err = walkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := dirEntryInfo(d)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		files++
		bytes += info.Size()
		return nil
	})
	return files, bytes, err
}

// hashFile 算一个文件的 sha256（十六进制小写）。
func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
