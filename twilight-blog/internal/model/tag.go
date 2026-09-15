package model

import "errors"

// 标签名在数据库层是 COLLATE NOCASE UNIQUE，因此 "Go" 与 "go" 属于同一个标签；
// 所有按名字的查找也都显式带 COLLATE NOCASE 以保持一致。
var (
	ErrTagNotFound    = errors.New("tag not found")
	ErrTagNameExists  = errors.New("tag name already exists")
	ErrInvalidTagName = errors.New("invalid tag name")
)

// Tag 是一个标签。它没有独立的「新建」入口，而是由文章正文提交的标签名隐式产生
// （见 replaceArticleTags）。因此删除文章只解除关联，不会删掉标签本身。
// ArticleCount 是引用该标签的未删除文章数，由查询聚合得出，不是表上的列。
type Tag struct {
	ID           int64
	Name         string
	ArticleCount int64
}
