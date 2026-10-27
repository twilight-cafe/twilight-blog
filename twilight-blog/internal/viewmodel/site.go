package viewmodel

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"
)

// MaxSiteDescriptionLength 是站点描述的长度上限（按字符数，不是字节数——
// 否则一句中文会因为 UTF-8 占 3 字节而被误判超长）。
//
// 上限刻意定得小：这段文字会被放进页脚、首页标语与 <meta name="description">，
// 按搜索引擎的习惯，超过这个长度就会被截断，允许更长只会让作者以为写下的
// 后半句真的会被读者看到。
const MaxSiteDescriptionLength = 200

// DefaultSiteDescription 是站长尚未设置描述时的内置文案。
//
// 它必须与 twilight-web/src/lib/site.ts 的 FALLBACK_DESCRIPTION 逐字相同：
// 外壳（服务端渲染）与 SPA（客户端）会各自用它兜底，两处不一致的话，
// 页面在 JS 启动的那一瞬间会把描述换掉——那正是这次改动要消灭的观感问题。
//
// 服务端用它还有一个额外的好处：不执行 JS 的抓取器读到的就是这句话，
// 而不是一个空标签。
const DefaultSiteDescription = "以代码为笔，以时间为墨。"

// 代码块行数上限的合法区间。与 internal/config 的 Min/MaxCodeBlockMaxLines
// 语义一致；刻意不 import config——viewmodel 的校验是 HTTP 边界的第一道关，
// 而 config 的常量服务于持久化层，两个包不该因此建立依赖。
const (
	MinCodeBlockLines = 3
	MaxCodeBlockLines = 500
)

// 首页每页文章数的合法区间。与 internal/config 的 Min/MaxHomePageSize 语义一致。
//
// 上界 12 刻意与 handler 层的 maxPageSize 对齐，理由见 config.HomePageSizeBounds：
// 首页发出的 page_size 会被列表端点按该上限校验，越界的配置会让首页第一次请求
// 就 400。此处只做「输入合法」判断，跨包一致性由 handler 层的守护测试保证。
const (
	MinHomePageSize = 1
	MaxHomePageSize = 12
)

// DefaultHomePageSize 是首页每页文章数在「配置层未提供可用值」时的兜底。
//
// 它只在 view 层组装外壳时用到（config 层有自己的 DefaultHomePageSize 常量）。
// 之所以要有这一份：view 包不该 import config（外壳渲染是纯展示层，
// 详见 index.go 的说明），但 data-home-page-size 又**必须**永远是个正数——
// 下发 0 会让首页发出一个 page_size=0 的列表请求。
const DefaultHomePageSize = 12

// 站点统一圆角的合法区间（px）。与 internal/config 的 Min/MaxBorderRadius 语义一致。
//
// 下界 0 是**合法值**（直角），不是「未设置」的哨兵——这是它与
// MinHomePageSize（下界 1）最要紧的区别。区间只是审美边界，
// 不像 home_page_size 那样被 handler 的硬上限钉死：圆角过大只会难看，
// 不会让任何请求失败。
const (
	MinBorderRadius = 0
	MaxBorderRadius = 24
)

// DefaultBorderRadius 是圆角在「配置层未提供可用值」时的兜底，单位 px。
//
// 只在 view 层组装外壳时用到（config 层有自己的 DefaultBorderRadius）。
// 理由同 DefaultHomePageSize：view 包不该 import config，
// 而 data-border-radius **必须**永远是个可用的数字——缺席会让前端
// 回退到它自己的兜底值，服务端与客户端两份兜底值不一致时首屏会跳变。
const DefaultBorderRadius = 4

// 文章头图最大高度的合法区间（px），与 internal/config 的
// Min/MaxPostBannerHeight 语义一致。0 表示「未设置」，由调用方兜底成默认值。
const (
	MinPostBannerHeight     = 120
	MaxPostBannerHeight     = 800
	DefaultPostBannerHeight = 400
)

// SiteDescription 返回可以直接上屏的站点描述：站长设置过就用它，
// 否则回退到 DefaultSiteDescription。
//
// 之所以要有这个包装，是因为「空」在两侧含义不同：配置里的空串表示「没设置」，
// 而页面上永远要有话说。
func SiteDescription(configured string) string {
	if strings.TrimSpace(configured) == "" {
		return DefaultSiteDescription
	}
	return configured
}

