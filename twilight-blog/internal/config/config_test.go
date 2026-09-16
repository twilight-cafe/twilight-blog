package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// testPasswordHash 是一份合法的 bcrypt 哈希，仅用于占位。
// 凭据本身已经不再由 config 校验，但它仍是 Load 判定「要不要迁移」的依据，
// 所以测试夹具里不能是空串。
const testPasswordHash = "$2a$10$AfQeX1gjl448zVrqV29jW.bMN8HjbLVIJdrSGsa7H39bOO.pm7da."

// TestAdminIdentityIsSplit is the guard for the rule that the administrator's
// sign-in name and display name are two independent fields.
//
// A single shared field would satisfy any assertion that only looks at "the
// admin's name", which is precisely how the two got conflated in the first
// place; so every assertion here checks both accessors after changing one.
func TestAdminIdentityIsSplit(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()

	if got := cfg.AdminLoginName(); got != DefaultAdminName {
		t.Fatalf("AdminLoginName() = %q, want %q", got, DefaultAdminName)
	}
	if got := cfg.AdminDisplayName(); got != DefaultAdminName {
		t.Fatalf("AdminDisplayName() = %q with nothing configured, want %q", got, DefaultAdminName)
	}

	if err := cfg.SetAdminLoginName("root"); err != nil {
		t.Fatalf("SetAdminLoginName() error = %v", err)
	}
	if err := cfg.SetAdminDisplayName("站长"); err != nil {
		t.Fatalf("SetAdminDisplayName() error = %v", err)
	}
	if got := cfg.AdminLoginName(); got != "root" {
		t.Fatalf("AdminLoginName() = %q, want root", got)
	}
	if got := cfg.AdminDisplayName(); got != "站长" {
		t.Fatalf("AdminDisplayName() = %q, want 站长", got)
	}

	// Renaming the display name must leave the sign-in name alone, and vice
	// versa. These two assertions are the ones that fail if the fields merge.
	if err := cfg.SetAdminDisplayName("另一个人"); err != nil {
		t.Fatalf("SetAdminDisplayName() error = %v", err)
	}
	if got := cfg.AdminLoginName(); got != "root" {
		t.Fatalf("changing the display name moved the sign-in name to %q", got)
	}
	if err := cfg.SetAdminLoginName("sudo"); err != nil {
		t.Fatalf("SetAdminLoginName() error = %v", err)
	}
	if got := cfg.AdminDisplayName(); got != "另一个人" {
		t.Fatalf("changing the sign-in name moved the display name to %q", got)
	}

	// Blank values are refused rather than silently becoming the default: an
	// empty sign-in name would lock the operator out, and an empty display name
	// would look like the save had failed.
	if err := cfg.SetAdminLoginName("   "); err == nil {
		t.Fatal("an empty sign-in name was accepted")
	}
	if err := cfg.SetAdminDisplayName(""); err == nil {
		t.Fatal("an empty display name was accepted")
	}
	if got := cfg.AdminLoginName(); got != "sudo" {
		t.Fatalf("a rejected rename still changed the sign-in name to %q", got)
	}
}

// TestAdminDisplayNameFallsBackToLoginName covers the state a hand-edited
// settings.json can produce: a sign-in name with no display name set.
func TestAdminDisplayNameFallsBackToLoginName(t *testing.T) {
	cfg := &Config{AdminUsername: "root"}
	if got := cfg.AdminDisplayName(); got != "root" {
		t.Fatalf("AdminDisplayName() = %q, want the sign-in name as the fallback", got)
	}
}

