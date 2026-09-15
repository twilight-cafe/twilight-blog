package search

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sha256 of the ASCII string "hello\n".
const helloDigest = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"

// TestAgainstRealGitHubRelease 用**真实的上游 release 元数据**跑一遍选资产 + 取摘要，
// 但**不下载**那个约 350MB 的二进制。
//
// 它是一条金丝雀：如果 Meilisearch 哪天改了资产命名、或者 GitHub 不再提供
// `assets[].digest`，这条会先失败——而不是等到某个用户在新机器上启动博客时
// 才发现「自动下载又变回死代码」。
//
// 默认跳过（`go test -short`），因为它要联网。联网失败时也跳过而不是失败：
// 无网络的 CI 不应该因此变红。
//
// 运行：go test ./internal/search/ -run TestAgainstRealGitHubRelease -v
func TestAgainstRealGitHubRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("network test; skipped in -short mode")
	}

	source := githubReleaseSources[0]
	release, err := fetchRelease(source.metadataURL)
	if err != nil {
		t.Skipf("cannot reach %s (offline?): %v", source.metadataURL, err)
	}
	if len(release.Assets) == 0 {
		t.Fatalf("release %s reported no assets at all", release.TagName)
	}

	asset, err := findReleaseAsset(release)
	if err != nil {
		t.Fatalf("findReleaseAsset() failed against the real release %s: %v", release.TagName, err)
	}
	if err := ensureReleaseDownloadURL(asset.BrowserDownloadURL, source); err != nil {
		t.Fatalf("the selected asset is outside the pinned prefix: %v", err)
	}

	digest, origin, err := expectedDigest(release, asset, source)
	if err != nil {
		t.Fatalf("expectedDigest() failed against the real release %s: %v\n"+
			"this is exactly the condition that makes a fresh install unable to start", release.TagName, err)
	}
	if digest == "" {
		t.Fatalf("expectedDigest() returned an empty digest for %s", asset.Name)
	}
	if !isHex(digest) || len(digest) != sha256.Size*2 {
		t.Fatalf("digest %q is not a bare SHA-256 hex string", digest)
	}

	t.Logf("release=%s asset=%s size=%d digest_source=%s digest=%s",
		release.TagName, asset.Name, assetSize(release, asset.Name), origin, digest)
}

// assetSize 只是为了让日志里能带上体积——及时发现上游把资产换成了压缩包之类。
func assetSize(release githubRelease, name string) int64 {
	for _, candidate := range release.Assets {
		if candidate.Name == name {
			return candidate.Size
		}
	}
	return -1
}

