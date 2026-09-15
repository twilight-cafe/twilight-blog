package logging

import (
	"bytes"
	stdlog "log"
	"strings"
	"testing"
	"unicode/utf8"
)

// 这一组测试钉住 logging 包的安全不变量：**每条日志恰好一行且不含控制字符**。
//
// 背景（第三轮渗透测试 R3-01）：大量调用点把请求数据直接拼进日志，而
// c.Request().URL.Path 是百分号**解码后**的路径——/x%0A[FATAL]%20... 会带着
// 真实换行进入日志。未认证的攻击者因此能写出与真实日志逐字节同构的伪造行。
// 一旦有人「为了日志更整齐」而放宽这里的转义，这个洞会立刻回来。

func TestSanitizeLogLineFlattensNewlines(t *testing.T) {
	// 这正是攻击者能构造出来的形态：伪造一整条带时间戳的 [FATAL] 行。
	injected := "/anything\n2026/09/13 12:00:00 [FATAL] FORGED remote=192.0.2.77"
	got := sanitizeLogLine(injected)

	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("sanitizeLogLine left a raw newline in %q", got)
	}
	// 反斜杠转义后必须仍能看出有人试图注入，而不是被静默吞掉。
	if !strings.Contains(got, `\n2026/09/13 12:00:00 [FATAL] FORGED`) {
		t.Fatalf("sanitizeLogLine did not preserve the attempted payload as text: %q", got)
	}
	if strings.Count(got, "\n") != 0 {
		t.Fatalf("output is not a single line: %q", got)
	}
}

func TestSanitizeLogLineEscapesControlCharacters(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"carriage return", "a\rb", `a\rb`},
		{"tab", "a\tb", `a\tb`},
		{"NUL", "a\x00b", `a\x00b`},
		{"escape", "a\x1bb", `a\x1bb`},
		{"DEL", "a\x7fb", `a\x7fb`},
		{"CRLF injection", "x\r\n[WARN] forged", `x\r\n[WARN] forged`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeLogLine(tc.in); got != tc.want {
				t.Fatalf("sanitizeLogLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// 可打印内容（含中文）必须逐字保留——脱敏不该变成篡改。
func TestSanitizeLogLinePreservesPrintableText(t *testing.T) {
	in := `[auth] login failed remote=127.0.0.1 path=/api/auth/login 用户名或密码错误`
	if got := sanitizeLogLine(in); got != in {
		t.Fatalf("sanitizeLogLine altered printable text:\n got %q\nwant %q", got, in)
	}
}

func TestSanitizeLogLineTruncatesOnRuneBoundary(t *testing.T) {
	// 构造一个必然超限、且边界落在多字节字符中间的输入。
	in := strings.Repeat("中", maxLogLineBytes)
	got := sanitizeLogLine(in)

	if len(got) <= maxLogLineBytes {
		t.Fatalf("expected a truncation marker, got %d bytes", len(got))
	}
	if !strings.Contains(got, "[truncated ") {
		t.Fatalf("missing truncation marker: %q", got[len(got)-40:])
	}
	body := got[:strings.Index(got, "...[truncated ")]
	if !utf8.ValidString(body) {
		t.Fatal("truncation split a multi-byte rune; the log line is no longer valid UTF-8")
	}
	if len(body) > maxLogLineBytes {
		t.Fatalf("truncated body is %d bytes, over the %d cap", len(body), maxLogLineBytes)
	}
}

func TestSanitizeLogLineEmptyStaysEmpty(t *testing.T) {
	if got := sanitizeLogLine(""); got != "" {
		t.Fatalf("sanitizeLogLine(\"\") = %q, want empty", got)
	}
}

// 端到端：经过 logf 真正写出来的东西必须是单行。
// 这里把标准库 log 的输出端换成缓冲区，因此不需要碰 log.txt。
func TestLogfWritesExactlyOneLine(t *testing.T) {
	previous := stdlog.Writer()
	var buf bytes.Buffer
	stdlog.SetOutput(&buf)
	defer func() {
		stdlog.SetOutput(previous)
	}()

	Warnf("[security] rejected cross-origin request path=%s", "/x\n[FATAL] FORGED-ADMIN-LOGIN")

	out := buf.String()
	if out == "" {
		t.Fatal("Warnf produced no output at all")
	}
	// 标准库会给每条记录补一个结尾换行，所以恰好一行时换行数应为 1。
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("expected exactly 1 newline (the terminator), got %d in %q", n, out)
	}
	if !strings.HasPrefix(out, "[WARN] ") && !strings.Contains(out, "[WARN] ") {
		t.Fatalf("level label missing from %q", out)
	}
	if !strings.Contains(out, `\n[FATAL] FORGED-ADMIN-LOGIN`) {
		t.Fatalf("the injected payload was not neutralised: %q", out)
	}
}
