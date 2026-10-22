// Package config 负责 settings.json 的读写，以及全部运行时配置的默认值与校验。
//
// 两条贯穿本包的不变量：
//   - Load 是「读取 + 就地迁移」：旧版明文密码会在加载时升级成 bcrypt 哈希
//     并立刻回写文件；取值非法（端口越界、错误的 cookie_secure / log_level）
//     直接返回 error 让进程启动失败，而不是带着可疑配置继续对外服务。
//   - Save 总是写出一份**完整**的 settings.json（先补默认值再序列化），
//     所以文件里不会出现「某个键缺失」这种需要靠代码兜底的状态。
//     写入是原子的（临时文件 + rename），权限固定 0600。
//     文件里**不应再有发信密钥**：mail.api_key 只存环境变量引用，
//     字面量会在保存时被换掉（见 MailConfig.ResolveAPIKey）。
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/logging"
)

// Hooks for deterministic failure-path tests. Production uses the real
// implementations; tests replace these to exercise filesystem/entropy errors
// that cannot be provoked reliably on a normal local filesystem.
var (
	writeFileAtomicFn = writeFileAtomic
	marshalConfig     = json.MarshalIndent
	bcryptGenerate    = bcrypt.GenerateFromPassword
	createTemp        = os.CreateTemp
	chmodTemp         = func(f *os.File, mode os.FileMode) error { return f.Chmod(mode) }
	writeTemp         = func(f *os.File, data []byte) (int, error) { return f.Write(data) }
	syncTemp          = func(f *os.File) error { return f.Sync() }
	closeTemp         = func(f *os.File) error { return f.Close() }
	renameSettings    = os.Rename
)

// ErrNotInstalled 表示 settings.json 尚不存在，即博客还没完成安装。
//
// 它是**预期状态而非故障**：Load 此时会返回一个可用的空 Config 加上这个错误，
// 调用方应当把该 Config 照常使用（settingsPath 已填好，只是没有密码哈希），
// 并引导用户去 /install，而不是当作启动失败。
var ErrNotInstalled = errors.New("settings not installed")

// 写入 settings.json 时用于补全缺省项的默认值。
// 只在「键缺失」时生效，绝不覆盖已经写好的值。
const (
	DefaultMediaDir      = "media"
	DefaultLogLevel      = "normal"
	DefaultMaxUploadSize = 50
	DefaultPort          = 8080
	// DefaultCodeBlockMaxLines 是「代码块最大显示行数」未设置时的默认值：
	// 正文里最常见的代码块在 20 行以内，把它作为起点既不打断阅读，
	// 又能在遇到超长代码时给出明显的「往下还有」的信号。
	DefaultCodeBlockMaxLines = 20
	// DefaultHomePageSize 是「首页每页文章数」未设置时的默认值。
	//
	// 12 不是随手取的：它同时是 `internal/view/handler.maxPageSize`（所有列表
	// 端点的分页硬上限）。默认值取在上限处，是为了让「没配置过的站点」的观感
	// 与升级前**逐字节一致**——首页此前就是写死的 12，改配置项不应该顺带改变
	// 任何既有站点的首屏。
	DefaultHomePageSize = 12
	// DefaultBorderRadius 是「圆角」未设置时的默认值，单位 px。
	//
	// 取 4 是为了与升级前**逐字节一致**：前端 `--radius` 历来就是 4px，
	// 新增一个可配置项不应该顺带改变任何既有站点的观感。
	DefaultBorderRadius = 4
)

// CodeBlockMaxLinesBounds 是 code_block_max_lines 的合法区间（含端点）。
//
// 下界 3：再小的「代码框」已经放不下一行像样的代码加行号，设置成 1/2 只会
// 让滚动条抖得没法用；上界 500：一个 500 行的块已经接近整屏文章的长度，
// 再大的值等于没有限制，不如让站长干脆理解成「这项设置管不了这么长的块」。
const (
	MinCodeBlockMaxLines = 3
	MaxCodeBlockMaxLines = 500
)

// HomePageSizeBounds 是 home_page_size 的合法区间（含端点）。
//
// 下界 1：允许「一页一篇」是有意义的最小分页设置，再小就没有意义了。
// 上界**必须**等于 handler 层的 maxPageSize（当前 12），不能只是「看起来合理」：
// 首页发出的 `page_size` 会被列表端点按 maxPageSize 校验，一旦设置值超过它，
// **首页的第一次请求就会 400**，整个站点白屏。这个约束由
// internal/view/handler/site_test.go 的 TestHomePageSizeBoundsMatchHandlerLimit
// 跨包守护——两处常量分属不同包，改一处忘另一处就是线上事故。
const (
	MinHomePageSize = 1
	MaxHomePageSize = 12
)

// BorderRadiusBounds 是 border_radius 的合法区间（含端点），单位 px。
//
// 下界 0：直角是一种正当的极简风格，不是「没设置」，所以不能拿 0 当哨兵值——
// 与 home_page_size 不同，这里**没有**「未设置」的中间态，缺失即默认值，
// 显式写 0 就是真的直角。
//
// 上界 24：再大的圆角会让分页按钮、小标签这类 30px 见方的元素变成近似圆形，
// 与它们「方正的排版件」身份不符；24 已经能覆盖卡片类的「大圆角」审美。
//
// 注意这里**没有「必须等于某处常量」的跨包约束**（不像 home_page_size 的上界
// 被 handler.maxPageSize 钉死）：圆角越大只会更好看或更难看，不会让请求失败，
// 所以上下界纯粹是审美判断，可以独立调整。
const (
	MinBorderRadius = 0
	MaxBorderRadius = 24
)

// PostBannerHeightBounds 是 post_banner_height 的合法区间（含端点），单位 px。
//
// 它是文章页头图（banner）的最大显示高度。下界 120：再矮就不是「头图」而是一条
// 装饰色带，标题会显得比图片更重；上界 800：一整屏高度已经足够，再高会把正文
// 推到首屏之外，读者必须滚一屏才看得到第一段。
//
// 0 表示「未设置」，由 ApplyDefaults 兜底成 DefaultPostBannerHeight。
const (
	MinPostBannerHeight = 120
	MaxPostBannerHeight = 800
)

// DefaultPostBannerHeight 是头图最大高度的默认值，单位 px。
//
// 400 与升级前 .post-cover img 的写死值一致：新增可配置项不应该顺带改变任何
// 既有站点的观感。
const DefaultPostBannerHeight = 400

// validatePostBannerHeight 校验 post_banner_height 的取值。
// 0 表示「未设置」，交给 ApplyDefaults 兜底；显式写下的值必须落在区间内。
func validatePostBannerHeight(value int) error {
	if value < 0 {
		return fmt.Errorf("invalid post_banner_height %d: cannot be negative", value)
	}
	if value == 0 {
		return nil
	}
	if value < MinPostBannerHeight || value > MaxPostBannerHeight {
		return fmt.Errorf("invalid post_banner_height %d: must be between %d and %d (0 means the default %d)",
			value, MinPostBannerHeight, MaxPostBannerHeight, DefaultPostBannerHeight)
	}
	return nil
}

// validateBorderRadius 校验 border_radius 的取值。
//
// 与 validateHomePageSize 的区别：这里不接受「0 表示未设置」的语义，
// 所以**没有既允许 0、又把它当缺失值的歧义**。缺失（键不存在）由
// applyDefaults 填 DefaultBorderRadius，显式写下的值一律照校验。
func validateBorderRadius(value int) error {
	if value < MinBorderRadius || value > MaxBorderRadius {
		return fmt.Errorf("invalid border_radius %d: must be between %d and %d",
			value, MinBorderRadius, MaxBorderRadius)
	}
	return nil
}

