package search

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"blog-engine/internal/logging"
)

// 内嵌 Meilisearch 二进制的自动下载与安装。
//
// 信任模型：下载物即将被当作程序执行，所以**不能因为走了 TLS 就信任它**。
// 必须用同一次 release 公布的 SHA-256 校验（verifyDownloadIntegrity），
// 且找不到校验值时失败关闭，而不是跳过校验继续。
//
// 但「用同一份元数据里的校验和验证同一份元数据里的二进制」有一个自证的空档：
// 若那份 JSON 本身被篡改，攻击者可以把下载地址与校验和地址一起指向自己的主机，
// 校验就变成了自己验证自己。所以下载地址还要被钉在 releaseSource.downloadPrefix
// 上（ensureReleaseDownloadURL）——篡改响应的人无法把下载引到 GitHub 之外。
//
// 这仍然挡不住「GitHub 与上游发布同时被攻陷」；那种情况只能靠内置已知哈希，
// 而那与「自动跟随 latest」是互斥的取舍，此处选择了跟随 latest。
//
// 安装流程：解析 latest release → 下载压缩包 → 校验 → 解包 → 拷到目标路径
// → 非 Windows 平台补可执行位。全程在临时目录操作，结束后清理。
type releaseSource struct {
	// metadataURL 是 release 元数据（JSON）的地址。
	metadataURL string
	// downloadPrefix 是该来源下所有资产下载地址必须满足的前缀。
	downloadPrefix string
}

// githubReleaseSources 是 release 元数据来源。做成列表是为了留出加镜像／
// 备用地址的位置，当前只有一个官方地址，失败即整体失败。
//
// 每一项都必须自带 downloadPrefix：镜像的下载地址与官方不同，
// 只按硬编码的官方前缀校验会让镜像永远无法使用，而放宽校验又等于取消这道闸。
var githubReleaseSources = []releaseSource{
	{
		metadataURL:    "https://api.github.com/repos/meilisearch/meilisearch/releases/latest",
		downloadPrefix: "https://github.com/meilisearch/meilisearch/releases/download/",
	},
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	// Assets 用的是下面的 releaseAsset 别名：它与这里逐字内联的匿名结构体
	// 是同一个类型，但有了名字之后，测试里就能直接构造 release 元数据。
	Assets []releaseAsset `json:"assets"`
}

// ensureReleaseDownloadURL 确认资产地址确实属于解析出它的那个来源。
//
// 二元组（元数据来源 → 允许的下载前缀）是这里的信任边界：校验和与二进制都
// 必须落在前缀之内，攻击者才无法通过篡改元数据把「验证」变成自我确认。
func ensureReleaseDownloadURL(rawURL string, source releaseSource) error {
	if strings.HasPrefix(rawURL, source.downloadPrefix) {
		return nil
	}
	return fmt.Errorf("refusing to download %q: assets of %s must come from %s", rawURL, source.metadataURL, source.downloadPrefix)
}