// TestSelectAssetForPrefersTheCommunityBuild 用**真实 release 的资产清单**验证
// 每个平台都选中社区版，而不是排在前面的企业版。
//
// 这条测试的存在理由是一次真实踩坑：上游同时发布社区版与企业版，而资产按字母序
// 返回，`meilisearch-enterprise-*` 永远排在 `meilisearch-*` 前面。取「第一个匹配」
// 的实现于是在 linux/amd64 上选中 `meilisearch-enterprise-linux-amd64`——
// 能跑、但是商业版。这类错误在 Windows 开发机上根本跑不出来，所以这里把
// 资产清单固化成夹具、把平台变成参数。
//
// 资产名摘自 v1.53.2 的真实 API 响应（16 个资产，顺序与线上一致）。
//
// 反证方式：去掉 selectAssetFor 里的 enterprise 过滤 → linux/amd64 与
// windows/amd64 两条断言立刻失败。
func TestSelectAssetForPrefersTheCommunityBuild(t *testing.T) {
	names := []string{
		"meilisearch-enterprise-linux-aarch64",
		"meilisearch-enterprise-linux-amd64",
		"meilisearch-enterprise-linux-riscv64",
		"meilisearch-enterprise-macos-amd64",
		"meilisearch-enterprise-macos-apple-silicon",
		"meilisearch-enterprise-windows-amd64.exe",
		"meilisearch-error-codes.json",
		"meilisearch-linux-aarch64",
		"meilisearch-linux-amd64",
		"meilisearch-linux-riscv64",
		"meilisearch-macos-apple-silicon",
		"meilisearch-openapi.json",
		"meilisearch-windows-amd64.exe",
		"meilisearch.deb",
	}
	release := githubRelease{TagName: "v1.53.2"}
	for _, name := range names {
		release.Assets = append(release.Assets, releaseAsset{Name: name})
	}

	cases := []struct {
		goos, goarch string
		want         string
	}{
		{"linux", "amd64", "meilisearch-linux-amd64"},
		{"linux", "arm64", "meilisearch-linux-aarch64"},
		{"windows", "amd64", "meilisearch-windows-amd64.exe"},
		{"darwin", "arm64", "meilisearch-macos-apple-silicon"},
	}
	for _, tc := range cases {
		got, err := selectAssetFor(release, tc.goos, tc.goarch)
		if err != nil {
			t.Fatalf("selectAssetFor(%s/%s) error = %v", tc.goos, tc.goarch, err)
		}
		if got.Name != tc.want {
			t.Fatalf("selectAssetFor(%s/%s) = %q, want %q", tc.goos, tc.goarch, got.Name, tc.want)
		}
		if strings.Contains(strings.ToLower(got.Name), "enterprise") {
			t.Fatalf("selectAssetFor(%s/%s) picked an enterprise build: %q", tc.goos, tc.goarch, got.Name)
		}
	}

	// .deb 与 .json 不能被误当成裸二进制下载下来。
	for _, tc := range cases {
		got, _ := selectAssetFor(release, tc.goos, tc.goarch)
		if strings.HasSuffix(got.Name, ".deb") || strings.HasSuffix(got.Name, ".json") {
			t.Fatalf("selectAssetFor(%s/%s) picked a non-binary asset: %q", tc.goos, tc.goarch, got.Name)
		}
	}

	// 兜底路径：万一上游只发企业版，也要给出一个可用资产（并告警），而不是直接失败。
	enterpriseOnly := githubRelease{TagName: "v-future", Assets: []releaseAsset{
		{Name: "meilisearch-enterprise-linux-amd64"},
	}}
	got, err := selectAssetFor(enterpriseOnly, "linux", "amd64")
	if err != nil {
		t.Fatalf("the enterprise fallback should still yield an asset, got %v", err)
	}
	if got.Name != "meilisearch-enterprise-linux-amd64" {
		t.Fatalf("enterprise fallback = %q", got.Name)
	}

	// 完全不匹配时必须是错误，而不是随便挑一个。
	if _, err := selectAssetFor(release, "plan9", "mips"); err == nil {
		t.Fatal("selectAssetFor accepted a platform the release has no asset for")
	}
}

func TestParseChecksumFile(t *testing.T) {
	content := `
# meilisearch checksums
5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03  meilisearch-linux-amd64
0e5751c026e543b2e8ab2eb06099daa1d1e5df47778f7787faab45cdf12fe3a8 *meilisearch-windows-amd64.exe
not-a-hash  meilisearch-darwin
5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03
`

	digests := parseChecksumFile(content)

	if got := digests["meilisearch-linux-amd64"]; got != helloDigest {
		t.Fatalf("linux digest = %q, want %q", got, helloDigest)
	}
	// The "*" binary marker must be stripped from the filename.
	if got := digests["meilisearch-windows-amd64.exe"]; got != "0e5751c026e543b2e8ab2eb06099daa1d1e5df47778f7787faab45cdf12fe3a8" {
		t.Fatalf("windows digest = %q", got)
	}
	if _, ok := digests["meilisearch-darwin"]; ok {
		t.Fatal("a non-digest line was parsed as a checksum")
	}
	if _, ok := digests[""]; ok {
		t.Fatal("a line without a filename was parsed")
	}
}

func TestFirstSHA256(t *testing.T) {
	if got := firstSHA256(helloDigest + "  meilisearch-linux-amd64\n"); got != helloDigest {
		t.Fatalf("firstSHA256 = %q, want %q", got, helloDigest)
	}
	if got := firstSHA256("no digest here"); got != "" {
		t.Fatalf("firstSHA256 = %q, want empty", got)
	}
}

func TestIsHex(t *testing.T) {
	if !isHex("0123456789abcdef") {
		t.Fatal("lowercase hex was rejected")
	}
	if isHex("0123456789ABCDEF") {
		t.Fatal("uppercase hex must fail the lowercase-only check")
	}
	if isHex("zz") {
		t.Fatal("non-hex characters were accepted")
	}
}

