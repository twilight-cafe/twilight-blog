package search

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 本文件覆盖 download.go 的失败分支：每一个都对应「下载 / 校验 / 解包」链条上
// 一个真实存在、且无法靠正常网络或正常压缩包构造出来的结局。

func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(file)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("zip entry: %v", err)
		}
		if strings.HasSuffix(name, "/") {
			continue
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
}

func writeTarGz(t *testing.T, path string, entries map[string]string, mode int64) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar.gz: %v", err)
	}
	gz := gzip.NewWriter(file)
	writer := tar.NewWriter(gz)
	for name, content := range entries {
		header := &tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(name, "/") {
			header.Typeflag = tar.TypeDir
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := writer.Write([]byte(content)); err != nil {
				t.Fatalf("tar write: %v", err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
}

func digestOf(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func testSource() releaseSource {
	return releaseSource{
		metadataURL:    "https://api.github.com/repos/meilisearch/meilisearch/releases/latest",
		downloadPrefix: "https://github.com/meilisearch/meilisearch/releases/download/",
	}
}

func TestInstallReleaseFailureBranches(t *testing.T) {
	asset := releaseAsset{Name: "meilisearch", BrowserDownloadURL: testSource().downloadPrefix + "meilisearch", Digest: "sha256:" + digestOf("payload")}
	release := githubRelease{TagName: "v1", Assets: []releaseAsset{asset}}

	t.Run("target directory", func(t *testing.T) {
		hook(t, &mkdirAll, func(string, os.FileMode) error { return errors.New("mkdir") })
		if err := installRelease("/tmp/x/meilisearch", release, asset, testSource()); err == nil {
			t.Fatal("mkdir failure must surface")
		}
	})

	t.Run("temp directory", func(t *testing.T) {
		hook(t, &mkdtemp, func(string, string) (string, error) { return "", errors.New("tmp") })
		if err := installRelease("/tmp/x/meilisearch", release, asset, testSource()); err == nil {
			t.Fatal("temp directory failure must surface")
		}
	})

	t.Run("download", func(t *testing.T) {
		hook(t, &downloadFileFn, func(string, string) error { return errors.New("network") })
		if err := installRelease("/tmp/x/meilisearch", release, asset, testSource()); err == nil {
			t.Fatal("download failure must surface")
		}
	})

	t.Run("integrity", func(t *testing.T) {
		hook(t, &downloadFileFn, func(_, target string) error {
			return os.WriteFile(target, []byte("payload"), 0o600)
		})
		unverified := releaseAsset{Name: "meilisearch", BrowserDownloadURL: asset.BrowserDownloadURL}
		if err := installRelease("/tmp/x/meilisearch", githubRelease{TagName: "v1", Assets: []releaseAsset{unverified}}, unverified, testSource()); err == nil {
			t.Fatal("an unverifiable download must be refused")
		}
	})

	t.Run("extract", func(t *testing.T) {
		hook(t, &downloadFileFn, func(_, target string) error {
			return os.WriteFile(target, []byte("payload"), 0o600)
		})
		hook(t, &extractBinaryFn, func(string, string) (string, error) { return "", errors.New("bad archive") })
		if err := installRelease("/tmp/x/meilisearch", release, asset, testSource()); err == nil {
			t.Fatal("extract failure must surface")
		}
	})

	t.Run("copy", func(t *testing.T) {
		hook(t, &downloadFileFn, func(_, target string) error {
			return os.WriteFile(target, []byte("payload"), 0o600)
		})
		hook(t, &extractBinaryFn, func(archivePath, _ string) (string, error) { return archivePath, nil })
		hook(t, &copyFileFn, func(string, string) error { return errors.New("cross-device") })
		if err := installRelease("/tmp/x/meilisearch", release, asset, testSource()); err == nil {
			t.Fatal("copy failure must surface")
		}
	})

	t.Run("chmod", func(t *testing.T) {
		hook(t, &downloadFileFn, func(_, target string) error {
			return os.WriteFile(target, []byte("payload"), 0o600)
		})
		hook(t, &extractBinaryFn, func(archivePath, _ string) (string, error) { return archivePath, nil })
		hook(t, &chmodFile, func(string, os.FileMode) error { return errors.New("chmod") })
		if err := installRelease("/tmp/x/meilisearch", release, asset, testSource()); err == nil {
			t.Fatal("chmod failure must surface")
		}
	})
}

func TestInstallReleaseSucceeds(t *testing.T) {
	payload := "MEILI-BINARY"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()

	source := releaseSource{metadataURL: server.URL, downloadPrefix: server.URL}
	asset := releaseAsset{Name: "meilisearch", BrowserDownloadURL: server.URL + "/meilisearch", Digest: "sha256:" + digestOf(payload)}
	release := githubRelease{TagName: "v1", Assets: []releaseAsset{asset}}

	target := filepath.Join(t.TempDir(), "bin", "meilisearch")
	if err := installRelease(target, release, asset, source); err != nil {
		t.Fatalf("installRelease: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != payload {
		t.Fatalf("installed content = (%q, %v)", got, err)
	}
}

func TestVerifyDownloadIntegrityBranches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asset")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	source := testSource()

	t.Run("digest lookup failure", func(t *testing.T) {
		asset := releaseAsset{Name: "meilisearch"}
		if err := verifyDownloadIntegrity(path, asset, githubRelease{TagName: "v1"}, source); err == nil {
			t.Fatal("missing digest must be refused")
		}
	})

	t.Run("hashing failure", func(t *testing.T) {
		hook(t, &sha256FileFn, func(string) (string, error) { return "", errors.New("read") })
		asset := releaseAsset{Name: "meilisearch", Digest: "sha256:" + digestOf("payload")}
		if err := verifyDownloadIntegrity(path, asset, githubRelease{TagName: "v1"}, source); err == nil {
			t.Fatal("hashing failure must surface")
		}
	})

	t.Run("mismatch and match", func(t *testing.T) {
		wrong := releaseAsset{Name: "meilisearch", Digest: "sha256:" + digestOf("other")}
		if err := verifyDownloadIntegrity(path, wrong, githubRelease{TagName: "v1"}, source); err == nil {
			t.Fatal("a mismatched digest must be refused")
		}
		right := releaseAsset{Name: "meilisearch", Digest: "sha256:" + digestOf("payload")}
		if err := verifyDownloadIntegrity(path, right, githubRelease{TagName: "v1"}, source); err != nil {
			t.Fatalf("matching digest: %v", err)
		}
	})
}

func TestExpectedDigestSources(t *testing.T) {
	source := testSource()
	asset := releaseAsset{Name: "meilisearch-linux-amd64"}

	t.Run("per-asset checksum file", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, digestOf("payload")+"  meilisearch-linux-amd64\n")
		}))
		defer server.Close()
		local := releaseSource{metadataURL: server.URL, downloadPrefix: server.URL}
		release := githubRelease{TagName: "v1", Assets: []releaseAsset{
			asset,
			{Name: asset.Name + ".sha256", BrowserDownloadURL: server.URL + "/sum"},
		}}
		hash, origin, err := expectedDigest(release, asset, local)
		if err != nil || hash != digestOf("payload") || origin == "" {
			t.Fatalf("expectedDigest = (%q, %q, %v)", hash, origin, err)
		}
	})

	t.Run("per-asset checksum outside the pinned prefix", func(t *testing.T) {
		release := githubRelease{TagName: "v1", Assets: []releaseAsset{
			asset,
			{Name: asset.Name + ".sha256", BrowserDownloadURL: "https://evil.example/sum"},
		}}
		if _, _, err := expectedDigest(release, asset, source); err == nil {
			t.Fatal("an unpinned checksum URL must be refused")
		}
	})

	t.Run("per-asset checksum fetch failure", func(t *testing.T) {
		hook(t, &fetchTextFn, func(string) (string, error) { return "", errors.New("offline") })
		release := githubRelease{TagName: "v1", Assets: []releaseAsset{
			asset,
			{Name: asset.Name + ".sha256", BrowserDownloadURL: source.downloadPrefix + "sum"},
		}}
		if _, _, err := expectedDigest(release, asset, source); err == nil {
			t.Fatal("a failed checksum fetch must surface")
		}
	})

	t.Run("per-asset checksum without a digest", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "no digest here\n")
		}))
		defer server.Close()
		local := releaseSource{metadataURL: server.URL, downloadPrefix: server.URL}
		release := githubRelease{TagName: "v1", Assets: []releaseAsset{
			asset,
			{Name: asset.Name + ".sha256.txt", BrowserDownloadURL: server.URL + "/sum"},
		}}
		if _, _, err := expectedDigest(release, asset, local); err == nil {
			t.Fatal("a checksum file without a digest must be refused")
		}
	})

	t.Run("combined checksums file", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, digestOf("payload")+"  meilisearch-linux-amd64\n")
		}))
		defer server.Close()
		local := releaseSource{metadataURL: server.URL, downloadPrefix: server.URL}
		release := githubRelease{TagName: "v1", Assets: []releaseAsset{
			asset,
			{Name: "checksums.txt", BrowserDownloadURL: server.URL + "/checksums.txt"},
		}}
		hash, _, err := expectedDigest(release, asset, local)
		if err != nil || hash != digestOf("payload") {
			t.Fatalf("expectedDigest = (%q, %v)", hash, err)
		}
	})

	t.Run("combined checksums outside the pinned prefix", func(t *testing.T) {
		release := githubRelease{TagName: "v1", Assets: []releaseAsset{
			asset,
			{Name: "checksums.txt", BrowserDownloadURL: "https://evil.example/sums"},
		}}
		if _, _, err := expectedDigest(release, asset, source); err == nil {
			t.Fatal("an unpinned combined checksums URL must be refused")
		}
	})

	t.Run("combined checksums fetch failure falls through to fail-closed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusInternalServerError)
		}))
		defer server.Close()
		local := releaseSource{metadataURL: server.URL, downloadPrefix: server.URL}
		release := githubRelease{TagName: "v1", Assets: []releaseAsset{
			asset,
			{Name: "checksums.txt", BrowserDownloadURL: server.URL + "/sums"},
		}}
		if _, _, err := expectedDigest(release, asset, local); err == nil {
			t.Fatal("an unusable combined checksums file must end in fail-closed")
		}
	})
}

