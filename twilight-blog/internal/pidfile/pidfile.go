// Package pidfile 让「服务是不是正在跑」变成一个可以直接回答的问题。
//
// 备份允许在线跑——VACUUM INTO 拿的是一致快照，不动生产库的写入路径。但**还原
// 不行**：它会替换 blog.db、删掉它的 -wal/-shm、把媒体目录整个换掉，而一个正在
// 运行的服务会继续往旧的文件句柄里写、把索引灌回刚还原的库上。两边的结果都是
// 「数据看起来在，但已经不是那一份了」。
//
// 判据不能靠猜。最容易被想到的启发式是「blog.db-wal 存在就说明有人在跑」，
// 而那个文件在进程崩溃之后也会留下——它区分不了「正在跑」和「上次没退干净」，
// 而这两种情况需要完全相反的动作。所以服务启动时写一个 PID 文件、退出时删掉，
// CLI 读到之后再去问操作系统「这个进程还活着吗」：只有**活着的**进程才构成拒止，
// 陈旧的锁文件只会换来一条提示。
package pidfile

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// FileName 是锁文件名，落在进程的工作目录（也就是 blog.db 旁边）。
const FileName = "blog.pid"

// Info 是锁文件的内容。
//
// 除了 PID 还记下启动时间与可执行文件路径：PID 会被操作系统复用，单看一个数字
// 分不清「就是那个服务」还是「另一个进程正好拿到了同一个号」，而这两种情况对应
// 的动作完全相反。多出来的这两项让现场的人能自己判断，也让日志里的误判可追。
type Info struct {
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	Executable string    `json:"executable"`
}

// Handle 是「本进程持有锁」的凭据。
type Handle struct {
	path string
	pid  int
}

// Acquire 写入锁文件并返回句柄。
//
// 它**不**检查是否已有别的实例在跑：这个包的职责只是记录事实，而「要不要因为
// 重复启动而拒绝」是服务端自己的策略（当前不拒绝——用户可能是故意跑第二份去
// 读另一份数据）。写失败则返回错误，由调用方决定是致命还是告警。
func Acquire(path string) (*Handle, error) {
	info := Info{PID: os.Getpid(), StartedAt: time.Now().UTC()}
	if executable, err := os.Executable(); err == nil {
		info.Executable = executable
	}

	data, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("encode the pid file: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("write the pid file: %w", err)
	}
	return &Handle{path: path, pid: info.PID}, nil
}

// Release 删掉锁文件。
//
// 它只删**属于本进程**的那一个：如果文件里的 PID 已经不是自己（说明期间有人
// 重新写过它），就什么都不做——否则一个正在退出的旧实例会把新实例的锁删掉，
// 于是还原再也拦不住任何东西。
//
// 走 os.Exit 的退出路径（信号处理、Fatalf）不会执行 defer，调用方要显式调它。
func (h *Handle) Release() {
	if h == nil {
		return
	}
	current, ok := Read(h.path)
	if !ok || current.PID != h.pid {
		return
	}
	_ = os.Remove(h.path)
}

// Read 读锁文件。第二个返回值为 false 表示文件不存在或内容不可解析。
//
// 内容坏掉与文件不存在被归为同一类：两者都提供不了「谁在跑」的答案，
// 而调用方对此的处理是一样的（当作没有锁，继续）。
func Read(path string) (Info, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Info{}, false
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, false
	}
	if info.PID <= 0 {
		return Info{}, false
	}
	return info, true
}

// Holder 回答「谁在跑」。
//
// running 为 true 才表示**确实有一个活着的进程**；running 为 false 而 info.PID
// 非零，表示这是一份陈旧锁（上次没退干净，或者机器重启过），调用方可以放心
// 越过它并提示用户。
func Holder(path string) (info Info, running bool) {
	stored, ok := Read(path)
	if !ok {
		return Info{}, false
	}
	return stored, processAlive(stored.PID)
}