// validateHomePageSize 校验 home_page_size 的取值。
// 0 表示「未设置」，由 ApplyDefaults 兜底成默认值；其余越界值直接报错，
// 让启动失败——理由同 validateCodeBlockMaxLines：静默回退会让「我明明设了 20」
// 变成无从排查的观感问题，而这里还多一层：越界的值会让首页直接请求失败。
func validateHomePageSize(value int) error {
	if value < 0 {
		return fmt.Errorf("invalid home_page_size %d: cannot be negative", value)
	}
	if value == 0 {
		return nil
	}
	if value < MinHomePageSize || value > MaxHomePageSize {
		return fmt.Errorf("invalid home_page_size %d: must be between %d and %d (0 means the default %d)",
			value, MinHomePageSize, MaxHomePageSize, DefaultHomePageSize)
	}
	return nil
}

// validateCodeBlockMaxLines 校验 code_block_max_lines 的取值。
// 0 表示「未设置」，由 ApplyDefaults 兜底成默认值；其余越界值直接报错，
// 让启动失败——一个静默回退的数字会让「我明明设了 800」变成无从排查的观感问题。
func validateCodeBlockMaxLines(value int) error {
	if value < 0 {
		return fmt.Errorf("invalid code_block_max_lines %d: cannot be negative", value)
	}
	if value == 0 {
		return nil
	}
	if value < MinCodeBlockMaxLines || value > MaxCodeBlockMaxLines {
		return fmt.Errorf("invalid code_block_max_lines %d: must be between %d and %d (0 means the default %d)",
			value, MinCodeBlockMaxLines, MaxCodeBlockMaxLines, DefaultCodeBlockMaxLines)
	}
	return nil
}

// 密码策略。
//
// 上界 72 字节不是随意定的：bcrypt 只使用输入的前 72 字节，
// 更长的密码会被静默截断。与其接受一个「实际只校验了前 72 字节」的密码，
// 不如直接拒绝，避免给出虚假的安全感。
// 下界按**字符数**而非字节数计算，所以中文密码不会被误判为过短。
const (
	MinPasswordLength = 8
	MaxPasswordBytes  = 72
)

// 限流默认额度（按客户端 IP 计），在 settings.json 未显式配置某项时生效。
// 注意限流的**开关**由 RateLimitConfig.Enabled 单独控制，与这里的数值无关。
const (
	DefaultLoginPerMinute   = 3
	DefaultEmailPerMinute   = 3
	DefaultEmailPerHour     = 10
	DefaultCommentPerMinute = 5
	DefaultVerifyPerMinute  = 10
	DefaultInstallPerMinute = 5
	// DefaultSearchPerMinute 限制公开搜索接口。
	//
	// 第三轮渗透测试补上的一项：这个端点此前**完全没有限流**，而它是唯一一个
	// 每个请求都要向 Meilisearch 外呼一次的公开端点——不限量就等于把后端的
	// 处理能力按请求数免费送出去。
	//
	// 60 的取法：前端是「提交才搜」而不是逐键搜（Search.tsx 的 onChange 只更新
	// 输入框草稿），正常读者远达不到；而滥用者能拿到的放大倍数被压到 1/60。
	DefaultSearchPerMinute = 60
)

// DefaultAdminName 是管理员登录名在改名之前的默认值。
const DefaultAdminName = "admin"

// DefaultTrustedProxies 只信任回环地址。
//
// 对应最常见的部署形态：同一台机器上的 nginx 终止 TLS 再反代到本进程。
// 远端客户端无法伪造出一个回环的对端地址，所以这个默认值是安全的；
// 回环之外的一切代理都必须显式列出，绝不默认信任。
var DefaultTrustedProxies = []string{"127.0.0.1/32", "::1/128"}

// Cookie security modes for the session cookie.
const (
	CookieSecureAuto   = "auto"   // Secure when the request reached us over HTTPS
	CookieSecureAlways = "always" // always Secure (TLS terminated upstream)
	CookieSecureNever  = "never"  // plain HTTP development only
)

// MailConfig 是 Resend 发信所需的配置。
// From 必须是 Resend 已验证域名下的地址，否则会被服务端拒收；
// 两项都可能为空，真正校验发生在发送时（见 mail.Mailer.Send）。
//
// ⚠️ APIKey 的语义已经变了，改动前务必读完 ResolveAPIKey 的说明：
// 这里存的**应该是引用而不是密钥本身**。字面量仍然能被识别（向后兼容），
// 但它不会被写回磁盘——Save 会把它换成指向环境变量的引用。
type MailConfig struct {
	From   string `json:"from"`
	APIKey string `json:"api_key"`
	// CommentNotifyTo 是「有新访客评论」通知邮件的收件地址。
	//
	// 空串表示关闭通知——这也是默认状态：没有征得同意就往某个地址发信是不对的，
	// 而管理员的 users.email 是空串哨兵（见 model.User），不能拿来当收件人。
	// 它只出现在仅管理员可读的响应里，绝不随公开的 GET /api/site 下发。
	CommentNotifyTo string `json:"comment_notify_to"`
}

// MailAPIKeyEnvVar 是默认的发信密钥环境变量名。
//
// 为什么密钥不能住在 settings.json 里：那份文件会被备份、打包、截图、
// 连同仓库一起分发，而它**被 git 跟踪过一次**（真实的 Resend Key 曾随
// 一份审计报告进入 git 历史）。密钥一旦落盘就等于进了无数份无法回收的副本，
// 唯一可靠的处置是轮换 + 不再落盘。
//
// 环境变量的好处是它天然不进入版本控制、不进备份、不进分发产物；
// 代价是运维要记得在服务启动前注入。Windows 服务可用系统环境变量，
// systemd 用 EnvironmentFile，容器用 secret 挂载。
const MailAPIKeyEnvVar = "BLOG_MAIL_API_KEY"

// mailAPIKeyEnvPrefix 让 settings.json 能显式指向**任意**环境变量，
// 例如 "api_key": "env:RESEND_KEY_PROD"。这让「同一份 settings.json 配多个
// 部署环境」成立，而不必为每个环境改文件名。
const mailAPIKeyEnvPrefix = "env:"

// ResolveAPIKey 返回本次进程实际使用的 Resend API Key 及其来源（只用于日志）。
//
// 解析顺序：
//  1. mail.api_key 写成 "env:NAME" → 读环境变量 NAME（NAME 为空则用默认变量名）；
//  2. mail.api_key 为空 → 读默认环境变量 BLOG_MAIL_API_KEY；
//  3. mail.api_key 是字面量 → 直接使用（**兼容旧配置**，会在启动时告警，
//     且 Save 不会把它写回磁盘）。
//
// 返回值 key 为空表示「未配置」，此时发信功能关闭——这与原来的语义一致，
// 调用方据此决定是告警还是报错。
func (m *MailConfig) ResolveAPIKey() (key string, source string) {
	if m == nil {
		return "", "unset"
	}

	configured := strings.TrimSpace(m.APIKey)

	// 第 2 种：未配置 → 默认环境变量。
	if configured == "" {
		if value := strings.TrimSpace(os.Getenv(MailAPIKeyEnvVar)); value != "" {
			return value, "env:" + MailAPIKeyEnvVar
		}
		return "", "unset"
	}

	// 第 1 种：显式环境变量引用。
	if name, ok := strings.CutPrefix(configured, mailAPIKeyEnvPrefix); ok {
		name = strings.TrimSpace(name)
		if name == "" {
			name = MailAPIKeyEnvVar
		}
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value, "env:" + name
		}
		logging.Warnf("[config] mail.api_key points at environment variable %s but it is empty or unset; sending email is disabled", name)
		return "", "env:" + name + " (unset)"
	}

	// 第 3 种：字面量。能用，但不该继续存在——见 Save 的处理。
	return configured, "settings.json (plaintext; migrate to " + MailAPIKeyEnvVar + ")"
}

