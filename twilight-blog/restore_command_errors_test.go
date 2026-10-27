package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blog-engine/internal/backup"
)

// These tests pin the failure paths of the restore sequence. Each one answers a
// different operational question: what does the operator see when the host is
// out of temp space, when the staged settings file is unreadable, when a part
// cannot be moved into place. They are kept apart from the behavioural tests in
// restore_command_test.go so that a failure points at one cause instead of at
// "restore broke".

// overrideRestoreHooks swaps one restore hook for the duration of a subtest.
func overrideRestoreHooks(t *testing.T) {
	t.Helper()
	previous := struct {
		logging  func(string) error
		mkdir    func(string, string) (string, error)
		staged   func(string, backup.Manifest) (string, error)
		read     func(string) (map[string]any, error)
		setMedia func(string, string) error
		relative func(string) (string, error)
		swap     func(string, string, bool) (string, error)
		sidecars func(string) error
	}{
		configureRestoreLogging, mkdirTempRestore, stagedSettingsPathFn,
		readRestoreSettingsMap, setSettingsMediaDirFn, relativePathRestore,
		swapInRestoreFn, removeSidecarsFn,
	}
	t.Cleanup(func() {
		configureRestoreLogging = previous.logging
		mkdirTempRestore = previous.mkdir
		stagedSettingsPathFn = previous.staged
		readRestoreSettingsMap = previous.read
		setSettingsMediaDirFn = previous.setMedia
		relativePathRestore = previous.relative
		swapInRestoreFn = previous.swap
		removeSidecarsFn = previous.sidecars
	})
}

// restoreManifestWith builds a manifest whose parts point at the usual data/
// prefix. Only the fields applyRestore reads are populated.
func restoreManifestWith(parts ...backup.Part) backup.Manifest {
	return backup.Manifest{Format: backup.FormatName, FormatVersion: 1, Parts: parts}
}

// TestApplyRestoreRefusesToSwapAnUnknownPart covers the default arm of the part
// switch: an archive written by a future build names a part this one does not
// know, and silently ignoring it would restore a site with data missing.
func TestApplyRestoreRefusesToSwapAnUnknownPart(t *testing.T) {
	inTempWorkdir(t)
	captureStreams(t, "")

	err := applyRestore(t.TempDir(), restoreManifestWith(
		backup.Part{Name: "telemetry", Path: "data/telemetry"},
	), restoreOptions{mediaDir: "media", keepSessions: true})
	if err == nil {
		t.Fatal("an unknown part must be refused")
	}
	if !strings.Contains(err.Error(), "does not understand") {
		t.Fatalf("error should name the unknown part: %v", err)
	}
}

// TestApplyRestoreReportsAnEmptyArchive covers the "nothing to restore" guard.
// A manifest with only a logs part passes the switch but leaves restored at
// zero; succeeding there would tell the operator a restore happened when the
// database was never replaced.
func TestApplyRestoreReportsAnEmptyArchive(t *testing.T) {
	inTempWorkdir(t)
	captureStreams(t, "")

	err := applyRestore(t.TempDir(), restoreManifestWith(
		backup.Part{Name: backup.PartLogs, Path: "data/logs"},
	), restoreOptions{mediaDir: "media", keepSessions: true})
	if err == nil {
		t.Fatal("an archive with no restorable part must be refused")
	}
	if !strings.Contains(err.Error(), "nothing this build knows how to restore") {
		t.Fatalf("error should explain that nothing was restored: %v", err)
	}
}

// TestApplyRestoreStopsWhenStagingTheSettingsPathFails covers the path lookup
// before anything touches the working directory.
func TestApplyRestoreStopsWhenStagingTheSettingsPathFails(t *testing.T) {
	inTempWorkdir(t)
	overrideRestoreHooks(t)
	captureStreams(t, "")

	stagedSettingsPathFn = func(string, backup.Manifest) (string, error) {
		return "", errors.New("staged settings path is unusable")
	}
	err := applyRestore(t.TempDir(), backup.Manifest{}, restoreOptions{mediaDir: "media"})
	if err == nil || !strings.Contains(err.Error(), "unusable") {
		t.Fatalf("the staging lookup error should surface unchanged, got %v", err)
	}
}