// TestLegacyAdminNameMigratesIntoBothFields covers the upgrade from the old
// single admin_name key. Both the sign-in name and the display name must come
// out unchanged, and the legacy key must not survive on disk — otherwise every
// start would re-run the migration.
func TestLegacyAdminNameMigratesIntoBothFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := map[string]any{
		"admin_name":    "oldname",
		"password_hash": testPasswordHash,
	}
	writeSettings(t, path, legacy)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.AdminLoginName(); got != "oldname" {
		t.Fatalf("AdminLoginName() = %q after migration, want oldname", got)
	}
	if got := cfg.AdminDisplayName(); got != "oldname" {
		t.Fatalf("AdminDisplayName() = %q after migration, want oldname", got)
	}
	if got := cfg.LegacyAdminCredentials().PasswordHash; got != testPasswordHash {
		t.Fatalf("the password hash was not preserved for the database migration: %q", got)
	}

	onDisk := readSettings(t, path)
	// 三个迁移专用键都必须从文件里消失。admin_name 是旧格式，另外两个是
	// 「凭据还在文件里」的那一版留下的——它们全是 json:"-"，所以新写出的文件里
	// 不该出现任何一个，即便内存里仍持有这些值等待迁移。
	for _, key := range []string{"admin_name", "admin_username", "admin_nickname"} {
		if value, ok := onDisk[key]; ok {
			t.Fatalf("%s = %v survived the migration and would be re-read on every start", key, value)
		}
	}

	// 重新加载必须是一次空操作，而且**身份**字段不该复活：它们唯一的来源是旧文件，
	// 搬到数据库之后就只剩数据库里有。
	//
	// password_hash 仍然读得回来，而且这是刻意的：文件名与内容都保留着那次升级
	// 留下的哈希，直到真实调用方在迁移成功后调用 ClearLegacyCredentials
	// （见 main.go）。Load 自己不会清它——清了就没有东西可以搬进数据库了。
	// 权威副本在 users 表里（见 model.MigrateAdminCredentials）。
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	if got := reloaded.LegacyAdminCredentials(); got.Username != "" || got.Nickname != "" {
		t.Fatalf("administrator identity was re-read from an already-migrated file: %+v", got)
	}
}

// TestSiteDescriptionRoundTrips covers the one field the site settings page
// writes: it must survive a save/load cycle, and clearing it must be possible.
func TestSiteDescriptionRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	cfg := &Config{PasswordHash: testPasswordHash, settingsPath: path}
	cfg.ApplyDefaults()
	cfg.SetSiteDescription("  一个人的博客：记录技术、设计与生活。  ")
	if got := cfg.SiteDescriptionValue(); got != "一个人的博客：记录技术、设计与生活。" {
		t.Fatalf("SiteDescriptionValue() = %q, want the trimmed text", got)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := reloaded.SiteDescriptionValue(); got != "一个人的博客：记录技术、设计与生活。" {
		t.Fatalf("SiteDescriptionValue() = %q after a reload", got)
	}

	// An empty description is a valid intent (fall back to the built-in copy),
	// so it must be storable rather than being treated as "unset".
	reloaded.SetSiteDescription("")
	if err := reloaded.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if got := readSettings(t, path); got["site_description"] != "" {
		t.Fatalf("site_description = %v after clearing, want an empty string", got["site_description"])
	}
}

// TestSaveWritesOwnerOnlyPermissions pins the file mode: settings.json holds the
// password hash and the mail API key.
//
// 在 Windows 上这条断言没有意义：Go 的 os.WriteFile 只把 mode 用于「要不要置
// 只读位」，读回来的 Mode().Perm() 与 0600 无关。跳过而不是放宽成「不检查」，
// 因为一旦放宽，这条守护在真正能生效的平台上也会形同虚设。
func TestSaveWritesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not enforceable through os.WriteFile on Windows")
	}

	path := filepath.Join(t.TempDir(), "settings.json")

	cfg := &Config{PasswordHash: testPasswordHash, settingsPath: path}
	cfg.ApplyDefaults()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat settings file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("settings.json mode = %o, want 600", mode)
	}
}

// TestNoPlaintextPasswordSurvivesRoundTrip guards the legacy field: whatever is
// read from the old key must never be written back.
//
// Load 仍然把明文升级成 bcrypt 哈希（旧行为，不能丢），但**不再立刻清空内存里的
// 明文字段**——清空推迟到 Save()，这样「明文 → 数据库里的管理员行」这一步能在
// 同一个启动周期内完成，中途不会出现既没有明文也没有哈希的空档。
func TestNoPlaintextPasswordSurvivesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writeSettings(t, path, map[string]any{"password": "Plaintext1"})

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	hash := cfg.LegacyAdminCredentials().PasswordHash
	if hash == "" {
		t.Fatal("the plaintext password was not migrated to a hash")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("Plaintext1")); err != nil {
		t.Fatalf("the migrated hash does not match the legacy password: %v", err)
	}

	if _, ok := readSettings(t, path)["password"]; ok {
		t.Fatal("the plaintext password was written back to disk")
	}
	if !strings.Contains(readFile(t, path), "password_hash") {
		t.Fatal("the migrated hash is missing from settings.json")
	}
}

