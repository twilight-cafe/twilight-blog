package search

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blog-engine/internal/childenv"
	"blog-engine/internal/logging"
)

// DefaultDataDir 是数据目录的兜底值。
const DefaultDataDir = "meilisearch-data"

// masterKeySuffix 拼在数据目录名之后，形成主密钥文件的名字。
//
// ⚠️ 密钥文件**不能**放在数据目录里面：那个目录是 Meilisearch 自己的数据库目录
// （--db-path）。它启动时会检查「目录非空但缺少版本文件」，命中即拒绝启动，报
// `failed to infer the version of the database`——而这条错误信息里完全不提密钥，
// 现场极难定位（实测踩过：首次启动直接失败，三次重试全部失败后进程退出）。
// 所以密钥与数据目录**同级**：`<父目录>/<数据目录名>.master.key`。
const masterKeySuffix = ".master.key"

// minMasterKeyLength 是 Meilisearch 在 production 模式下对主密钥长度的硬要求
// （短于它会直接拒绝启动）。随机生成的值是 64 个十六进制字符，远超这条线。
const minMasterKeyLength = 16

// masterKeyPath 返回主密钥文件的位置（与数据目录同级，理由见 masterKeySuffix）。
func masterKeyPath(dataDir string) string {
	return filepath.Join(filepath.Dir(dataDir), filepath.Base(dataDir)+masterKeySuffix)
}

// resolveMasterKey 取本地 Meilisearch 的主密钥：首次随机生成，之后落盘复用。
//
// 此前的实现是一句写死的常量 `"meili-local"`，理由写在注释里——「只监听回环，
// 外界够不着，所以无需保密」。那句话把**网络位置**当成了授权边界，而回环不是
// 边界：同一台机器上的任何进程（共享主机、容器、任何本地落脚点）都能枚举端口、
// 拿这个公开常量读走全部索引内容、甚至 DELETE 索引。需要的性质是「别人猜不到」，
// 不是「别人连不上」。
//
// 为什么必须落盘：Meilisearch 会把主密钥记进数据目录自己的鉴权库，每次启动换一把
// 新钥匙会让旧库对不上。所以生成一次就存下来复用，权限 0600
// （Windows 上 os.Chmod 只切只读位、动不了 ACL——与 blog.db 同一处境，靠部署 ACL）。
//
// 密钥文件损坏或过短时**重新生成**而不是沿用：一个被截断的值可能短到不满足
// production 模式的要求，那时子进程会拒绝启动，故障现象比「换一把钥匙」难查得多。
func resolveMasterKey(dataDir string) (string, error) {
	path := masterKeyPath(dataDir)

	if data, err := os.ReadFile(path); err == nil {
		if key := strings.TrimSpace(string(data)); len(key) >= minMasterKeyLength {
			logging.Debugf("[search] reusing the meilisearch master key stored in %s", path)
			return key, nil
		}
		logging.Warnf("[search] ignoring %s: the stored meilisearch master key is shorter than %d characters; generating a new one",
			path, minMasterKeyLength)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate a meilisearch master key: %w", err)
	}
	key := hex.EncodeToString(buf)

	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		// 落盘失败不阻断启动：密钥只在「本进程 ↔ 它自己拉起的子进程」之间使用，
		// 一次性的随机值同样能工作，代价只是下次启动会再生成一个。
		logging.Warnf("[search] persisting the meilisearch master key to %s failed: %v; "+
			"continuing with a per-run key", path, err)
		return key, nil
	}
	_ = os.Chmod(path, 0o600)
	logging.Debugf("[search] generated a new meilisearch master key and stored it in %s", path)
	return key, nil
}

// Manager 持有 Meilisearch 子进程及其 HTTP 客户端。
//
// stopOnce 与 stopping 是一对：Stop 用 once 保证进程只被杀一次，
// stopping 则让 monitor 能区分「主动关闭」与「意外崩溃」——两者的处理方式完全不同。
//
// done 在子进程真正退出（cmd.Wait 返回）时关闭。它与 waitCh 的区别是**可重复观察**：
// waitCh 只能被读一次，而 waitCh 的读者（monitor / waitReady）各自都需要那一次；
// Stop 要判断「优雅退出到底成没成」，就得有一个不消耗通道的退出信号。
type Manager struct {
	cmd       *exec.Cmd
	baseURL   string
	masterKey string
	client    *Client
	stopOnce  sync.Once
	stopping  atomic.Bool
	waitCh    chan error
	done      chan struct{}
}

