package viewmodel

import (
	"fmt"
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
	// CodeBlockMaxLines 是代码块最大显示行数；nil 表示本次请求不修改它。
	CodeBlockMaxLines *int `json:"code_block_max_lines,omitempty"`
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
}

// Validate 清洗描述：折叠换行与连续空白，使它在页脚这种单行场景里不会撑破布局。
// 允许为空（清空描述是合法意图，前台会回退到内置文案）。
// CodeBlockMaxLines 为 nil（字段省略）时跳过校验——那是「保持现值」；
// 显式给出的值必须在合法区间内，负数或 0 一律拒绝而不是夹取到边界。
func (r *SiteSettingsRequest) Validate() error {
	r.Description = normalizeSiteDescription(r.Description)
	if utf8.RuneCountInString(r.Description) > MaxSiteDescriptionLength {
		return fmt.Errorf("description must be at most %d characters", MaxSiteDescriptionLength)
	}
	if r.CodeBlockMaxLines != nil {
		lines := *r.CodeBlockMaxLines
		if lines < MinCodeBlockLines || lines > MaxCodeBlockLines {
			return fmt.Errorf("code_block_max_lines must be between %d and %d", MinCodeBlockLines, MaxCodeBlockLines)
		}
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
