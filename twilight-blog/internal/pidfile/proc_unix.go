//go:build unix

package pidfile

import (
	"errors"
	"syscall"
)

// kill is the syscall used to probe a process by PID. It is a variable so tests
// can exercise the permission-denied and unknown-error branches deterministically.
var kill = syscall.Kill

// processAlive 用信号 0 探测进程是否存在。
//
// 信号 0 不发送任何东西，只做「存在性 + 权限」检查，所以这是零副作用的探测手段：
//
//   - 返回 nil     → 进程存在，且我们有权限给它发信号；
//   - 返回 EPERM   → 进程存在，只是主人不是我们。这恰恰是「服务在跑」的一种
//     真实形态（它以另一个用户身份运行），所以必须算活着；
//   - 返回 ESRCH   → 进程不存在，即陈旧锁。
//
// 其余错误一律当成「还活着」：宁可多拦一次（用户还能用 --force 越过），
// 也不要在不确定的时候放行一次不可逆的还原。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := kill(pid, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPERM) {
		return true
	}
	return !errors.Is(err, syscall.ESRCH)
}
