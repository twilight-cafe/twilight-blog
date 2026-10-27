package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blog-engine/internal/model"
	"blog-engine/internal/testtool"
)

func overrideServiceHook[T any](t *testing.T, target *T, value T) {
	t.Helper()
	original := *target
	*target = value
	t.Cleanup(func() { *target = original })
}

type mediaFakeInfo struct{ mode os.FileMode }

func (f mediaFakeInfo) Name() string       { return "x" }
func (f mediaFakeInfo) Size() int64        { return 0 }
func (f mediaFakeInfo) Mode() os.FileMode  { return f.mode }
func (f mediaFakeInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (f mediaFakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f mediaFakeInfo) Sys() any           { return nil }

func TestStoreFileFilesystemFailureBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("rewind fails", func(t *testing.T) {
		rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
		overrideServiceHook(t, &seekStreamFn, func(io.Seeker, int64, int) (int64, error) {
			return 0, errors.New("seek failed")
		})
		_, err := rig.svc.StoreFile(ctx, uploadPart(t, "holiday.png", craftPNG(20, 20)))
		if err == nil || !strings.Contains(err.Error(), "rewind") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("reading the head fails", func(t *testing.T) {
		rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
		overrideServiceHook(t, &readUploadHeadFn, func(io.Reader, []byte) (int, error) {
			return 0, errors.New("connection reset")
		})
		_, err := rig.svc.StoreFile(ctx, uploadPart(t, "holiday.png", craftPNG(20, 20)))
		if err == nil || !strings.Contains(err.Error(), "read uploaded file") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("second rewind fails", func(t *testing.T) {
		// 第一次倒带（读取类型探测之后）成功，第二次（图片尺寸探测之后）失败。
		// 只让第一次失败的话，这条分支永远走不到。
		rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
		calls := 0
		overrideServiceHook(t, &seekStreamFn, func(s io.Seeker, offset int64, whence int) (int64, error) {
			calls++
			if calls == 2 {
				return 0, errors.New("seek failed after the header probe")
			}
			return s.Seek(offset, whence)
		})
		_, err := rig.svc.StoreFile(ctx, uploadPart(t, "holiday.png", craftPNG(20, 20)))
		if err == nil || !strings.Contains(err.Error(), "rewind") {
			t.Fatalf("err = %v", err)
		}
		if calls != 2 {
			t.Fatalf("expected two rewinds, saw %d", calls)
		}
	})

	t.Run("temp input cannot be opened", func(t *testing.T) {
		rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
		overrideServiceHook(t, &openFileFn, func(string, int, os.FileMode) (*os.File, error) {
			return nil, errors.New("open failed")
		})
		_, err := rig.svc.StoreFile(ctx, uploadPart(t, "holiday.png", craftPNG(20, 20)))
		if err == nil || !strings.Contains(err.Error(), "create temp input") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("copying the upload fails", func(t *testing.T) {
		rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
		overrideServiceHook(t, &copyStream, func(io.Writer, io.Reader) (int64, error) {
			return 0, errors.New("copy failed")
		})
		_, err := rig.svc.StoreFile(ctx, uploadPart(t, "holiday.png", craftPNG(20, 20)))
		if err == nil || !strings.Contains(err.Error(), "copy uploaded file") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("closing the temp input fails", func(t *testing.T) {
		rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
		overrideServiceHook(t, &closeFileFn, func(c io.Closer) error {
			_ = c.Close()
			return errors.New("close failed")
		})
		_, err := rig.svc.StoreFile(ctx, uploadPart(t, "holiday.png", craftPNG(20, 20)))
		if err == nil || !strings.Contains(err.Error(), "close temp input") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("stat of the stored file fails", func(t *testing.T) {
		rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
		overrideServiceHook(t, &statPathFn, func(string) (os.FileInfo, error) { return nil, errors.New("stat failed") })
		_, err := rig.svc.StoreFile(ctx, uploadPart(t, "song.mp3", mp3Bytes()))
		if err == nil || !strings.Contains(err.Error(), "stat stored media") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("stat of the converted file fails", func(t *testing.T) {
		rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
		overrideServiceHook(t, &statPathFn, func(string) (os.FileInfo, error) { return nil, errors.New("stat failed") })
		_, err := rig.svc.StoreFile(ctx, uploadPart(t, "holiday.png", craftPNG(20, 20)))
		if err == nil || !strings.Contains(err.Error(), "stat") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("chmod failure is only a warning", func(t *testing.T) {
		rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
		overrideServiceHook(t, &chmodPathFn, func(string, os.FileMode) error { return errors.New("chmod failed") })
		if _, err := rig.svc.StoreFile(ctx, uploadPart(t, "song.mp3", mp3Bytes())); err != nil {
			t.Fatalf("a chmod failure must not break the upload: %v", err)
		}
	})
}

// TestUpgradeToWebPReportsAnUnstattableOutput covers the gap between "the
// converter wrote a file" and "we know its size". Without the size the record
// cannot be updated honestly, so the new file must be removed and the old one
// left untouched.
func TestUpgradeToWebPReportsAnUnstattableOutput(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	original := rig.seedImage(t, "old.png", craftPNG(120, 80), "image/png")

	overrideServiceHook(t, &statPathFn, func(string) (os.FileInfo, error) {
		return nil, errors.New("stat failed")
	})
	_, err := rig.svc.UpgradeToWebP(context.Background(), original)
	if err == nil || !strings.Contains(err.Error(), "stat the converted file") {
		t.Fatalf("err = %v", err)
	}
	// 旧文件仍在，升级失败没有破坏原记录。
	if _, err := os.Stat(filepath.Join(rig.dir, "old.png")); err != nil {
		t.Fatalf("the original file is gone after a failed upgrade: %v", err)
	}
}

func TestSaveFileCloseFailure(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	if err := os.MkdirAll(rig.dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := t.TempDir() + "/src"
	if err := os.WriteFile(src, []byte("body"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	overrideServiceHook(t, &closeFileFn, func(c io.Closer) error {
		_ = c.Close()
		return errors.New("close failed")
	})
	if err := rig.svc.saveFile(src, "dst"); err == nil || !strings.Contains(err.Error(), "close stored media") {
		t.Fatalf("err = %v", err)
	}
}

func TestMediaPathRejectsSymlink(t *testing.T) {
	rig := newMediaTestRig(t, fakeConverter(t, testtool.ModeOK, "cwebp"))
	overrideServiceHook(t, &lstatPathFn, func(string) (os.FileInfo, error) {
		return mediaFakeInfo{mode: os.ModeSymlink}, nil
	})
	if _, err := rig.svc.Path(&model.Media{StorageName: "x.png"}); !errors.Is(err, ErrInvalidStorageName) {
		t.Fatalf("err = %v, want ErrInvalidStorageName", err)
	}
}
