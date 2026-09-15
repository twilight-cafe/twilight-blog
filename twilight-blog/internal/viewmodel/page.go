package viewmodel

import (
	"fmt"
	"strings"

	"blog-engine/internal/model"

	"blog-engine/internal/logging"
)

// CreatePageRequest 是新建页面的请求体。页面没有标签字段。
type CreatePageRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// UpdatePageRequest 是更新页面的请求体。
//
// Content 用指针：nil 表示「这次不改正文」，非 nil 才是「把正文换成这个值」。
// 这与 UpdateArticleRequest 的处理一致，原因是同一个：非指针时，一个只改标题的
// PUT 会把页面正文整段清空，而且返回 200——静默的数据丢失。
type UpdatePageRequest struct {
	Title   string  `json:"title"`
	Content *string `json:"content"`
}

// PageResponse 是页面的对外形状。字段与 ArticleResponse 基本一致，
// 差别是没有 tags。
type PageResponse struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	DeletedAt *int64 `json:"deleted_at,omitempty"`
}

func NewPageResponse(page *model.Page) PageResponse {
	logging.Debugf("[viewmodel] NewPageResponse called")
	return PageResponse{
		ID:        page.ID,
		Title:     page.Title,
		Content:   page.Content,
		CreatedAt: page.CreatedAt,
		UpdatedAt: page.UpdatedAt,
		DeletedAt: page.DeletedAt,
	}
}

func NewPageResponseList(pages []model.Page) []PageResponse {
	logging.Debugf("[viewmodel] NewPageResponseList called")
	result := make([]PageResponse, 0, len(pages))
	for i := range pages {
		result = append(result, NewPageResponse(&pages[i]))
	}
	return result
}

func (r *CreatePageRequest) Validate() error {
	r.Title = strings.TrimSpace(r.Title)
	if r.Title == "" {
		return fmt.Errorf("title is required")
	}
	return nil
}

func (r *UpdatePageRequest) Validate() error {
	r.Title = strings.TrimSpace(r.Title)
	if r.Title == "" {
		return fmt.Errorf("title is required")
	}
	return nil
}
