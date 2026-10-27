package search

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 这个文件收口 manager.go / download.go / client.go 里**结构上难以从外部触发**
// 的分支：平台相关的进程处理、走不到的闭包去重、以及若干「文件系统返回错误」
// 的内部路径。

func TestAppendUniqueSkipsEmptyAndDuplicates(t *testing.T) {
	seen := map[string]struct{}{}
	names := appendUnique(nil, seen, "")
	if len(names) != 0 {
		t.Fatalf("an empty name must be skipped: %v", names)
	}
	names = appendUnique(names, seen, "meilisearch")
	names = appendUnique(names, seen, "meilisearch")
	if len(names) != 1 {
		t.Fatalf("duplicates must be skipped: %v", names)
	}
}

func TestFindExecutableRejectsGroupWritableFiles(t *testing.T) {
	root := t.TempDir()
	hook(t, &osExecutable, func() (string, error) { return filepath.Join(root, "blog"), nil })
	hook(t, &osGetwd, func() (string, error) { return root, nil })

	// 0776：带可执行位（否则会被「不是可执行文件」那条提前跳过），
	// 同时组可写——正是归属检查要拦的情形。
	//
	// 用 statFile 钩子返回这个模式，而不是真的 chmod：Windows 不保留 POSIX
	// 组权限位，真实文件永远是 0o666/0o444，这条用例会在那边无声地失败。
	candidate := filepath.Join(root, "bin", binaryNames()[0])
	hook(t, &statFile, func(path string) (os.FileInfo, error) {
		if path == candidate {
			return fakeFileInfo{mode: 0o776}, nil
		}
		return nil, os.ErrNotExist
	})
	// 归属判断本身由 TestPlatformHelpers 用真实模式参数化验证；这里替换成
	// 「判定为不安全」，使 FindExecutable 的跳过分支在任何宿主上都能执行。
	// Windows 的 candidateIsSafelyOwned 恒为 true（拿不到 ACL），不能靠真实模式触发。
	hook(t, &candidateIsSafe, func(string, os.FileInfo) bool { return false })
	if _, err := FindExecutable(); err == nil {
		t.Fatal("a group-writable candidate must not be executed")
	}
}

func TestEnsureBinaryFallsBackToArgv0(t *testing.T) {
	source := releaseSource{metadataURL: "meta", downloadPrefix: "https://github.com/meilisearch/meilisearch/releases/download/"}
	hook(t, &findExecutableFn, func() (string, error) { return "", errors.New("missing") })
	hook(t, &latestAssetFn, func() (githubRelease, releaseAsset, releaseSource, error) {
		return githubRelease{TagName: "v1"}, releaseAsset{Name: "a", BrowserDownloadURL: source.downloadPrefix + "a"}, source, nil
	})
	hook(t, &osExecutable, func() (string, error) { return "", errors.New("no /proc") })
	hook(t, &installReleaseFn, func(string, githubRelease, releaseAsset, releaseSource) error { return nil })
	if _, err := EnsureBinary(); err != nil {
		t.Fatalf("EnsureBinary must fall back to argv[0]: %v", err)
	}
}

func TestStartUsesDefaultDataDirAndKillsUnreadyChild(t *testing.T) {
	t.Run("empty data dir falls back to the default", func(t *testing.T) {
		t.Setenv("GO_FAKE_MEILI", "1")
		hook(t, &ensureBinaryFn, func() (string, error) { return os.Args[0], nil })
		hook(t, &execCommand, func(string, ...string) *exec.Cmd {
			return exec.Command(os.Args[0], "-test.run=^TestFakeMeilisearchProcess$")
		})
		hook(t, &readyInterval, 10*time.Millisecond)
		hook(t, &readyTimeout, 5*time.Second)
		hook(t, &osGetwd, func() (string, error) { return t.TempDir(), nil })

		manager, err := Start(context.Background(), "")
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		manager.Stop()
	})

	t.Run("child exits before becoming ready", func(t *testing.T) {
		hook(t, &ensureBinaryFn, func() (string, error) { return os.Args[0], nil })
		hook(t, &execCommand, func(string, ...string) *exec.Cmd {
			// 一个立刻退出的进程：waitReady 会先收到 waitCh。
			return exec.Command(os.Args[0], "-test.run=^TestThisTestDoesNotExist$")
		})
		hook(t, &readyInterval, time.Millisecond)
		hook(t, &readyTimeout, 2*time.Second)
		if _, err := Start(context.Background(), t.TempDir()); err == nil {
			t.Fatal("a child that dies before /health answers must surface")
		}
	})
}

