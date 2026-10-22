package search

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件补齐 manager.go 的平台 / 故障分支。所有用例都通过 manager.go 顶部的
// 间接层替换具体的系统调用；sandbox 在用例结束时**逆序**还原，因此同一个
// 用例里可以先构造一种故障、还原、再构造下一种，而不会泄漏到后续断言。

// swap 把一个间接层临时换成测试用的实现，并返回还原函数。
//
// 之所以提供还原而不是直接赋值：这些变量是**包级**的，一个用例改了不还原就会
// 泄漏到同包的后续用例里，而那种污染的表现往往是「另一条用例莫名其妙变红」。
// hook 在当前测试结束时还原被替换的间接层。
//
// 与 swap 的区别只在「何时还原」：hook 用 t.Cleanup，作用域就是这一个测试
// （含它的子测试）；swap 适合「先设、用、再立刻还原」的顺序控制。
func hook[T any](t *testing.T, target *T, value T) {
	original := *target
	*target = value
	t.Cleanup(func() { *target = original })
}

func swap[T any](target *T, value T) func() {
	original := *target
	*target = value
	return func() { *target = original }
}

// TestFakeMeilisearchProcess 是子进程侧的「假 Meilisearch」。
// 只有父进程用 GO_FAKE_MEILI=1 拉起它时才真正监听；其余时候直接跳过。
func TestFakeMeilisearchProcess(t *testing.T) {
	if os.Getenv("GO_FAKE_MEILI") != "1" {
		t.Skip("helper process for search.Start")
	}
	addr := os.Getenv("MEILI_HTTP_ADDR")
	if addr == "" {
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: time.Second}
	_ = server.ListenAndServe()
	os.Exit(0)
}

func TestResolveMasterKeyFailureBranches(t *testing.T) {
	t.Run("read failure falls back to generating a new key", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "data")
		if err := os.MkdirAll(masterKeyPath(dataDir), 0o700); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		key, err := resolveMasterKey(dataDir)
		if err != nil {
			t.Fatalf("resolveMasterKey: %v", err)
		}
		if len(key) < minMasterKeyLength {
			t.Fatalf("key too short: %q", key)
		}
	})

	t.Run("short stored key is replaced", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "data")
		if err := os.MkdirAll(filepath.Dir(dataDir), 0o700); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if err := os.WriteFile(masterKeyPath(dataDir), []byte("short\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		key, err := resolveMasterKey(dataDir)
		if err != nil {
			t.Fatalf("resolveMasterKey: %v", err)
		}
		if key == "short" || len(key) < minMasterKeyLength {
			t.Fatalf("short key was reused: %q", key)
		}
	})

	t.Run("random source failure", func(t *testing.T) {
		hook(t, &randRead, func([]byte) (int, error) { return 0, errors.New("entropy") })
		if _, err := resolveMasterKey(filepath.Join(t.TempDir(), "data")); err == nil {
			t.Fatal("rand failure must surface")
		}
	})

	t.Run("persist failure keeps the per-run key", func(t *testing.T) {
		hook(t, &writeFile, func(string, []byte, os.FileMode) error { return errors.New("read-only") })
		key, err := resolveMasterKey(filepath.Join(t.TempDir(), "data"))
		if err != nil {
			t.Fatalf("persist failure must not be fatal: %v", err)
		}
		if len(key) < minMasterKeyLength {
			t.Fatalf("key too short: %q", key)
		}
	})
}

