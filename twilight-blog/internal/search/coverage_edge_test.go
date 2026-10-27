package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestUsableCandidateFoldsTheDirectoryAndModeChecks pins both refusals, including
// the directory case that a Windows host can produce in the real candidate loop.
func TestUsableCandidateFoldsTheDirectoryAndModeChecks(t *testing.T) {
	if usableCandidate("windows", fakeFileInfo{mode: os.ModeDir | 0o755}) {
		t.Fatal("a directory must never be a candidate")
	}
	if usableCandidate("linux", fakeFileInfo{mode: 0o644}) {
		t.Fatal("a non-executable file must not be a candidate on Unix")
	}
	if !usableCandidate("linux", fakeFileInfo{mode: 0o755}) {
		t.Fatal("an executable file must be accepted on Unix")
	}
	if !usableCandidate("windows", fakeFileInfo{mode: 0o666}) {
		t.Fatal("Windows has no execute bit; regular files must be accepted")
	}
}

// TestHasExecutableBit pins both halves of the platform rule. It is a plain
// argument-taking helper precisely so the non-Windows branch can be verified
// on a Windows host, where runtime.GOOS would otherwise hide it.
func TestHasExecutableBit(t *testing.T) {
	if !hasExecutableBit("windows", 0o644) {
		t.Fatal("Windows has no executable bit in os.FileMode; every regular file must stay a candidate")
	}
	if hasExecutableBit("linux", 0o644) {
		t.Fatal("a non-executable file must not be offered as the meilisearch binary")
	}
	if !hasExecutableBit("linux", 0o755) {
		t.Fatal("an executable file must be accepted")
	}
	if !hasExecutableBit("darwin", 0o100) {
		t.Fatal("owner-only execute permission is enough")
	}
}

// TestWaitTaskStopsWhenTheContextIsCancelledMidPoll covers the select arm that
// only runs when a poll succeeds and the caller goes away while waiting for the
// next one. An already-cancelled context fails earlier, in do(), so it does not
// reach this branch.
func TestWaitTaskStopsWhenTheContextIsCancelledMidPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	polled := make(chan struct{}, 1)
	client := newSearchClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The task stays "enqueued" forever; the cancellation is what ends it.
		_ = json.NewEncoder(w).Encode(map[string]any{"uid": 1, "status": "enqueued"})
		select {
		case polled <- struct{}{}:
		default:
		}
	}))

	restoreInterval := swap(&taskPollInterval, time.Hour)
	defer restoreInterval()

	done := make(chan error, 1)
	go func() { done <- client.waitTask(ctx, 1) }()

	<-polled
	// 让本次 do() 把响应读完：取消发生在上一个请求仍未结束时，
	// 错误会从 do() 返回，而不是走到轮询之间的 select。
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitTask error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitTask did not stop after cancellation")
	}
}

// TestSearchHelperSleep is the child side of TestKillChildProcessGroup. It only
// sleeps when the parent asks for it, so a normal test run skips it.
func TestSearchHelperSleep(t *testing.T) {
	if os.Getenv("SEARCH_HELPER_SLEEP") != "1" {
		t.Skip("helper process for TestKillChildProcessGroup")
	}
	time.Sleep(30 * time.Second)
}

// TestKillChildProcessGroup stops a real child process. It runs on every
// platform: on Linux it exercises the process-group path, elsewhere the
// single-process fallback that used to be impossible to reach from a test.
func TestKillChildProcessGroup(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSearchHelperSleep$")
	cmd.Env = append(os.Environ(), "SEARCH_HELPER_SLEEP=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	if err := killChildProcessGroup(cmd.Process); err != nil {
		t.Fatalf("killChildProcessGroup: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the helper should have been killed, not exited cleanly")
	}
}

// TestConfigureChildProcessIsCallable 在非 Linux 上它只是空实现，
// 但仍要被调用一次，保证平台分支的文件不是零覆盖。
func TestConfigureChildProcessIsCallable(t *testing.T) {
	configureChildProcess(&exec.Cmd{})
}