// TestApplyRestoreStopsWhenLocalSettingsCannotBeRead covers the host side of the
// archived-settings comparison. Without the local values there is nothing to
// compare against, so the restore must stop rather than accept the archive.
func TestApplyRestoreStopsWhenLocalSettingsCannotBeRead(t *testing.T) {
	inTempWorkdir(t)
	overrideRestoreHooks(t)
	captureStreams(t, "")

	readRestoreSettingsMap = func(path string) (map[string]any, error) {
		return nil, errors.New("cannot read the host's settings: " + path)
	}
	err := applyRestore(t.TempDir(), restoreManifestWith(
		backup.Part{Name: backup.PartSettings, Path: "data/settings.json"},
	), restoreOptions{mediaDir: "media", keepSessions: true})
	if err == nil || !strings.Contains(err.Error(), "host's settings") {
		t.Fatalf("a failure to read the local settings must abort, got %v", err)
	}
}

// TestApplyRestoreStopsWhenArchivedSettingsCannotBeRead covers the archive side.
// The first hook call (the host) succeeds, the second (the staged file) fails:
// the distinction matters because the two files have different trust levels.
func TestApplyRestoreStopsWhenArchivedSettingsCannotBeRead(t *testing.T) {
	inTempWorkdir(t)
	overrideRestoreHooks(t)
	captureStreams(t, "")

	staged := filepath.Join(t.TempDir(), "data", "settings.json")
	calls := 0
	readRestoreSettingsMap = func(path string) (map[string]any, error) {
		calls++
		if calls == 1 {
			return map[string]any{"media_dir": "media"}, nil
		}
		return nil, errors.New("the archived settings are unreadable")
	}
	err := applyRestore(filepath.Dir(filepath.Dir(staged)), restoreManifestWith(
		backup.Part{Name: backup.PartSettings, Path: "data/settings.json"},
	), restoreOptions{mediaDir: "media", keepSessions: true})
	if err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("a failure to read the archived settings must abort, got %v", err)
	}
}

// TestApplyRestoreStopsWhenRewritingTheMediaDirFails covers the rewrite of the
// staged settings.json. It is only reached when the archive and this host
// disagree about where media lives, which is exactly the case worth pinning.
func TestApplyRestoreStopsWhenRewritingTheMediaDirFails(t *testing.T) {
	inTempWorkdir(t)
	overrideRestoreHooks(t)
	captureStreams(t, "")

	// RelativePath 会剥掉归档里的 data/ 前缀，所以暂存目录里直接就是
	// settings.json —— 与 Extract 的落盘布局一致。
	staging := t.TempDir()
	staged := filepath.Join(staging, "settings.json")
	if err := os.MkdirAll(filepath.Dir(staged), 0o700); err != nil {
		t.Fatalf("prepare staged settings dir: %v", err)
	}
	if err := os.WriteFile(staged, []byte(`{"media_dir":"elsewhere"}`), 0o600); err != nil {
		t.Fatalf("prepare staged settings: %v", err)
	}
	setSettingsMediaDirFn = func(string, string) error {
		return errors.New("the staged settings are read-only")
	}

	err := applyRestore(staging, restoreManifestWith(
		backup.Part{Name: backup.PartSettings, Path: "data/settings.json"},
	), restoreOptions{mediaDir: "media", keepSessions: true})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("a failed settings rewrite must abort, got %v", err)
	}
}

// TestApplyRestoreStopsWhenAPartPathIsRejected covers the path guard.
// Every part path goes through backup.RelativePath, which is what keeps an
// archive from writing outside the staging directory.
func TestApplyRestoreStopsWhenAPartPathIsRejected(t *testing.T) {
	inTempWorkdir(t)
	overrideRestoreHooks(t)
	captureStreams(t, "")

	relativePathRestore = func(string) (string, error) {
		return "", errors.New("that path escapes the archive")
	}
	err := applyRestore(t.TempDir(), restoreManifestWith(
		backup.Part{Name: backup.PartDatabase, Path: "../../etc/passwd"},
	), restoreOptions{mediaDir: "media", keepSessions: true})
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("a rejected part path must abort, got %v", err)
	}
}