// installRelease 把已经解析好的 release 资产下载、校验、解包并安装到 target。
//
// 元数据解析（latestAsset）**刻意留在这里的调用方**，见 EnsureBinary 的说明：
// 它是整个流程里唯一需要访问 GitHub API 的一步，放进这个「每个落点调一次」的
// 函数里会让失败路径变成 9 轮网络超时。
//
// 顺序上最关键的一点：**校验发生在解包与拷贝之前**，所以未经校验的文件
// 既不会被落到最终路径，更不会被执行。临时目录 defer 清理，失败路径也不留残骸。
func installRelease(target string, release githubRelease, asset releaseAsset, source releaseSource) error {
	logging.Debugf("[search] installing release asset name=%s url=%s", asset.Name, asset.BrowserDownloadURL)

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create target directory: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "meilisearch-download-*")
	if err != nil {
		return fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, filepath.Base(asset.Name))
	logging.Debugf("[search] downloading archive to %s", archivePath)
	if err := downloadFile(asset.BrowserDownloadURL, archivePath); err != nil {
		return fmt.Errorf("download %s: %w", asset.Name, err)
	}
	logging.Debugf("[search] downloaded archive asset=%s", asset.Name)

	// The download is about to become an executed binary, so its integrity must
	// be established from the release metadata rather than trusted because the
	// transfer used TLS. This fails closed when no checksum can be found.
	if err := verifyDownloadIntegrity(archivePath, asset, release, source); err != nil {
		return err
	}

	binaryPath, err := extractBinary(archivePath, asset.Name)
	if err != nil {
		return fmt.Errorf("extract %s: %w", asset.Name, err)
	}
	logging.Debugf("[search] extracted binary %s", binaryPath)

	if err := copyFile(binaryPath, target); err != nil {
		return fmt.Errorf("install binary to %s: %w", target, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, 0o755); err != nil {
			return fmt.Errorf("mark %s executable: %w", target, err)
		}
	}
	logging.Infof("[search] meilisearch installed to %s", target)
	return nil
}

// verifyDownloadIntegrity 用同一次 GitHub release 里公布的 SHA-256 校验下载物。
//
// 找不到校验值时**拒绝继续**（fail closed）：静默跳过校验正是这里要防的缺口。
func verifyDownloadIntegrity(path string, asset releaseAsset, release githubRelease, source releaseSource) error {
	expected, origin, err := expectedDigest(release, asset, source)
	if err != nil {
		return err
	}

	actual, err := sha256File(path)
	if err != nil {
		return fmt.Errorf("hash downloaded file: %w", err)
	}

	return compareDigest(asset.Name, expected, actual, origin)
}

// compareDigest 比对摘要，把「不一致」与「一致」两种结局都写清楚。
// 单独拆出来是为了能在测试里不走网络地验证它确实会拒绝错误摘要。
func compareDigest(assetName, expected, actual, origin string) error {
	if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s (source %s)", assetName, expected, actual, origin)
	}

	logging.Infof("[search] verified sha256 of %s against %s", assetName, origin)
	return nil
}

