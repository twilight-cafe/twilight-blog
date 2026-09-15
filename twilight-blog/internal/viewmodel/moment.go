package viewmodel

import (
	"fmt"
	"strings"

	"blog-engine/internal/model"

	"blog-engine/internal/logging"
)

// CreateMomentRequest 是新建动态的请求体。MediaIDs 缺失等价于没有配图。
type CreateMomentRequest struct {
	Content  string  `json:"content"`
	MediaIDs []int64 `json:"media_ids"`
}

type UpdateMomentRequest struct {
	Content string `json:"content"`
	// MediaIDs 用指针，是为了让「字段缺失」表示「保留原有配图」，
	// 而不是把它误当成「清空全部配图」。
	MediaIDs *[]int64 `json:"media_ids"`
}

// MomentResponse 是动态的对外形状。
// 注意字段名是 media_items 而不是 media：它装的是完整的媒体对象（含 URL），
// 不是 id 列表——id 列表只出现在写请求里。
type MomentResponse struct {
	ID         int64           `json:"id"`
	Content    string          `json:"content"`
	MediaItems []MediaResponse `json:"media_items"`
	CreatedAt  int64           `json:"created_at"`
	UpdatedAt  int64           `json:"updated_at"`
	DeletedAt  *int64          `json:"deleted_at,omitempty"`
}

func NewMomentResponse(moment *model.Moment) MomentResponse {
	logging.Debugf("[viewmodel] NewMomentResponse called")
	mediaItems := make([]MediaResponse, 0, len(moment.Media))
	for i := range moment.Media {
		mediaItems = append(mediaItems, NewMediaResponse(&moment.Media[i]))
	}
	return MomentResponse{
		ID:         moment.ID,
		Content:    moment.Content,
		MediaItems: mediaItems,
		CreatedAt:  moment.CreatedAt,
		UpdatedAt:  moment.UpdatedAt,
		DeletedAt:  moment.DeletedAt,
	}
}

func NewMomentResponseList(moments []model.Moment) []MomentResponse {
	logging.Debugf("[viewmodel] NewMomentResponseList called")
	result := make([]MomentResponse, 0, len(moments))
	for i := range moments {
		result = append(result, NewMomentResponse(&moments[i]))
	}
	return result
}

func (r *CreateMomentRequest) Validate() error {
	r.Content = strings.TrimSpace(r.Content)
	if r.Content == "" {
		return fmt.Errorf("content is required")
	}
	return nil
}

func (r *UpdateMomentRequest) Validate() error {
	r.Content = strings.TrimSpace(r.Content)
	if r.Content == "" {
		return fmt.Errorf("content is required")
	}
	return nil
}
