package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"blog-engine/internal/backup"
	"blog-engine/internal/config"
)

// 本文件实现 restore 在「归档里的 settings.json 该不该被信任」这条边界上的判定。
//
// internal/backup 的包注释把归档明确定义为**不可信输入**（它可能来自下载、U 盘、
// 别人的机器），但归档携带的 settings.json 却会被原样写回，并在下一次启动时被
// config.Load 全盘采信。第七轮渗透测试据此指出两条问题：
//
//   - R7-01：归档里的 media_dir 被直接当成**文件系统目标路径**，于是一份格式合法
//     的归档就能让 restore 删除/替换工作目录之外的任意目录；
//   - R7-03：归档里的安全字段（限流开关、trusted_proxies、cookie_secure、
//     webp_tool）被整体信任，于是 restore 可以在一次操作里同时破坏数据与削弱防护。
//
// 本文件的做法与报告的建议一致：**目标目录由本机决定**（归档里的值只用来提示），
// 而归档配置里凡是会削弱防护的字段，默认拒绝、必须用 --accept-archive-settings
// 显式接受。

// readSettingsMap 把一份 settings.json 读成一张保留全部原始键的表。
//
// 用 map 而不是 config.Config：还原要的是「原样写回归档的配置」，而 config.Load
// 会做迁移、可能回写文件，还会把不认识的键丢掉——那等于在还原过程中改写归档内容。
// 文件不存在时返回 (nil, nil)：调用方按「本机还没有配置」处理。
func readSettingsMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var values map[string]any
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), &values); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if values == nil {
		values = map[string]any{}
	}
	return values, nil
}

