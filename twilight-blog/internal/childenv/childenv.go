// Package childenv 构造交给外部子进程的环境变量。
//
// 存在的理由：本进程会启动若干个**第三方二进制**——内嵌的 Meilisearch，
// 以及用于媒体转码的 cwebp / ffmpeg / avconv。它们都由 exec.Cmd 启动，
// 而 exec.Cmd 默认继承父进程的**完整**环境，其中包含那些只属于本站的凭据
// （当前是 BLOG_MAIL_API_KEY，即能冒充站点发信的 Resend 密钥）。
//
// 把不需要的凭据递给第三方进程，等于给它多开一个泄漏面：子进程崩溃转储、
// /proc/<pid>/environ、以及它自身的任何信息泄露问题都会顺带带上这枚密钥。
// 转码器只需要读一个文件、写一个文件，最小权限在这里几乎零成本。
//
// 另一个用途是拒绝「外部环境覆盖显式传入的配置」：Meilisearch 会优先读取
// 环境里的 MEILI_* 变量，若不清理，外部设置的 MEILI_MASTER_KEY / MEILI_HTTP_ADDR
// 会盖掉我们显式传的参数，表现为「配置明明写对了却没生效」。
//
// 这两件事此前只在 search 包里做（cleanMeiliEnv），而 mediaconv 完全没有做——
// 同一个不变量在两个地方各写一遍，迟早有一处漏掉。第三轮渗透测试发现漏的正是
// 转码那一路，于是把它提成本包，让所有子进程共用同一份清单。
package childenv

import (
	"os"
	"slices"
	"strings"

	"blog-engine/internal/config"
)

// CredentialDenylist 是不应该交给任何外部子进程的环境变量。
//
// 新增「本进程持有、子进程不需要」的凭据时，**必须**在这里登记：
// 这是唯一的登记点，漏掉不会报错、不会失败编译，只会静默地继续外传。
var CredentialDenylist = []string{
	config.MailAPIKeyEnvVar,
}

// Sanitized 返回可以从父环境继承给子进程的变量列表。
//
// dropPrefixes 用于丢掉会干扰显式配置的变量族（例如 Meilisearch 的 "MEILI_"）。
// 不传前缀时只做凭据过滤，这正是转码器需要的：它们没有配置覆盖的问题，
// 但同样不该看到发信密钥。
func Sanitized(dropPrefixes ...string) []string {
	source := os.Environ()
	env := make([]string, 0, len(source))

	for _, entry := range source {
		if hasAnyPrefix(entry, dropPrefixes) {
			continue
		}
		name, _, _ := strings.Cut(entry, "=")
		if slices.Contains(CredentialDenylist, name) {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func hasAnyPrefix(entry string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}
