//go:build windows

package pidfile

import (
	"os"
	"testing"
)

// TestProcessAliveRejectsNonPositivePIDs 钉住那个 pid <= 0 的守卫。
//
// 少了它，「PID 是 0」会被当成一个合法的探测目标交给 OpenProcess，而在 Windows
// 上 0 是「系统空闲进程」——于是任何一份内容为 0 的畸形锁文件都会让还原拒止，
// 而用户手上没有任何说法能解释为什么。
//
// 之所以只在 Windows 上测：Unix 那边的 processAlive(0) 走的是 kill(0, 0)，
// 它探测的是**进程组**并且会成功返回，两者语义本就不同。（Read 已经挡住了
// pid <= 0，所以这个差异在真实路径上不会被触发。）
func TestProcessAliveRejectsNonPositivePIDs(t *testing.T) {
	if processAlive(0) {
		t.Fatal("pid 0 不该被判定为「有进程在跑」")
	}
	if processAlive(-1) {
		t.Fatal("负数 pid 不该被判定为「有进程在跑」")
	}

	// 对照：本进程一定活着，否则上面两条断言只是「它恒返回 false」。
	if !processAlive(os.Getpid()) {
		t.Fatal("本进程应当被判定为活着")
	}
}