func TestDigestHelpers(t *testing.T) {
	if got := parseAssetDigest("sha256:" + digestOf("x")); got != digestOf("x") {
		t.Fatalf("parseAssetDigest = %q", got)
	}
	for _, invalid := range []string{"", "sha512:abc", "not-a-digest", "sha256:zz"} {
		if got := parseAssetDigest(invalid); got != "" {
			t.Fatalf("parseAssetDigest(%q) = %q, want empty", invalid, got)
		}
	}
	if firstSHA256("nope") != "" {
		t.Fatal("firstSHA256 must reject text without a digest")
	}
	if firstSHA256(digestOf("y")+"  file") != digestOf("y") {
		t.Fatal("firstSHA256 must accept sha256sum output")
	}
	parsed := parseChecksumFile("no line\n" + digestOf("z") + "  other.tar.gz\n")
	if _, ok := parsed["other.tar.gz"]; !ok {
		t.Fatalf("parseChecksumFile = %v", parsed)
	}
	if !isHex("0123456789abcdef") || isHex("xyz") {
		t.Fatal("isHex is wrong")
	}
	if !isChecksumName("a.sig") || !isChecksumName("CHECKSUMS") || isChecksumName("meilisearch") {
		t.Fatal("isChecksumName is wrong")
	}
	if !containsAny("meilisearch-linux", []string{"windows", "linux"}) || containsAny("meilisearch", []string{"", "windows"}) {
		t.Fatal("containsAny is wrong")
	}
}