// expectedDigest 找出被选中资产对应的已公布 SHA-256。
//
// 三条路径，按可信度与普适性排序：
//
//  1. **`assets[].digest`**——GitHub 在资产上传时由服务端计算并写进 release 元数据
//     （格式 `sha256:<hex>`）。这是首选，理由见下面的「为什么必须读它」。
//  2. 每个资产配一个 `<name>.sha256` / `.sha256.txt`（发布方自己上传的校验和文件）。
//  3. 一份汇总的校验和文件（名字含 sum/checksum，内容形如 sha256sum 输出）。
//
// # 为什么必须读 digest
//
// 原先只认第 2、3 类，而 **Meilisearch 的 release 里根本没有任何校验和资产**：
// 实测 v1.53.2 的 16 个资产只有裸二进制（meilisearch-linux-amd64 / -aarch64 /
// -enterprise-* / .deb / *.json），没有 .sha256、没有 checksum 文件。于是
// expectedDigest 必然走到下面的 fail-closed 分支，整条自动下载链（downloadLatest
// → EnsureBinary → search.Start）随之失败，而 main.go 对这一失败是 **Fatalf**：
// 在没有本地 Meilisearch 的机器上，博客**根本无法启动**。也就是说这条
// 「首次运行自动下载」的路径此前是死代码，能跑只是因为有人手工放了二进制。
//
// # 读 digest 会不会削弱信任模型
//
// 不会——它与第 2、3 类处在**同一个信任级别**：三者都来自同一次 GitHub API 响应。
// 一个能篡改该响应的人本来就能同时篡改下载地址与校验值（这正是 downloadPrefix
// 钉扎存在的理由，它挡住了「把二进制引到 GitHub 之外」）。而且 digest 是
// **GitHub 服务端算出并存储**的，比发布方自己上传的 .sha256 文件更难伪造，
// 严格说更强而不是更弱。
//
// 每个候选地址在发起请求**之前**都要过 ensureReleaseDownloadURL：校验和是这份
// 信任模型里的锚点，锚点本身绝不能被元数据指向别处。
func expectedDigest(release githubRelease, asset releaseAsset, source releaseSource) (hash string, origin string, err error) {
	target := strings.ToLower(filepath.Base(asset.Name))

	// 1. GitHub 服务端计算的摘要（首选）。
	if digest := parseAssetDigest(asset.Digest); digest != "" {
		return digest, "assets[].digest (computed by GitHub)", nil
	}

	// 2. A dedicated per-asset checksum file.
	for _, candidate := range release.Assets {
		lower := strings.ToLower(candidate.Name)
		if lower == target+".sha256" || lower == target+".sha256.txt" {
			if err := ensureReleaseDownloadURL(candidate.BrowserDownloadURL, source); err != nil {
				return "", "", err
			}
			content, fetchErr := fetchText(candidate.BrowserDownloadURL)
			if fetchErr != nil {
				return "", "", fmt.Errorf("fetch checksum file %s: %w", candidate.Name, fetchErr)
			}
			if hash := firstSHA256(content); hash != "" {
				return hash, candidate.Name, nil
			}
			return "", "", fmt.Errorf("checksum file %s contained no SHA-256 digest", candidate.Name)
		}
	}

	// 3. A combined checksums file listing several assets.
	for _, candidate := range release.Assets {
		lower := strings.ToLower(candidate.Name)
		if !isChecksumName(lower) {
			continue
		}
		if err := ensureReleaseDownloadURL(candidate.BrowserDownloadURL, source); err != nil {
			return "", "", err
		}
		content, fetchErr := fetchText(candidate.BrowserDownloadURL)
		if fetchErr != nil {
			logging.Warnf("[search] fetch checksum file %s failed: %v", candidate.Name, fetchErr)
			continue
		}
		if hash, ok := parseChecksumFile(content)[target]; ok {
			return hash, candidate.Name, nil
		}
	}

	// 走到这里说明三条路都没有拿到摘要。仍然 fail closed——**执行一个未经校验的
	// 二进制**比「搜索不可用」危险得多。但错误信息要给出可执行的下一步，
	// 因为这条路径在真实部署里意味着进程起不来。
	return "", "", fmt.Errorf(
		"no SHA-256 digest available for %s in release %s: the release publishes neither "+
			"an assets[].digest nor any checksum file.\n"+
			"Refusing to execute an unverified binary. Workarounds:\n"+
			"  - download the binary manually and place it next to the blog binary, "+
			"in ./bin/ or in the working directory (see search.FindExecutable for the exact names);\n"+
			"  - or pin a release that does publish checksums",
		asset.Name, release.TagName)
}

