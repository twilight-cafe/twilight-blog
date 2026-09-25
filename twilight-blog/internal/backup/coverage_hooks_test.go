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
	if err := os.Symlink("a.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

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