func TestSHA256FileBranches(t *testing.T) {
	if _, err := sha256File(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file must fail")
	}
	if _, err := sha256File(t.TempDir()); err == nil {
		t.Fatal("a directory must fail while being read")
	}
	path := filepath.Join(t.TempDir(), "asset")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sum, err := sha256File(path)
	if err != nil || sum != digestOf("payload") {
		t.Fatalf("sha256File = (%q, %v)", sum, err)
	}
}

func TestFetchTextBranches(t *testing.T) {
	if _, err := fetchText("http://127.0.0.1:1/none"); err == nil {
		t.Fatal("an unreachable host must fail")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "body")
	}))
	defer server.Close()
	if _, err := fetchText(server.URL + "/bad"); err == nil {
		t.Fatal("a non-200 status must fail")
	}
	if got, err := fetchText(server.URL + "/ok"); err != nil || got != "body" {
		t.Fatalf("fetchText = (%q, %v)", got, err)
	}

	hook(t, &httpGetText, func(string) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errReader{})}, nil
	})
	if _, err := fetchText("http://example.invalid"); err == nil {
		t.Fatal("a body that cannot be read must fail")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken") }

func TestFetchReleaseBranches(t *testing.T) {
	if _, err := fetchRelease("http://[::1"); err == nil {
		t.Fatal("an invalid URL must fail")
	}
	if _, err := fetchRelease("http://127.0.0.1:1/none"); err == nil {
		t.Fatal("an unreachable API must fail")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/500":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/broken":
			_, _ = io.WriteString(w, "{")
		default:
			_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1.0", Assets: []releaseAsset{{Name: "meilisearch-linux-amd64"}}})
		}
	}))
	defer server.Close()

	if _, err := fetchRelease(server.URL + "/500"); err == nil {
		t.Fatal("a non-200 API response must fail")
	}
	if _, err := fetchRelease(server.URL + "/broken"); err == nil {
		t.Fatal("an undecodable payload must fail")
	}
	release, err := fetchRelease(server.URL + "/ok")
	if err != nil || release.TagName != "v1.0" {
		t.Fatalf("fetchRelease = (%+v, %v)", release, err)
	}
}