// parseAssetDigest 把 GitHub 的 `assets[].digest` 字段转成裸的十六进制 SHA-256。
//
// 字段格式是 `<algorithm>:<hex>`，例如
// `sha256:6c00019a887813ccbce54eec52718c9343bf4234383871146c67c76b1d2646cd`。
//
// 只接受 sha256：算法可协商就意味着攻击者可以挑一个更弱的算法（例如某个未来的
// `md5:` 或干脆伪造的算法名），而这里只需要一个确定答案。空值、未知算法、
// 长度或字符集不符一律返回空串，让调用方继续尝试后面的路径。
func parseAssetDigest(digest string) string {
	digest = strings.TrimSpace(digest)
	algorithm, value, found := strings.Cut(digest, ":")
	if !found || !strings.EqualFold(strings.TrimSpace(algorithm), "sha256") {
		return ""
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha256.Size*2 || !isHex(value) {
		return ""
	}
	return value
}

// parseChecksumFile 解析 sha256sum 的输出格式：
//
//	<64 位十六进制>  <文件名>
//	<64 位十六进制> *<文件名>
//
// 文件名前的 * 表示二进制模式，需要剥掉；键统一取小写的 basename，
// 因为 release 里的路径写法与资产名不一定完全一致。
func parseChecksumFile(content string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		hash := strings.ToLower(fields[0])
		if len(hash) != sha256.Size*2 || !isHex(hash) {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		result[strings.ToLower(filepath.Base(name))] = hash
	}
	return result
}

// firstSHA256 取文件中第一个裸 SHA-256 摘要，
// 用于那些只含一行摘要、不带文件名的校验文件。
func firstSHA256(content string) string {
	for _, field := range strings.Fields(content) {
		candidate := strings.ToLower(strings.TrimPrefix(field, "*"))
		if len(candidate) == sha256.Size*2 && isHex(candidate) {
			return candidate
		}
	}
	return ""
}

func isHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// fetchText 把一份小的元数据文件（校验和清单）读进内存，读取上限 4MB。
// 只接受 200，其他状态码一律视为失败。
func fetchText(url string) (string, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// latestAsset 依次尝试各元数据来源，返回 release、选中的平台资产，以及
// 解析出它的那个来源（下载地址要按该来源的前缀校验，所以必须一并返回）。
// 逐个来源重试，只有全部失败才把最后一次的错误抛出去。
func latestAsset() (githubRelease, releaseAsset, releaseSource, error) {
	logging.Debugf("[search] resolving latest meilisearch release")
	var lastErr error
	for _, source := range githubReleaseSources {
		logging.Debugf("[search] fetching release metadata from %s", source.metadataURL)
		release, fetchErr := fetchRelease(source.metadataURL)
		if fetchErr != nil {
			lastErr = fetchErr
			logging.Warnf("[search] fetch release metadata failed: %v", fetchErr)
			continue
		}
		logging.Debugf("[search] fetched release %s", release.TagName)
		asset, assetErr := findReleaseAsset(release)
		if assetErr == nil {
			logging.Debugf("[search] release asset selected: %s", asset.Name)
			return release, asset, source, nil
		}
		lastErr = assetErr
		logging.Warnf("[search] find release asset failed: %v", assetErr)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no meilisearch release available")
	}
	return githubRelease{}, releaseAsset{}, releaseSource{}, lastErr
}

type releaseAsset = struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	// Size 是资产字节数，只用于日志与诊断（例如发现上游把裸二进制换成了压缩包）。
	Size int64 `json:"size"`
	// Digest 形如 "sha256:<hex>"，由 GitHub 在资产上传时计算。
	//
	// 它是这条下载链**唯一可用的**校验值来源：Meilisearch 的 release 不发布任何
	// 校验和文件（见 expectedDigest 的说明）。GitHub 在 2025 年为 release 资产
	// 加上了这个字段，较早的 release 里可能为空——那时会回退到校验和文件路径。
	Digest string `json:"digest"`
}

// fetchRelease 拉取 release 元数据，响应读取上限 8MB。
// 显式带 Accept: application/vnd.github+json，避免 GitHub 回退到旧格式。
func fetchRelease(releaseURL string) (githubRelease, error) {
	var release githubRelease
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest(http.MethodGet, releaseURL, nil)
	if err != nil {
		return release, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return release, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return release, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&release); err != nil {
		return release, err
	}
	return release, nil
}

// findReleaseAsset 在 release 的资产列表里挑出属于当前平台的一个。
//
// 官方命名并不统一，所以 OS 与架构都做别名匹配
// （darwin↔macos/mac、amd64↔x86_64/x64、arm64↔aarch64），
// 并跳过校验和类资产，避免把 ".sha256" 当成二进制下载下来。
//
// # 为什么必须把 enterprise 排到最后（这一条是实测出来的）
//
// 上游同时发布社区版与企业版，而且**资产是按字母序返回的**：
//
//	meilisearch-enterprise-linux-aarch64
//	meilisearch-enterprise-linux-amd64      ← 第一个命中的
//	...
//	meilisearch-linux-amd64                 ← 我们真正想要的那个
//
// 原来的实现返回**第一个**匹配项，于是 enterprise 每次都抢先命中——在 Linux 上
// 会下载企业版二进制。企业版是商业产品，无授权运行既可能违反其许可条款，
// 也不保证行为与社区版一致，属于「能跑但不是我们想要的东西」这类极难察觉的错。
//
// 因此分两轮：第一轮只看非 enterprise 的资产，第二轮才把 enterprise 作为兜底
// （万一上游哪天只发企业版，宁可给出一个能用的二进制并告警，也不要直接失败）。
func findReleaseAsset(release githubRelease) (releaseAsset, error) {
	return selectAssetFor(release, runtime.GOOS, runtime.GOARCH)
}

// selectAssetFor 是 findReleaseAsset 的平台参数化版本。
//
// 参数化不是为了「更通用」，而是为了**可测试**：资产选择的分支全部由 GOOS/GOARCH
// 决定，如果只能在各自平台上验证，那么在 Windows 开发机上就永远测不到 Linux 的
// 选择结果——而「Linux 上选中了企业版二进制」正是这么漏掉的（见上面的说明）。
// 拆出参数之后，任何一台机器都能用真实资产名跑遍所有平台。
func selectAssetFor(release githubRelease, goos, goarch string) (releaseAsset, error) {
	osName := strings.ToLower(goos)
	archName := strings.ToLower(goarch)
	osCandidates := []string{osName}
	archCandidates := []string{archName}
	if osName == "windows" {
		osCandidates = append(osCandidates, "win")
	}
	if osName == "darwin" {
		osCandidates = append(osCandidates, "macos", "mac")
	}
	if archName == "amd64" {
		archCandidates = append(archCandidates, "x86_64", "x64")
	}
	if archName == "arm64" {
		archCandidates = append(archCandidates, "aarch64")
	}
	if osName == "darwin" && archName == "arm64" {
		// macOS 的 arm64 资产叫 meilisearch-macos-apple-silicon——名字里既没有
		// "arm64" 也没有 "aarch64"，只有 "apple-silicon"。不加这个别名的话
		// darwin/arm64 永远匹配不到任何资产（实测 v1.53.2）。
		archCandidates = append(archCandidates, "apple-silicon", "silicon")
	}

	matchesPlatform := func(lower string) bool {
		if !strings.HasPrefix(lower, "meilisearch") {
			return false
		}
		if isChecksumName(lower) {
			return false
		}
		if !containsAny(lower, osCandidates) {
			return false
		}
		return containsAny(lower, archCandidates)
	}

	// 第一轮：社区版。
	for _, asset := range release.Assets {
		lower := strings.ToLower(asset.Name)
		if strings.Contains(lower, "enterprise") {
			continue
		}
		if matchesPlatform(lower) {
			return asset, nil
		}
	}

	// 第二轮：只有企业版可选。给出可用的东西并明确告警——
	// 静默失败会让用户完全无从下手，静默使用则可能踩到许可问题。
	for _, asset := range release.Assets {
		lower := strings.ToLower(asset.Name)
		if !strings.Contains(lower, "enterprise") {
			continue
		}
		if matchesPlatform(lower) {
			logging.Warnf("[search] release %s publishes no community build for %s/%s; "+
				"falling back to the enterprise asset %q. Review its licence terms before relying on it.",
				release.TagName, goos, goarch, asset.Name)
			return asset, nil
		}
	}

	return releaseAsset{}, fmt.Errorf("no release asset found for %s/%s in release %s", goos, goarch, release.TagName)
}

// isChecksumName 判断资产名是否属于校验和／签名类文件，
// 覆盖 sha256 / sha512 / md5 / sum / sig / minisig 以及含 "checksum" 的命名。
func isChecksumName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".sha256") ||
		strings.HasSuffix(lower, ".sha512") ||
		strings.HasSuffix(lower, ".md5") ||
		strings.HasSuffix(lower, ".sum") ||
		strings.HasSuffix(lower, ".sig") ||
		strings.HasSuffix(lower, ".minisig") ||
		strings.Contains(lower, "checksum")
}

