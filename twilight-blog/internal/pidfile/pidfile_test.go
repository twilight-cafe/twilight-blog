package pidfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stalePID 返回一个几乎不可能存在的 PID。
//
// 上界取 1<<29：Linux 的 pid_max 默认是 2^22，Windows 的进程号也远达不到这个
// 量级，而它仍稳稳落在 pid_t 的 32 位整数范围里——再大一点的两个平台行为就分岔了
// （Windows 的 OpenProcess 会直接判参数非法，Linux 的 kill 会返回 ESRCH，
// 两者都算「不存在」，但没必要去踩这个边界）。
func stalePID() int { return 1 << 29 }

// TestAcquireReadReleaseRoundTrip 钉住锁文件的生命周期。
//
// 反证方式：把 Acquire 里的 os.WriteFile 换成 os.WriteFile 到一个错误路径
// → 本条在「刚写下的锁文件应当可读」处失败。
func TestAcquireReadReleaseRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	handle, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() 报错: %v", err)
	}

	info, ok := Read(path)
	if !ok {
		t.Fatal("刚写下的锁文件应当可读")
	}
	if info.PID != os.Getpid() {
		t.Fatalf("PID = %d, 期望 %d", info.PID, os.Getpid())
	}
	if info.StartedAt.IsZero() {
		t.Fatal("启动时间没有被记下来：PID 会被复用，只靠它分不清是不是同一个进程")
	}

	handle.Release()
	if _, ok := Read(path); ok {
		t.Fatal("Release 之后不该还能读到锁文件")
	}

	// Release 必须可以重复调用：信号处理与正常退出路径都可能走到它。
	handle.Release()
}

// TestReleaseDoesNotDeleteSomeoneElsesLock 钉住「只删自己的锁」。
//
// 漏掉这层判断的后果很具体：一个正在退出的旧实例会把**新实例**的锁删掉，
// 于是还原再也拦不住任何东西——而那时服务确实在跑。
//
// 反证方式：删掉 Release 里的 PID 比较 → 本条失败。
func TestReleaseDoesNotDeleteSomeoneElsesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	handle, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() 报错: %v", err)
	}

	// 模拟「另一个实例接管了这把锁」：文件里的 PID 已经不是我们的。
	other := Info{PID: os.Getpid() + 1, StartedAt: time.Now().UTC()}
	data, err := json.Marshal(other)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("改写锁文件失败: %v", err)
	}

	handle.Release()

	if _, err := os.Stat(path); err != nil {
		t.Fatal("Release 删掉了不属于自己的锁文件")
	}
}

// TestReadRejectsMalformedFiles 确认坏内容与「没有文件」被归为同一类。
//
// 两者都回答不了「谁在跑」，而调用方对此的处理是一样的（当作没有锁）。
// 但要是把它们当成有效的锁，还原就会被一个内容都读不出来的文件永久挡住。
func TestReadRejectsMalformedFiles(t *testing.T) {
	dir := t.TempDir()

	cases := map[string]string{
		"不是 JSON": "not json at all",
		"PID 为零":  `{"pid":0}`,
		"PID 为负":  `{"pid":-3}`,
		"空文件":     "",
	}
	for label, body := range cases {
		path := filepath.Join(dir, "case.pid")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("准备 %s 失败: %v", label, err)
		}
		if info, ok := Read(path); ok {
			t.Fatalf("%s 不该被当成有效的锁文件（读出了 pid=%d）", label, info.PID)
		}
	}

	if _, ok := Read(filepath.Join(dir, "missing.pid")); ok {
		t.Fatal("不存在的文件不该被当成有效的锁文件")
	}
}

// TestHolderDistinguishesStaleFromLive 是这套机制存在的全部理由。
//
// 「服务在跑」与「上次没退干净」对应的动作完全相反：前者必须挡住还原，
// 后者必须放行（否则用户会被永久挡在门外，而唯一的出路是手动去删文件）。
// 靠 blog.db-wal 之类的启发式区分不了这两者，只有真去问操作系统才行。
//
// 反证方式：让 processAlive 恒返回 true → 「不存在的进程算陈旧」子用例失败；
// 恒返回 false → 「本进程是活的」子用例失败。
func TestHolderDistinguishesStaleFromLive(t *testing.T) {
	dir := t.TempDir()

	t.Run("本进程是活的", func(t *testing.T) {
		path := filepath.Join(dir, "live.pid")
		handle, err := Acquire(path)
		if err != nil {
			t.Fatalf("Acquire() 报错: %v", err)
		}
		defer handle.Release()

		info, running := Holder(path)
		if !running {
			t.Fatalf("自己的 PID (%d) 应当被判为在运行", os.Getpid())
		}
		if info.PID != os.Getpid() {
			t.Fatalf("PID = %d, 期望 %d", info.PID, os.Getpid())
		}
	})

	t.Run("不存在的进程算陈旧", func(t *testing.T) {
		path := filepath.Join(dir, "stale.pid")
		data, err := json.Marshal(Info{PID: stalePID(), StartedAt: time.Now().UTC()})
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("写锁文件失败: %v", err)
		}

		info, running := Holder(path)
		if running {
			t.Fatalf("已经不存在的进程不该被判为在运行 (pid %d)", info.PID)
		}
		// 陈旧锁仍然要把它记录的 PID 交出来：调用方靠这句提示告诉用户
		// 「这是上次没退干净留下的」，而不是默默无事发生。
		if info.PID != stalePID() {
			t.Fatalf("陈旧锁应返回它记录的 PID，实际 %d", info.PID)
		}
	})

	t.Run("没有锁文件", func(t *testing.T) {
		info, running := Holder(filepath.Join(dir, "nothing.pid"))
		if running || info.PID != 0 {
			t.Fatalf("没有锁文件时不该报告任何东西: %+v running=%v", info, running)
		}
	})
}

// TestAcquireReportsFailure 确认写失败是**错误**而不是静默。
//
// 主进程据此打一条告警——它必须知道「这次没有锁」，否则一个磁盘满的站点会在
// 毫无提示的情况下失去还原保护。
func TestAcquireReportsFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", FileName)
	if _, err := Acquire(path); err == nil {
		t.Fatal("目标目录不存在时 Acquire 应当报错")
	}
}

// TestNilHandleReleaseIsSafe 覆盖主进程里那个真实的形态：
// 写锁文件失败时 lock 为 nil，而释放它的代码在信号处理里无条件执行。
func TestNilHandleReleaseIsSafe(t *testing.T) {
	var handle *Handle
	handle.Release()
}
