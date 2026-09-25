package search

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func makeTarGz(t *testing.T, path, name string, data []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar: %v", err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func makeZip(t *testing.T, path, name string, data []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	entry, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFetchTextFetchReleaseAndLatestAsset(t *testing.T) {
	textServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/not-ok" {
			http.Error(w, "no", http.StatusTeapot)
			return
		}
		_, _ = w.Write([]byte("hello checksum"))
	}))
	defer textServer.Close()
	if got, err := fetchText(textServer.URL + "/ok"); err != nil || got != "hello checksum" {
		t.Fatalf("fetchText = (%q, %v)", got, err)
	}
	if _, err := fetchText(textServer.URL + "/not-ok"); err == nil {
		t.Fatal("non-200 fetchText should fail")
	}
	if _, err := fetchText("http://[::1"); err == nil {
		t.Fatal("invalid fetchText URL should fail")
	}

	releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad-json":
			_, _ = w.Write([]byte("{"))
		case "/not-ok":
			http.Error(w, "no", http.StatusInternalServerError)
		default:
			assetName := fmt.Sprintf("meilisearch-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.2.3",
				"assets": []map[string]any{{
					"name": assetName, "browser_download_url": "https://github.com/x/y/" + assetName,
					"size": 1, "digest": "sha256:" + strings.Repeat("a", 64),
				}},
			})
		}
	}))
	defer releaseServer.Close()
	release, err := fetchRelease(releaseServer.URL + "/ok")
	if err != nil {
		t.Fatalf("fetchRelease: %v", err)
	}
	if release.TagName != "v1.2.3" || len(release.Assets) != 1 {
		t.Fatalf("fetchRelease = %+v", release)
	}
	if _, err := fetchRelease(releaseServer.URL + "/not-ok"); err == nil {
		t.Fatal("non-200 fetchRelease should fail")
	}
	if _, err := fetchRelease(releaseServer.URL + "/bad-json"); err == nil {
		t.Fatal("invalid JSON fetchRelease should fail")
	}
	if _, err := fetchRelease("http://[::1"); err == nil {
		t.Fatal("invalid fetchRelease URL should fail")
	}

	oldSources := githubReleaseSources
	githubReleaseSources = []releaseSource{{metadataURL: releaseServer.URL + "/ok", downloadPrefix: releaseServer.URL + "/"}}
	defer func() { githubReleaseSources = oldSources }()
	gotRelease, asset, source, err := latestAsset()
	if err != nil {
		t.Fatalf("latestAsset: %v", err)
	}
	if gotRelease.TagName != "v1.2.3" || asset.Name == "" || source.metadataURL == "" {
		t.Fatalf("latestAsset = (%+v, %+v, %+v)", gotRelease, asset, source)
	}
}

func TestDownloadCopyAndInstallRelease(t *testing.T) {
	payload := []byte("#!/bin/sh\necho meilisearch\n")
	archivePath := filepath.Join(t.TempDir(), "asset.tar.gz")
	makeTarGz(t, archivePath, "meilisearch", payload)
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archiveBytes)
	digest := hex.EncodeToString(sum[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/asset.tar.gz":
			w.Header().Set("Content-Length", fmt.Sprint(len(archiveBytes)))
			_, _ = w.Write(archiveBytes)
		case "/missing":
			http.NotFound(w, r)
		case "/too-big":
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("x"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	downloaded := filepath.Join(t.TempDir(), "asset.tar.gz")
	if err := downloadFile(server.URL+"/asset.tar.gz", downloaded); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	if body, _ := os.ReadFile(downloaded); string(body) != string(archiveBytes) {
		t.Fatal("downloaded bytes differ")
	}
	if err := downloadFile(server.URL+"/missing", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("non-200 downloadFile should fail")
	}

	if err := copyFile(downloaded, filepath.Join(t.TempDir(), "copy")); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	if err := copyFile(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatal("copyFile(missing) should fail")
	}
	if _, err := extractBinary(archivePath, "meilisearch-linux-amd64.tar.gz"); err != nil {
		t.Fatalf("extractBinary(tar): %v", err)
	}
	zipPath := filepath.Join(t.TempDir(), "asset.zip")
	makeZip(t, zipPath, "meilisearch", payload)
	if _, err := extractBinary(zipPath, "asset.zip"); err != nil {
		t.Fatalf("extractBinary(zip): %v", err)
	}
	if got, err := extractBinary("/plain/binary", "meilisearch-linux-amd64"); err != nil || got != "/plain/binary" {
		t.Fatalf("extractBinary(plain) = (%q, %v)", got, err)
	}

	target := filepath.Join(t.TempDir(), "bin", "meilisearch")
	asset := releaseAsset{Name: "meilisearch-linux-amd64.tar.gz", BrowserDownloadURL: server.URL + "/asset.tar.gz", Digest: "sha256:" + digest}
	if err := installRelease(target, githubRelease{TagName: "v1"}, asset, releaseSource{}); err != nil {
		t.Fatalf("installRelease: %v", err)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != string(payload) {
		t.Fatalf("installed binary = (%q, %v)", body, err)
	}
}

func TestExpectedDigestErrorPathsAndExtractErrors(t *testing.T) {
	source := releaseSource{metadataURL: "https://api.github.com/x", downloadPrefix: "https://github.com/x/"}
	release := githubRelease{TagName: "v1", Assets: []releaseAsset{{Name: "other", BrowserDownloadURL: "https://evil.test/file"}}}

	// No digest and no checksum -> fail closed.
	if _, _, err := expectedDigest(release, releaseAsset{Name: "meilisearch-linux-amd64"}, source); err == nil {
		t.Fatal("missing digest should fail closed")
	}

	// A per-asset checksum file outside the pinned prefix is refused before fetch.
	release.Assets = []releaseAsset{{Name: "meilisearch-linux-amd64.sha256", BrowserDownloadURL: "https://evil.test/file"}}
	if _, _, err := expectedDigest(release, releaseAsset{Name: "meilisearch-linux-amd64"}, source); err == nil {
		t.Fatal("foreign checksum URL should be refused")
	}

	// A combined checksum URL outside the pinned prefix is refused.
	release.Assets = []releaseAsset{{Name: "SHA256SUMS", BrowserDownloadURL: "https://evil.test/file"}}
	if _, _, err := expectedDigest(release, releaseAsset{Name: "meilisearch-linux-amd64"}, source); err == nil {
		t.Fatal("foreign combined checksum URL should be refused")
	}

	if err := os.WriteFile(filepath.Join(t.TempDir(), "not-an-archive.zip"), []byte("nope"), 0o600); err == nil {
		_ = err
	}
	if _, err := extractBinary(filepath.Join(t.TempDir(), "missing.zip"), "missing.zip"); err == nil {
		t.Fatal("extractBinary(missing zip) should fail")
	}
}