// MailAPIKeyEnvRef 是写回 settings.json 时使用的引用值。
// 它让文件本身说明「密钥从哪来」，而不是留下一个空字符串让人猜。
func MailAPIKeyEnvRef() string { return mailAPIKeyEnvPrefix + MailAPIKeyEnvVar }

// RateLimitConfig 配置按 IP 的限流额度。
//
// 单项额度为 0（或负数）表示「用包内默认值」而不是「禁止请求」——
// 要关掉限流请用 Enabled = false。
type RateLimitConfig struct {
	// Enabled 用指针是为了让「键缺失」表示「开启」。
	//
	// 安全控制不能是「默认关」：这个选项出现之前写下的 settings.json 里没有
	// 这个键，若把缺失当成 false，那些旧配置会无声地失去限流保护。
	// 要关掉必须显式写 false（调试时有用）。
	Enabled          *bool `json:"enabled"`
	LoginPerMinute   int   `json:"login_per_minute"`
	EmailPerMinute   int   `json:"email_per_minute"`
	EmailPerHour     int   `json:"email_per_hour"`
	CommentPerMinute int   `json:"comment_per_minute"`
	VerifyPerMinute  int   `json:"verify_per_minute"`
	InstallPerMinute int   `json:"install_per_minute"`
	SearchPerMinute  int   `json:"search_per_minute"`
}

// IsEnabled 报告限流是否生效。配置缺失（Enabled 为 nil）视为生效。
func (r RateLimitConfig) IsEnabled() bool {
	return r.Enabled == nil || *r.Enabled
}

