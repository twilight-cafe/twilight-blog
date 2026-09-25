package logging

import (
	"bytes"
	"errors"
	stdlog "log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevelAcceptsAllAliasesAndRejectsUnknown(t *testing.T) {
	cases := []struct {
		in   string
		want Level
	}{
		{"", LevelInfo},
		{"dev", LevelDebug},
		{"DEBUG", LevelDebug},
		{"normal", LevelInfo},
		{"info", LevelInfo},
		{"warn", LevelWarn},
		{"WARNING", LevelWarn},
		{"error", LevelError},
		{"quiet", LevelError},
	}
	for _, tc := range cases {
		got, err := ParseLevel(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("ParseLevel(%q) = (%v, %v), want (%v, nil)", tc.in, got, err, tc.want)
		}
	}

	if _, err := ParseLevel("verbose"); err == nil {
		t.Fatal("an unknown level must be rejected rather than silently mapped")
	}
}

func TestLevelStringAndLabelCoverAllValues(t *testing.T) {
	cases := map[Level]string{
		LevelDebug: "dev",
		LevelInfo:  "normal",
		LevelWarn:  "warn",
		LevelError: "error",
		Level(99):  "normal",
	}
	for level, want := range cases {
		if got := level.String(); got != want {
			t.Fatalf("Level(%d).String() = %q, want %q", level, got, want)
		}
	}

	labels := map[Level]string{
		LevelDebug: "[DEBUG]",
		LevelInfo:  "[INFO]",
		LevelWarn:  "[WARN]",
		LevelError: "[ERROR]",
		Level(99):  "[LOG]",
	}
	for level, want := range labels {
		if got := label(level); got != want {
			t.Fatalf("label(%d) = %q, want %q", level, got, want)
		}
	}
}

func TestCurrentLevelSetLevelAndConfigure(t *testing.T) {
	original := CurrentLevel()
	t.Cleanup(func() { SetLevel(original) })

	SetLevel(LevelWarn)
	if got := CurrentLevel(); got != LevelWarn {
		t.Fatalf("CurrentLevel() = %v, want %v", got, LevelWarn)
	}
	if err := Configure("definitely-not-a-level"); err == nil {
		t.Fatal("Configure must reject an invalid level before trying to open the log file")
	}
}

func TestFatalfUsesHookAndSanitizesMessage(t *testing.T) {
	original := fatalf
	var got string
	fatalf = func(format string, args ...any) {
		got = format
		if len(args) > 0 {
			got += "|" + args[0].(string)
		}
	}
	t.Cleanup(func() { fatalf = original })

	Fatalf("fatal %s", "x\ny")
	if !strings.Contains(got, "[FATAL] fatal x\\ny") {
		t.Fatalf("Fatalf delegation = %q, want sanitized fatal message", got)
	}
}

func TestRedactEmailRejectsMalformedAddresses(t *testing.T) {
	for _, address := range []string{"", "no-at-sign", "@example.com", "user@"} {
		if got := RedactEmail(address); got != "***" {
			t.Fatalf("RedactEmail(%q) = %q, want ***", address, got)
		}
	}
	if got := RedactEmail("user@example.com"); got != "***@example.com" {
		t.Fatalf("RedactEmail valid = %q", got)
	}
}

func resetLogState(t *testing.T) {
	t.Helper()
	logMu.Lock()
	defer logMu.Unlock()
	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}
	loggedLen = 0
}

func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(old)
		resetLogState(t)
	})
	return dir
}

func TestWriteFallsBackToStderrWhenOpenFails(t *testing.T) {
	chdirTemp(t)
	resetLogState(t)

	originalOpen := openLogFile
	openLogFile = func(string, int, os.FileMode) (*os.File, error) {
		return nil, errors.New("cannot open log file")
	}
	t.Cleanup(func() { openLogFile = originalOpen })

	n, err := Writer().Write([]byte("fallback line\n"))
	if err != nil {
		t.Fatalf("stderr fallback returned an error: %v", err)
	}
	if n != len("fallback line\n") {
		t.Fatalf("stderr fallback wrote %d bytes, want %d", n, len("fallback line\n"))
	}
}