// FindExecutable 按以下顺序查找 Meilisearch 可执行文件：
//  1. <可执行文件所在目录>/bin/<meilisearch>
//  2. <工作目录>/bin/<meilisearch>
//  3. <工作目录>/<meilisearch>
//
// 先看可执行文件所在目录，是为了让「单文件部署」成立：把博客二进制和
// meilisearch 放在同一层或它的 bin 下即可，与启动时的工作目录无关。
// 非 Windows 平台还要求文件带可执行位，避免把一个同名的普通文件当成程序启动。
//
// ⚠️ **这个函数是「本地文件优先于已校验下载物」的实现点，攻击面就在这里。**
// EnsureBinary 先调它、找不到才下载，因此候选目录里放一个同名文件即可让进程
// 执行它，而 download.go 里那套 SHA-256 校验被整个跳过。能写这些目录的攻击者
// 本来也能替换主程序（边际风险有限），所以处置方式是**收窄而不是重构**：
//   - Unix 上拒绝组内/其他人可写的候选（candidateIsSafelyOwned），
//     这几乎零成本地挡住了「共享目录里被别人放一个」的常见情形；
//   - 被选中的路径与来源会写进日志（logExecutableChoice），使替换可见；
//   - Windows 上 Go 拿不到 ACL，无法自动判定，只能靠日志 + 运维约束。
//
// 若要在任何平台上彻底钉死，把二进制放进程序目录的 bin/ 并确保该目录
// 只允许管理员写入——那是候选列表里的第一项，会优先命中。
func FindExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	exeDir, err := filepath.Abs(filepath.Dir(exe))
	if err != nil {
		return "", fmt.Errorf("resolve executable directory: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}

	type candidate struct {
		path   string
		origin string
	}
	candidates := make([]candidate, 0, len(binaryNames())*3)
	for _, dir := range []struct{ path, origin string }{
		{filepath.Join(exeDir, "bin"), "executable-dir/bin"},
		{filepath.Join(cwd, "bin"), "working-dir/bin"},
		{cwd, "working-dir"},
	} {
		for _, name := range binaryNames() {
			candidates = append(candidates, candidate{filepath.Join(dir.path, name), dir.origin})
		}
	}

	for _, item := range candidates {
		info, err := os.Stat(item.path)
		if err != nil || info.IsDir() {
			continue
		}
		if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
			continue
		}
		if !candidateIsSafelyOwned(item.path, info) {
			logging.Warnf("[search] ignoring %s (%s): the file is writable by group or others, "+
				"so it cannot be trusted as an executable; continuing the search", item.path, item.origin)
			continue
		}
		abs, absErr := filepath.Abs(item.path)
		if absErr != nil {
			logExecutableChoice(item.path, item.origin)
			return item.path, nil
		}
		logExecutableChoice(abs, item.origin)
		return abs, nil
	}
	return "", fmt.Errorf("meilisearch not found in %d candidate path(s)", len(candidates))
}

// candidateIsSafelyOwned 判断一个「本地找到的」二进制是否可以放心执行。
//
// Unix：拒绝组内或其他人可写的文件。这是 POSIX 权限位能表达的最强约束，
// 而且不需要任何额外依赖。注意它对**属主**无能为力——属主可写是正常的，
// 我们假定程序目录的属主就是运维本人。
//
// Windows：Go 的 os.FileMode 在 Windows 上只反映只读属性，拿不到 ACL，
// 因此这里只能放行。这是**已知的残余缺口**，不是疏忽：真正的防线是
// 「程序目录只允许管理员写入」这条部署约束，logExecutableChoice 会在每次
// 启动时把实际采用的路径打出来，便于发现被替换。
func candidateIsSafelyOwned(path string, info os.FileInfo) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o022 == 0
}

// logExecutableChoice 记录最终选中的本地二进制及其来源，并在它来自
// 非「程序目录/bin」的位置时额外告警。
//
// 存在的理由是**可观测性**：本地查找优先于下载，所以一个被放进去的同名文件
// 会被静默采用。至少让日志把它说清楚，运维才有可能发现。
func logExecutableChoice(path, origin string) {
	if origin == "executable-dir/bin" {
		logging.Debugf("[search] using local meilisearch binary path=%s origin=%s", path, origin)
		return
	}
	logging.Warnf("[search] using a locally found meilisearch binary path=%s origin=%s; "+
		"it is executed without the SHA-256 verification that downloads go through. "+
		"Place the binary in <executable-dir>/bin to make it the preferred, trusted location.", path, origin)
}