// TestClearLegacyCredentialsEmptiesTheFile is the guard for the new invariant
// that makes blog.db the only place holding administrator credentials: after the
// migration the file must carry no hash and no administrator identity at all.
//
// 「三个键都不在文件里」比「三个键都是空值」更强，也更符合意图：留一个空的
// admin_username 在文件里，会让人以为改它就能改管理员的登录名——而真正的权威
// 存储已经是 users 表那一行（改它走 PUT /api/admin/profile）。
//
// 顺序很重要，所以这条断言也把「迁移之后才清空、然后 Save」写成了显式的一对
// 调用：真实调用方必须先迁移成功再清空（见 main.go）。
func TestClearLegacyCredentialsEmptiesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writeSettings(t, path, map[string]any{
		"password_hash":  testPasswordHash,
		"admin_username": "root",
		"admin_nickname": "站长",
	})

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	legacy := cfg.LegacyAdminCredentials()
	if legacy.PasswordHash != testPasswordHash || legacy.Username != "root" || legacy.Nickname != "站长" {
		t.Fatalf("the legacy credentials were not readable before the migration ran: %+v", legacy)
	}

	cfg.ClearLegacyCredentials()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	onDisk := readSettings(t, path)
	for _, key := range []string{"password_hash", "admin_username", "admin_nickname", "admin_name", "password"} {
		if value, ok := onDisk[key]; ok && value != "" && value != nil {
			t.Fatalf("%s = %v survived the migration; settings.json must hold no administrator credentials", key, value)
		}
	}
	if strings.Contains(readFile(t, path), testPasswordHash) {
		t.Fatal("the bcrypt hash is still present in settings.json")
	}

	// 重新加载后也不该「复活」：内存里的迁移来源必须是空的。
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	if got := reloaded.LegacyAdminCredentials(); got.PasswordHash != "" || got.Username != "" || got.Nickname != "" {
		t.Fatalf("credentials reappeared after a reload: %+v", got)
	}
}

func writeSettings(t *testing.T, path string, value map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal settings fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write settings fixture: %v", err)
	}
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &value); err != nil {
		t.Fatalf("parse settings file %s: %v", path, err)
	}
	return value
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

/* -------------------------------------------------------------------------- */
/* 发信密钥：只从环境变量解析，绝不回写磁盘                                    */
/* -------------------------------------------------------------------------- */

// TestResolveAPIKeyPrefersTheEnvironment 钉住密钥的解析顺序。
//
// 背景是一条真实发生过的事故：一枚仍在使用的 Resend API Key 被明文写进了
// settings.json，又随一份审计报告进了 git 历史。密钥一旦落盘就等于进了无数份
// 无法回收的副本，所以它现在只能住在环境变量里。
//
// 反证方式：让 ResolveAPIKey 直接返回 m.APIKey（不看环境变量）→ 第二条断言失败。
func TestResolveAPIKeyPrefersTheEnvironment(t *testing.T) {
	t.Setenv(MailAPIKeyEnvVar, "re_from_environment")

	// ① 字段为空 → 用默认环境变量。
	if key, source := (&MailConfig{}).ResolveAPIKey(); key != "re_from_environment" {
		t.Fatalf("ResolveAPIKey() = (%q, %q), want the value from %s", key, source, MailAPIKeyEnvVar)
	}

	// ② 显式 env: 引用 → 用它指向的变量。
	t.Setenv("PENTEST_ALT_MAIL_KEY", "re_from_alt")
	if key, _ := (&MailConfig{APIKey: "env:PENTEST_ALT_MAIL_KEY"}).ResolveAPIKey(); key != "re_from_alt" {
		t.Fatalf("an explicit env: reference was not honoured: %q", key)
	}

	// ③ 引用的变量不存在 → 当作未配置，而不是回退到字面量。
	if key, source := (&MailConfig{APIKey: "env:PENTEST_MISSING_KEY"}).ResolveAPIKey(); key != "" {
		t.Fatalf("a dangling env: reference produced key=%q (source %q), want empty", key, source)
	}

	// ④ 向后兼容：字面量仍被识别，但来源会被标注出来。
	key, source := (&MailConfig{APIKey: "re_legacy_literal"}).ResolveAPIKey()
	if key != "re_legacy_literal" {
		t.Fatalf("a literal key must still work for compatibility, got %q", key)
	}
	if !strings.Contains(source, "plaintext") {
		t.Fatalf("the source %q should flag that the key is a plaintext literal", source)
	}

	// ⑤ nil 配置不 panic。
	var nilCfg *MailConfig
	if key, _ := nilCfg.ResolveAPIKey(); key != "" {
		t.Fatalf("a nil MailConfig produced %q", key)
	}
}

