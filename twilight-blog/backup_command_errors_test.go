package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"

	"blog-engine/internal/backup"
)

func TestBackupCommandFailureBranches(t *testing.T) {
	t.Run("logging", func(t *testing.T) {
		overrideCommandHook(t, &configureBackupLogging, func(string) error { return errors.New("no log") })
		useTempInstance(t)
		feedStdin(t, "")
		if code := runBackupCommand(nil); code != exitFailure {
			t.Fatalf("exit code = %d", code)
		}
	})

	t.Run("creating the archive fails", func(t *testing.T) {
		dir := useTempInstance(t)
		seedSite(t, map[string]string{"a.png": "x"})
		overrideCommandHook(t, &backupCreateFn, func(io.Writer, []backup.Entry, backup.BuiltBy, time.Time) (*backup.Manifest, error) {
			return nil, errors.New("archive failed")
		})
		output := filepath.Join(dir, "out.tar.gz")
		if code := runBackupCommand([]string{"-o", output, "--quiet"}); code != exitFailure {
			t.Fatalf("exit code = %d", code)
		}
	})

	t.Run("closing the archive fails", func(t *testing.T) {
		dir := useTempInstance(t)
		seedSite(t, map[string]string{"a.png": "x"})
		overrideCommandHook(t, &closeArchiveTemp, func(f *os.File) error {
			_ = f.Close()
			return errors.New("close failed")
		})
		output := filepath.Join(dir, "out.tar.gz")
		if code := runBackupCommand([]string{"-o", output, "--quiet"}); code != exitFailure {
			t.Fatalf("exit code = %d", code)
		}
	})

	t.Run("hashing failure is only a warning", func(t *testing.T) {
		dir := useTempInstance(t)
		seedSite(t, map[string]string{"a.png": "x"})
		overrideCommandHook(t, &hashArchiveFn, func(string) (string, error) {
			return "", errors.New("hash failed")
		})
		output := filepath.Join(dir, "out.tar.gz")
		if code := runBackupCommand([]string{"-o", output, "--quiet"}); code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	})
}

func TestCheckOutputTargetIgnoresUnresolvablePaths(t *testing.T) {
	overrideCommandHook(t, &absPathForBackup, func(string) (string, error) { return "", errors.New("abs failed") })
	if err := checkOutputTarget("out.tar.gz", "media"); err != nil {
		t.Fatalf("an unresolvable path must be deferred: %v", err)
	}
}

// TestCheckOutputTargetIgnoresAnUnresolvableMediaDir covers the second
// normalisation: the output path resolves but the media dir does not. The guard
// then has nothing to compare and must defer to the write step.
func TestCheckOutputTargetIgnoresAnUnresolvableMediaDir(t *testing.T) {
	calls := 0
	overrideCommandHook(t, &absPathForBackup, func(path string) (string, error) {
		calls++
		if calls == 1 {
			return filepath.Abs(path)
		}
		return "", errors.New("cannot resolve the media dir")
	})
	if err := checkOutputTarget("out.tar.gz", "media"); err != nil {
		t.Fatalf("an unresolvable media dir must not block the backup: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected both paths to be resolved, got %d call(s)", calls)
	}
}

func TestBuildRevisionBranches(t *testing.T) {
	overrideCommandHook(t, &readBuildInfoFn, func() (*debug.BuildInfo, bool) { return nil, false })
	if got := buildRevision(); got != "" {
		t.Fatalf("buildRevision = %q, want empty", got)
	}

	overrideCommandHook(t, &readBuildInfoFn, func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}}}, true
	})
	if got := buildRevision(); got != "abc123" {
		t.Fatalf("buildRevision = %q", got)
	}

	overrideCommandHook(t, &readBuildInfoFn, func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.time", Value: "yesterday"}}}, true
	})
	if got := buildRevision(); got != "" {
		t.Fatalf("buildRevision = %q, want empty", got)
	}
}