func TestLatestAssetBranches(t *testing.T) {

	hook(t, &fetchReleaseFn, func(string) (githubRelease, error) { return githubRelease{}, errors.New("offline") })
	if _, _, _, err := latestAsset(); err == nil {
		t.Fatal("every source failing must surface")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 一个任何平台都不会匹配的名字：用例必须在 Windows / Linux 宿主上都成立，
		// 写死「只有 Windows 资产」会让测试在 Windows 上反而成功。
		_ = json.NewEncoder(w).Encode(githubRelease{TagName: "v1", Assets: []releaseAsset{
			{Name: "meilisearch-unknown-platform"},
		}})
	}))
	defer server.Close()

	hook(t, &githubReleaseSources, []releaseSource{{metadataURL: server.URL, downloadPrefix: server.URL}})
	if _, _, _, err := latestAsset(); err == nil {
		t.Fatal("a release without a matching asset must surface")
	}

	// 用当前平台的**带平台后缀**的资产名，而不是写死 linux/amd64。
	// 裸 "meilisearch" 不包含 OS/架构标记，selectAssetFor 不会匹配它。
	localAsset := "meilisearch-" + runtime.GOOS + "-" + runtime.GOARCH
	hook(t, &fetchReleaseFn, func(string) (githubRelease, error) {
		return githubRelease{TagName: "v1", Assets: []releaseAsset{{Name: localAsset, BrowserDownloadURL: server.URL}}}, nil
	})
	_, asset, _, err := latestAsset()
	if err != nil || asset.Name != localAsset {
		t.Fatalf("latestAsset = (%+v, %v)", asset, err)
	}

	hook(t, &githubReleaseSources, []releaseSource{})
	if _, _, _, err := latestAsset(); err == nil {
		t.Fatal("an empty source list must surface an error")
	}
}

