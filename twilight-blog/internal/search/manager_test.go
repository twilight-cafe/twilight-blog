package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blog-engine/internal/config"
)

// TestCleanMeiliEnvStripsSecretsAndMeiliOverrides 钉住子进程环境的两条清理规则。
//
// 两条规则各防一件事：
//
//  1. MEILI_ 前缀必须清掉——外部设置的 MEILI_MASTER_KEY / MEILI_HTTP_ADDR 会覆盖
//     显式传给子进程的配置，表现为「配置写对了却不生效」。
//  2. **凭据不能转交**。发信密钥住在环境变量 BLOG_MAIL_API_KEY 里（见
//     config.MailConfig.ResolveAPIKey），而 Meilisearch 是第三方二进制，完全不需要它。
//     把不需要的凭据递给第三方进程，等于白送一个泄漏面：崩溃转储、
//     /proc/<pid>/environ、以及它自身的任何信息泄露漏洞都会带上这枚密钥。
//
// 反证方式：把 childEnvDenylist 判断从 cleanMeiliEnv 里删掉 → 第二条断言失败。
func TestCleanMeiliEnvStripsSecretsAndMeiliOverrides(t *testing.T) {
	t.Setenv(config.MailAPIKeyEnvVar, "re_must_not_reach_the_child")
	t.Setenv("MEILI_MASTER_KEY", "hijacked")
	t.Setenv("MEILI_HTTP_ADDR", "0.0.0.0:9999")
	t.Setenv("PENTEST_HARMLESS_VAR", "kept")

	env := cleanMeiliEnv()

	joined := strings.Join(env, "\n")
	lines := make(map[string]bool, len(env))
	for _, entry := range env {
		lines[entry] = true
	}

	for _, forbidden := range []string{
		config.MailAPIKeyEnvVar + "=re_must_not_reach_the_child",
		"MEILI_MASTER_KEY=hijacked",
		"MEILI_HTTP_ADDR=0.0.0.0:9999",
	} {
		if lines[forbidden] {
			t.Fatalf("the child process environment still contains %q", forbidden)
		}
		if strings.Contains(joined, strings.SplitN(forbidden, "=", 2)[1]) {
			t.Fatalf("a forbidden value leaked into the child environment: %q", forbidden)
		}
	}

	// 清理不能变成「清空」——PATH 之类的变量必须留下，否则子进程根本起不来。
	if !lines["PENTEST_HARMLESS_VAR=kept"] {
		t.Fatal("cleanMeiliEnv dropped an unrelated variable; the child would lose its environment")
	}
}

// TestResolveMasterKeyIsRandomAndPersistent 钉住 R3-03 的修复：主密钥不再是
// 写死的公开常量，而是随机生成后落盘复用。
//
// 三条断言各对应一个必须成立的性质：
//   - **不是公开值**：硬编码的 meili-local 之所以是问题，是因为同一台机器上的
//     任何进程都能拿它读走全部索引；密钥必须不可猜。
//   - **够长**：production 模式拒绝短密钥，短了子进程直接起不来。
//   - **跨调用稳定**：Meilisearch 把它记进自己的鉴权库，每次换钥匙会对不上，
//     所以第二次调用必须拿到同一个值（真正的「跨重启」由文件持久化保证，
//     这里验证的是读取路径）。
//
// 反证方式：把 resolveMasterKey 的第一行改成 `return "meili-local", nil` → 前两条失败。
func TestResolveMasterKeyIsRandomAndPersistent(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), DefaultDataDir)

	first, err := resolveMasterKey(dataDir)
	if err != nil {
		t.Fatalf("resolveMasterKey() error = %v", err)
	}
	if first == "meili-local" {
		t.Fatal("the master key is the old hard-coded public constant")
	}
	if len(first) < minMasterKeyLength {
		t.Fatalf("master key length = %d, want at least %d (production mode refuses shorter keys)",
			len(first), minMasterKeyLength)
	}

	second, err := resolveMasterKey(dataDir)
	if err != nil {
		t.Fatalf("second resolveMasterKey() error = %v", err)
	}
	if second != first {
		t.Fatalf("the master key changed between calls: %q then %q — meilisearch's own auth store would no longer match",
			first, second)
	}

	// 落盘的必须是同一把钥匙：文件内容对不上意味着重启后子进程与客户端会各说各话。
	stored, err := os.ReadFile(masterKeyPath(dataDir))
	if err != nil {
		t.Fatalf("read the stored master key: %v", err)
	}
	if strings.TrimSpace(string(stored)) != first {
		t.Fatalf("stored key %q != resolved key %q", strings.TrimSpace(string(stored)), first)
	}

	// 另一个数据目录必须得到另一把钥匙——否则「随机」只是错觉。
	other, err := resolveMasterKey(filepath.Join(t.TempDir(), DefaultDataDir))
	if err != nil {
		t.Fatalf("resolveMasterKey() in a second directory error = %v", err)
	}
	if other == first {
		t.Fatal("two data directories produced the same master key")
	}
}

