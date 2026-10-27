package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type swapFakeEntry struct {
	name string
	mode fs.FileMode
}

func (e swapFakeEntry) Name() string               { return e.name }
func (e swapFakeEntry) IsDir() bool                { return e.mode.IsDir() }
func (e swapFakeEntry) Type() fs.FileMode          { return e.mode.Type() }
func (e swapFakeEntry) Info() (fs.FileInfo, error) { return swapFakeInfo{mode: e.mode}, nil }

type swapFakeInfo struct{ mode fs.FileMode }

func (f swapFakeInfo) Name() string       { return "entry" }
func (f swapFakeInfo) Size() int64        { return 0 }
func (f swapFakeInfo) Mode() fs.FileMode  { return f.mode }
func (f swapFakeInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (f swapFakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f swapFakeInfo) Sys() any           { return nil }

func TestSwapInRollbackBranches(t *testing.T) {
	newTree := func(t *testing.T) (src, dst string) {
		root := t.TempDir()
		src = filepath.Join(root, "src")
		dst = filepath.Join(root, "dst")
		for _, dir := range []string{src, dst} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}
		return src, dst
	}

	t.Run("placement and rollback both fail", func(t *testing.T) {
		src, dst := newTree(t)
		overrideCommandHook(t, &placeFn, func(string, string) error { return errors.New("place failed") })
		overrideCommandHook(t, &renamePath, func(oldpath, newpath string) error {
			if oldpath == dst {
				return os.Rename(oldpath, newpath)
			}
			return errors.New("rollback failed")
		})
		if _, err := swapIn(src, dst, false); err == nil {
			t.Fatal("a failed rollback must surface")
		}
	})

	t.Run("placement fails but rollback works", func(t *testing.T) {
		src, dst := newTree(t)
		overrideCommandHook(t, &placeFn, func(string, string) error { return errors.New("place failed") })
		if _, err := swapIn(src, dst, false); err == nil {
			t.Fatal("a failed placement must surface")
		}
	})

	t.Run("removing the old copy fails", func(t *testing.T) {
		src, dst := newTree(t)
		overrideCommandHook(t, &removeAllPath, func(string) error { return errors.New("remove failed") })
		kept, err := swapIn(src, dst, false)
		if err != nil || kept == "" {
			t.Fatalf("swapIn = (%q, %v)", kept, err)
		}
	})
}

func TestCopyTreeReadDirFailure(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	overrideCommandHook(t, &readDirPath, func(string) ([]os.DirEntry, error) {
		return nil, errors.New("readdir failed")
	})
	if err := copyTree(src, filepath.Join(root, "dst")); err == nil {
		t.Fatal("a failed ReadDir must surface")
	}
}

func TestCopyTreeSkipsNonRegularEntries(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "regular.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	overrideCommandHook(t, &readDirPath, func(string) ([]os.DirEntry, error) {
		return []os.DirEntry{
			swapFakeEntry{name: "regular.txt", mode: 0},
			swapFakeEntry{name: "link", mode: fs.ModeSymlink},
		}, nil
	})
	if err := copyTree(src, filepath.Join(root, "dst")); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "dst", "regular.txt")); err != nil {
		t.Fatalf("the regular file must be copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "dst", "link")); !os.IsNotExist(err) {
		t.Fatalf("the symlink must be skipped: %v", err)
	}
}

func TestCopyFileCloseFailure(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.WriteFile(src, []byte("body"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	overrideCommandHook(t, &closeSwapFile, func(f *os.File) error {
		_ = f.Close()
		return errors.New("close failed")
	})
	if err := copyFile(src, filepath.Join(root, "dst"), 0o600); err == nil {
		t.Fatal("a close failure must surface")
	}
}

func TestSamePathAbsFailure(t *testing.T) {
	overrideCommandHook(t, &absPathForSwap, func(string) (string, error) { return "", errors.New("abs failed") })
	if samePath("a", "b") {
		t.Fatal("unresolvable paths must not be considered equal")
	}
}