// TestSaveNeverWritesTheAPIKeyToDisk 是本组里最重要的一条：
// 明文密钥可以在内存里用，但**绝不能回到磁盘上**。
//
// 反证方式：删掉 Save 里把 c.Mail.APIKey 换成 MailAPIKeyEnvRef() 的那几行
// → settings.json 里会重新出现明文密钥，本条失败。
func TestSaveNeverWritesTheAPIKeyToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	cfg := &Config{
		PasswordHash: testPasswordHash,
		settingsPath: path,
		Mail:         &MailConfig{From: "noreply@example.test", APIKey: "re_super_secret_literal"},
	}
	cfg.ApplyDefaults()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	raw := readFile(t, path)
	if strings.Contains(raw, "re_super_secret_literal") {
		t.Fatalf("the plaintext API key was written back to settings.json:\n%s", raw)
	}

	settings := readSettings(t, path)
	mail, ok := settings["mail"].(map[string]any)
	if !ok {
		t.Fatalf("settings.json has no mail object: %s", raw)
	}
	if apiKey, _ := mail["api_key"].(string); apiKey != MailAPIKeyEnvRef() {
		t.Fatalf("mail.api_key = %q, want the environment reference %q", apiKey, MailAPIKeyEnvRef())
	}

	// 内存里的值必须原样保留——本次进程仍要能正常发信，
	// 不能为了不落盘而把运行中的配置也一起清掉。
	if cfg.Mail.APIKey != "re_super_secret_literal" {
		t.Fatalf("Save() clobbered the in-memory key: %q", cfg.Mail.APIKey)
	}

	// 已经写成 env: 引用的配置再存一次应当原样保留，不被当成字面量清掉。
	cfg.Mail.APIKey = "env:SOME_OTHER_VAR"
	if err := cfg.Save(path); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}
	settings = readSettings(t, path)
	mail, _ = settings["mail"].(map[string]any)
	if apiKey, _ := mail["api_key"].(string); apiKey != "env:SOME_OTHER_VAR" {
		t.Fatalf("an explicit env: reference was rewritten to %q", apiKey)
	}
}