// setSettingsMediaDir 把 settings.json 里的 media_dir 改成 dir，其余键原样保留。
//
// 只在「值确实要变」时调用，所以正常的还原（归档与本机的 media_dir 一致）根本不
// 重写文件，归档里的字节原样落地。
func setSettingsMediaDir(path, dir string) error {
	values, err := readSettingsMap(path)
	if err != nil {
		return err
	}
	values["media_dir"] = dir

	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	data = append(data, '\n')
	// 目标一定是暂存目录里的文件（同卷、私有），普通写入即可；不存在「写坏线上
	// settings.json」的窗口——真正的换位由 applyRestore 用 swapIn 完成。
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// settingsWeakening 返回归档配置相对于本机配置**削弱了防护**的地方，供 restore
// 默认拒绝。返回空切片表示没有发现问题。
//
// 判定刻意保守：只要归档在某一个安全字段上「更宽」，就报出来让人决定。误报的代价
// 是操作者多打一个 --accept-archive-settings，漏报的代价是整站防护被一次还原改弱。
func settingsWeakening(archive, local map[string]any) []string {
	var reasons []string

	// 限流开关：归档显式关闭就是削弱，无论本机当前是什么状态。
	if enabled, present := boolAt(nestedMap(archive, "rate_limit"), "enabled"); present && !enabled {
		reasons = append(reasons, "rate_limit.enabled is false, which disables rate limiting")
	}
	// 只检查开关是不够的：enabled=true 但把额度调到天文数字，实际效果同样是关闭限流。
	reasons = append(reasons, rateLimitWeakening(archive, local)...)

	// cookie_secure：除了拒绝 never/非法值，还要拒绝“比本机更弱”的模式。
	archiveCookie := stringAt(archive, "cookie_secure")
	switch archiveCookie {
	case "", config.CookieSecureAuto, config.CookieSecureAlways:
		if cookieSecureRank(archiveCookie) < cookieSecureRank(stringAt(local, "cookie_secure")) {
			reasons = append(reasons, fmt.Sprintf("cookie_secure is %q, weaker than this host's %q",
				archiveCookie, stringAt(local, "cookie_secure")))
		}
	default:
		reasons = append(reasons, fmt.Sprintf("cookie_secure is %q, but only auto/always are accepted", archiveCookie))
	}

	// server_name：把监听地址从具体地址改成全网卡是典型的“扩大暴露面”。
	reasons = append(reasons, serverNameWeakening(archive, local)...)

	// 上传体积上限：放宽上限会放大单请求可消耗的磁盘/内存。
	reasons = append(reasons, maxUploadSizeWeakening(archive, local)...)

	// trusted_proxies / allowed_origins：归档只能是本机的子集（收窄或维持）。
	// trusted_proxies 的“键缺失”与“显式空数组”语义不同：缺失表示采用回环默认值，
	// 显式空数组才是“一个代理都不信”。两边都要按实际生效值比较，否则本机显式
	// 收窄为 [] 时，归档只要省略这个键就会悄悄恢复回环信任。
	reasons = append(reasons, widenedList("trusted_proxies", effectiveTrustedProxies(archive), effectiveTrustedProxies(local))...)
	reasons = append(reasons, widenedList("allowed_origins", stringListAt(archive, "allowed_origins"), stringListAt(local, "allowed_origins"))...)

	// 转码工具：任何变化都可能让下一次启动/上传执行另一份二进制；包括把本机
	// 已配置的路径删空、从而回退到 PATH 查找。
	for _, key := range []string{"webp_tool", "webm_tool"} {
		archived := stringAt(archive, key)
		current := stringAt(local, key)
		if archived != current {
			reasons = append(reasons, fmt.Sprintf("%s changes from %q to %q; only this host's configured tool is accepted", key, current, archived))
		}
	}
	return reasons
}

// rateLimitField 描述一个按 IP 的限流额度及其默认值。
type rateLimitField struct {
	key      string
	fallback int
}

var rateLimitFields = []rateLimitField{
	{key: "login_per_minute", fallback: config.DefaultLoginPerMinute},
	{key: "email_per_minute", fallback: config.DefaultEmailPerMinute},
	{key: "email_per_hour", fallback: config.DefaultEmailPerHour},
	{key: "comment_per_minute", fallback: config.DefaultCommentPerMinute},
	{key: "verify_per_minute", fallback: config.DefaultVerifyPerMinute},
	{key: "install_per_minute", fallback: config.DefaultInstallPerMinute},
	{key: "search_per_minute", fallback: config.DefaultSearchPerMinute},
}

// rateLimitWeakening 把 archive 与 local 的 rate_limit 额度都换算成实际生效值，
// 只报告“归档比本机更宽”的项。额度 <= 0 表示采用默认值，这与 config 的语义一致。
func rateLimitWeakening(archive, local map[string]any) []string {
	archiveLimit := nestedMap(archive, "rate_limit")
	localLimit := nestedMap(local, "rate_limit")

	var reasons []string
	for _, field := range rateLimitFields {
		archived := effectiveRateLimit(archiveLimit, field.key, field.fallback)
		current := effectiveRateLimit(localLimit, field.key, field.fallback)
		if archived > current {
			reasons = append(reasons, fmt.Sprintf("rate_limit.%s is %d, above this host's %d", field.key, archived, current))
		}
	}
	return reasons
}

func effectiveRateLimit(values map[string]any, key string, fallback int) int {
	value, ok := numberAt(values, key)
	if !ok || value <= 0 {
		return fallback
	}
	return value
}

func maxUploadSizeWeakening(archive, local map[string]any) []string {
	archived := effectiveMaxUploadSize(archive)
	current := effectiveMaxUploadSize(local)
	if archived > current {
		return []string{fmt.Sprintf("max_upload_size is %d, above this host's %d", archived, current)}
	}
	return nil
}

func effectiveMaxUploadSize(values map[string]any) int {
	value, ok := numberAt(values, "max_upload_size")
	if !ok || value <= 0 {
		return config.DefaultMaxUploadSize
	}
	return value
}

func numberAt(values map[string]any, key string) (int, bool) {
	if values == nil {
		return 0, false
	}
	switch value := values[key].(type) {
	case float64:
		n := int(value)
		if float64(n) != value {
			return 0, false
		}
		return n, true
	case int:
		return value, true
	default:
		return 0, false
	}
}

// cookieSecureRank 给 cookie_secure 模式排序：never < auto/空 < always。
// 值越小越弱；比较归档与本机时只拒绝归档更弱。
func cookieSecureRank(mode string) int {
	switch mode {
	case config.CookieSecureAlways:
		return 2
	case "", config.CookieSecureAuto:
		return 1
	default:
		return 0
	}
}

func serverNameWeakening(archive, local map[string]any) []string {
	archived := stringAt(archive, "server_name")
	current := stringAt(local, "server_name")
	if archived == current {
		return nil
	}
	// 从“监听所有网卡”收窄到具体地址是安全的；其余变化先按可疑处理。
	if isBroadBindAddress(current) && !isBroadBindAddress(archived) {
		return nil
	}
	return []string{fmt.Sprintf("server_name changes from %q to %q, which may expose the service more broadly", current, archived)}
}

func isBroadBindAddress(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = value[1 : len(value)-1]
	}
	ip := net.ParseIP(value)
	return ip != nil && ip.IsUnspecified()
}

// effectiveTrustedProxies 返回 trusted_proxies 键缺失时实际生效的回环默认值；
// 显式出现时（哪怕为空数组）则返回显式列表。
func effectiveTrustedProxies(values map[string]any) []string {
	if values == nil {
		return config.DefaultTrustedProxies
	}
	if _, present := values["trusted_proxies"]; !present {
		return config.DefaultTrustedProxies
	}
	return stringListAt(values, "trusted_proxies")
}