func containsAny(s string, candidates []string) bool {
	for _, candidate := range candidates {
		if candidate != "" && strings.Contains(s, candidate) {
			return true
		}
	}
	return false
}

// downloadFile 把资产流式写入磁盘。
//
// 注意这是本文件唯一**不设读取上限**的下载：二进制压缩包的体积不可预知，
// 只用 10 分钟的总超时兜底。校验和文件与 release 元数据分别有 4MB / 8MB 上限。
// 下载与解包的体积上限。
//
// 存在的理由是「这条路径的成本不由本进程决定」：下载地址来自 release 元数据，
// 解包后的体积则由压缩包自己声明。没有上限时，一个几百 KB 的 tar.gz 可以解出
// 几百 MB（实测 261KB → 256MB，放大 1028×），而这段代码跑在**启动阶段**、
// 以服务账号权限执行——磁盘被写满即整站起不来。
//
// 触顶即失败而不是截断：截断下来的东西是半个可执行文件，绝不能被执行。
//
// 取值依据（Meilisearch 官方资产）：压缩包约 60–90 MiB、解包后二进制约
// 100–150 MiB、包内条目只有 1–2 个。下面的上限留了数倍余量，
// 既能容纳将来更大的版本，又能挡住数量级上的异常。
const (
	maxDownloadBytes = 512 << 20 // 512 MiB
	// maxTextBytes 用于校验和之类的小文件：它们只有几十字节到几 KB。
	maxTextBytes = 1 << 20 // 1 MiB
)

