package viewmodel

import "blog-engine/internal/logging"

// PaginatedResponse 是所有分页接口的统一信封。
// 前端的分页组件直接依赖这几个字段名，改名会同时破坏前后端。
type PaginatedResponse[T any] struct {
	Items      []T   `json:"items"`
	Page       int   `json:"page"`
	PageSize   int   `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// NewPaginatedResponse 组装分页信封。两个容易踩的点：
//   - items 为 nil 时规范成空数组，客户端不必对 null 做特判；
//   - TotalPages 向上取整；当 total 为 0 或 pageSize 非法时取 0——
//     此时「第 1 页」在语义上是一份空结果，而不是一个不存在的页，前端要按 0 处理。
func NewPaginatedResponse[T any](items []T, total int64, page, pageSize int) PaginatedResponse[T] {
	logging.Debugf("[viewmodel] NewPaginatedResponse called items=%d total=%d page=%d page_size=%d", len(items), total, page, pageSize)
	if items == nil {
		items = make([]T, 0)
	}
	totalPages := 0
	if total > 0 && pageSize > 0 {
		totalPages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}
	return PaginatedResponse[T]{
		Items:      items,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages,
	}
}