// binaryNames 列出各平台可能出现的二进制文件名，含 x86_64 / aarch64 等别名，
// 以兼容官方 release 压缩包里带平台后缀的命名。去重后按优先级返回。
func binaryNames() []string {
	seen := make(map[string]struct{})
	var names []string
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}

	add("meilisearch")
	if runtime.GOOS == "windows" {
		add("meilisearch.exe")
		add("meilisearch-" + runtime.GOOS + "-" + runtime.GOARCH + ".exe")
		if runtime.GOARCH == "amd64" {
			add("meilisearch-windows-x86_64.exe")
		}
	} else {
		add("meilisearch-" + runtime.GOOS + "-" + runtime.GOARCH)
		if runtime.GOARCH == "amd64" {
			add("meilisearch-" + runtime.GOOS + "-x86_64")
		}
		if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
			add("meilisearch-macos-aarch64")
		}
	}
	return names
}

// EnsureBinary 先在本地找，找不到就下载一份，返回最终可用的路径。
//
// 落点会依次尝试「可执行文件目录/bin」「工作目录/bin」「工作目录」，
// 优先写进 bin 目录——那正是 FindExecutable 的首选位置，下次启动即命中缓存，
// 不必重复下载。全部落点都失败才返回错误，并带上最后一次的失败原因
// （通常是网络不可达）。
//
// ⚠️ **release 元数据只解析一次**，然后拿同一份元数据去试各个落点。
// 这既是为了省流量，更是为了错误路径的行为：落点有 3 个目录 × 3 个文件名
// = 最多 9 次尝试，如果每次都重新拉一遍 GitHub API，一台没有外网的机器启动时
// 会先卡满 9 轮 60 秒超时（约 9 分钟）才报错退出；而未认证的 GitHub API 还有
// 60 次/小时/IP 的限流，9 次调用很容易把它撞满，把「偶尔连不上」放大成
// 「一小时内都连不上」。元数据取不到时直接失败——换落点也解决不了网络问题。
//
// 落点写不进去（例如二进制装在 /usr/local/bin 而 <exe目录>/bin 不可写）是
// **逐个跳过并继续**的：这是预期内的正常降级，最终通常落到工作目录下的 bin/。
func EnsureBinary() (string, error) {
	path, err := FindExecutable()
	if err == nil {
		logging.Debugf("[search] meilisearch executable found: %s", path)
		return path, nil
	}

	release, asset, source, err := latestAsset()
	if err != nil {
		return "", fmt.Errorf("resolve the meilisearch release to download: %w", err)
	}
	if err := ensureReleaseDownloadURL(asset.BrowserDownloadURL, source); err != nil {
		return "", err
	}
	logging.Infof("[search] meilisearch binary not found locally; downloading %s from release %s",
		asset.Name, release.TagName)

	exe, exeErr := os.Executable()
	if exeErr != nil {
		exe = os.Args[0]
	}
	exeDir, _ := filepath.Abs(filepath.Dir(exe))
	cwd, _ := os.Getwd()

	targets := make([]string, 0, len(binaryNames())*3)
	for _, dir := range []string{filepath.Join(exeDir, "bin"), filepath.Join(cwd, "bin"), cwd} {
		for _, name := range binaryNames() {
			targets = append(targets, filepath.Join(dir, name))
		}
	}
	var lastErr error
	for _, target := range targets {
		if err := installRelease(target, release, asset, source); err != nil {
			lastErr = err
			logging.Warnf("[search] install meilisearch to %s failed: %v", target, err)
			continue
		}
		return target, nil
	}
	if lastErr == nil {
		lastErr = err
	}
	return "", fmt.Errorf("download meilisearch: %w", lastErr)
}

