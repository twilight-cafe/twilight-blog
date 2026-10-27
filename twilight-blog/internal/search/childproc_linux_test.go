//go:build linux

package search

import (
	"os"
	"os/exec"
	"testing"
)

// TestChildProcessHelpersOnLinux 覆盖 Linux 专用子进程辅助函数。
//
// 这些断言必须放在 linux 构建标签下：SysProcAttr.Setpgid 与 Pdeathsig 只在
// Linux 的 syscall 包里存在，写在通用测试文件里会让 Windows / macOS 直接
// 编译失败（而失败信息是「结构体没有这个字段」，与测试逻辑无关）。
func TestChildProcessHelpersOnLinux(t *testing.T) {
	// configureChildProcess 必须设上进程组与父进程死亡信号，否则父进程被
	// SIGKILL 之后 Meilisearch 会变成孤儿并占住数据目录的 LMDB 锁。
	cmd := exec.Command("/bin/sh", "-c", "sleep 5")
	configureChildProcess(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Skip("this platform has no process-group support")
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	if err := requestChildShutdown(cmd.Process); err != nil {
		t.Fatalf("requestChildShutdown: %v", err)
	}
	if err := killChildProcessGroup(cmd.Process); err != nil {
		t.Fatalf("killChildProcessGroup: %v", err)
	}
	_ = cmd.Wait()

	// pid <= 0 走单进程 Kill 那条退路。
	if err := killChildProcessGroup(&os.Process{}); err == nil {
		t.Fatal("killing an unstarted process must report an error")
	}
}

// TestKillChildProcessGroupFallsBackToSingleKill 覆盖进程组退路：目标组不存在时
// 退回单进程 Kill。它必须留在 Linux 构建下——Windows 的 os.Process.Kill 在
// Process 未初始化时会直接 panic（handleTransientAcquire invalid mode），
// 而不是像 Unix 那样返回一个错误。
func TestKillChildProcessGroupFallsBackToSingleKill(t *testing.T) {
	process := &os.Process{Pid: 1 << 20}
	if err := killChildProcessGroup(process); err == nil {
		t.Fatal("killing a process that is already gone must report an error")
	}
}
