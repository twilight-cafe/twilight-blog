package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadPasswordTwiceRejectsBadInput 覆盖密码输入的三种结局：
// 空、两次不一致、stdin 直接结束。
//
// 这一段是「忘记密码」的唯一恢复路径，所以它必须是**确定性**的：读不到、
// 读错、读空都必须给出明确错误，而不是把一个空密码写进数据库。
func TestReadPasswordTwiceRejectsBadInput(t *testing.T) {
	prompt := &strings.Builder{}

	if _, err := readPasswordTwice(strings.NewReader("\n"), prompt); err == nil {
		t.Fatal("an empty password must be refused")
	}
	if _, err := readPasswordTwice(strings.NewReader("first1\nsecond1\n"), prompt); err == nil {
		t.Fatal("two different passwords must be refused")
	}
	if _, err := readPasswordTwice(strings.NewReader(""), prompt); err == nil {
		t.Fatal("an exhausted stdin must be refused")
	}

	// 两次一致（带 CR 的 Windows 风格行尾）必须被接受。
	if _, err := readPasswordTwice(strings.NewReader("password1\r\npassword1\r\n"), prompt); err != nil {
		t.Fatalf("matching passwords must be accepted: %v", err)
	}
}

// TestReadPasswordLineReportsReadFailures 覆盖读取本身出错的那条路。
func TestReadPasswordLineReportsReadFailures(t *testing.T) {
	if _, err := readPasswordLine(bufio.NewReader(failingReader{}), &strings.Builder{}, "New password: "); err == nil {
		t.Fatal("a failing reader must be reported")
	}
}

// failingReader 永远返回错误，用来制造「stdin 读不动」这种处境。
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("stdin is broken") }

// TestAdminSetPasswordReportsBrokenEnvironment 覆盖 CLI 的三条环境性失败：
// 配置读不出来、数据库打不开、日志写不了。
func TestAdminSetPasswordReportsBrokenEnvironment(t *testing.T) {
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	// settings.json 不是合法 JSON → 「load settings」失败。
	if err := os.WriteFile("settings.json", []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	restoreStdin := swapStdin(strings.NewReader("password1\npassword1\n"))
	defer restoreStdin()
	if code := runAdminCommand([]string{"set-password"}); code == 0 {
		t.Fatal("a broken configuration must fail")
	}

	// 配置文件正常，但 blog.db 是个目录 → 打开数据库失败。
	if err := os.WriteFile("settings.json", []byte("{}"), 0o600); err != nil {
		t.Fatalf("rewrite settings.json: %v", err)
	}
	if err := os.Mkdir("blog.db", 0o755); err != nil {
		t.Fatalf("create a blocking directory: %v", err)
	}
	if code := runAdminCommand([]string{"set-password"}); code == 0 {
		t.Fatal("an unopenable database must fail")
	}
}

// TestAdminSetPasswordRejectsWeakAndMismatchedPasswords 覆盖输入侧的两条拒绝。
func TestAdminSetPasswordRejectsWeakAndMismatchedPasswords(t *testing.T) {
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	restore := swapStdin(strings.NewReader("weak\nweak\n"))
	if code := runAdminCommand([]string{"set-password"}); code != exitUsageOrValidation {
		t.Fatalf("exit code = %d, want %d", code, exitUsageOrValidation)
	}
	restore()

	restore = swapStdin(strings.NewReader("password1\npassword2\n"))
	if code := runAdminCommand([]string{"set-password"}); code != exitUsageOrValidation {
		t.Fatalf("exit code = %d, want %d", code, exitUsageOrValidation)
	}
	restore()
}

// swapStdin 把 CLI 的输入源换掉，并在返回的函数里还原。
func swapStdin(reader io.Reader) func() {
	original := stdin
	stdin = func() io.Reader { return reader }
	return func() { stdin = original }
}

// TestEmailCommandRejectsBadTargets 覆盖 email 子命令的参数校验。
func TestEmailCommandRejectsBadTargets(t *testing.T) {
	if err := runEmailCommand(nil); err == nil {
		t.Fatal("a bare email command must be refused")
	}
	if err := runEmailCommand([]string{"nonsense"}); err == nil {
		t.Fatal("an unknown email subcommand must be refused")
	}
	if err := runEmailTest([]string{"-target", "not-an-address"}); err == nil {
		t.Fatal("a malformed address must be refused")
	}
	if err := runEmailTest(nil); err == nil {
		t.Fatal("a missing target must be refused")
	}
}

// TestEmailCommandReportsAMissingConfiguration 覆盖「站点还没装好」这条路径。
func TestEmailCommandReportsAMissingConfiguration(t *testing.T) {
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	// settings.json 不存在 → 站点未安装。
	if err := runEmailTest([]string{"-target", "owner@example.com"}); err == nil {
		t.Fatal("a missing configuration must be refused")
	}

	// 配置存在但不是合法 JSON → 另一种失败。
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	if err := runEmailTest([]string{"-target", "owner@example.com"}); err == nil {
		t.Fatal("a broken configuration must be refused")
	}
}