// Start 解析（必要时下载）Meilisearch 二进制，在随机回环端口上启动，
// 并等 /health 返回 2xx 才算成功。返回的 Manager 必须在博客退出时 Stop。
//
// 几处刻意的选择：
//   - 端口随机而非固定，避免与已在运行的实例或本机其他服务冲突；
//   - cleanMeiliEnv 先清掉从父进程继承的 MEILI_* 变量，否则外部环境会把
//     下面显式传入的配置悄悄覆盖掉，造成「配置看着对、实际没生效」；
//   - 子进程的 stdout/stderr 逐行转发进博客日志（pipeLines），
//     便于把搜索侧故障和博客日志放在一起排查。
//
// waitReady 失败时会主动 Kill 子进程，不会留下孤儿进程。
func Start(ctx context.Context, dataDir string) (*Manager, error) {
	binary, err := EnsureBinary()
	if err != nil {
		return nil, err
	}
	logging.Debugf("[search] starting meilisearch binary=%s data_dir=%q", binary, dataDir)

	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	if !filepath.IsAbs(dataDir) {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return nil, fmt.Errorf("resolve data dir: %w", cwdErr)
		}
		dataDir = filepath.Join(cwd, dataDir)
	}
	// 数据目录收成 0700：里面是 Meilisearch 的索引与它自己的鉴权库，
	// 只有本进程（以及同用户的 Meilisearch 子进程）需要读。
	//
	// 显式 Chmod 是必需的，因为 MkdirAll 对**已存在**的目录不做任何改动——
	// 老部署里那个 0755 的目录不会被这一步自动收紧。
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create meilisearch data directory: %w", err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		logging.Warnf("[search] tightening permissions on the meilisearch data directory %s failed: %v", dataDir, err)
	}

	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocate random port: %w", err)
	}
	logging.Debugf("[search] allocated meilisearch port %d", port)

	// 主密钥在数据目录里持久化，理由是「必须跨重启稳定」——见 resolveMasterKey。
	masterKey, err := resolveMasterKey(dataDir)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(binary)
	cmd.Env = cleanMeiliEnv()
	cmd.Env = append(cmd.Env,
		"MEILI_HTTP_ADDR=127.0.0.1:"+strconv.Itoa(port),
		"MEILI_DB_PATH="+dataDir,
		"MEILI_MASTER_KEY="+masterKey,
		// production 而不是 development：development 模式允许「没有密钥也能起」，
		// 且会在启动横幅里把建议密钥打出来。本项目始终自带密钥（上面刚生成），
		// 所以没有理由留在这个宽松模式里。
		"MEILI_ENV=production",
		"MEILI_NO_ANALYTICS=true",
		"MEILI_LOG_LEVEL=WARN",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pipe meilisearch stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("pipe meilisearch stderr: %w", err)
	}

	// 子进程的进程组与「父进程死亡信号」在这里配置（仅 Linux 生效，见
	// configureChildProcess）：父进程被 SIGKILL 时，孤儿 Meilisearch 会一直占着
	// 数据目录的 LMDB 锁，导致博客重启后再也起不来——这类故障现场很难定位。
	configureChildProcess(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start meilisearch: %w", err)
	}

	waitCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		waitCh <- cmd.Wait()
		close(done)
	}()
	go pipeLines(stdout, "[meilisearch] ")
	go pipeLines(stderr, "[meilisearch] ")

	m := &Manager{
		cmd:       cmd,
		baseURL:   fmt.Sprintf("http://127.0.0.1:%d", port),
		masterKey: masterKey,
		waitCh:    waitCh,
		done:      done,
	}
	m.client = NewClient(m.baseURL, masterKey)

	if err := m.waitReady(ctx, waitCh); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	go m.monitor()
	logging.Infof("[search] meilisearch started at %s", m.baseURL)
	return m, nil
}

func (m *Manager) Client() *Client {
	return m.client
}

// Stop 主动关闭子进程，可被多个路径重复调用（stopOnce 保证只执行一次）。
//
// 顺序上有一处不能反：先置 stopping 再发信号。反过来 monitor 会把它误判成
// 意外崩溃，于是正常退出也会打 Fatal 日志。
//
// 关闭策略是「先请、后杀」：先请求优雅退出（Linux 上是 SIGTERM），给
// childShutdownGrace 的收尾时间，超时再 SIGKILL。旧实现直接 Kill，
// 在 Linux 上等于每次关站都是 SIGKILL——Meilisearch 基于 LMDB，数据本身不会坏，
// 但索引的落盘与清理步骤被整段跳过，而且没有机会释放数据目录锁。
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		m.stopping.Store(true)
		if m.cmd == nil || m.cmd.Process == nil {
			return
		}

		logging.Debugf("[search] stopping meilisearch pid=%d", m.cmd.Process.Pid)
		if err := requestChildShutdown(m.cmd.Process); err != nil {
			// 进程可能已经自己退出了，这不是错误。
			logging.Debugf("[search] graceful shutdown request returned: %v", err)
		}

		if m.done != nil {
			select {
			case <-m.done:
				logging.Debugf("[search] meilisearch exited after the graceful shutdown request")
				return
			case <-time.After(childShutdownGrace):
				logging.Warnf("[search] meilisearch did not exit within %s; killing it", childShutdownGrace)
			}
		}

		_ = killChildProcessGroup(m.cmd.Process)
	})
}