// TestMasterKeyFileStaysOutsideTheDataDirectory 是一条**回归守护**，
// 记录一个真实踩过的坑：密钥文件最初就写在数据目录里面。
//
// Meilisearch 把 --db-path 指向的目录当成自己的数据库目录，启动时若发现
// 「目录非空、但缺少版本文件」就拒绝启动，报
// `failed to infer the version of the database`——错误信息里完全不提密钥文件。
// 于是现象是：换随机密钥之后，**首次启动直接失败**，三次重试全部失败后进程退出，
// 而日志里只有 Meilisearch 那句与环境无关的版本错误。
//
// 反证方式：把 masterKeyPath 改成 filepath.Join(dataDir, ...) → 本条失败。
func TestMasterKeyFileStaysOutsideTheDataDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), DefaultDataDir)

	if _, err := resolveMasterKey(dataDir); err != nil {
		t.Fatalf("resolveMasterKey() error = %v", err)
	}

	// 数据目录本身应当保持「只有 Meilisearch 自己会写的东西」——此刻它甚至还不该存在。
	if entries, err := os.ReadDir(dataDir); err == nil && len(entries) > 0 {
		t.Fatalf("the data directory is no longer empty right after key generation: %d entrie(s) — "+
			"meilisearch would refuse to start with \"failed to infer the version of the database\"", len(entries))
	}

	keyPath := masterKeyPath(dataDir)
	if inside(dataDir, keyPath) {
		t.Fatalf("the master key file %s is inside the data directory %s", keyPath, dataDir)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("the master key file was not written to %s: %v", keyPath, err)
	}
}

// inside 判断 child 是否位于 dir 之内（用相对路径判定，避免前缀比较的假阳性，
// 例如 /data/meilisearch-datakey 会被 strings.HasPrefix 误判为在 /data/meilisearch-data 内）。
func inside(dir, child string) bool {
	rel, err := filepath.Rel(dir, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// TestResolveMasterKeyReplacesATruncatedStoredKey 覆盖损坏的密钥文件：
// 一个被截断（或人工写入的短值）的密钥会让 production 模式的子进程拒绝启动，
// 而那种故障现场（「meilisearch exited before becoming ready」）很难指向真正的原因。
// 因此读到的值不满足长度要求时必须重新生成，而不是照用。
func TestResolveMasterKeyReplacesATruncatedStoredKey(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), DefaultDataDir)
	path := masterKeyPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create the key directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("short\n"), 0o600); err != nil {
		t.Fatalf("seed a truncated key: %v", err)
	}

	key, err := resolveMasterKey(dataDir)
	if err != nil {
		t.Fatalf("resolveMasterKey() error = %v", err)
	}
	if key == "short" {
		t.Fatal("a truncated stored key was reused; meilisearch would refuse to start")
	}
	if len(key) < minMasterKeyLength {
		t.Fatalf("master key length = %d, want at least %d", len(key), minMasterKeyLength)
	}

	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the stored master key: %v", err)
	}
	if strings.TrimSpace(string(stored)) != key {
		t.Fatalf("the truncated key was not replaced on disk (stored %q)", strings.TrimSpace(string(stored)))
	}
}