// widenedList 返回 archive 里所有不在 local 中的项（即「放宽」的那部分）。
func widenedList(name string, archive, local []string) []string {
	trusted := make(map[string]struct{}, len(local))
	for _, item := range local {
		trusted[item] = struct{}{}
	}

	var reasons []string
	for _, item := range archive {
		if _, ok := trusted[item]; !ok {
			reasons = append(reasons, fmt.Sprintf("%s adds %q, which this host does not trust", name, item))
		}
	}
	return reasons
}

// nestedMap 取出一个嵌套对象；类型不对或不存在时返回 nil。
func nestedMap(values map[string]any, key string) map[string]any {
	if values == nil {
		return nil
	}
	child, _ := values[key].(map[string]any)
	return child
}

// boolAt 取出一个布尔值，并报告它是否**显式存在**（键缺失与 false 是两件事）。
func boolAt(values map[string]any, key string) (bool, bool) {
	if values == nil {
		return false, false
	}
	value, ok := values[key].(bool)
	return value, ok
}

// stringAt 取出一个去空白的字符串；类型不对或不存在时返回空串。
func stringAt(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

// stringListAt 取出一个去空白、丢空项的字符串列表；类型不对或不存在时返回 nil。
func stringListAt(values map[string]any, key string) []string {
	if values == nil {
		return nil
	}
	items, ok := values[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				result = append(result, trimmed)
			}
		}
	}
	return result
}

// validateArchiveSettings 检查即将落地的归档配置，返回需要的处理方式。
//
// accept 为 false 且发现削弱时返回错误——restore 在动任何数据**之前**就会失败，
// 这样操作者可以先看一眼归档里到底改了什么，而不是先丢掉旧配置再发现防护被关掉。
func validateArchiveSettings(archive, local map[string]any, accept bool) error {
	reasons := settingsWeakening(archive, local)
	if len(reasons) == 0 {
		return nil
	}
	if accept {
		for _, reason := range reasons {
			fmt.Fprintf(stdout(), "Warning: the archive weakens this site's settings: %s\n", reason)
		}
		return nil
	}
	return fmt.Errorf("the archive's settings.json would weaken this site's security:\n  - %s\n"+
		"if you have reviewed them, pass --accept-archive-settings to restore it anyway (or --keep-settings to keep this host's settings.json)",
		strings.Join(reasons, "\n  - "))
}

// restoreMediaDir 决定媒体目录的最终落点。
//
// 关键不变量：**归档里的 media_dir 永远不参与这个决定**。没有 --media-dir 时用本机
// settings.json 的值（缺失时是默认的 "media"），有则用显式给出的那个，两者都要通过
// config.ValidateMediaDir（拒绝绝对路径、`..`、以及把工作目录本身当媒体目录）。
func restoreMediaDir(opts restoreOptions) (string, error) {
	if opts.mediaDir != "" {
		if err := config.ValidateMediaDir(opts.mediaDir); err != nil {
			return "", fmt.Errorf("invalid --media-dir: %w", err)
		}
		return opts.mediaDir, nil
	}

	local := readMediaDir(settingsPath)
	if err := config.ValidateMediaDir(local); err != nil {
		return "", fmt.Errorf("this host's media_dir is unusable: %w", err)
	}
	return local, nil
}

// stagedSettingsPath 返回归档里 settings.json 解包之后的本地路径；归档没有这一部分
// 时返回空串（此时本机 settings.json 保持不变）。
func stagedSettingsPath(staging string, manifest backup.Manifest) (string, error) {
	archived := manifest.PartPath(backup.PartSettings)
	if archived == "" {
		return "", nil
	}
	rel, err := backup.RelativePath(archived)
	if err != nil {
		return "", err
	}
	return filepath.Join(staging, rel), nil
}

// checkRestoreOptions 挡下自相矛盾的选项组合。
//
// --media-dir 的意义是「采用归档的 settings.json，但把媒体放到本机指定的位置」。
// 如果根本不落地归档配置（--keep-settings，或归档里压根没有 settings.json），本机
// settings.json 仍然指向它自己的 media_dir，而媒体却被放到别处——站点起来就是
// 「文章都在、图全挂了」。宁可在动任何数据之前就失败。
func checkRestoreOptions(manifest backup.Manifest, opts restoreOptions) error {
	if opts.mediaDir == "" {
		return nil
	}
	if opts.keepSettings {
		return errors.New("--media-dir cannot be combined with --keep-settings: " +
			"this host's settings.json would keep pointing at its own media_dir")
	}
	if manifest.PartPath(backup.PartSettings) == "" {
		return errors.New("--media-dir requires the archive to contain a settings.json: " +
			"without it this host's settings.json would keep pointing at its own media_dir")
	}
	return nil
}