func TestWaitReadySkipsUnreachableProbe(t *testing.T) {
	hook(t, &readyInterval, time.Millisecond)
	hook(t, &readyTimeout, 10*time.Millisecond)
	// 目标端口无人监听：探测请求全部失败，只能走到超时那条路。
	manager := &Manager{baseURL: "http://127.0.0.1:1"}
	if err := manager.waitReady(context.Background(), make(chan error)); err == nil {
		t.Fatal("an unreachable probe must eventually time out")
	}
}

func TestSyncSurfacesResetFailure(t *testing.T) {
	failing := newSearchClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	if err := failing.Sync(context.Background(), stubArticleRepo{}, stubPageRepo{}); err == nil {
		t.Fatal("a failing ResetIndex must surface")
	}
}

func TestWaitTaskPollsBeforeTimingOut(t *testing.T) {
	hook(t, &taskPollInterval, 5*time.Millisecond)
	hook(t, &taskPollTimeout, 60*time.Millisecond)
	enqueued := newSearchClient(t, &searchServer{tasks: "enqueued"})
	if err := enqueued.waitTask(context.Background(), 3); err == nil {
		t.Fatal("a task that never finishes must time out")
	}
}

func TestSelectAssetForIgnoresForeignNames(t *testing.T) {
	release := githubRelease{TagName: "v1", Assets: []releaseAsset{
		{Name: "unrelated-linux-amd64.txt"},
		{Name: "meilisearch-linux-amd64"},
	}}
	asset, err := selectAssetFor(release, "linux", "amd64")
	if err != nil || asset.Name != "meilisearch-linux-amd64" {
		t.Fatalf("selectAssetFor = (%+v, %v)", asset, err)
	}
}

func TestLatestAssetRetriesAndReportsAssetFailure(t *testing.T) {
	hook(t, &fetchReleaseFn, func(string) (githubRelease, error) {
		// 一个不会匹配任何平台的资产名，保证 Windows / Linux 宿主都走失败分支。
		return githubRelease{TagName: "v1", Assets: []releaseAsset{{Name: "meilisearch-unknown-platform"}}}, nil
	})
	if _, _, _, err := latestAsset(); err == nil || !strings.Contains(err.Error(), "no release asset found") {
		t.Fatalf("latestAsset error = %v", err)
	}
}

func TestExtractBinaryPropagatesWalkCallbackErrors(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "good.zip")
	writeZip(t, archive, map[string]string{"meilisearch": "binary"})
	hook(t, &walkDirPath, func(root string, fn fs.WalkDirFunc) error {
		return fn(root, nil, errors.New("cannot stat"))
	})
	if _, err := extractBinary(archive, "good.zip"); err == nil {
		t.Fatal("a walk callback error must surface")
	}
}

func TestUnzipToSystemErrorBranches(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "dest")
	archive := filepath.Join(dir, "entries.zip")
	writeZip(t, archive, map[string]string{"nested/": "", "nested/meilisearch": "binary"})

	t.Run("directory entries", func(t *testing.T) {
		hook(t, &mkdirAll, func(string, os.FileMode) error { return errors.New("mkdir") })
		if err := unzipTo(archive, dest); err == nil {
			t.Fatal("a directory entry that cannot be created must fail")
		}
	})

	t.Run("parent directories", func(t *testing.T) {
		onlyFile := filepath.Join(dir, "file.zip")
		writeZip(t, onlyFile, map[string]string{"meilisearch": "binary"})
		hook(t, &mkdirAll, func(string, os.FileMode) error { return errors.New("mkdir") })
		if err := unzipTo(onlyFile, dest); err == nil {
			t.Fatal("a parent directory that cannot be created must fail")
		}
	})

	t.Run("entry open and copy failures", func(t *testing.T) {
		restore := swap(&openZipEntry, func(*zip.File) (io.ReadCloser, error) {
			return nil, errors.New("unsupported compression")
		})
		if err := unzipTo(archive, filepath.Join(dir, "opened")); err == nil {
			t.Fatal("an entry that cannot be opened must fail")
		}
		restore()

		restoreReader := swap(&openZipEntry, func(*zip.File) (io.ReadCloser, error) {
			return io.NopCloser(errReader{}), nil
		})
		if err := unzipTo(archive, filepath.Join(dir, "read")); err == nil {
			t.Fatal("an entry that cannot be read must fail")
		}
		restoreReader()
	})
}

