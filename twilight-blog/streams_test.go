package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// captureStreams 把命令看到的三个流换成缓冲区，返回 stdout 与 stderr 的句柄。
//
// stdin / stdout / stderr 是包级变量（见 admin_command.go），替换它们比给每个命令
// 都加一个 io.Writer 参数要轻——而且这三个流本来就是全局的，测试里替换它们与真实
// 调用是同一种语义（真实调用里它们就是指进程自己的三个流）。
//
// 替换是全局的，所以这个包里的测试不能并行；t.Cleanup 保证每条用例结束后恢复。
func captureStreams(t *testing.T, input string) (out, errOut *bytes.Buffer) {
	t.Helper()

	out = &bytes.Buffer{}
	errOut = &bytes.Buffer{}

	previousOut, previousErr, previousIn := stdout, stderr, stdin
	stdout = func() io.Writer { return out }
	stderr = func() io.Writer { return errOut }
	stdin = func() io.Reader { return strings.NewReader(input) }

	t.Cleanup(func() {
		stdout, stderr, stdin = previousOut, previousErr, previousIn
	})
	return out, errOut
}
