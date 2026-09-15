package main

import (
	"errors"
	"testing"
	"time"

	"blog-engine/internal/search"
)

// TestStartWithRetriesRecoversFromATransientFailure 守护 R3-13 的修复：
// 启动期的一次性失败必须被重试掉，而不是把整站拖进 Fatalf。
//
// 真实场景：端口是「先探测、后交接」的，交接窗口里被别的进程抢走 →
// 子进程绑定失败立刻退出 → waitReady 返回「exited before becoming ready」。
// 这类失败换一个端口就能成功，属于可自愈的偶发故障。
//
// 反证方式：把 startWithRetries 的循环改成「只调一次 start」→
// 本条的第一个用例失败（返回了错误，而期望成功）。
func TestStartWithRetriesRecoversFromATransientFailure(t *testing.T) {
	manager := &search.Manager{}
	transient := errors.New("meilisearch exited before becoming ready: exit status 1")

	t.Run("succeeds immediately", func(t *testing.T) {
		calls := 0
		got, err := startWithRetries(3, time.Millisecond, func() (*search.Manager, error) {
			calls++
			return manager, nil
		})
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		if got != manager {
			t.Fatal("returned a different manager than the one start() produced")
		}
		if calls != 1 {
			t.Fatalf("start() calls = %d, want 1 (a successful start must not be retried)", calls)
		}
	})

	t.Run("recovers on a later attempt", func(t *testing.T) {
		calls := 0
		got, err := startWithRetries(3, time.Millisecond, func() (*search.Manager, error) {
			calls++
			if calls < 3 {
				return nil, transient
			}
			return manager, nil
		})
		if err != nil {
			t.Fatalf("error = %v, want nil after a retry succeeded", err)
		}
		if got != manager {
			t.Fatal("returned a different manager than the one start() produced")
		}
		if calls != 3 {
			t.Fatalf("start() calls = %d, want 3", calls)
		}
	})

	t.Run("gives up after the attempt budget", func(t *testing.T) {
		calls := 0
		last := errors.New("third failure")
		got, err := startWithRetries(3, time.Millisecond, func() (*search.Manager, error) {
			calls++
			if calls == 3 {
				return nil, last
			}
			return nil, transient
		})
		if got != nil {
			t.Fatal("a failed start returned a manager")
		}
		if calls != 3 {
			t.Fatalf("start() calls = %d, want 3 — the retry budget must be bounded", calls)
		}
		// 必须是**最后一次**的错误：返回第一次的错误会把运维引向已经翻篇的现场。
		if !errors.Is(err, last) {
			t.Fatalf("error = %v, want the last failure (%v)", err, last)
		}
	})
}