// 解包上限刻意声明为 var 而不是 const：测试需要把它们调小到几 KB 级别，
// 才能在不写出上 GB 数据的前提下验证这道闸真的会拦。用 const 的话，
// 唯一能触发它的方式就是真的解一个 1 GiB 的包——那种测试没人愿意跑，
// 于是这道防线会永远处于「未验证」状态。
var (
	maxExtractTotalBytes int64 = 1 << 30 // 1 GiB
	maxExtractEntries          = 256
)

func downloadFile(url, target string) error {
	logging.Debugf("[search] downloading %s", url)
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	defer out.Close()

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	// 服务端自报的体积若已超限，就不必把流量真的拉下来。它只是快速路径，
	// 权威判断仍然是下面的读取计数——Content-Length 可以撒谎，也可以缺失。
	if resp.ContentLength > maxDownloadBytes {
		return fmt.Errorf("refusing %s: the server declares %d bytes, above the %d byte limit",
			url, resp.ContentLength, maxDownloadBytes)
	}

	// 多读 1 字节用于判定「刚好超限」：只读到上限为止的话，无法区分
	// 「正好等于上限」与「还有更多被截断」。
	written, err := io.Copy(out, io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return err
	}
	if written > maxDownloadBytes {
		return fmt.Errorf("refusing %s: the download exceeded the %d byte limit", url, maxDownloadBytes)
	}
	logging.Debugf("[search] download complete: %s (%d bytes)", target, written)
	return nil
}