type Config struct {
	// mu 保护那些在进程运行期间会被修改的字段（PasswordHash、AdminUsername、
	// AdminNickname、SiteDescription、CodeBlockMaxLines 与 HomePageSize）。
	// 不需要 `json:"-"` 标记：
	// sync.RWMutex 没有导出字段，encoding/json 本来就会跳过它。
	mu sync.RWMutex

	// settingsPath 是本配置对象的来源文件（或最近一次写入的目标）。
	settingsPath string

	// PasswordHash 是管理员密码的 bcrypt 哈希。
	//
	// 它**不再是权威存储**：管理员凭据住在 users 表里 is_admin = 1 的那一行。
	// 这个字段现在只剩一个用途——把老版本 settings.json 里的哈希读出来，交给
	// database.MigrateAdminCredentials 搬进数据库。搬完之后 ClearLegacyCredentials
	// 会把它清空，Save 之后文件里就不再含有任何密码哈希。
	//
	// 于是备份清单变了：blog.db 成为必须备份的那一个文件，丢它就等于丢凭据。
	// 管理员忘记密码也就无法再靠编辑 JSON 恢复，只能跑 `main.exe admin set-password`。
	PasswordHash string `json:"password_hash"`

	// Password 只为一次性迁移旧版明文配置而保留。
	//
	// Load 读到它时会就地换成 bcrypt 哈希写进 PasswordHash（与旧行为一致），
	// 但**不立刻清空本字段**——清空推迟到 Save()，好让「明文密码 → 数据库里的
	// 管理员行」这一步在同一个启动周期内完成，中途不会出现「文件里既没有明文
	// 也没有哈希」的空档。Save() 每次都把它清空再写出，所以它绝不会被写回磁盘。
	Password string `json:"password,omitempty"`

	// ServerName 是 HTTP 服务绑定的主机/IP。为空表示监听所有网卡
	// （main.go 会为此打一条 Warn，因为那通常不是部署者想要的）。
	ServerName string `json:"server_name"`

	// Port 是 HTTP 监听端口，默认 8080；取值区间 1..65535，越界会让启动失败。
	Port int `json:"port"`

	// MediaDir 是上传媒体的落盘目录。
	// 为空时会被兜底成 "media"，而且有三层重复的兜底（ApplyDefaults、
	// NewMediaService、router），结果一致，因此任一层缺失也不会出问题。
	MediaDir string `json:"media_dir"`

	// WebPTool 可选，指向用于把图片转成 WebP 的 cwebp 或 ffmpeg 可执行文件。
	// 为空则在 PATH 里查找；找不到不算错误，只是不启用 WebP 转换。
	WebPTool string `json:"webp_tool"`

	// WebMTool 可选，指向用于把音频转成 WebM 的 ffmpeg/avconv 可执行文件。
	// 为空则在 PATH 里查找；找不到不算错误，只是不启用 WebM 转换。
	WebMTool string `json:"webm_tool"`

	// LogLevel 控制日志详细程度，可选 dev / normal / warn / error
	// （别名见 logging.ParseLevel，例如 debug / info / quiet）。
	// 取值不合法会让启动失败，而不是静默回退到默认级别。
	LogLevel string `json:"log_level"`

	// MaxUploadSize 是单次 HTTP 上传的体积上限，单位 MB（0 表示用默认值）。
	// 它同时作用于 echo 的 BodyLimit 与媒体存储层的校验。
	MaxUploadSize int64 `json:"max_upload_size"`

	// TrustedProxies 是允许信任其 X-Forwarded-For / X-Real-IP /
	// X-Forwarded-Proto 头的反向代理地址，可写 CIDR，也可写裸 IP。
	//
	// 「键缺失」与「显式空数组」语义不同，且这个区别必须保留：
	//   - 缺失 → 用回环默认值（覆盖常见的「同机 nginx」部署）；
	//   - 显式 [] → 一个代理都不信任，客户端 IP 只取网络对端地址。
	//
	// 这个列表一旦放宽，攻击者就能靠伪造转发头轮换「客户端 IP」，
	// 从而绕过限流并污染日志，所以宁可保守。
	TrustedProxies []string `json:"trusted_proxies"`

	// AdminUsername 是管理员**登录时**要填的名字，默认 "admin"。
	//
	// 与 PasswordHash 一样，它现在只是**一次性迁移来源**：权威存储是 users 表里
	// is_admin = 1 那一行的 username。因此它是 json:"-"——写出的文件里没有这个键，
	// 免得有人以为改它就能改管理员的登录名（那需要 PUT /api/admin/profile）。
	//
	// 它与下面的 AdminNickname 是两件事，不得互相顶替：登录名要唯一、稳定，
	// 昵称可以随便改。旧版把它们合成一个 admin_name，于是「改个显示名」等于
	// 「换掉登录名」，很容易把自己锁在门外。
	AdminUsername string `json:"-"`

	// AdminNickname 是管理员在**前台展示**的名字：后台侧栏、页脚与评论署名
	// 都用它。它不参与任何认证判断。迁移之后同样降级为一次性来源
	// （users 表那一行的 nickname 才是权威），所以也是 json:"-"。
	AdminNickname string `json:"-"`

	// AdminName 只为一次性迁移旧配置而保留——旧版用它同时充当登录名与显示名。
	//
	// Load 会把它的值同时喂给上面两个字段（登录与展示因此保持原样），
	// 而它自己从不参与序列化，所以绝不会被写回磁盘。
	AdminName string `json:"-"`

	// SiteDescription 是站点描述，用于首页标语、页脚简介与页面元信息。
	//
	// 它是**展示用文案**，不是配置项以外的开关，所以允许为空
	// （为空时前台回退到内置文案，而不是显示一片空白）。
	SiteDescription string `json:"site_description"`

	// CodeBlockMaxLines 限制 Markdown 围栏代码块（``` 块）在页面上最多显示的行数，
	// 超出部分在块内上下滚动。0 表示「未设置」，由 ApplyDefaults 兜底成
	// DefaultCodeBlockMaxLines；显式配置的值必须落在
	// [MinCodeBlockMaxLines, MaxCodeBlockMaxLines] 内，越界让启动失败。
	//
	// 它通过公开的 GET /api/site 下发（读者看到的排版参数，本就是公开信息），
	// 前端把它折算成容器上的 CSS 变量，真正的「限高 + 滚动」由样式层完成。
	CodeBlockMaxLines int `json:"code_block_max_lines"`

	// HomePageSize 是首页文章列表每页展示的文章数，超出部分翻页。
	// 0 表示「未设置」，由 ApplyDefaults 兜底成 DefaultHomePageSize；
	// 显式配置的值必须落在 [MinHomePageSize, MaxHomePageSize] 内，越界让启动失败。
	//
	// 它走**首屏属性**通道（index.html 的 data-home-page-size）而不是
	// GET /api/site，原因与 SearchEnabled 不同：搜索开关是「界面要不要出现入口」
	// 的决策，而首页每页数量决定**第一次请求怎么发**。若等 getSite 回来再设
	// pageSize，首页会先用兜底值发一次请求、拿到数据后再用正确值发第二次
	// （或先渲染错页数再重排）。首屏属性让第一次请求就是对的。
	HomePageSize int `json:"home_page_size"`

	// BorderRadius 是站点统一圆角，单位 px，作用于**卡片类**组件
	// （卡片、按钮、输入框、下拉、弹窗、分页按钮等），
	// 不作用于形状即语义的组件（头像恒 50%、胶囊标签恒 999px、
	// 状态点、勾选框）——那些圆角一旦跟着变，就不再是「圆」和「胶囊」了。
	//
	// 用指针：0 是**合法值**（直角是一种正当风格），不能拿它当「未设置」的哨兵。
	// nil 表示键缺失，由 ApplyDefaults 填 DefaultBorderRadius；
	// 显式写下的值必须落在 [MinBorderRadius, MaxBorderRadius] 内。
	//
	// 它走**首屏属性**通道（index.html 的 data-border-radius），
	// 与 HomePageSize 同理：圆角是首屏就要定下来的排版参数，
	// 若等 GET /api/site 回来再改 CSS 变量，首屏会先按默认圆角画一遍再跳变。
	BorderRadius *int `json:"border_radius,omitempty"`

	// PostBannerHeight 是文章页头图（banner）的最大显示高度，单位 px。
	//
	// 0 表示「未设置」，由 ApplyDefaults 兜底成 DefaultPostBannerHeight；
	// 显式配置的值必须落在 [MinPostBannerHeight, MaxPostBannerHeight] 内。
	//
	// 它走**首屏属性**通道（index.html 的 data-post-banner-height），与
	// HomePageSize 同理：头图在文章数据到达后立刻要按最终高度占位，若等
	// GET /api/site 回来再改 CSS 变量，读者会看到一次高度跳变。
	PostBannerHeight int `json:"post_banner_height"`

	// MomentsEnabled 控制**微语（Moments）功能**是否启用。
	//
	// 用指针是为了让「键缺失」表示「开启」：这个选项出现之前写下的
	// settings.json 里没有这个键，若把缺失当成 false，那些旧配置升级后会
	// 无声地失去微语入口与已发布的内容。要关闭必须显式写 false。
	//
	// 关闭时的行为（三处必须一致）：
	//   - router 不再注册公开的 /api/moments 与 /api/moments/:id（404）；
	//   - view 层向 index.html 注入 momentsEnabled 标志，前端隐藏导航、
	//     页脚、侧栏与后台菜单里的入口；
	//   - 管理员写接口保留，内容不会因此不可达，重新开启即可恢复。
	MomentsEnabled *bool `json:"moments_enabled"`

	// SearchEnabled 控制**内嵌 Meilisearch 与搜索功能**是否启用。
	//
	// 用指针是为了让「键缺失」表示「开启」：这个选项出现之前写下的
	// settings.json 里没有这个键，若把缺失当成 false，那些旧配置升级后会
	// 无声地失去搜索能力。要关闭搜索必须显式写 false。
	//
	// 关闭时的行为（三处必须一致，改任何一处先读完另外两处）：
	//   - main.go 不再启动 Meilisearch 子进程（搜索彻底不占资源）；
	//   - router.go 不再注册 /api/search（后端不开放搜索 API，
	//     未知的 /api 路径维持 web.go 的 404 语义）；
	//   - view 层向 index.html 注入 searchEnabled 标志，
	//     前端据此隐藏全部搜索入口（导航、页脚与搜索页）。
	SearchEnabled *bool `json:"search_enabled"`

	// CookieSecure 控制会话 cookie 的 Secure 属性：
	// "auto"（默认，按请求是否走 HTTPS 判断）/ "always" / "never"。
	// "never" 只适用于本地明文 HTTP 开发。
	CookieSecure string `json:"cookie_secure"`

	// AllowedOrigins 是额外允许的来源，同时作用于 CORS 与同源（CSRF）检查。
	// 为空表示只接受同源请求——SPA 与 API 同源部署时，这正是期望状态。
	AllowedOrigins []string `json:"allowed_origins"`

	// RateLimit 配置鉴权与邮件类端点的按 IP 限流。为 nil 时按默认额度启用。
	RateLimit *RateLimitConfig `json:"rate_limit"`

	// Mail 是 Resend 发信配置。为 nil 时 ApplyDefaults 会补一个空配置，
	// 于是发信功能处于「已禁用」状态（相关接口返回 503）。
	Mail *MailConfig `json:"mail"`
}

