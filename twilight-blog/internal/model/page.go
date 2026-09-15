package model

import "errors"

var ErrPageNotFound = errors.New("page not found")

// Page 是一个独立页面（如「关于」「友链」）。字段语义与 Article 相同：
// 时间戳为 Unix 秒，DeletedAt 为 nil 表示未删除，同样只做软删。
// 与文章的区别仅在「没有标签」。
type Page struct {
	ID        int64
	Title     string
	Content   string
	CreatedAt int64
	UpdatedAt int64
	DeletedAt *int64
}