// TestApplyRestoreLabelsWhichPartFailed covers the wrapping of swap errors.
// "restore media: ..." tells the operator where to look; a bare rename error
// would leave them guessing which of four files is half-moved.
func TestApplyRestoreLabelsWhichPartFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		part backup.Part
		want string
	}{
		{"settings", backup.Part{Name: backup.PartSettings, Path: "data/settings.json"}, "settings.json"},
		{"database", backup.Part{Name: backup.PartDatabase, Path: "data/blog.db"}, "blog.db"},
		{"search key", backup.Part{Name: backup.PartSearchKey, Path: "data/search.key"}, "master.key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inTempWorkdir(t)
			overrideRestoreHooks(t)
			captureStreams(t, "")

			staging := t.TempDir()
			if tc.part.Name == backup.PartSettings {
				staged := filepath.Join(staging, "settings.json")
				if err := os.MkdirAll(filepath.Dir(staged), 0o700); err != nil {
					t.Fatalf("prepare staged settings dir: %v", err)
				}
				if err := os.WriteFile(staged, []byte(`{"media_dir":"media"}`), 0o600); err != nil {
					t.Fatalf("prepare staged settings: %v", err)
				}
			}
			swapInRestoreFn = func(string, string, bool) (string, error) {
				return "", errors.New("the staged copy cannot be moved")
			}

			err := applyRestore(staging, restoreManifestWith(tc.part),
				restoreOptions{mediaDir: "media", keepSessions: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the error should identify %s, got %v", tc.want, err)
			}
		})
	}
}

// TestPerformRestoreRejectsContradictoryOptions covers the option check running
// before the archive is unpacked: the operator should not read ten seconds of
// verification output before learning the flags cannot work together.
func TestPerformRestoreRejectsContradictoryOptions(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	captureStreams(t, "")

	err := performRestore(restoreOptions{
		archive:      "site.tar.gz",
		yes:          true,
		keepSettings: true,
		mediaDir:     "photos",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("contradictory options must be refused, got %v", err)
	}
}

// TestPerformRestoreStopsWhenStagingCannotBeCreated covers a full disk or a
// working directory that is not writable. Nothing has been changed at that
// point, and the error must say that it was the staging step.
func TestPerformRestoreStopsWhenStagingCannotBeCreated(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, nil)
	if _, err := performBackup(backupOptions{output: "site.tar.gz"}); err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	overrideRestoreHooks(t)
	captureStreams(t, "")

	mkdirTempRestore = func(string, string) (string, error) {
		return "", errors.New("no space left on device")
	}
	err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true})
	if err == nil || !strings.Contains(err.Error(), "staging directory") {
		t.Fatalf("a staging failure must be reported as such, got %v", err)
	}
	if !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("the underlying cause should be preserved: %v", err)
	}
}

// TestRunRestoreCommandReportsLoggingFailure covers the very first step: if the
// log file cannot be opened the command stops before touching any data.
func TestRunRestoreCommandReportsLoggingFailure(t *testing.T) {
	inTempWorkdir(t)
	overrideRestoreHooks(t)
	_, errOut := captureStreams(t, "")

	configureRestoreLogging = func(string) error {
		return errors.New("log file is not writable")
	}
	if code := runRestoreCommand([]string{"site.tar.gz", "--yes"}); code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "configure logging") {
		t.Fatalf("stderr should name the failing step: %q", errOut.String())
	}
}

// TestRunUpgradeMediaCommandReportsLoggingFailure covers the equivalent guard in
// upgrade-media. It also proves the logging setup happens before the settings
// file is stat'ed, so a broken log setup is not misreported as "settings.json
// not found".
func TestRunUpgradeMediaCommandReportsLoggingFailure(t *testing.T) {
	inTempWorkdir(t)
	_, errOut := captureStreams(t, "")

	previous := configureUpgradeLogging
	t.Cleanup(func() { configureUpgradeLogging = previous })
	configureUpgradeLogging = func(string) error {
		return errors.New("log file is not writable")
	}

	if code := runUpgradeMediaCommand(nil); code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "configure logging") {
		t.Fatalf("stderr should name the failing step: %q", errOut.String())
	}
	if strings.Contains(errOut.String(), "not found") {
		t.Fatalf("the missing-settings guard must not run before logging is set up: %q", errOut.String())
	}
}