// extractBinary 从压缩包里取出 Meilisearch 可执行文件。
//
// 支持 .zip 与 .tar.gz/.tgz；不带压缩包后缀的资产按裸二进制处理。
// 解包后是**遍历目录按文件名匹配**，而不是硬编码路径——官方压缩包里的
// 目录层级并不稳定，按名字找更耐版本升级。
func extractBinary(archivePath, assetName string) (string, error) {
	logging.Debugf("[search] extracting asset %s from %s", assetName, archivePath)
	tmpDir := filepath.Dir(archivePath)
	switch {
	case strings.HasSuffix(strings.ToLower(assetName), ".zip"):
		if err := unzipTo(archivePath, tmpDir); err != nil {
			return "", err
		}
	case strings.HasSuffix(strings.ToLower(assetName), ".tar.gz"), strings.HasSuffix(strings.ToLower(assetName), ".tgz"):
		if err := untargzTo(archivePath, tmpDir); err != nil {
			return "", err
		}
	default:
		// Plain binary asset.
		return archivePath, nil
	}

	var matches []string
	err := filepath.WalkDir(tmpDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := strings.ToLower(d.Name())
		if base == "meilisearch" || base == "meilisearch.exe" ||
			(strings.HasPrefix(base, "meilisearch-") && !isChecksumName(base)) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("meilisearch executable not found inside archive")
	}
	return matches[0], nil
}

// unzipTo 解压 zip。
//
// 每个条目都要过 zip slip 检查：解压路径必须仍落在目标目录内，
// 否则压缩包可以用 "../" 把文件写到目录之外（覆盖任意文件）。
// 文件权限统一给 0o755，因为下载物的用途就是被执行。
//
// 除路径之外还限制**条目数**与**解包后总字节数**：zip 的压缩比没有上限，
// 一个几 KB 的包可以解出几十 GB（zip bomb）。计数按实际写入的字节省，
// 不采信 header 里的声明值——那是压缩包自己写的，可以撒谎。
func unzipTo(archivePath, dest string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) > maxExtractEntries {
		return fmt.Errorf("refusing %s: the archive declares %d entries, above the %d entry limit",
			filepath.Base(archivePath), len(reader.File), maxExtractEntries)
	}
	var total int64
	for _, file := range reader.File {
		target := filepath.Join(dest, file.Name)
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(filepath.Separator)) {
			return fmt.Errorf("invalid zip entry path: %s", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			src.Close()
			return err
		}
		// 再读 1 字节是为了识别「还有更多内容被截断」——只读到上限为止的话，
		// 一个正好等于上限的条目与一个被截断的条目无法区分。
		written, err := io.Copy(dst, io.LimitReader(src, maxExtractTotalBytes-total+1))
		src.Close()
		dst.Close()
		if err != nil {
			return err
		}
		total += written
		if total > maxExtractTotalBytes {
			return fmt.Errorf("refusing %s: the archive expands beyond the %d byte limit",
				filepath.Base(archivePath), maxExtractTotalBytes)
		}
	}
	return nil
}

// untargzTo 解压 tar.gz，同样带路径穿越检查。
//
// 与 unzipTo 的一处差异：普通文件的权限取自归档条目（header.Mode），
// 而不是固定 0o755。这里可以接受，因为归档已通过 SHA-256 校验；
// 但两处写法不一致——若日后放宽校验策略，这里需要一并收紧。
//
// 与 unzipTo 一样限制条目数与解包总量。tar.gz 是长链压缩，压缩比同样没有上限
// （实测 261KB → 256MB），而这条路径跑在启动阶段。
func untargzTo(archivePath, dest string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var total int64
	entries := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		entries++
		if entries > maxExtractEntries {
			return fmt.Errorf("refusing %s: the archive holds more than %d entries",
				filepath.Base(archivePath), maxExtractEntries)
		}
		target := filepath.Join(dest, header.Name)
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(filepath.Separator)) {
			return fmt.Errorf("invalid tar entry path: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			written, err := io.Copy(out, io.LimitReader(tr, maxExtractTotalBytes-total+1))
			out.Close()
			if err != nil {
				return err
			}
			total += written
			if total > maxExtractTotalBytes {
				return fmt.Errorf("refusing %s: the archive expands beyond the %d byte limit",
					filepath.Base(archivePath), maxExtractTotalBytes)
			}
		}
	}
	return nil
}

// copyFile 拷贝文件并保留可执行权限。
// 关闭输出文件时的错误也会被返回：写入成功但 close 失败，
// 意味着数据未必真正落盘，不能当成功处理。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}
