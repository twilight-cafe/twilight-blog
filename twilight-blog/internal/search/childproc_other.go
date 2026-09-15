//go:build !linux

package search

import (
	"os"
	"os/exec"
	"time"
)

// childShutdownGrace 是等待子进程优雅退出的时长。
//
// 非 Linux 平台上 requestChildShutdown 退化为直接 Kill（见下），这个常量
// 因此不参与等待，保留它是为了让 Stop 里的代码在两种构建下保持同一形状。
const childShutdownGrace = 3 * time.Second

// configureChildProcess 在非 Linux 平台上不做任何事。
//
// 这里没有对应的能力可映射：
//   - SysProcAttr.Pdeathsig 只存在于 Linux（darwin/BSD 没有这个字段，
//     写了会编译失败）；
//   - Windows 没有进程组/Pdeathsig 的概念，Go 也没暴露 Job Object。
//
// 也就是说：**父进程被 SIGKILL 时，孤儿 Meilisearch 的防护只在 Linux 上有**。
// 这是有意的取舍——目标部署是 Linux，为其余平台引入平台特有代码收益很低。
func configureChildProcess(cmd *exec.Cmd) {}

// requestChildShutdown 在非 Linux 平台上直接强杀。
//
// Windows 不支持向进程发送 SIGTERM（Process.Signal 对任何信号都返回
// "not supported by windows"），所以在那里「优雅关闭」无从谈起，直接 Kill
// 与旧行为一致。
func requestChildShutdown(process *os.Process) error {
	return process.Kill()
}

// killChildProcessGroup 在非 Linux 平台退化为单进程 Kill（没有进程组可用）。
func killChildProcessGroup(process *os.Process) error {
	return process.Kill()
}