func TestFindExecutablePrefersTrustedCandidates(t *testing.T) {
	root := t.TempDir()
	hook(t, &osExecutable, func() (string, error) { return filepath.Join(root, "blog"), nil })
	hook(t, &osGetwd, func() (string, error) { return root, nil })

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	trusted := filepath.Join(binDir, binaryNames()[0])
	if err := os.WriteFile(trusted, []byte("x"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	path, err := FindExecutable()
	if err != nil {
		t.Fatalf("FindExecutable: %v", err)
	}
	if path != trusted {
		t.Fatalf("FindExecutable = %q, want %q", path, trusted)
	}
}

func TestFindExecutableSkipsUnsafeCandidates(t *testing.T) {
	root := t.TempDir()
	hook(t, &osExecutable, func() (string, error) { return filepath.Join(root, "blog"), nil })
	hook(t, &osGetwd, func() (string, error) { return root, nil })
	name := binaryNames()[0]

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	// 目录当候选：跳过。
	if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
		t.Fatalf("mkdir candidate: %v", err)
	}
	// 组可写：跳过（同时覆盖 0o111 之外的权限位判断）。
	if err := os.WriteFile(filepath.Join(binDir, name), []byte("x"), 0o664); err != nil {
		t.Fatalf("write: %v", err)
	}
	// 没有可执行位：跳过。
	if len(binaryNames()) > 1 {
		if err := os.WriteFile(filepath.Join(binDir, binaryNames()[1]), []byte("x"), 0o644); err != nil {
			t.Fatalf("write plain: %v", err)
		}
	}

	if _, err := FindExecutable(); err == nil {
		t.Fatal("no safe candidate should have been selected")
	}
}

func TestFindExecutableErrorBranches(t *testing.T) {
	t.Run("executable lookup falls back to argv[0]", func(t *testing.T) {
		root := t.TempDir()
		hook(t, &osExecutable, func() (string, error) { return "", errors.New("no /proc") })
		hook(t, &osGetwd, func() (string, error) { return root, nil })
		if _, err := FindExecutable(); err == nil {
			t.Fatal("an empty candidate list must fail")
		}
	})

	t.Run("abs failure and cwd failure", func(t *testing.T) {
		hook(t, &absPath, func(string) (string, error) { return "", errors.New("abs") })
		if _, err := FindExecutable(); err == nil {
			t.Fatal("abs failure must surface")
		}
	})

	t.Run("getwd failure", func(t *testing.T) {
		hook(t, &osGetwd, func() (string, error) { return "", errors.New("getwd") })
		if _, err := FindExecutable(); err == nil {
			t.Fatal("getwd failure must surface")
		}
	})

	t.Run("abs failure on the selected candidate falls back to the raw path", func(t *testing.T) {
		root := t.TempDir()
		hook(t, &osExecutable, func() (string, error) { return filepath.Join(root, "blog"), nil })
		hook(t, &osGetwd, func() (string, error) { return root, nil })
		target := filepath.Join(root, binaryNames()[0])
		if err := os.WriteFile(target, []byte("x"), 0o755); err != nil {
			t.Fatalf("write: %v", err)
		}
		calls := 0
		hook(t, &absPath, func(path string) (string, error) {
			calls++
			if calls == 1 {
				return filepath.Abs(path)
			}
			return "", errors.New("abs")
		})
		path, err := FindExecutable()
		if err != nil {
			t.Fatalf("FindExecutable: %v", err)
		}
		if path != target {
			t.Fatalf("path = %q, want the raw candidate %q", path, target)
		}
	})
}

func TestPlatformHelpers(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		want         []string
	}{
		{"linux", "amd64", []string{"meilisearch", "meilisearch-linux-amd64", "meilisearch-linux-x86_64"}},
		{"linux", "arm64", []string{"meilisearch", "meilisearch-linux-arm64"}},
		{"windows", "amd64", []string{"meilisearch", "meilisearch.exe", "meilisearch-windows-amd64.exe", "meilisearch-windows-x86_64.exe"}},
		{"windows", "arm64", []string{"meilisearch", "meilisearch.exe", "meilisearch-windows-arm64.exe"}},
		{"darwin", "arm64", []string{"meilisearch", "meilisearch-darwin-arm64", "meilisearch-macos-aarch64"}},
	} {
		got := binaryNamesFor(tc.goos, tc.goarch)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("binaryNamesFor(%s/%s) = %v, want %v", tc.goos, tc.goarch, got, tc.want)
		}
	}

	if !candidateIsSafelyOwnedFor("windows", fakeFileInfo{mode: 0o777}) {
		t.Fatal("windows must accept candidates regardless of mode")
	}
	if candidateIsSafelyOwnedFor("linux", fakeFileInfo{mode: 0o664}) {
		t.Fatal("group-writable candidates must be rejected on unix")
	}
	if !candidateIsSafelyOwnedFor("linux", fakeFileInfo{mode: 0o755}) {
		t.Fatal("owner-writable candidates are fine")
	}
}

type fakeFileInfo struct{ mode os.FileMode }

