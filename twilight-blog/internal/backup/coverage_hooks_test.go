package backup

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateReportsTarCloseError(t *testing.T) {
	src := filepath.Join(t.TempDir(), "blog.db")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	original := closeTarWriter
	closeTarWriter = func(*tar.Writer) error { return errors.New("tar close failed") }
	t.Cleanup(func() { closeTarWriter = original })

	var buf bytes.Buffer
	if _, err := Create(&buf, []Entry{{
		Name:   PartDatabase,
		Source: src,
		Target: DataPrefix + "blog.db",
	}}, BuiltBy{}, fixedTime()); err == nil {
		t.Fatal("Create must report a tar.Close error")
	}
}

func TestScanEntryReportsTreeTotalsError(t *testing.T) {
	dir := t.TempDir()
	seedTree(t, dir, map[string]string{"a.txt": "a"})

	original := walkDir
	walkDir = func(string, fs.WalkDirFunc) error { return errors.New("walk failed") }
	t.Cleanup(func() { walkDir = original })

	if _, _, err := scanEntry(Entry{
		Name:   PartMedia,
		Source: dir,
		Target: DataPrefix + "media",
	}); err == nil {
		t.Fatal("scanEntry must report a treeTotals error")
	}
}

func TestScanEntryReportsHashFileError(t *testing.T) {
	src := filepath.Join(t.TempDir(), "blog.db")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	original := hashFileFn
	hashFileFn = func(string) (string, error) { return "", errors.New("hash failed") }
	t.Cleanup(func() { hashFileFn = original })

	if _, _, err := scanEntry(Entry{
		Name:   PartDatabase,
		Source: src,
		Target: DataPrefix + "blog.db",
		Hash:   true,
	}); err == nil {
		t.Fatal("scanEntry must report a hashFile error")
	}
}

func TestWriteTreeAndTreeTotalsSkipNonRegularEntries(t *testing.T) {
	dir := t.TempDir()
	seedTree(t, dir, map[string]string{"a.txt": "a"})
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}

	// 用 walkDir 钩子注入一个符号链接条目，而不是真的创建 symlink：
	// Windows 默认不允许普通用户创建符号链接（需要 SeCreateSymbolicLinkPrivilege），
	// 真建会让整条用例在开发机上以权限错误失败，与「跳过非常规文件」的断言无关。
	originalWalk := walkDir
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		if err := fn(root, fs.FileInfoToDirEntry(backupFakeFileInfo{mode: os.ModeDir | 0o755}), nil); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := fn(filepath.Join(root, entry.Name()), entry, nil); err != nil {
				return err
			}
		}
		return fn(filepath.Join(root, "link.txt"),
			fs.FileInfoToDirEntry(backupFakeFileInfo{mode: os.ModeSymlink | 0o777}), nil)
	}
	t.Cleanup(func() { walkDir = originalWalk })

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := writeTree(tw, DataPrefix+"media", dir); err != nil {
		t.Fatalf("writeTree() error = %v", err)
	}

	files, size, err := treeTotals(dir)
	if err != nil {
		t.Fatalf("treeTotals() error = %v", err)
	}
	if files != 1 || size != 1 {
		t.Fatalf("treeTotals() = (%d, %d), want (1, 1)", files, size)
	}
}

// backupFakeFileInfo 只实现 walkDir 回调真正读取的字段。
type backupFakeFileInfo struct{ mode os.FileMode }

func (f backupFakeFileInfo) Name() string       { return "entry" }
func (f backupFakeFileInfo) Size() int64        { return 0 }
func (f backupFakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f backupFakeFileInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (f backupFakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f backupFakeFileInfo) Sys() any           { return nil }

func TestWriteTreeAndTreeTotalsReportInfoErrors(t *testing.T) {
	dir := t.TempDir()
	seedTree(t, dir, map[string]string{"a.txt": "a"})

	original := dirEntryInfo
	dirEntryInfo = func(fs.DirEntry) (fs.FileInfo, error) { return nil, errors.New("info failed") }
	t.Cleanup(func() { dirEntryInfo = original })

	if err := writeTree(tar.NewWriter(io.Discard), DataPrefix+"media", dir); err == nil {
		t.Fatal("writeTree must report a DirEntry.Info error")
	}
	if _, _, err := treeTotals(dir); err == nil {
		t.Fatal("treeTotals must report a DirEntry.Info error")
	}
}

func TestExtractFileReportsCloseError(t *testing.T) {
	original := extractFileClose
	extractFileClose = func(f *os.File) error {
		_ = f.Close()
		return errors.New("close failed")
	}
	t.Cleanup(func() { extractFileClose = original })

	if _, err := extractFile(filepath.Join(t.TempDir(), "out.bin"), strings.NewReader("body")); err == nil {
		t.Fatal("extractFile must report a Close error")
	}
}

func TestPrepareRootReportsMkdirAndReadDirErrors(t *testing.T) {
	originalMkdir := mkdirAll
	mkdirAll = func(string, os.FileMode) error { return errors.New("mkdir failed") }
	if err := prepareRoot(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("prepareRoot must report a MkdirAll error")
	}
	mkdirAll = originalMkdir

	originalReadDir := readDir
	readDir = func(string) ([]os.DirEntry, error) { return nil, errors.New("readdir failed") }
	t.Cleanup(func() { readDir = originalReadDir })
	if err := prepareRoot(t.TempDir()); err == nil {
		t.Fatal("prepareRoot must report a ReadDir error")
	}
}
