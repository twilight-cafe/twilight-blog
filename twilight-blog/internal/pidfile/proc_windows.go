//go:build windows

package pidfile

import (
	"errors"
	"syscall"
)

// processQueryLimitedInformation 是 OpenProcess 需要的最小权限位。
//
// 不用 PROCESS_QUERY_INFORMATION（0x0400）：后者从 Vista 起对某些受保护进程
// 会直接失败，而我们要的只是「这个 PID 背后有没有进程」——LIMITED 就够，
// 且在更多情况下能成功打开。
const processQueryLimitedInformation = 0x1000

// processAlive 用 OpenProcess 探测进程是否存在。
//
// Windows 上没有信号 0（syscall.Kill 在 Windows 上根本不存在，而 os.Process.Signal
// 只支持 Kill），用「发个信号探一探」等于真的去杀进程。OpenProcess 是只读地打开
// 一个句柄：能打开就说明进程在，关掉句柄即可，没有任何副作用。
//
// ERROR_ACCESS_DENIED 同样说明进程存在，只是我们无权查看它——与 Unix 上的 EPERM
// 是同一回事，必须算活着。
//
// 其余错误（最典型的是 ERROR_INVALID_PARAMETER，即 PID 不存在）一律当作「不在了」，
// 于是被系统复用过的陈旧锁不会把还原挡在门外。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	syscall.CloseHandle(handle)
	return true
}
