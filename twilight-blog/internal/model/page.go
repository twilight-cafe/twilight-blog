package model

import "errors"

var ErrPageNotFound = errors.New("page not found")

// Page 是一个独立页面（如「关于」「友链」）。字段语义与 Article 相同：
// 时间戳为 Unix 秒，DeletedAt 为 nil 表示未删除，同样只做软删。
//
// Title / Content 是中文版本，也是唯一必填的一份；TitleEN / ContentEN 是
// 附加的英文版本，空串表示没有，前台按当前语言取用并逐字段回退中文。
// 与文章的区别仅在「没有标签」。
type Page struct {
	ID        int64
	Title     string
	Content   string
	TitleEN   string
	ContentEN string
	CreatedAt int64
	UpdatedAt int64
	DeletedAt *int64
}