// SiteSettingsRequest 修改站点级文案设置。
//
// Description 必填；CodeBlockMaxLines 是可选的三态字段：省略（nil）=保持现值，
// 数字=更新为该值。用指针而不是 int，是为了兼容只发 description 的旧客户端
// （SDK 与前端都有按字段省略的调用方），与 OptionalID 的设计动机一致。
type SiteSettingsRequest struct {
	Description string `json:"description"`
	// DescriptionEN 是英文描述；nil 表示本次不修改（兼容只发中文描述的旧客户端），
	// 空串表示清空英文版（前台回退中文）。
	DescriptionEN *string `json:"description_en,omitempty"`
	// CodeBlockMaxLines 是代码块最大显示行数；nil 表示本次请求不修改它。
	CodeBlockMaxLines *int `json:"code_block_max_lines,omitempty"`
	// HomePageSize 是首页每页文章数；nil 表示本次请求不修改它。
	HomePageSize *int `json:"home_page_size,omitempty"`
	// BorderRadius 是站点统一圆角（px）；nil 表示本次请求不修改它。
	//
	// 与另外两个三态字段的区别：这里的合法值**包含 0**，
	// 所以「nil = 不改动」与「0 = 改成直角」必须分得清——
	// 用 int（零值即 0）会把「没发这个字段」误当成「要设成直角」，
	// 于是任何只发 description 的旧客户端都会把站点的圆角悄悄改成 0。
	BorderRadius *int `json:"border_radius,omitempty"`
	// PostBannerHeight 是文章头图最大高度（px）；nil 表示本次请求不修改它。
	// 0 不是合法输入（它只表示旧配置里的「未设置」），必须给出明确值。
	PostBannerHeight *int `json:"post_banner_height,omitempty"`
	// MomentsEnabled 是微语功能开关；nil 表示本次请求不修改它。
	// 用指针是为了让「省略」与「显式 false」分得清。
	MomentsEnabled *bool `json:"moments_enabled,omitempty"`
	// CommentNotifyTo 是评论通知收件地址；nil 表示本次请求不修改它，
	// 传空串表示关闭通知。它只出现在管理员请求里，公开响应绝不下发。
	CommentNotifyTo *string `json:"comment_notify_to,omitempty"`
}

// SiteSettingsResponse 是站点设置的**公开**视图：任何人都能看到，
// 因为它描述的就是前台已经展示给所有人的那部分内容（站点描述、站长头像）。
//
// 注意这里只有这两项，而且两项都满足同一条准入标准：**它们本来就已经展示在
// 每一个读者面前**。把 settings.json 里的其他字段（密码哈希、邮件 API key、
// 限流额度、trusted_proxies）暴露出去会直接构成信息泄露，
// 新增字段前请先确认它是「已经公开可见的文案 / 形象」而不是「配置」。
type SiteSettingsResponse struct {
	Description string `json:"description"`
	// DescriptionEN 是英文站点描述；空串表示没有英文版，前端在英文界面
	// 回退到 description。恒输出，便于前端区分「未设置」与「服务端没有该字段」。
	DescriptionEN string `json:"description_en"`
	// AvatarURL 是站长头像的站内地址；空串表示站长没设头像，前端回退到
	// 首字母占位块。
	//
	// 它放在这个公开端点上，是因为头像与描述是同一个作者卡里的两样东西
	// （见 PublicSidebar）：让 SPA 为了头像再单独发一次请求，等于把一块
	// 「本来就要一起渲染」的内容拆成两次往返。
	//
	// 恒输出（不带 omitempty）：空串是有意义的取值，而字段缺席会让前端
	// 分不清「没有头像」与「服务端还没升级」。
	AvatarURL string `json:"avatar_url"`
	// CodeBlockMaxLines 是代码块最大显示行数，前台用它设置限高 CSS 变量。
	// 恒输出（不带 omitempty）：它是渲染参数而不是状态位，缺席会让前端
	// 分不清「服务端没下发」与「前端该用默认值」。
	CodeBlockMaxLines int `json:"code_block_max_lines"`
	// HomePageSize 是首页每页文章数，前台据此决定列表请求的 page_size。
	// 恒输出（不带 omitempty）：理由同 CodeBlockMaxLines。
	HomePageSize int `json:"home_page_size"`
	// BorderRadius 是站点统一圆角（px），前台据此设置 --radius CSS 变量。
	// 恒输出（不带 omitempty）：理由同 CodeBlockMaxLines。
	BorderRadius int `json:"border_radius"`
	// PostBannerHeight 是文章头图最大高度（px），前端据此设置
	// --post-banner-height CSS 变量。恒输出（不带 omitempty）：理由同上。
	PostBannerHeight int `json:"post_banner_height"`
	// MomentsEnabled 是微语功能开关。它本来就会通过导航与页脚展示给所有读者，
	// 因此放在公开响应里不构成信息泄露；前端的首屏决策仍走外壳属性。
	MomentsEnabled bool `json:"moments_enabled"`
}