func (f fakeFileInfo) Name() string       { return "meilisearch" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestLogExecutableChoiceCoversBothOrigins(t *testing.T) {
	// 两条路径都只写日志，不返回值；这里断言的是「不 panic 且分支真的走过」。
	logExecutableChoice("/tmp/trusted", "executable-dir/bin")
	logExecutableChoice("/tmp/untrusted", "working-dir")
}

func TestEnsureBinaryBranches(t *testing.T) {
	t.Run("local hit", func(t *testing.T) {
		hook(t, &findExecutableFn, func() (string, error) { return "/local/meilisearch", nil })
		path, err := EnsureBinary()
		if err != nil || path != "/local/meilisearch" {
			t.Fatalf("EnsureBinary = (%q, %v)", path, err)
		}
	})

	t.Run("release lookup failure", func(t *testing.T) {
		hook(t, &findExecutableFn, func() (string, error) { return "", errors.New("missing") })
		hook(t, &latestAssetFn, func() (githubRelease, releaseAsset, releaseSource, error) {
			return githubRelease{}, releaseAsset{}, releaseSource{}, errors.New("offline")
		})
		if _, err := EnsureBinary(); err == nil {
			t.Fatal("release lookup failure must surface")
		}
	})

	t.Run("download URL outside the pinned prefix", func(t *testing.T) {
		source := releaseSource{metadataURL: "meta", downloadPrefix: "https://github.com/meilisearch/meilisearch/releases/download/"}
		hook(t, &findExecutableFn, func() (string, error) { return "", errors.New("missing") })
		hook(t, &latestAssetFn, func() (githubRelease, releaseAsset, releaseSource, error) {
			return githubRelease{TagName: "v1"}, releaseAsset{Name: "a", BrowserDownloadURL: "https://evil.example/a"}, source, nil
		})
		if _, err := EnsureBinary(); err == nil {
			t.Fatal("an unpinned download URL must be refused")
		}
	})

	t.Run("every install target fails", func(t *testing.T) {
		source := releaseSource{metadataURL: "meta", downloadPrefix: "https://github.com/meilisearch/meilisearch/releases/download/"}
		hook(t, &findExecutableFn, func() (string, error) { return "", errors.New("missing") })
		hook(t, &latestAssetFn, func() (githubRelease, releaseAsset, releaseSource, error) {
			return githubRelease{TagName: "v1"}, releaseAsset{Name: "a", BrowserDownloadURL: source.downloadPrefix + "a"}, source, nil
		})
		hook(t, &installReleaseFn, func(string, githubRelease, releaseAsset, releaseSource) error {
			return errors.New("disk full")
		})
		hook(t, &osGetwd, func() (string, error) { return t.TempDir(), nil })
		_, err := EnsureBinary()
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("EnsureBinary error = %v", err)
		}
	})

	t.Run("first target fails, second succeeds", func(t *testing.T) {
		source := releaseSource{metadataURL: "meta", downloadPrefix: "https://github.com/meilisearch/meilisearch/releases/download/"}
		calls := 0
		hook(t, &findExecutableFn, func() (string, error) { return "", errors.New("missing") })
		hook(t, &latestAssetFn, func() (githubRelease, releaseAsset, releaseSource, error) {
			return githubRelease{TagName: "v1"}, releaseAsset{Name: "a", BrowserDownloadURL: source.downloadPrefix + "a"}, source, nil
		})
		hook(t, &installReleaseFn, func(string, githubRelease, releaseAsset, releaseSource) error {
			calls++
			if calls == 1 {
				return errors.New("permission denied")
			}
			return nil
		})
		hook(t, &osGetwd, func() (string, error) { return t.TempDir(), nil })
		path, err := EnsureBinary()
		if err != nil {
			t.Fatalf("EnsureBinary: %v", err)
		}
		if calls != 2 || path == "" {
			t.Fatalf("calls = %d, path = %q", calls, path)
		}
	})
}