// Load 读取并规范化 settings.json，必要时就地完成旧格式迁移。
//
// 关键契约：**文件不存在时返回一个可用的空 Config 加上 ErrNotInstalled**，
// 而不是 (nil, err)。调用方必须先判 cfg 非 nil，并且只在错误不是
// ErrNotInstalled 时当成致命错误——「尚未安装」是正常的初始状态，
// 进程要照常启动以便提供 /install 引导页。
//
// 其他失败一律致命（解析失败、取值非法）。这是刻意的：宁可启动失败，
// 也不要带着可疑配置对外服务。
//
// 本函数还会做四件带副作用的事：
//   - 容忍并剥掉 UTF-8 BOM（Windows 编辑器常加，而 encoding/json 会因此
//     报出一句很难懂的 invalid character，导致进程根本起不来）；
//   - 把旧版明文密码升级成 bcrypt 哈希并立刻回写；
//   - 把旧版合一的 admin_name 拆成 admin_username（登录）与 admin_nickname
//     （展示）并回写；
//   - 记下 trusted_proxies 是否「键缺失」，以便区分「用默认值」与「显式不信任」。
func Load(path string) (*Config, error) {
	logging.Debugf("[config] loading settings from %s", path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logging.Debugf("[config] settings file not found: %s (not installed)", path)
			return &Config{settingsPath: path}, ErrNotInstalled
		}
		return nil, fmt.Errorf("read settings file: %w", err)
	}

	var cfg Config
	cfg.settingsPath = path
	// Tolerate a UTF-8 byte order mark. Windows editors add one routinely, and
	// encoding/json rejects it with a confusing "invalid character" error that
	// prevents the server from starting at all.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse settings file: %w", err)
	}
	// 管理员凭据的三个键被标了 json:"-"（它们只是迁移来源，见 LegacyAdminCredentials），
	// 所以上面的反序列化看不到它们，需要在这里单独读一遍。老配置里的
	// admin_username / admin_nickname 是**升级路径**，新写出的文件里不会再出现。
	var identity adminIdentityKeys
	if err := json.Unmarshal(data, &identity); err != nil {
		return nil, fmt.Errorf("parse admin identity keys: %w", err)
	}
	if cfg.AdminUsername == "" {
		cfg.AdminUsername = identity.AdminUsername
	}
	if cfg.AdminNickname == "" {
		cfg.AdminNickname = identity.AdminNickname
	}
	if cfg.AdminUsername == "" && cfg.AdminNickname == "" {
		// 再老一版把两者合成一个 admin_name。它只在这两个新键都缺席时才生效，
		// 免得把一个陈旧的 admin_name 覆盖到已经写好的新键上。
		cfg.AdminUsername = identity.AdminName
		cfg.AdminNickname = identity.AdminName
	}
	logging.Debugf("[config] settings file parsed: %s", path)

	cfg.PasswordHash = strings.TrimSpace(cfg.PasswordHash)
	cfg.Password = strings.TrimSpace(cfg.Password)
	cfg.ServerName = strings.TrimSpace(cfg.ServerName)
	cfg.MediaDir = strings.TrimSpace(cfg.MediaDir)
	cfg.WebPTool = strings.TrimSpace(cfg.WebPTool)
	cfg.WebMTool = strings.TrimSpace(cfg.WebMTool)
	cfg.LogLevel = strings.TrimSpace(cfg.LogLevel)
	cfg.AdminName = strings.TrimSpace(cfg.AdminName)
	cfg.AdminUsername = strings.TrimSpace(cfg.AdminUsername)
	cfg.AdminNickname = strings.TrimSpace(cfg.AdminNickname)
	cfg.SiteDescription = strings.TrimSpace(cfg.SiteDescription)
	cfg.CookieSecure = strings.ToLower(strings.TrimSpace(cfg.CookieSecure))

	// An absent trusted_proxies key means "use the loopback default"; an explicit
	// empty array means "trust nothing". The distinction has to be captured before
	// normalisation turns nil into an empty slice.
	trustedProxiesAbsent := cfg.TrustedProxies == nil
	cfg.TrustedProxies = normalizeStringList(cfg.TrustedProxies)
	if trustedProxiesAbsent {
		cfg.TrustedProxies = append([]string(nil), DefaultTrustedProxies...)
	}
	cfg.AllowedOrigins = normalizeStringList(cfg.AllowedOrigins)

	if err := validateCookieSecure(cfg.CookieSecure); err != nil {
		return nil, err
	}
	if err := ValidateMediaDir(cfg.MediaDir); err != nil {
		return nil, err
	}
	if err := validateCodeBlockMaxLines(cfg.CodeBlockMaxLines); err != nil {
		return nil, err
	}
	if err := validateHomePageSize(cfg.HomePageSize); err != nil {
		return nil, err
	}
	// 只有显式写下的值才校验：键缺失（nil）留给 ApplyDefaults 兜底，
	// 不能因为旧配置文件里没有这个键就让升级后的站点起不来。
	if cfg.BorderRadius != nil {
		if err := validateBorderRadius(*cfg.BorderRadius); err != nil {
			return nil, err
		}
	}
	if err := validatePostBannerHeight(cfg.PostBannerHeight); err != nil {
		return nil, err
	}

	cfg.Mail = normalizeMailConfig(cfg.Mail)
	if err := ValidateEmailAddress(cfg.Mail.CommentNotifyTo); err != nil {
		return nil, fmt.Errorf("invalid mail.comment_notify_to: %w", err)
	}

	if _, err := logging.ParseLevel(cfg.LogLevel); err != nil {
		return nil, fmt.Errorf("invalid log_level: %w", err)
	}

	if cfg.Port != 0 && (cfg.Port < 1 || cfg.Port > 65535) {
		return nil, fmt.Errorf("invalid port %d: must be between 1 and 65535", cfg.Port)
	}

	if cfg.MaxUploadSize < 0 {
		return nil, fmt.Errorf("invalid max_upload_size %d: cannot be negative", cfg.MaxUploadSize)
	}

	// 旧版把 admin_name 同时当作登录名与显示名，上面已经把它拆进两个新字段。
	// 这一次加载是否需要立刻落盘，取决于文件里是否还留着这些**迁移专用键**：
	// 留着一个就说明这份文件写自旧版本，值得马上用新格式覆盖一遍。
	migratingAdminIdentity := identity.AdminName != "" || identity.AdminUsername != "" || identity.AdminNickname != ""
	// admin_name 本身必须从内存里清掉，否则它会被 LegacyAdminCredentials 当成
	// 又一次迁移的来源（见 Save 里对它的清空）。
	cfg.AdminName = ""

	// 迁移旧版明文密码到 bcrypt 哈希。与上面的身份迁移共用一次落盘：
	// 两次写文件没有必要，而且中间那一次会把 settings.json 短暂地置于
	// 「只迁了一半」的状态。
	//
	// 注意这里**不再**清空 cfg.Password：清空推迟到 Save()。原因是管理员凭据
	// 现在要搬进数据库（database.MigrateAdminCredentials），而那一步在 Load
	// 之后才发生；在 Load 里就把明文抹掉会让它在被搬走之前先消失。
	// Save() 仍然保证它不会被写回磁盘。
	needsSave := migratingAdminIdentity
	if cfg.PasswordHash != "" && cfg.Password != "" {
		cfg.Password = ""
		needsSave = true
	} else if cfg.PasswordHash == "" && cfg.Password != "" {
		if err := cfg.SetPassword(cfg.Password); err != nil {
			return nil, fmt.Errorf("migrate plaintext password: %w", err)
		}
		needsSave = true
	}
	if needsSave {
		if err := cfg.Save(path); err != nil {
			return nil, fmt.Errorf("migrate settings file: %w", err)
		}
	}

	logging.Debugf("[config] settings loaded path=%s", path)
	return &cfg, nil
}

// adminIdentityKeys 用于读取**已不再写出**的三个管理员身份键。
//
// 它们都是升级路径：admin_name 来自更老的版本，admin_username / admin_nickname
// 来自「凭据还在文件里」的那一版。新写出的 settings.json 里不会出现任何一个，
// 所以 Config 上对应的字段都标了 json:"-"，读旧值只能靠这个独立的结构体。
type adminIdentityKeys struct {
	AdminName     string `json:"admin_name"`
	AdminUsername string `json:"admin_username"`
	AdminNickname string `json:"admin_nickname"`
}

