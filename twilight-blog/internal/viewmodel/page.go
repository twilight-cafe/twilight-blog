package viewmodel

import (
	"fmt"
	"strings"

	"blog-engine/internal/model"

	"blog-engine/internal/logging"
)

// CreatePageRequest 是新建页面的请求体。页面没有标签字段。
//
// TitleEN / ContentEN 与文章同义：可选，空串表示没有英文版。
type CreatePageRequest struct {
	Title     string `json:"title"`
	Content   string `json:"content"`
	TitleEN   string `json:"title_en"`
	ContentEN string `json:"content_en"`
}

// UpdatePageRequest 是更新页面的请求体。
//
// Content / ContentEN 用指针：nil 表示「这次不改正文」，非 nil 才是「把正文
// 换成这个值」。这与 UpdateArticleRequest 的处理一致，原因是同一个：非指针时，
// 一个只改标题的 PUT 会把页面正文整段清空，而且返回 200——静默的数据丢失。
// TitleEN 用指针同理：缺省表示不改英文标题，而不是清空。
type UpdatePageRequest struct {
	Title     string  `json:"title"`
	Content   *string `json:"content"`
	TitleEN   *string `json:"title_en"`
	ContentEN *string `json:"content_en"`
}

// PageResponse 是页面的对外形状。字段与 ArticleResponse 基本一致，
// 差别是没有 tags。
type PageResponse struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
	// TitleEN / ContentEN 恒输出（空串表示没有英文版）。前端按语言选择，
	// 英文缺失时逐字段回退中文，因此不能让字段缺席。
	TitleEN   string `json:"title_en"`
	ContentEN string `json:"content_en"`
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
		TitleEN:   page.TitleEN,
		ContentEN: page.ContentEN,
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
	r.TitleEN = strings.TrimSpace(r.TitleEN)
	if r.Title == "" {
		return fmt.Errorf("title is required")
	}
	return nil
}

func (r *UpdatePageRequest) Validate() error {
	r.Title = strings.TrimSpace(r.Title)
	if r.TitleEN != nil {
		trimmed := strings.TrimSpace(*r.TitleEN)
		r.TitleEN = &trimmed
	}
	if r.Title == "" {
		return fmt.Errorf("title is required")
	}
	return nil
}