func TestStartFailureBranches(t *testing.T) {
	t.Run("binary resolution", func(t *testing.T) {
		hook(t, &ensureBinaryFn, func() (string, error) { return "", errors.New("no binary") })
		if _, err := Start(context.Background(), t.TempDir()); err == nil {
			t.Fatal("missing binary must surface")
		}
	})

	t.Run("relative data dir and getwd failure", func(t *testing.T) {
		hook(t, &ensureBinaryFn, func() (string, error) { return "/bin/true", nil })
		hook(t, &osGetwd, func() (string, error) { return "", errors.New("getwd") })
		if _, err := Start(context.Background(), "relative"); err == nil {
			t.Fatal("getwd failure must surface")
		}
	})

	t.Run("mkdir failure", func(t *testing.T) {
		hook(t, &ensureBinaryFn, func() (string, error) { return "/bin/true", nil })
		hook(t, &mkdirAll, func(string, os.FileMode) error { return errors.New("mkdir") })
		if _, err := Start(context.Background(), t.TempDir()); err == nil {
			t.Fatal("mkdir failure must surface")
		}
	})

	t.Run("chmod failure is only a warning, listen failure surfaces", func(t *testing.T) {
		hook(t, &ensureBinaryFn, func() (string, error) { return "/bin/true", nil })
		hook(t, &chmodFile, func(string, os.FileMode) error { return errors.New("chmod") })
		hook(t, &listenTCP, func(string, string) (net.Listener, error) { return nil, errors.New("no port") })
		if _, err := Start(context.Background(), t.TempDir()); err == nil {
			t.Fatal("listen failure must surface")
		}
	})

	t.Run("master key failure", func(t *testing.T) {
		hook(t, &ensureBinaryFn, func() (string, error) { return "/bin/true", nil })
		hook(t, &randRead, func([]byte) (int, error) { return 0, errors.New("entropy") })
		if _, err := Start(context.Background(), t.TempDir()); err == nil {
			t.Fatal("resolveMasterKey failure must surface")
		}
	})

	t.Run("stdout pipe failure", func(t *testing.T) {
		hook(t, &ensureBinaryFn, func() (string, error) { return "/bin/true", nil })
		hook(t, &execCommand, func(string, ...string) *exec.Cmd {
			cmd := exec.Command("/bin/true")
			cmd.Stdout = os.Stdout
			return cmd
		})
		if _, err := Start(context.Background(), t.TempDir()); err == nil {
			t.Fatal("stdout pipe failure must surface")
		}
	})

	t.Run("stderr pipe failure", func(t *testing.T) {
		hook(t, &ensureBinaryFn, func() (string, error) { return "/bin/true", nil })
		hook(t, &execCommand, func(string, ...string) *exec.Cmd {
			cmd := exec.Command("/bin/true")
			cmd.Stderr = os.Stderr
			return cmd
		})
		if _, err := Start(context.Background(), t.TempDir()); err == nil {
			t.Fatal("stderr pipe failure must surface")
		}
	})

	t.Run("exec start failure", func(t *testing.T) {
		hook(t, &ensureBinaryFn, func() (string, error) { return "/bin/true", nil })
		hook(t, &execCommand, func(string, ...string) *exec.Cmd {
			return exec.Command(filepath.Join(t.TempDir(), "missing-binary"))
		})
		if _, err := Start(context.Background(), t.TempDir()); err == nil {
			t.Fatal("exec start failure must surface")
		}
	})
}