// LegacyAdminCredentials 是管理员凭据的**一次性迁移来源**。
//
// 老版本的 settings.json 是权威存储；现在权威存储是 users 表里 is_admin = 1
// 的那一行。这两个文件之间的搬运只能在两个包的交界处完成（config 不该认识数据库），
// 所以 Load 负责读出来、database.MigrateAdminCredentials 负责搬进去、
// ClearLegacyCredentials 负责让它从文件里消失。
//
// Password 只在「旧版明文密码」这一个场景下非空：Load 已经把它换成了 bcrypt 哈希
// 写进 PasswordHash 并落盘，这里保留它只是为了让调用方能看出「这次迁移来自明文配置」，
// 调用方**不应该**去哈希它。
type LegacyAdminCredentials struct {
	// PasswordHash 是可直接写进 users.password_hash 的 bcrypt 哈希；
	// 为空表示老配置里没有管理员密码，即博客尚未安装。
	PasswordHash string
	// Password 非空表示哈希是从旧版明文密码现场生成的。
	Password string
	// Username 是管理员登录名；为空时调用方应回退到 DefaultAdminName。
	Username string
	// Nickname 是管理员展示名；为空时调用方应回退到登录名。
	Nickname string
}

// LegacyAdminCredentials 返回仍留在配置里的管理员凭据（见上面那个类型的说明）。
//
// 它返回**拷贝**而不是指针：调用方可能会持有它跨越一次 Save，而 Save 会把源字段清空。
func (c *Config) LegacyAdminCredentials() LegacyAdminCredentials {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return LegacyAdminCredentials{
		PasswordHash: c.PasswordHash,
		Password:     c.Password,
		Username:     c.AdminUsername,
		Nickname:     c.AdminNickname,
	}
}

// ClearLegacyCredentials 清空配置里的管理员凭据字段，使它们不再出现在落盘的
// settings.json 中（**不负责写盘**，调用方需自行 Save）。
//
// 必须在 database.MigrateAdminCredentials **成功之后**调用。顺序反了会让凭据在
// 搬进数据库之前先从文件里消失——升级后第一个启动瞬间库里没有管理员行，
// 于是 /install 重新开放，任何能访问站点的人都能设一个新的管理员密码。
//
// 注意它同时清空 AdminUsername / AdminNickname：这两个字段一旦被清空，
// 管理员的登录名与展示名就只能从数据库里读（AuthHandler 已经改成这样做）。
// 这也正是保留它们到迁移完成为止的原因。
func (c *Config) ClearLegacyCredentials() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.PasswordHash = ""
	c.Password = ""
	c.AdminUsername = ""
	c.AdminNickname = ""
}

// AdminLoginName 返回配置里记着的管理员登录名，仅供**迁移**使用。
//
// 迁移之后这个字段会被清空，函数的返回值就不再是权威了：管理员的登录名住在
// users 表里（见 model.AdminRepository），运行期请从那里读。
func (c *Config) AdminLoginName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.AdminUsername == "" {
		return DefaultAdminName
	}
	return c.AdminUsername
}

// AdminDisplayName 返回配置里记着的管理员展示名，仅供**迁移**使用。
// 迁移之后请从 users 表那一行的 nickname 读（理由同 AdminLoginName）。
func (c *Config) AdminDisplayName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.AdminNickname != "" {
		return c.AdminNickname
	}
	// 昵称可以缺失而登录名不能，所以回退到登录名而不是默认值：
	// 至少显示的是本人认识的那个名字。
	if c.AdminUsername != "" {
		return c.AdminUsername
	}
	return DefaultAdminName
}

// SiteDescription 返回站点描述；未配置时返回空串，由调用方决定回退文案。
func (c *Config) SiteDescriptionValue() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SiteDescription
}

// SetAdminLoginName 只在内存里更新登录名，**不负责落盘**，调用方需自行 Save。
//
// 空输入被拒绝：一个空的登录名等于把管理员账号永久锁在门外，
// 而 settings.json 又会把空值当成「键缺失」用默认值兜底，两者叠加会让
// 「我明明改了」与「它自己变回去了」同时发生，非常难排查。
func (c *Config) SetAdminLoginName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("admin login name cannot be empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.AdminUsername = name
	return nil
}

// SetAdminDisplayName 只在内存里更新展示名，**不负责落盘**。
// 空输入被拒绝，理由同 SetAdminLoginName——留空只会让前台退回登录名，
// 看上去像保存失败了。
func (c *Config) SetAdminDisplayName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("admin display name cannot be empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.AdminNickname = name
	return nil
}

// SetSiteDescription 只在内存里更新站点描述，**不负责落盘**。
// 这里允许空串——描述是可选文案，清空是合法意图，前台会回退到内置文案。
func (c *Config) SetSiteDescription(description string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.SiteDescription = strings.TrimSpace(description)
}

// SearchEnabledValue 报告搜索（内嵌 Meilisearch）是否启用。
//
// 键缺失（nil）视为启用：老版本 settings.json 里没有这个键，升级后必须
// 保持既有行为——静默关闭已上线的搜索功能属于破坏性变更。
// 只有显式写 false 才是关闭。
func (c *Config) SearchEnabledValue() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SearchEnabled == nil || *c.SearchEnabled
}

// CodeBlockMaxLinesValue 返回代码块最大显示行数的当前值。
// 未设置（0，即 ApplyDefaults 之前读到的旧文件）时返回默认值，
// 保证调用方拿到的永远是一个可直接使用的正数。
func (c *Config) CodeBlockMaxLinesValue() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.CodeBlockMaxLines == 0 {
		return DefaultCodeBlockMaxLines
	}
	return c.CodeBlockMaxLines
}

// SetCodeBlockMaxLines 只在内存里更新代码块行数上限，**不负责落盘**。
// 与 Load 时的校验不同，这里连 0 也拒绝：Load 面对的是老文件（键缺失=未设置，
// 必须放行交给 ApplyDefaults 兜底），而 Set 面对的是**显式输入**——
// 0 没有合理含义，悄悄改写默认值会让保存结果与输入框显示不一致。
func (c *Config) SetCodeBlockMaxLines(lines int) error {
	if lines == 0 {
		return fmt.Errorf("code_block_max_lines cannot be empty: specify a value between %d and %d", MinCodeBlockMaxLines, MaxCodeBlockMaxLines)
	}
	if err := validateCodeBlockMaxLines(lines); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.CodeBlockMaxLines = lines
	return nil
}

// HomePageSizeValue 返回首页每页文章数的当前值。
// 未设置（0，即 ApplyDefaults 之前读到的旧文件）时返回默认值，
// 保证调用方拿到的永远是一个可直接使用的正数。
func (c *Config) HomePageSizeValue() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.HomePageSize == 0 {
		return DefaultHomePageSize
	}
	return c.HomePageSize
}

// SetHomePageSize 只在内存里更新首页每页文章数，**不负责落盘**。
// 与 Load 时的校验不同，这里连 0 也拒绝，理由同 SetCodeBlockMaxLines：
// Set 面对显式输入，0 没有合理含义。
func (c *Config) SetHomePageSize(size int) error {
	if size == 0 {
		return fmt.Errorf("home_page_size cannot be empty: specify a value between %d and %d", MinHomePageSize, MaxHomePageSize)
	}
	if err := validateHomePageSize(size); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.HomePageSize = size
	return nil
}

// BorderRadiusValue 返回站点统一圆角的当前值（px）。
// 键缺失（nil，即 ApplyDefaults 之前读到的旧文件）时返回默认值，
// 保证调用方拿到的永远是一个可直接写进 CSS 的数字。
//
// 注意**不能**用 0 判断「未设置」：0 是合法配置（直角）。
func (c *Config) BorderRadiusValue() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.BorderRadius == nil {
		return DefaultBorderRadius
	}
	return *c.BorderRadius
}

// SetBorderRadius 只在内存里更新圆角，**不负责落盘**。
// 与 SetHomePageSize 不同，这里**接受 0**——0 是有意义的直角，
// 不是「空值」；只有越界才报错。
func (c *Config) SetBorderRadius(radius int) error {
	if err := validateBorderRadius(radius); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	value := radius
	c.BorderRadius = &value
	return nil
}

