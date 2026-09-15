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

// SiteSettingsRequest 修改站点级文案设置。目前只有「网站描述」一项。
//
// 它刻意是独立于 admin_profile 的一条路由：站长资料（登录名 / 展示名）与
// 站点文案是两件事，更新频率与出错后的影响面都不一样。
type SiteSettingsRequest struct {
	Description string `json:"description"`
}

// SiteSettingsResponse 是站点设置的**公开**视图：任何人都能看到，
// 因为它描述的就是前台已经展示给所有人的那段文字。
//
// 注意这里只有描述。把 settings.json 里的其他字段（密码哈希、邮件 API key、
// 限流额度、trusted_proxies）暴露出去会直接构成信息泄露，
// 新增字段前请先确认它是「已经公开可见的文案」而不是「配置」。
type SiteSettingsResponse struct {
	Description string `json:"description"`
}

// Validate 清洗描述：折叠换行与连续空白，使它在页脚这种单行场景里不会撑破布局。
// 允许为空（清空描述是合法意图，前台会回退到内置文案）。
func (r *SiteSettingsRequest) Validate() error {
	r.Description = normalizeSiteDescription(r.Description)
	if utf8.RuneCountInString(r.Description) > MaxSiteDescriptionLength {
		return fmt.Errorf("description must be at most %d characters", MaxSiteDescriptionLength)
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