// monitor 监听子进程退出。
//
// 关键行为：只要不是通过 Stop 主动关闭（stopping 为 false），就以 Fatal 结束
// 整个进程——也就是说 Meilisearch 意外崩溃会**带崩博客**。
// 这是有意的取舍：宁可整站明确不可用，也不要留一个静默失效的搜索框；
// 代价是可用性下降。若要改成降级模式，改这里即可，
// 但必须同时检查 service 层对搜索错误的假设是否还成立。
func (m *Manager) monitor() {
	err := <-m.waitCh
	if err == nil {
		err = errors.New("process exited")
	}
	if !m.stopping.Load() {
		logging.Fatalf("[search] meilisearch exited unexpectedly: %v", err)
	}
}

// waitReady 轮询 /health 直到就绪，最多等 60 秒，每 200ms 一次。
//
// 三个早退条件对应三种不同故障，分别带上具体原因返回——比一句「启动失败」
// 有用得多：子进程自己先退了（waitCh）、上层取消（ctx）、超时（deadline）。
// 单次探测只给 2 秒超时且失败后继续下一轮：启动初期连不上是正常现象，
// 不该因为第一次探测失败就放弃。
func (m *Manager) waitReady(ctx context.Context, waitCh <-chan error) error {
	timeout := 60 * time.Second
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err := <-waitCh:
			return fmt.Errorf("meilisearch exited before becoming ready: %w", err)
		case <-ctx.Done():
			return fmt.Errorf("wait for meilisearch: %w", ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("meilisearch did not become ready within %s", timeout)
		case <-ticker.C:
			req, _ := http.NewRequest(http.MethodGet, m.baseURL+"/health", nil)
			req.Header.Set("Authorization", "Bearer "+m.masterKey)
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
	}
}

// freePort 让内核分配一个空闲端口，然后立刻关闭监听把它交还给子进程。
//
// 这里有一个固有竞态：关闭监听之后、子进程真正绑定之前，该端口理论上有机会
// 被别的进程抢走。概率很低，但要知道此时的失败表现是 waitReady 超时，
// 而不是「端口被占用」这种一眼能看懂的报错。
func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port, nil
}

// cleanMeiliEnv 构造 Meilisearch 子进程的环境：过滤掉 MEILI_ 前缀，
// 以及不该外传给第三方进程的凭据。
//
// 过滤 MEILI_ 前缀是必要的：exec.Cmd 默认继承父进程环境，若不清理，外部设置的
// MEILI_MASTER_KEY / MEILI_HTTP_ADDR 会覆盖下面显式传入的配置，
// 从而出现「配置明明写对了却没有生效」这类极难定位的问题。
//
// 凭据清单与转码子进程共用 childenv 包里那一份。此前它只存在于本文件
// （childEnvDenylist），而 mediaconv 完全没有过滤——同一个不变量写两遍，
// 漏掉的那一处不会报错，只会静默地把发信密钥交给 cwebp/ffmpeg。
func cleanMeiliEnv() []string {
	return childenv.Sanitized("MEILI_")
}

func pipeLines(reader io.Reader, prefix string) {
	// 必须逐行读。若按固定大小分块直接把子进程输出打进日志，其中内嵌的换行
	// 会造出既无前缀也无时间戳的「裸行」，与真正的日志条目无法区分——
	// 那等于把伪造日志的能力交给了子进程。逐行 + 清洗（sanitizeLogLine）
	// 才能保证每条日志都能追溯到来源。
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := sanitizeLogLine(scanner.Text())
		if line != "" {
			logging.Infof("%s%s", prefix, line)
		}
	}
	if err := scanner.Err(); err != nil {
		logging.Warnf("[search] reading child process output failed: %v", err)
	}
}

// sanitizeLogLine 去掉控制字符（含残留的回车与换行），并把制表符换成空格，
// 防止子进程往日志里注入伪造条目。
func sanitizeLogLine(line string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, line)
	return strings.TrimSpace(cleaned)
}