// PostBannerHeightValue 返回文章头图最大高度的当前值（px）。
// 未设置（0，即 ApplyDefaults 之前读到的旧文件）时返回默认值，
// 保证调用方拿到的永远是一个可直接写进 CSS 的正数。
func (c *Config) PostBannerHeightValue() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.PostBannerHeight == 0 {
		return DefaultPostBannerHeight
	}
	return c.PostBannerHeight
}

// SetPostBannerHeight 只在内存里更新头图高度，**不负责落盘**。
// 与 SetHomePageSize 相同，显式输入不接受 0（0 只表示旧文件里的「未设置」）。
func (c *Config) SetPostBannerHeight(height int) error {
	if height == 0 {
		return fmt.Errorf("post_banner_height cannot be empty: specify a value between %d and %d", MinPostBannerHeight, MaxPostBannerHeight)
	}
	if err := validatePostBannerHeight(height); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.PostBannerHeight = height
	return nil
}

// MomentsEnabledValue 报告微语功能是否启用。
//
// 键缺失（nil）视为启用：老版本 settings.json 里没有这个键，升级后必须保持
// 既有行为——静默关掉已经发布的内容属于破坏性变更。只有显式写 false 才是关闭。
func (c *Config) MomentsEnabledValue() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.MomentsEnabled == nil || *c.MomentsEnabled
}

// SetMomentsEnabled 只在内存里更新微语开关，**不负责落盘**。
func (c *Config) SetMomentsEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := enabled
	c.MomentsEnabled = &value
}

// CommentNotifyToValue 返回评论通知的收件地址；未配置时返回空串。
// 每次发信前都读一次，好让站长在设置页改完立即生效，不必重启。
func (c *Config) CommentNotifyToValue() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.Mail == nil {
		return ""
	}
	return strings.TrimSpace(c.Mail.CommentNotifyTo)
}

// SetCommentNotifyTo 只在内存里更新评论通知收件地址，**不负责落盘**。
// 空串是合法值（关闭通知）；非空时必须是纯邮箱地址（不允许 "Name <a@b>"）。
func (c *Config) SetCommentNotifyTo(address string) error {
	address = strings.TrimSpace(address)
	if err := ValidateEmailAddress(address); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Mail == nil {
		c.Mail = &MailConfig{}
	}
	c.Mail.CommentNotifyTo = address
	return nil
}

// ValidateEmailAddress 校验一个**纯邮箱地址**（不含显示名）。
//
// 空串合法：它表示「不配置」。非空时要求 net/mail 能解析，且解析结果里不能
// 带显示名——`Twilight <a@b>` 这种形式在 To 头里是合法的，但本站只接受一个
// 地址，允许显示名只会让「保存了什么」与「发到哪」看起来不一致。
func ValidateEmailAddress(address string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil {
		return fmt.Errorf("invalid email address %q", address)
	}
	if parsed.Name != "" || parsed.Address != address {
		return fmt.Errorf("email address %q must be a bare address without a display name", address)
	}
	return nil
}

// ApplyDefaults 为习惯上会被省略的键补上默认值，绝不覆盖已设置的值。
// Save() 也会调用它，因此写出的文件总是「完整」的。
//
// 注意它**不加锁**：调用方（Save / EffectiveRateLimit 等）已经持有锁，
// 内部再加锁会死锁。
func (c *Config) ApplyDefaults() {
	if c.MediaDir == "" {
		c.MediaDir = DefaultMediaDir
	}
	if c.LogLevel == "" {
		c.LogLevel = DefaultLogLevel
	}
	if c.MaxUploadSize == 0 {
		c.MaxUploadSize = DefaultMaxUploadSize
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	// 登录名不能为空（迁移时它是管理员行的 username 来源），昵称可以缺省成登录名。
	// 两者刻意不共用同一个兜底：把昵称也填成 DefaultAdminName 会让
	// 「用户把登录名改成 root、却没设昵称」的站点显示成 admin，看着像回退了。
	if c.AdminUsername == "" {
		c.AdminUsername = DefaultAdminName
	}
	if c.AdminNickname == "" {
		c.AdminNickname = c.AdminUsername
	}
	if c.CookieSecure == "" {
		c.CookieSecure = CookieSecureAuto
	}
	if c.TrustedProxies == nil {
		c.TrustedProxies = append([]string(nil), DefaultTrustedProxies...)
	}
	if c.RateLimit == nil {
		c.RateLimit = &RateLimitConfig{}
	}
	c.RateLimit.applyDefaults()
	if c.CodeBlockMaxLines == 0 {
		c.CodeBlockMaxLines = DefaultCodeBlockMaxLines
	}
	if c.HomePageSize == 0 {
		c.HomePageSize = DefaultHomePageSize
	}
	// nil 才是「键缺失」。0 是合法配置（直角），绝不能被这里覆盖掉。
	if c.BorderRadius == nil {
		value := DefaultBorderRadius
		c.BorderRadius = &value
	}
	if c.PostBannerHeight == 0 {
		c.PostBannerHeight = DefaultPostBannerHeight
	}
	if c.Mail == nil {
		c.Mail = &MailConfig{}
	}
}

// validateCookieSecure 校验 cookie_secure 取值。
// 空值合法（ApplyDefaults 会补成 auto）；其余非法值直接报错——
// 一个拼错的模式会让 Secure 属性走上意料之外的分支，不如启动即失败。
func validateCookieSecure(value string) error {
	switch value {
	case "", CookieSecureAuto, CookieSecureAlways, CookieSecureNever:
		return nil
	default:
		return fmt.Errorf("invalid cookie_secure %q: must be one of auto, always, never", value)
	}
}

// ValidateMediaDir 校验 media_dir 是不是一个可以安全使用的本地目录。
//
// 空值合法：ApplyDefaults 会把它补成 DefaultMediaDir，所以「键缺失」不是错误。
// 非空时要求 filepath.IsLocal——它一次性排掉绝对路径、盘符，以及任何会走出工作
// 目录的形态（`..`、`a/../../b`）。此外还要拒绝 Clean 之后等于 "." 的值：
// IsLocal(".") 为真，但把整个工作目录当成媒体目录意味着「一次 restore 会把站点
// 自己的源码连同 blog.db 一起换掉」，那不是任何正常部署会想要的。
//
// 为什么这件事必须由 config 来做（而不是只在 restore 里做）：media_dir 会被
// 媒体服务、备份与还原三条路径使用，任何一条按非法值落盘都是不可逆的。第七轮
// 渗透测试 R7-01 之后，这里多了一道「非法配置直接启动失败」的硬门槛，而不是
// 靠「它在当前目录下相对可写」这种隐式约定。
func ValidateMediaDir(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !filepath.IsLocal(value) {
		return fmt.Errorf("invalid media_dir %q: it must be a relative path inside the working directory", value)
	}
	if filepath.Clean(value) == "." {
		return fmt.Errorf("invalid media_dir %q: it must name a directory of its own, not the working directory", value)
	}
	return nil
}

// applyDefaults 把额度为 0 或负数的项补成默认值。
// 用 <= 0 判断，所以负值同样会被纠正成默认值，而不会变成「禁止请求」。
func (r *RateLimitConfig) applyDefaults() {
	if r.LoginPerMinute <= 0 {
		r.LoginPerMinute = DefaultLoginPerMinute
	}
	if r.EmailPerMinute <= 0 {
		r.EmailPerMinute = DefaultEmailPerMinute
	}
	if r.EmailPerHour <= 0 {
		r.EmailPerHour = DefaultEmailPerHour
	}
	if r.CommentPerMinute <= 0 {
		r.CommentPerMinute = DefaultCommentPerMinute
	}
	if r.VerifyPerMinute <= 0 {
		r.VerifyPerMinute = DefaultVerifyPerMinute
	}
	if r.InstallPerMinute <= 0 {
		r.InstallPerMinute = DefaultInstallPerMinute
	}
	if r.SearchPerMinute <= 0 {
		r.SearchPerMinute = DefaultSearchPerMinute
	}
}

// ValidatePasswordStrength 执行服务端的密码策略，**这才是权威校验**：
// 安装表单里的长度提示只是便利，直接调接口就能绕过。
//
// 规则：去空白后非空；UTF-8 字符数不少于 MinPasswordLength；字节数不超过
// MaxPasswordBytes；且至少各含一个字母与一个数字（用 unicode 判定，
// 所以中文等非 ASCII 字母同样算「字母」）。
func ValidatePasswordStrength(password string) error {
	password = strings.TrimSpace(password)
	if password == "" {
		return errors.New("password cannot be empty")
	}
	// bcrypt ignores everything past 72 bytes, so a longer password would give a
	// false sense of strength while only 72 bytes are actually verified.
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes", MaxPasswordBytes)
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}

	var hasLetter, hasDigit bool
	for _, r := range password {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errors.New("password must contain at least one letter and one digit")
	}
	return nil
}

