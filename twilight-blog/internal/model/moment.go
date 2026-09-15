package model

import "errors"

var ErrMomentNotFound = errors.New("moment not found")

// Moment 是一条「动态」（短内容 + 可选配图），软删语义与文章一致。
//
// MediaIDs 与 Media 是同一批关联媒体的两种视图：写路径只读 MediaIDs
// （按 id 列表重建关联），读路径则两者都填充，Media 用于取文件名等元数据，
// 且只包含未被删除的媒体。
type Moment struct {
	ID        int64
	Content   string
	MediaIDs  []int64
	Media     []Media
	CreatedAt int64
	UpdatedAt int64
	DeletedAt *int64
}
