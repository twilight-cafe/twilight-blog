package view_test

import (
	"os"
	"testing"

	"blog-engine/internal/testtool"
)

// TestMain 让这个包也能用 testtool 的假转码器。
//
// 这是必须的：本包的端到端用例会真的走 POST /api/admin/media 上传图片，而图片必须
// 被转成 WebP（硬性依赖）。假转码器就是**这个测试二进制自己**，因此子进程起来后要
// 先在这里被拦下，按环境变量扮演 cwebp。
//
// 它按 flag 解析之前拦：传进来的参数是 cwebp 的命令行，交给 testing 包会直接以
// 用法错误退出。
func TestMain(m *testing.M) {
	os.Exit(testtool.Main(m))
}