// TestSaveIsAtomicAcrossTheOldFile 检查原子写不会在失败时留下半个文件。
//
// 这里只验证「正常路径下旧内容被完整替换、且目录里没有残留临时文件」——
// 真正的崩溃注入需要故障文件系统，超出单元测试的范围。
func TestSaveLeavesNoTemporaryFilesBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	cfg := &Config{PasswordHash: testPasswordHash, settingsPath: path}
	cfg.ApplyDefaults()
	for i := 0; i < 3; i++ {
		if err := cfg.Save(path); err != nil {
			t.Fatalf("Save() #%d error = %v", i+1, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read settings dir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "settings.json" {
			t.Fatalf("a temporary file was left behind: %s", entry.Name())
		}
	}
	if !json.Valid([]byte(readFile(t, path))) {
		t.Fatal("settings.json is not valid JSON after repeated saves")
	}
}

/* -------------------------------------------------------------------------- */
/* 搜索开关与代码块行数上限                                                    */
/* -------------------------------------------------------------------------- */

// TestSearchEnabledDefaultsToOn 钉住 nil=启用的兼容语义：老 settings.json 里没有
// search_enabled 键，升级后必须保持搜索开着——静默关掉已上线的功能是破坏性变更。
func TestSearchEnabledDefaultsToOn(t *testing.T) {
	cfg := &Config{}
	if !cfg.SearchEnabledValue() {
		t.Fatal("a missing search_enabled key must default to enabled")
	}

	on := true
	cfg.SearchEnabled = &on
	if !cfg.SearchEnabledValue() {
		t.Fatal("an explicit true must stay enabled")
	}

	off := false
	cfg.SearchEnabled = &off
	if cfg.SearchEnabledValue() {
		t.Fatal("an explicit false must disable search")
	}
}

// TestCodeBlockMaxLinesAccessorCoversUnset 钉住三个访问器的行为：
// 未设置（0）时读出默认值；合法值原样往返；区间外的值被拒绝而不是夹取。
func TestCodeBlockMaxLinesAccessorCoversUnset(t *testing.T) {
	cfg := &Config{}
	if got := cfg.CodeBlockMaxLinesValue(); got != DefaultCodeBlockMaxLines {
		t.Fatalf("CodeBlockMaxLinesValue() = %d on an unset config, want %d", got, DefaultCodeBlockMaxLines)
	}

	if err := cfg.SetCodeBlockMaxLines(42); err != nil {
		t.Fatalf("SetCodeBlockMaxLines(42) error = %v", err)
	}
	if got := cfg.CodeBlockMaxLinesValue(); got != 42 {
		t.Fatalf("CodeBlockMaxLinesValue() = %d, want 42", got)
	}

	// 边界值必须都收：区间的意义就靠它们定义。
	for _, edge := range []int{MinCodeBlockMaxLines, MaxCodeBlockMaxLines} {
		if err := cfg.SetCodeBlockMaxLines(edge); err != nil {
			t.Fatalf("SetCodeBlockMaxLines(%d) error = %v", edge, err)
		}
	}

	// 拒绝而不是夹取：0 在这里与「未设置」同形，但调用方显式提交 0 是输入错误，
	// 悄悄改成默认值会让保存结果与输入框显示不一致。
	for _, bad := range []int{-1, 0, 1, 2, MaxCodeBlockMaxLines + 1} {
		if err := cfg.SetCodeBlockMaxLines(bad); err == nil {
			t.Fatalf("SetCodeBlockMaxLines(%d) was accepted", bad)
		}
	}
}

// TestCodeBlockMaxLinesRoundTripsAndValidatesOnLoad 覆盖持久化路径：
// 合法值随 Save/Load 往返；Load 遇到非法值必须启动即失败，
// 而不是带着一个渲染时才暴露的坏参数继续跑。
func TestCodeBlockMaxLinesRoundTripsAndValidatesOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	cfg := &Config{PasswordHash: testPasswordHash, settingsPath: path}
	cfg.ApplyDefaults()
	if err := cfg.SetCodeBlockMaxLines(35); err != nil {
		t.Fatalf("SetCodeBlockMaxLines(35) error = %v", err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if got := readSettings(t, path)["code_block_max_lines"]; got != float64(35) {
		t.Fatalf("code_block_max_lines = %v in the file, want 35", got)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := reloaded.CodeBlockMaxLinesValue(); got != 35 {
		t.Fatalf("CodeBlockMaxLinesValue() = %d after a reload, want 35", got)
	}

	// 缺失键 → ApplyDefaults 补默认值：ApplyDefaults 在 Load 之后仍可能被调用
	// （Save 前），兜底必须补上而不是留 0。
	empty := &Config{}
	empty.ApplyDefaults()
	if empty.CodeBlockMaxLines != DefaultCodeBlockMaxLines {
		t.Fatalf("ApplyDefaults left CodeBlockMaxLines at %d, want %d", empty.CodeBlockMaxLines, DefaultCodeBlockMaxLines)
	}

	// 非法值 → Load 报错（写一个越界文件直接喂给 Load）。
	badPath := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badPath, []byte(`{"code_block_max_lines": 501}`), 0o600); err != nil {
		t.Fatalf("write bad fixture: %v", err)
	}
	if _, err := Load(badPath); err == nil {
		t.Fatal("Load accepted an out-of-range code_block_max_lines")
	}
}