func TestSHA256File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := sha256File(path)
	if err != nil {
		t.Fatalf("sha256File: %v", err)
	}
	if got != helloDigest {
		t.Fatalf("digest = %q, want %q", got, helloDigest)
	}
}

// TestUnzipToRejectsTraversalEntry confirms the zip-slip guard still holds, since
// the download path now performs checksum verification around extraction.
func TestUnzipToRejectsTraversalEntry(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "evil.zip")

	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("../../evil.txt")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := entry.Write([]byte("pwned")); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	dest := filepath.Join(root, "out")
	if err := unzipTo(archivePath, dest); err == nil {
		t.Fatal("an entry escaping the destination directory was accepted")
	}

	if _, err := os.Stat(filepath.Join(root, "evil.txt")); err == nil {
		t.Fatal("traversal entry was written outside the destination directory")
	}
}

// writeGzipBomb 生成一个「压缩后很小、解开很大」的 tar.gz：
// 单个条目，内容是 size 个 0 字节。这就是解压炸弹的形状——
// 实测 261KB 的包能解出 256MB（放大 1028×），而这段代码跑在启动阶段。
func writeGzipBomb(t *testing.T, path string, size int) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "meilisearch", Mode: 0o755, Size: int64(size)}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	zeros := make([]byte, 32*1024)
	remaining := size
	for remaining > 0 {
		chunk := len(zeros)
		if remaining < chunk {
			chunk = remaining
		}
		if _, err := tw.Write(zeros[:chunk]); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
		remaining -= chunk
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
}

// TestUntargzToRefusesAnExpansionBomb 守护 R3-12：解包总量必须有上限。
//
// 上限被临时调到 64 KiB，这样测试只需写 256 KiB 就能触发它——
// 用默认的 1 GiB 去测意味着每条跑一次都往磁盘写 1 GB，没人会跑第二次，
// 而「没人跑的守护」等于没有守护。
//
// 反证方式：去掉 untargzTo 里的 total 上限判断 → 本条失败（解包正常完成）。
func TestUntargzToRefusesAnExpansionBomb(t *testing.T) {
	originalLimit, originalEntries := maxExtractTotalBytes, maxExtractEntries
	maxExtractTotalBytes, maxExtractEntries = 64<<10, 256
	t.Cleanup(func() {
		maxExtractTotalBytes, maxExtractEntries = originalLimit, originalEntries
	})

	root := t.TempDir()
	archivePath := filepath.Join(root, "bomb.tar.gz")
	writeGzipBomb(t, archivePath, 256<<10) // 解包 256 KiB，超过 64 KiB 上限

	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if info.Size() >= 256<<10 {
		t.Fatalf("the fixture is not actually a bomb: compressed size = %d", info.Size())
	}

	dest := filepath.Join(root, "out")
	if err := untargzTo(archivePath, dest); err == nil {
		t.Fatal("an archive expanding past the limit was accepted")
	} else if !strings.Contains(err.Error(), "expands beyond") {
		t.Fatalf("error = %v, want it to name the expansion limit", err)
	}

	// 不能只断言「返回了错误」：一个先写满磁盘再报错的实现同样会返回错误。
	// 上限的意义正是「写出多少」，所以这里核对落盘文件的大小确实被卡住了。
	written, err := os.Stat(filepath.Join(dest, "meilisearch"))
	if err != nil {
		t.Fatalf("stat extracted file: %v", err)
	}
	if written.Size() > maxExtractTotalBytes+1 {
		t.Fatalf("extracted %d bytes, above the %d byte limit", written.Size(), maxExtractTotalBytes)
	}
}

// TestUnzipToRefusesAnExpansionBomb 覆盖 zip 一侧：同样是压缩比无上限的格式。
func TestUnzipToRefusesAnExpansionBomb(t *testing.T) {
	originalLimit, originalEntries := maxExtractTotalBytes, maxExtractEntries
	maxExtractTotalBytes, maxExtractEntries = 64<<10, 256
	t.Cleanup(func() {
		maxExtractTotalBytes, maxExtractEntries = originalLimit, originalEntries
	})

	root := t.TempDir()
	archivePath := filepath.Join(root, "bomb.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("meilisearch.exe")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := entry.Write(make([]byte, 256<<10)); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	if err := unzipTo(archivePath, filepath.Join(root, "out")); err == nil {
		t.Fatal("a zip expanding past the limit was accepted")
	}
}