// AdminSiteSettingsResponse 是**仅管理员**可读的站点设置视图。
//
// 它在公开快照之上多一个评论通知收件地址。那个地址属于管理员的私人联系方式，
// 绝不能出现在公开的 GET /api/site 里——这正是本类型单独存在的理由。
type AdminSiteSettingsResponse struct {
	SiteSettingsResponse
	CommentNotifyTo string `json:"comment_notify_to"`
}

// Validate 清洗描述：折叠换行与连续空白，使它在页脚这种单行场景里不会撑破布局。
// 允许为空（清空描述是合法意图，前台会回退到内置文案）。
// CodeBlockMaxLines 为 nil（字段省略）时跳过校验——那是「保持现值」；
// 显式给出的值必须在合法区间内，负数或 0 一律拒绝而不是夹取到边界。
// HomePageSize 同理。
func (r *SiteSettingsRequest) Validate() error {
	r.Description = normalizeSiteDescription(r.Description)
	if utf8.RuneCountInString(r.Description) > MaxSiteDescriptionLength {
		return fmt.Errorf("description must be at most %d characters", MaxSiteDescriptionLength)
	}
	if r.DescriptionEN != nil {
		normalized := normalizeSiteDescription(*r.DescriptionEN)
		if utf8.RuneCountInString(normalized) > MaxSiteDescriptionLength {
			return fmt.Errorf("description_en must be at most %d characters", MaxSiteDescriptionLength)
		}
		r.DescriptionEN = &normalized
	}
	if r.CodeBlockMaxLines != nil {
		lines := *r.CodeBlockMaxLines
		if lines < MinCodeBlockLines || lines > MaxCodeBlockLines {
			return fmt.Errorf("code_block_max_lines must be between %d and %d", MinCodeBlockLines, MaxCodeBlockLines)
		}
	}
	if r.HomePageSize != nil {
		size := *r.HomePageSize
		if size < MinHomePageSize || size > MaxHomePageSize {
			return fmt.Errorf("home_page_size must be between %d and %d", MinHomePageSize, MaxHomePageSize)
		}
	}
	// 注意这里**没有** `radius == 0` 的早退：0 是合法配置，
	// 只要落在 [MinBorderRadius, MaxBorderRadius] 内就该被接受。
	if r.BorderRadius != nil {
		radius := *r.BorderRadius
		if radius < MinBorderRadius || radius > MaxBorderRadius {
			return fmt.Errorf("border_radius must be between %d and %d", MinBorderRadius, MaxBorderRadius)
		}
	}
	if r.PostBannerHeight != nil {
		height := *r.PostBannerHeight
		if height < MinPostBannerHeight || height > MaxPostBannerHeight {
			return fmt.Errorf("post_banner_height must be between %d and %d", MinPostBannerHeight, MaxPostBannerHeight)
		}
	}
	if r.CommentNotifyTo != nil {
		address := strings.TrimSpace(*r.CommentNotifyTo)
		if err := ValidateNotifyAddress(address); err != nil {
			return err
		}
		r.CommentNotifyTo = &address
	}
	return nil
}

// ValidateNotifyAddress 校验评论通知收件地址（空串 = 关闭通知）。
//
// 规则与 config.ValidateEmailAddress 保持一致，但刻意不 import config：
// viewmodel 是 HTTP 边界，config 是持久化层，两者不该为了一个字符串规则建立依赖。
// 两边都要求「纯地址、不带显示名」——`Twilight <a@b>` 在邮件头里合法，
// 但本站只接受一个地址，允许显示名只会让「保存了什么」与「发到哪」看起来不一致。
func ValidateNotifyAddress(address string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil {
		return fmt.Errorf("comment_notify_to must be a valid email address")
	}
	if parsed.Name != "" || parsed.Address != address {
		return fmt.Errorf("comment_notify_to must be a bare address without a display name")
	}
	return nil
}

// normalizeSiteDescription 折行成空格、去掉其余控制字符、压缩连续空白。
func normalizeSiteDescription(value string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	return strings.Join(strings.Fields(cleaned), " ")
}