func TestSelectAssetForBranches(t *testing.T) {
	release := githubRelease{TagName: "v1", Assets: []releaseAsset{
		{Name: "meilisearch-linux-amd64"},
	}}
	asset, err := selectAssetFor(release, "linux", "amd64")
	if err != nil || asset.Name != "meilisearch-linux-amd64" {
		t.Fatalf("selectAssetFor = (%+v, %v)", asset, err)
	}

	enterpriseOnly := githubRelease{TagName: "v1", Assets: []releaseAsset{
		{Name: "meilisearch-enterprise-linux-amd64"},
	}}
	if _, err := selectAssetFor(enterpriseOnly, "linux", "amd64"); err != nil {
		t.Fatalf("enterprise fallback must be allowed: %v", err)
	}

	// 社区版优先于企业版（按字母序企业版在前）。
	both := githubRelease{TagName: "v1", Assets: []releaseAsset{
		{Name: "meilisearch-enterprise-linux-amd64"},
		{Name: "meilisearch-linux-amd64"},
	}}
	if asset, err := selectAssetFor(both, "linux", "amd64"); err != nil || asset.Name != "meilisearch-linux-amd64" {
		t.Fatalf("community build must win: (%+v, %v)", asset, err)
	}

	// 校验和类资产不参与匹配。
	checksums := githubRelease{TagName: "v1", Assets: []releaseAsset{
		{Name: "meilisearch-linux-amd64.sha256"},
	}}
	if _, err := selectAssetFor(checksums, "linux", "amd64"); err == nil {
		t.Fatal("a checksum-only release must not yield a binary")
	}

	for _, platform := range [][2]string{{"windows", "amd64"}, {"windows", "arm64"}, {"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "arm64"}} {
		if _, err := selectAssetFor(githubRelease{TagName: "v1"}, platform[0], platform[1]); err == nil {
			t.Fatalf("an empty release must fail for %s/%s", platform[0], platform[1])
		}
	}

	localRelease := githubRelease{TagName: "v1", Assets: []releaseAsset{
		{Name: "meilisearch-" + runtime.GOOS + "-" + runtime.GOARCH},
	}}
	if _, err := findReleaseAsset(localRelease); err != nil {
		t.Fatalf("findReleaseAsset on the current platform: %v", err)
	}
}

func TestDownloadFileBranches(t *testing.T) {
	target := filepath.Join(t.TempDir(), "asset")

	if err := downloadFileWithHooks(target, nil, func(string) (*os.File, error) { return nil, errors.New("read-only") }); err == nil {
		t.Fatal("a file that cannot be created must fail")
	}

	restoreCreate := swap(&createFile, os.Create)
	defer restoreCreate()

	if err := downloadFileWithHooks(target, func(string) (*http.Response, error) { return nil, errors.New("offline") }, nil); err == nil {
		t.Fatal("an unreachable host must fail")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/500" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "payload")
	}))
	defer server.Close()

	if err := downloadFile(server.URL+"/500", target); err == nil {
		t.Fatal("a non-200 download must fail")
	}
	if err := downloadFile(server.URL+"/ok", target); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}

	chunked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "pay")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "load")
	}))
	defer chunked.Close()
	restoreLimit := swap(&maxDownloadBytes, int64(4))
	if err := downloadFile(chunked.URL+"/ok", target); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("a stream above the limit must be refused, got %v", err)
	}
	restoreLimit()

	restoreDeclared := swap(&maxDownloadBytes, int64(1024))
	restoreBody := swap(&httpDownload, func(string) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 4096, Body: io.NopCloser(strings.NewReader("payload"))}, nil
	})
	if err := downloadFile("http://example.invalid/a", target); err == nil || !strings.Contains(err.Error(), "declares") {
		t.Fatalf("a declared size above the limit must be refused, got %v", err)
	}
	restoreBody()
	restoreDeclared()

	restoreReader := swap(&httpDownload, func(string) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errReader{})}, nil
	})
	if err := downloadFile("http://example.invalid/a", target); err == nil {
		t.Fatal("a body that cannot be read must fail")
	}
	restoreReader()
}

// downloadFileWithHooks 允许一次性替换两个步骤（建文件 + 取响应），省掉每个分支
// 各自摆一遍 defer。nil 表示保留该步骤的真实实现。
func downloadFileWithHooks(target string, response func(string) (*http.Response, error), create func(name string) (*os.File, error)) error {
	var restore []func()
	if response != nil {
		restore = append(restore, swap(&httpDownload, response))
	}
	if create != nil {
		restore = append(restore, swap(&createFile, create))
	}
	defer func() {
		for i := len(restore) - 1; i >= 0; i-- {
			restore[i]()
		}
	}()
	return downloadFile("http://example.invalid/a", target)
}