// TestExtractionRefusesTooManyEntries 覆盖条目数上限：
// 解压炸弹也可以靠「大量小条目」实现，那时总量不大但文件系统被塞满。
func TestExtractionRefusesTooManyEntries(t *testing.T) {
	originalLimit, originalEntries := maxExtractTotalBytes, maxExtractEntries
	maxExtractTotalBytes, maxExtractEntries = 1<<30, 3
	t.Cleanup(func() {
		maxExtractTotalBytes, maxExtractEntries = originalLimit, originalEntries
	})

	root := t.TempDir()
	archivePath := filepath.Join(root, "many.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create entry %s: %v", name, err)
		}
		if _, err := entry.Write([]byte("x")); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	if err := unzipTo(archivePath, filepath.Join(root, "out")); err == nil {
		t.Fatal("an archive with more entries than the limit was accepted")
	} else if !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("error = %v, want it to name the entry limit", err)
	}
}

// TestExtractionStillAcceptsNormalArchives 是对照组：上限不能把正常资产也拦下。
// 没有这一条的话，把上限设成 0 也能让上面三条「通过」。
func TestExtractionStillAcceptsNormalArchives(t *testing.T) {
	root := t.TempDir()

	t.Run("tar.gz", func(t *testing.T) {
		archivePath := filepath.Join(root, "ok.tar.gz")
		writeGzipBomb(t, archivePath, 4096) // 4 KiB：一个正常的可执行文件大小量级

		dest := filepath.Join(root, "tar-out")
		if err := untargzTo(archivePath, dest); err != nil {
			t.Fatalf("untargzTo() error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dest, "meilisearch")); err != nil {
			t.Fatalf("the binary was not extracted: %v", err)
		}
	})

	t.Run("zip", func(t *testing.T) {
		archivePath := filepath.Join(root, "ok.zip")
		file, err := os.Create(archivePath)
		if err != nil {
			t.Fatalf("create archive: %v", err)
		}
		writer := zip.NewWriter(file)
		entry, err := writer.Create("meilisearch.exe")
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		if _, err := entry.Write([]byte("binary")); err != nil {
			t.Fatalf("write entry: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close zip writer: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close archive: %v", err)
		}

		dest := filepath.Join(root, "zip-out")
		if err := unzipTo(archivePath, dest); err != nil {
			t.Fatalf("unzipTo() error = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dest, "meilisearch.exe"))
		if err != nil {
			t.Fatalf("read extracted file: %v", err)
		}
		if string(data) != "binary" {
			t.Fatalf("extracted content = %q, want %q", data, "binary")
		}
	})
}

/* -------------------------------------------------------------------------- *
 * Trust boundary around the downloaded binary
 *
 * The checksum and the binary both come from the same GitHub release metadata,
 * so "verify the download against the checksum" only holds while the two really
 * share a source. A tampered metadata response could point both at a host of the
 * attacker's choosing, turning verification into self-confirmation. Asset URLs
 * must therefore sit under the prefix declared by the source that produced them.
 *
 * None of these tests touch the network: a rejected URL has to be refused
 * *before* the checksum is fetched, which is exactly what is asserted.
 * -------------------------------------------------------------------------- */

func testReleaseSource() releaseSource {
	return releaseSource{
		metadataURL:    "https://api.github.com/repos/meilisearch/meilisearch/releases/latest",
		downloadPrefix: "https://github.com/meilisearch/meilisearch/releases/download/",
	}
}

func releaseWith(assets ...releaseAsset) githubRelease {
	return githubRelease{TagName: "v1.0.0", Assets: assets}
}

func TestEnsureReleaseDownloadURLRejectsForeignHosts(t *testing.T) {
	source := testReleaseSource()

	allowed := []string{
		source.downloadPrefix + "v1.0.0/meilisearch-linux-amd64",
		source.downloadPrefix + "v1.0.0/meilisearch-linux-amd64.sha256",
	}
	for _, rawURL := range allowed {
		if err := ensureReleaseDownloadURL(rawURL, source); err != nil {
			t.Fatalf("ensureReleaseDownloadURL(%q) = %v, want nil", rawURL, err)
		}
	}

	rejected := []string{
		"https://evil.test/meilisearch-linux-amd64",
		"https://evil.test/github.com/meilisearch/meilisearch/releases/download/v1.0.0/x",
		"http://github.com/meilisearch/meilisearch/releases/download/v1.0.0/x",
		"https://github.com/attacker/meilisearch/releases/download/v1.0.0/x",
		"https://github.com/meilisearch/meilisearch/releases/download-evil/v1.0.0/x",
		"https://objects.githubusercontent.com/x",
		"",
	}
	for _, rawURL := range rejected {
		if err := ensureReleaseDownloadURL(rawURL, source); err == nil {
			t.Fatalf("ensureReleaseDownloadURL(%q) = nil, want the download to be refused", rawURL)
		}
	}
}

// TestExpectedDigestRefusesAnOffRepositoryChecksumURL is the regression test for
// the self-confirming check: a tampered response that keeps an official-looking
// asset name but points the checksum at its own host must be refused before that
// host is ever contacted.
func TestExpectedDigestRefusesAnOffRepositoryChecksumURL(t *testing.T) {
	const assetName = "meilisearch-linux-amd64"
	asset := releaseAsset{Name: assetName, BrowserDownloadURL: "https://github.com/meilisearch/meilisearch/releases/download/v1.0.0/" + assetName}

	perAsset := releaseWith(releaseAsset{
		Name:               assetName + ".sha256",
		BrowserDownloadURL: "https://evil.test/" + assetName + ".sha256",
	})
	if _, _, err := expectedDigest(perAsset, asset, testReleaseSource()); err == nil {
		t.Fatal("expectedDigest accepted a checksum file hosted outside the release source")
	} else if !strings.Contains(err.Error(), "refusing to download") {
		t.Fatalf("error = %v, want it to name the refusal to download", err)
	}

	combined := releaseWith(releaseAsset{
		Name:               "SHA256SUMS",
		BrowserDownloadURL: "https://evil.test/SHA256SUMS",
	})
	if _, _, err := expectedDigest(combined, asset, testReleaseSource()); err == nil {
		t.Fatal("expectedDigest accepted a combined checksum file hosted outside the release source")
	}
}

// TestExpectedDigestFailsClosedWithoutAnyChecksum pins the other half of the
// trust model: no published checksum means no execution, whatever the reason.
func TestExpectedDigestFailsClosedWithoutAnyChecksum(t *testing.T) {
	asset := releaseAsset{
		Name:               "meilisearch-linux-amd64",
		BrowserDownloadURL: "https://github.com/meilisearch/meilisearch/releases/download/v1.0.0/meilisearch-linux-amd64",
	}
	release := releaseWith(asset)

	if _, _, err := expectedDigest(release, asset, testReleaseSource()); err == nil {
		t.Fatal("expectedDigest returned a digest for a release that publishes no checksum")
	}
}

// TestExpectedDigestUsesGitHubAssetDigest 钉住自动下载链路真正能用的那条校验路径。
//
// 背景（这是一条实测出来的 BLOCKER）：Meilisearch 的 release **不发布任何校验和
// 文件**——v1.53.2 的 16 个资产只有裸二进制、.deb 与 .json。而旧实现只认
// `.sha256` / 汇总校验和两类资产，找不到就 fail closed，于是整条
// downloadLatest → EnsureBinary → search.Start 必然失败，main.go 对此是 Fatalf：
// **没有本地 Meilisearch 的机器上博客根本起不来**。能跑只是因为有人手工放了二进制。
//
// 修复是读 GitHub 服务端在 `assets[].digest` 里给出的 `sha256:<hex>`——那条字段
// 在真实 release 里是存在的。这条测试用的就是真实响应的形状：一个资产、
// 带 digest、**没有任何校验和资产**。
//
// 反证方式：把 expectedDigest 里 parseAssetDigest 那一步去掉 → 本条失败
// （会退化成「找不到校验和文件」）。
func TestExpectedDigestUsesGitHubAssetDigest(t *testing.T) {
	const want = "6c00019a887813ccbce54eec52718c9343bf4234383871146c67c76b1d2646cd"
	asset := releaseAsset{
		Name:               "meilisearch-linux-amd64",
		BrowserDownloadURL: "https://github.com/meilisearch/meilisearch/releases/download/v1.53.2/meilisearch-linux-amd64",
		Digest:             "sha256:" + want,
	}
	// 真实 release 里除资产本身外没有任何校验和文件。
	release := releaseWith(asset)

	got, origin, err := expectedDigest(release, asset, testReleaseSource())
	if err != nil {
		t.Fatalf("expectedDigest() error = %v; the auto-download path would fail closed", err)
	}
	if got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}
	if !strings.Contains(origin, "digest") {
		t.Fatalf("origin = %q, should name the assets[].digest source", origin)
	}

	// 资产自身的 digest 优先于同名校验和文件：后者是发布方上传的，
	// 前者由 GitHub 计算，冲突时应当采信更强的那个。
	withBoth := releaseWith(asset, releaseAsset{
		Name:               "meilisearch-linux-amd64.sha256",
		BrowserDownloadURL: "https://github.com/meilisearch/meilisearch/releases/download/v1.53.2/meilisearch-linux-amd64.sha256",
	})
	if got, _, err := expectedDigest(withBoth, asset, testReleaseSource()); err != nil || got != want {
		t.Fatalf("expectedDigest() = (%q, %v), want the asset digest to win", got, err)
	}
}

// TestParseAssetDigestRejectsAnythingButSha256 覆盖 digest 字段的解析边界。
//
// 只接受 sha256 是刻意的：算法名若可协商，就等于允许挑一个更弱的算法
// （或一个伪造的算法名）来通过校验。
func TestParseAssetDigestRejectsAnythingButSha256(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a", 64)
	if got := parseAssetDigest(valid); got != strings.Repeat("a", 64) {
		t.Fatalf("parseAssetDigest(%q) = %q, want the bare hex", valid, got)
	}
	// 大小写不敏感（GitHub 与算法名都可能大写），但输出统一小写。
	if got := parseAssetDigest("SHA256:" + strings.Repeat("A", 64)); got != strings.Repeat("a", 64) {
		t.Fatalf("parseAssetDigest should be case-insensitive, got %q", got)
	}

	for _, bad := range []string{
		"",                      // 老 release 没有这个字段
		"sha256:",               // 只有算法名
		strings.Repeat("a", 64), // 没有算法前缀
		"md5:" + strings.Repeat("a", 32),
		"sha1:" + strings.Repeat("a", 40),
		"sha512:" + strings.Repeat("a", 128),
		"sha256:" + strings.Repeat("a", 63), // 长度不对
		"sha256:" + strings.Repeat("a", 65),
		"sha256:" + strings.Repeat("z", 64), // 非十六进制
		"sha256:sha256:" + strings.Repeat("a", 64),
	} {
		if got := parseAssetDigest(bad); got != "" {
			t.Fatalf("parseAssetDigest(%q) = %q, want it rejected", bad, got)
		}
	}
}

// TestCompareDigestRejectsMismatchAndAcceptsExactMatch covers the verification
// step itself over a real file, so a future refactor cannot quietly turn the
// comparison into something that always passes.
func TestCompareDigestRejectsMismatchAndAcceptsExactMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary")
	content := []byte("pretend meilisearch binary")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	sum := sha256.Sum256(content)
	correct := hex.EncodeToString(sum[:])
	wrong := strings.Repeat("0", 64)
	if wrong == correct {
		t.Fatal("fixture problem: the wrong digest equals the correct one")
	}

	if err := compareDigest("binary", wrong, correct, "SHA256SUMS"); err == nil {
		t.Fatal("compareDigest accepted a digest that does not match the file")
	}

	actual, err := sha256File(path)
	if err != nil {
		t.Fatalf("sha256File() error = %v", err)
	}
	if actual != correct {
		t.Fatalf("sha256File() = %q, want %q", actual, correct)
	}
	if err := compareDigest("binary", correct, actual, "SHA256SUMS"); err != nil {
		t.Fatalf("compareDigest() rejected a matching digest: %v", err)
	}
}