func TestStartAndStopWithHelperProcess(t *testing.T) {
	t.Setenv("GO_FAKE_MEILI", "1")
	hook(t, &ensureBinaryFn, func() (string, error) { return os.Args[0], nil })
	hook(t, &execCommand, func(string, ...string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=^TestFakeMeilisearchProcess$")
	})
	hook(t, &readyInterval, 10*time.Millisecond)
	hook(t, &readyTimeout, 5*time.Second)

	manager, err := Start(context.Background(), filepath.Join(t.TempDir(), "meili"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if manager.Client() == nil {
		t.Fatal("Client() must not be nil")
	}
	manager.Stop()
	manager.Stop() // once 守卫：第二次是空操作

	select {
	case <-manager.done:
	case <-time.After(5 * time.Second):
		t.Fatal("helper process did not exit")
	}
}

func TestStopBranches(t *testing.T) {
	t.Run("no process", func(t *testing.T) {
		manager := &Manager{}
		manager.Stop()
		if !manager.stopping.Load() {
			t.Fatal("Stop must mark the manager as stopping")
		}
	})

	t.Run("graceful exit", func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		shutdowns := 0
		hook(t, &requestShutdown, func(*os.Process) error { shutdowns++; return errors.New("already gone") })
		hook(t, &killGroup, func(*os.Process) error { t.Fatal("graceful exit must not kill"); return nil })
		manager := &Manager{cmd: &exec.Cmd{Process: &os.Process{Pid: 1}}, done: done}
		manager.Stop()
		if shutdowns != 1 {
			t.Fatalf("shutdown requests = %d", shutdowns)
		}
	})

	t.Run("timeout kills the group", func(t *testing.T) {
		kills := 0
		hook(t, &requestShutdown, func(*os.Process) error { return nil })
		hook(t, &killGroup, func(*os.Process) error { kills++; return nil })
		hook(t, &shutdownGrace, time.Millisecond)
		manager := &Manager{cmd: &exec.Cmd{Process: &os.Process{Pid: 1}}, done: make(chan struct{})}
		manager.Stop()
		if kills != 1 {
			t.Fatalf("group kills = %d", kills)
		}
	})
}

func TestMonitorReportsUnexpectedExit(t *testing.T) {
	t.Run("expected shutdown", func(t *testing.T) {
		manager := &Manager{waitCh: make(chan error, 1)}
		manager.stopping.Store(true)
		manager.waitCh <- errors.New("signal: terminated")
		manager.monitor()
	})

	t.Run("unexpected exit uses fatal", func(t *testing.T) {
		var captured string
		hook(t, &fatalLog, func(format string, _ ...any) { captured = format })
		manager := &Manager{waitCh: make(chan error, 1)}
		manager.waitCh <- nil
		manager.monitor()
		if captured == "" {
			t.Fatal("unexpected exit must be fatal")
		}
	})
}

func TestWaitReadyBranches(t *testing.T) {
	t.Run("child exits first", func(t *testing.T) {
		manager := &Manager{baseURL: "http://127.0.0.1:1"}
		waitCh := make(chan error, 1)
		waitCh <- errors.New("boom")
		if err := manager.waitReady(context.Background(), waitCh); err == nil {
			t.Fatal("child exit must surface")
		}
	})

	t.Run("context canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		manager := &Manager{baseURL: "http://127.0.0.1:1"}
		if err := manager.waitReady(ctx, make(chan error)); err == nil {
			t.Fatal("ctx cancellation must surface")
		}
	})

	t.Run("timeout then success", func(t *testing.T) {
		hook(t, &readyTimeout, 200*time.Millisecond)
		hook(t, &readyInterval, time.Millisecond)
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			if calls < 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		manager := &Manager{baseURL: server.URL, masterKey: "k"}
		if err := manager.waitReady(context.Background(), make(chan error)); err != nil {
			t.Fatalf("waitReady: %v", err)
		}
	})

	t.Run("deadline exceeded", func(t *testing.T) {
		hook(t, &readyTimeout, 5*time.Millisecond)
		hook(t, &readyInterval, time.Millisecond)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		manager := &Manager{baseURL: server.URL}
		if err := manager.waitReady(context.Background(), make(chan error)); err == nil {
			t.Fatal("deadline must surface")
		}
	})
}

func TestFreePortBranches(t *testing.T) {
	port, err := freePort()
	if err != nil || port <= 0 {
		t.Fatalf("freePort = (%d, %v)", port, err)
	}
	hook(t, &listenTCP, func(string, string) (net.Listener, error) { return nil, errors.New("no sockets") })
	if _, err := freePort(); err == nil {
		t.Fatal("listen failure must surface")
	}
}

func TestPipeLinesAndSanitize(t *testing.T) {
	var lines []string
	hook(t, &logLine, func(format string, args ...any) {
		lines = append(lines, format+":"+args[1].(string))
	})

	pipeLines(strings.NewReader("hello\n\n\tworld\x01\n"), "[x] ")
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	if !strings.Contains(lines[0], "hello") || strings.Contains(lines[1], "\t") {
		t.Fatalf("unexpected sanitized lines: %v", lines)
	}

	if got := sanitizeLogLine("  a\tb\x00c  "); got != "a bc" {
		t.Fatalf("sanitizeLogLine = %q", got)
	}

	// 超过 scanner 上限的一行会触发 Err()。
	pipeLines(strings.NewReader(strings.Repeat("x", 2<<20)), "")
	if len(lines) != 2 {
		t.Fatalf("oversized line must be dropped, lines = %v", lines)
	}
}

func TestMasterKeyPathHelpers(t *testing.T) {
	if got := MasterKeyPath(filepath.Join("a", "b", "data")); got != filepath.Join("a", "b", "data.master.key") {
		t.Fatalf("MasterKeyPath = %q", got)
	}
}