// SetPassword 校验强度后生成 bcrypt 哈希写入内存（不落盘）。
// 使用 bcrypt.DefaultCost，同时清空旧版明文字段，确保它不会被后续 Save 写回。
func (c *Config) SetPassword(password string) error {
	if err := ValidatePasswordStrength(password); err != nil {
		return err
	}

	hash, err := bcryptGenerate([]byte(strings.TrimSpace(password)), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.PasswordHash = string(hash)
	c.Password = ""
	return nil
}

// EffectiveRateLimit 返回补全默认值后的限流配置**副本**。
// 返回副本而非就地修改，避免把默认值「粘」进原对象、影响之后对「是否配置过」的判断。
func (c *Config) EffectiveRateLimit() RateLimitConfig {
	if c.RateLimit == nil {
		cfg := RateLimitConfig{}
		cfg.applyDefaults()
		return cfg
	}
	cfg := *c.RateLimit
	cfg.applyDefaults()
	return cfg
}

// normalizeMailConfig 去空白，并把 nil 当成空配置处理，保证返回值非 nil。
func normalizeMailConfig(m *MailConfig) *MailConfig {
	if m == nil {
		m = &MailConfig{}
	}
	m.From = strings.TrimSpace(m.From)
	m.APIKey = strings.TrimSpace(m.APIKey)
	m.CommentNotifyTo = strings.TrimSpace(m.CommentNotifyTo)
	return m
}

// normalizeStringList 去空白、丢弃空项，并按原顺序去重。
// 去重是必要的：同一项在列表里出现两次只会让相关日志重复。
func normalizeStringList(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// Save 把配置规范化后写回 path。
//
// 总是写出完整文件（先 ApplyDefaults），并保证**三个迁移专用键**
// （password_hash / admin_username / admin_nickname）以及旧版并存的
// password、admin_name 都不会出现在文件里——前四个字段标了 json:"-"，
// password / admin_name 则在这里被显式清空。
//
// 凭据字段被清空，正是「settings.json 不再含任何密码哈希」这条不变量的实现方式：
// 它让 ClearLegacyCredentials 的调用点不必自己再 Save 一次。
//
// 写入是**原子**的：先写同目录下的临时文件，fsync 后再 rename 覆盖。
// 直接用 os.WriteFile 会先截断原文件，一旦中途崩溃或磁盘写满，留下的是一个
// 被截断的 settings.json —— 下次 Load 解析失败，进程直接 Fatal 起不来。
//
// 权限固定 0600。注意文件里**不应再有发信密钥**：字面量会被换成环境变量引用
// （见 ResolveAPIKey），所以 0600 现在保护的是站点描述一类的普通配置。
func (c *Config) Save(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 总是写出完整的 settings.json：先补默认值，让被省略的键以默认值落盘，
	// 这样文件读回来时不需要再靠代码兜底。
	c.ApplyDefaults()
	if c.Mail != nil {
		c.Mail = normalizeMailConfig(c.Mail)
	}
	// 旧版明文字段只用于迁移读取，绝不写回磁盘。
	c.Password = ""
	// 同理：admin_name 已被 admin_username / admin_nickname 取代，
	// 留着它只会让下次 Load 误以为又需要迁移一次。
	c.AdminName = ""

	// 明文发信密钥绝不回写磁盘。
	//
	// 实现方式是把字段临时摘下来、marshal 完再放回去：这样**内存里的配置不受影响**
	// （本次进程仍能正常发信），磁盘上却只剩一个指向环境变量的引用。
	// 用一个「影子结构体」也能做到，但 Config 含 sync.RWMutex，整体拷贝会触发
	// go vet 的 copylocks，得不偿失。
	savedAPIKey := ""
	if c.Mail != nil {
		savedAPIKey = c.Mail.APIKey
		if savedAPIKey != "" && !strings.HasPrefix(savedAPIKey, mailAPIKeyEnvPrefix) {
			logging.Warnf("[config] mail.api_key is a plaintext secret in settings.json; " +
				"it will NOT be written back — move it to the " + MailAPIKeyEnvVar +
				" environment variable (or the key will be gone after the next restart)")
			c.Mail.APIKey = MailAPIKeyEnvRef()
		}
	}

	logging.Debugf("[config] saving settings to %s", path)
	data, err := marshalConfig(c, "", "  ")
	if c.Mail != nil {
		c.Mail.APIKey = savedAPIKey
	}
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	data = append(data, '\n')

	if err := writeFileAtomicFn(path, data, 0o600); err != nil {
		return fmt.Errorf("write settings file: %w", err)
	}

	c.settingsPath = path
	logging.Infof("[config] settings saved to %s", path)
	return nil
}

// writeFileAtomic 以「同目录临时文件 + rename」的方式原子替换目标文件。
//
// 三个细节都是必需的：
//   - 临时文件必须与目标**同目录**：跨设备/跨卷的 rename 不是原子操作，
//     在 Windows 上还会直接失败；
//   - 先 Sync 再 Close 再 Rename：否则崩溃可能留下一个长度为 0 的新文件
//     ——比被截断的原文件更糟，因为它是「成功」的样子；
//   - 任一步失败都要清掉临时文件，不在目录里留垃圾。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	tmp, err := createTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp settings file: %w", err)
	}
	tmpName := tmp.Name()

	cleanup := func() {
		_ = closeTemp(tmp)
		_ = os.Remove(tmpName)
	}

	if err := chmodTemp(tmp, perm); err != nil {
		cleanup()
		return fmt.Errorf("set temp settings permissions: %w", err)
	}
	if _, err := writeTemp(tmp, data); err != nil {
		cleanup()
		return fmt.Errorf("write temp settings file: %w", err)
	}
	if err := syncTemp(tmp); err != nil {
		cleanup()
		return fmt.Errorf("sync temp settings file: %w", err)
	}
	if err := closeTemp(tmp); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp settings file: %w", err)
	}
	if err := renameSettings(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace settings file: %w", err)
	}
	return nil
}