func TestOpenLockedReportsStatError(t *testing.T) {
	chdirTemp(t)
	resetLogState(t)

	originalStat := statLogFile
	statLogFile = func(*os.File) (os.FileInfo, error) {
		return nil, errors.New("stat failed")
	}
	t.Cleanup(func() { statLogFile = originalStat })

	if err := ensureLogFile(); err == nil {
		t.Fatal("ensureLogFile must report a Stat error")
	}
}

func TestRotateLockedCoversCloseRenameAndReopenErrors(t *testing.T) {
	dir := chdirTemp(t)
	resetLogState(t)

	file, err := os.OpenFile(filepath.Join(dir, "old.log"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open placeholder log: %v", err)
	}
	logFile = file
	loggedLen = 17

	originalRename := renameLogFile
	originalOpen := openLogFile
	renameLogFile = func(string, string) error { return errors.New("rename failed") }
	openLogFile = func(string, int, os.FileMode) (*os.File, error) {
		return nil, errors.New("reopen failed")
	}
	t.Cleanup(func() {
		renameLogFile = originalRename
		openLogFile = originalOpen
	})

	rotateLocked()
	if logFile != nil {
		t.Fatal("rotateLocked must leave logFile nil after a failed reopen")
	}
	if loggedLen != 0 {
		t.Fatalf("rotateLocked loggedLen = %d, want 0", loggedLen)
	}
}

func TestWriteRotatesWhenLimitReached(t *testing.T) {
	dir := chdirTemp(t)
	resetLogState(t)

	file, err := os.OpenFile(filepath.Join(dir, DefaultLogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	logFile = file
	loggedLen = DefaultMaxLogSize

	originalRename := renameLogFile
	renameLogFile = func(string, string) error { return nil }
	t.Cleanup(func() { renameLogFile = originalRename })

	if _, err := (rotatingWriter{}).Write([]byte("after rotation")); err != nil {
		t.Fatalf("Write after rotation: %v", err)
	}
}

func TestWriteReportsStderrWriteFailure(t *testing.T) {
	dir := chdirTemp(t)
	resetLogState(t)

	file, err := os.OpenFile(filepath.Join(dir, DefaultLogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	logFile = file

	brokenStderr, err := os.CreateTemp(dir, "stderr")
	if err != nil {
		t.Fatalf("create stderr: %v", err)
	}
	if err := brokenStderr.Close(); err != nil {
		t.Fatalf("close stderr: %v", err)
	}
	originalStderr := os.Stderr
	os.Stderr = brokenStderr
	t.Cleanup(func() { os.Stderr = originalStderr })

	_, err = (rotatingWriter{}).Write([]byte("file write succeeds"))
	if err == nil {
		t.Fatal("Write must surface a stderr write failure when the file write succeeded")
	}
}

func TestLogfHonoursLevelAndSanitizes(t *testing.T) {
	var buf bytes.Buffer
	originalWriter := stdlog.Writer()
	stdlog.SetOutput(&buf)
	stdlog.SetFlags(0)
	defer func() {
		stdlog.SetOutput(originalWriter)
	}()

	original := CurrentLevel()
	t.Cleanup(func() { SetLevel(original) })

	SetLevel(LevelWarn)
	Debugf("discarded")
	if buf.Len() != 0 {
		t.Fatalf("Debugf below the threshold wrote %q", buf.String())
	}
	Errorf("kept")
	if !strings.Contains(buf.String(), "[ERROR] kept") {
		t.Fatalf("Errorf output = %q", buf.String())
	}
}

func TestConfigureAcceptsValidLevelAndOpensFile(t *testing.T) {
	chdirTemp(t)
	resetLogState(t)
	original := CurrentLevel()
	t.Cleanup(func() { SetLevel(original) })

	if err := Configure("debug"); err != nil {
		t.Fatalf("Configure(debug) error = %v", err)
	}
	if got := CurrentLevel(); got != LevelDebug {
		t.Fatalf("CurrentLevel() after Configure = %v, want debug", got)
	}

	var buf bytes.Buffer
	originalWriter := stdlog.Writer()
	stdlog.SetOutput(&buf)
	stdlog.SetFlags(0)
	defer stdlog.SetOutput(originalWriter)

	Infof("configured")
	if !strings.Contains(buf.String(), "[INFO] configured") {
		t.Fatalf("Infof output = %q", buf.String())
	}
}