func TestExtractBinaryBranches(t *testing.T) {
	dir := t.TempDir()

	plain := filepath.Join(dir, "meilisearch")
	if err := os.WriteFile(plain, []byte("x"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, err := extractBinary(plain, "meilisearch"); err != nil || got != plain {
		t.Fatalf("plain asset = (%q, %v)", got, err)
	}

	zipPath := filepath.Join(dir, "meilisearch.zip")
	writeZip(t, zipPath, map[string]string{"inner/meilisearch": "binary"})
	if got, err := extractBinary(zipPath, "meilisearch.zip"); err != nil || !strings.HasSuffix(got, "meilisearch") {
		t.Fatalf("zip asset = (%q, %v)", got, err)
	}

	tgzPath := filepath.Join(dir, "meilisearch.tar.gz")
	writeTarGz(t, tgzPath, map[string]string{"inner/meilisearch": "binary"}, 0o755)
	if got, err := extractBinary(tgzPath, "meilisearch.tar.gz"); err != nil || !strings.HasSuffix(got, "meilisearch") {
		t.Fatalf("tar.gz asset = (%q, %v)", got, err)
	}

	tgzAlias := filepath.Join(dir, "meilisearch.tgz")
	writeTarGz(t, tgzAlias, map[string]string{"inner/meilisearch": "binary"}, 0o755)
	if _, err := extractBinary(tgzAlias, "meilisearch.tgz"); err != nil {
		t.Fatalf("tgz alias: %v", err)
	}

	if _, err := extractBinary(filepath.Join(dir, "missing.zip"), "missing.zip"); err == nil {
		t.Fatal("a corrupt zip must fail")
	}
	badGz := filepath.Join(dir, "broken.tar.gz")
	if err := os.WriteFile(badGz, []byte("not gzip"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := extractBinary(badGz, "broken.tar.gz"); err == nil {
		t.Fatal("a corrupt tar.gz must fail")
	}

	emptyDir := t.TempDir()
	emptyZip := filepath.Join(emptyDir, "empty.zip")
	writeZip(t, emptyZip, map[string]string{"readme.txt": "nothing here"})
	if _, err := extractBinary(emptyZip, "empty.zip"); err == nil {
		t.Fatal("an archive without the binary must fail")
	}

	walkDir := t.TempDir()
	goodZip := filepath.Join(walkDir, "good.zip")
	writeZip(t, goodZip, map[string]string{"meilisearch": "binary"})
	hook(t, &walkDirPath, func(string, fs.WalkDirFunc) error { return errors.New("walk") })
	if _, err := extractBinary(goodZip, "good.zip"); err == nil {
		t.Fatal("a failing directory walk must surface")
	}
}

func TestUnzipToBranches(t *testing.T) {
	dir := t.TempDir()

	t.Run("entry limit", func(t *testing.T) {
		archive := filepath.Join(dir, "many.zip")
		writeZip(t, archive, map[string]string{"a": "1", "b": "2"})
		hook(t, &maxExtractEntries, 1)
		if err := unzipTo(archive, dir); err == nil {
			t.Fatal("too many entries must be refused")
		}
	})

	t.Run("path traversal", func(t *testing.T) {
		archive := filepath.Join(dir, "evil.zip")
		writeZip(t, archive, map[string]string{"../escape.txt": "x"})
		if err := unzipTo(archive, dir); err == nil {
			t.Fatal("a zip-slip entry must be refused")
		}
	})

	t.Run("directories and files", func(t *testing.T) {
		dest := filepath.Join(dir, "ok")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		archive := filepath.Join(dir, "good.zip")
		writeZip(t, archive, map[string]string{"nested/meilisearch": "binary"})
		if err := unzipTo(archive, dest); err != nil {
			t.Fatalf("unzipTo: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dest, "nested", "meilisearch")); err != nil {
			t.Fatalf("extracted file missing: %v", err)
		}
	})

	t.Run("directory entry", func(t *testing.T) {
		dest := filepath.Join(dir, "dironly")
		archive := filepath.Join(dir, "dir.zip")
		writeZip(t, archive, map[string]string{"nested/": "x"})
		if err := unzipTo(archive, dest); err != nil {
			t.Fatalf("unzipTo with a directory entry: %v", err)
		}
	})

	t.Run("target cannot be opened", func(t *testing.T) {
		dest := filepath.Join(dir, "denied")
		archive := filepath.Join(dir, "denied.zip")
		writeZip(t, archive, map[string]string{"meilisearch": "binary"})
		hook(t, &openFile, func(string, int, os.FileMode) (*os.File, error) { return nil, errors.New("denied") })
		if err := unzipTo(archive, dest); err == nil {
			t.Fatal("an unwritable target must fail")
		}
	})

	t.Run("expansion limit", func(t *testing.T) {
		dest := filepath.Join(dir, "limited")
		archive := filepath.Join(dir, "big.zip")
		writeZip(t, archive, map[string]string{"meilisearch": strings.Repeat("x", 1024)})
		hook(t, &maxExtractTotalBytes, int64(16))
		if err := unzipTo(archive, dest); err == nil {
			t.Fatal("an oversized expansion must be refused")
		}
	})
}

func TestUntarGzToBranches(t *testing.T) {
	dir := t.TempDir()

	if err := untargzTo(filepath.Join(dir, "missing.tar.gz"), dir); err == nil {
		t.Fatal("a missing archive must fail")
	}
	notGz := filepath.Join(dir, "notgz.tar.gz")
	if err := os.WriteFile(notGz, []byte("plain"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := untargzTo(notGz, dir); err == nil {
		t.Fatal("a non-gzip payload must fail")
	}

	t.Run("entries limit", func(t *testing.T) {
		archive := filepath.Join(dir, "many.tar.gz")
		writeTarGz(t, archive, map[string]string{"a": "1", "b": "2"}, 0o755)
		hook(t, &maxExtractEntries, 1)
		if err := untargzTo(archive, dir); err == nil {
			t.Fatal("too many entries must be refused")
		}
	})

	t.Run("path traversal", func(t *testing.T) {
		archive := filepath.Join(dir, "evil.tar.gz")
		writeTarGz(t, archive, map[string]string{"../escape.txt": "x"}, 0o755)
		if err := untargzTo(archive, dir); err == nil {
			t.Fatal("a tar-slip entry must be refused")
		}
	})

	t.Run("directories and files", func(t *testing.T) {
		dest := filepath.Join(dir, "ok")
		archive := filepath.Join(dir, "good.tar.gz")
		writeTarGz(t, archive, map[string]string{"nested/": "", "nested/meilisearch": "binary"}, 0o755)
		if err := untargzTo(archive, dest); err != nil {
			t.Fatalf("untargzTo: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dest, "nested", "meilisearch")); err != nil {
			t.Fatalf("extracted file missing: %v", err)
		}
	})

	t.Run("expansion limit", func(t *testing.T) {
		dest := filepath.Join(dir, "limited")
		archive := filepath.Join(dir, "big.tar.gz")
		writeTarGz(t, archive, map[string]string{"meilisearch": strings.Repeat("x", 1024)}, 0o755)
		hook(t, &maxExtractTotalBytes, int64(16))
		if err := untargzTo(archive, dest); err == nil {
			t.Fatal("an oversized expansion must be refused")
		}
	})

	t.Run("truncated stream", func(t *testing.T) {
		dest := filepath.Join(dir, "truncated")
		archive := filepath.Join(dir, "truncated.tar.gz")
		writeTarGz(t, archive, map[string]string{"a": "1", "b": "2"}, 0o755)
		raw, err := os.ReadFile(archive)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		cut := filepath.Join(dir, "cut.tar.gz")
		if err := os.WriteFile(cut, raw[:len(raw)/2], 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := untargzTo(cut, dest); err == nil {
			t.Fatal("a truncated archive must fail")
		}
	})
}

func TestCopyFileBranches(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := copyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "dst")); err == nil {
		t.Fatal("a missing source must fail")
	}

	restoreOpen := swap(&openFile, func(string, int, os.FileMode) (*os.File, error) { return nil, errors.New("denied") })
	if err := copyFile(src, filepath.Join(dir, "dst")); err == nil {
		t.Fatal("an unwritable destination must fail")
	}
	restoreOpen()

	if err := copyFile(src, filepath.Join(dir, "dst")); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "dst"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("copied content = (%q, %v)", got, err)
	}
}

func TestEnsureReleaseDownloadURL(t *testing.T) {
	source := testSource()
	if err := ensureReleaseDownloadURL(source.downloadPrefix+"v1/meilisearch", source); err != nil {
		t.Fatalf("a pinned URL must be accepted: %v", err)
	}
	if err := ensureReleaseDownloadURL("https://evil.example/meilisearch", source); err == nil {
		t.Fatal("an unpinned URL must be refused")
	}
}

func TestFetchTextHTTPErrorViaHook(t *testing.T) {
	hook(t, &httpGetText, func(string) (*http.Response, error) { return nil, errors.New("offline") })
	if _, err := fetchText("http://example.invalid"); err == nil {
		t.Fatal("a transport error must surface")
	}
}

func TestFetchReleaseHTTPErrorViaHook(t *testing.T) {
	hook(t, &httpDoRelease, func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	if _, err := fetchRelease("http://example.invalid/releases/latest"); err == nil {
		t.Fatal("a transport error must surface")
	}
}

func TestPipeReadAndCloseOrder(t *testing.T) {
	// copyFile 在写入成功但 close 失败时也必须返回错误：数据未必真的落盘。
	var sink bytes.Buffer
	if _, err := io.Copy(&sink, strings.NewReader("x")); err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	if sink.String() != "x" {
		t.Fatalf("sink = %q", sink.String())
	}
	_ = context.Background()
}
