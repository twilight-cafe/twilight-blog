//go:build linux

package search

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// childShutdownGrace 是等待子进程优雅退出的时长。
//
// Meilisearch 收到 SIGTERM 后会停止接受写入、把索引刷盘、释放 LMDB 环境再退出，
// 正常在百毫秒级完成。3 秒足够覆盖偶发的慢盘，又不至于让关站变得难以忍受。
const childShutdownGrace = 3 * time.Second

// configureChildProcess 为 Meilisearch 子进程设置进程组与父进程死亡信号。
//
// 这两项都是 Linux 特有的，而且各自堵一个真实故障：
//
//   - **Pdeathsig**：父进程被 **SIGKILL** 时（OOM killer、`systemctl kill -9`、
//     容器被强杀），内核会替我们给子进程发一个 SIGTERM。没有它，Meilisearch 会
//     变成孤儿继续活着并**占住数据目录的 LMDB 锁**，而博客重启时 Meilisearch
//     起不来（或起在另一个端口上却读不到原索引），症状是「服务莫名其妙起不来」，
//     排查成本极高。注意 Pdeathsig 只在**创建它的线程**退出时触发，Go 运行时的
//     线程调度让这个语义不够严格，所以它是兜底而不是唯一防线——配合下面的
//     进程组，以及进程组信号来一起看。
//
//   - **Setpgid**：让子进程自成进程组。这样关站时若要走「整组终止」的路径，
//     不会误伤同组的其他进程；同时使子进程脱离父进程所在的前台进程组，
//     不会被终端的 Ctrl-C 连带（也不受其影响）。附带好处是子进程若自己 fork 了
//     孙进程，它们同属一组，可以被一并处理。
func configureChildProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}
}

// requestChildShutdown 请求子进程优雅退出：先 SIGTERM。
//
// 只发信号、不等待——等待由调用方 Stop 用 done 通道 + 超时来完成，
// 因为「等多久」是策略，不该埋在信号发送里。
//
// 进程已经退出时返回 os.ErrProcessDone（或其他 ESRCH 类错误），调用方按
// 「无需处理」对待。
func requestChildShutdown(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}

// killChildProcessGroup 强杀整个子进程组。
//
// 这里刻意用负 pid：SIGKILL 发给进程组而非单个进程，确保 Meilisearch 万一
// fork 出的辅助进程也一起收掉，不留孤儿。Setpgid 已在上面的
// configureChildProcess 里设好，所以子进程的 pgid 就是它自己的 pid。
func killChildProcessGroup(process *os.Process) error {
	if process.Pid <= 0 {
		return process.Kill()
	}
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil {
		// 进程组不存在（子进程早已退出）或权限不足时退回单进程 Kill。
		return process.Kill()
	}
	return nil
}