func TestUntarGzToSystemErrorBranches(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "dest")

	t.Run("directory entries", func(t *testing.T) {
		archive := filepath.Join(dir, "dir.tar.gz")
		writeTarGz(t, archive, map[string]string{"nested/": ""}, 0o755)
		hook(t, &mkdirAll, func(string, os.FileMode) error { return errors.New("mkdir") })
		if err := untargzTo(archive, dest); err == nil {
			t.Fatal("a directory entry that cannot be created must fail")
		}
	})

	t.Run("parent directories", func(t *testing.T) {
		archive := filepath.Join(dir, "file.tar.gz")
		writeTarGz(t, archive, map[string]string{"nested/meilisearch": "binary"}, 0o755)
		hook(t, &mkdirAll, func(string, os.FileMode) error { return errors.New("mkdir") })
		if err := untargzTo(archive, filepath.Join(dir, "a")); err == nil {
			t.Fatal("a parent directory that cannot be created must fail")
		}
	})

	t.Run("unwritable target", func(t *testing.T) {
		archive := filepath.Join(dir, "denied.tar.gz")
		writeTarGz(t, archive, map[string]string{"meilisearch": "binary"}, 0o755)
		hook(t, &openFile, func(string, int, os.FileMode) (*os.File, error) {
			return nil, errors.New("denied")
		})
		if err := untargzTo(archive, filepath.Join(dir, "b")); err == nil {
			t.Fatal("a file that cannot be created must fail")
		}
	})

	t.Run("truncated entry content", func(t *testing.T) {
		// 条目头完整、正文在传输途中断掉：于是错误出现在拷贝阶段，
		// 而不是 tr.Next()。
		archive := filepath.Join(dir, "cut.tar.gz")
		writeTruncatedTarGz(t, archive, 4096)
		if err := untargzTo(archive, filepath.Join(dir, "c")); err == nil {
			t.Fatal("a truncated entry must fail")
		}
	})
}

func TestCopyFileReportsReadFailure(t *testing.T) {
	dir := t.TempDir()
	// 打开目录「成功」，读它失败——拷贝写入失败必须在 copyFile 里变成错误。
	hook(t, &openSource, func(path string) (*os.File, error) { return os.Open(path) })
	if err := copyFile(dir, filepath.Join(dir, "dst")); err == nil {
		t.Fatal("an unreadable source must fail")
	}
}

// writeTruncatedTarGz 生成一个「条目头完整、正文被截断」的 tar.gz：
// 只压缩 tar 流的前两个 512 字节块（条目头 + 头部数据），tar 头却声称条目
// 有 4096 字节。读取方因此会在拷贝正文时拿到 io.ErrUnexpectedEOF。
func writeTruncatedTarGz(t *testing.T, path string, size int) {
	t.Helper()
	var raw bytes.Buffer
	header := &tar.Header{Name: "meilisearch", Mode: 0o755, Size: int64(size), Typeflag: tar.TypeReg}
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(header); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := writer.Write([]byte(strings.Repeat("x", size))); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	full := raw.Bytes()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	gz := gzip.NewWriter(file)
	// 只写前 1024 字节：条目头（512）完整，正文只剩 512 字节。
	if _, err := gz.Write(full[:1024]); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// waitTask 的取消分支要在「请求已经成功返回之后」才触发：上下文若在请求之前
// 就被取消，HTTP 请求自己就会失败，select 根本轮不到。取消动作因此放在服务端
// 的响应里。
func TestWaitTaskReturnsWhenContextIsCanceledMidFlight(t *testing.T) {
	hook(t, &taskPollInterval, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := true
	client := newSearchClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"uid":9,"status":"enqueued"}`)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if first {
			first = false
			// 响应已经发出去之后再取消：否则取消会先把这次 HTTP 请求打断，
			// waitTask 就走不到那个 select 了。
			time.Sleep(20 * time.Millisecond)
			cancel()
		}
	}))

	if err := client.waitTask(ctx, 9); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitTask error = %v, want context.Canceled", err)
	}
}
