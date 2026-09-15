package viewmodel

import (
	"fmt"
	"strings"

	"blog-engine/internal/model"

	"blog-engine/internal/logging"
)

// TagResponse 是标签的对外形状；ArticleCount 统计的是「未删除文章」的引用数。
type TagResponse struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	ArticleCount int64  `json:"article_count"`
}

// CountResponse 是「只返回一个计数」的接口所用统一形状
// （例如某标签下的文章数）。
type CountResponse struct {
	Count int64 `json:"count"`
}

// RenameTagRequest 是标签改名请求。
type RenameTagRequest struct {
	Name string `json:"name"`
}

func NewTagResponse(tag *model.Tag) TagResponse {
	logging.Debugf("[viewmodel] NewTagResponse called")
	return TagResponse{
		ID:           tag.ID,
		Name:         tag.Name,
		ArticleCount: tag.ArticleCount,
	}
}

func NewTagResponseList(tags []model.Tag) []TagResponse {
	logging.Debugf("[viewmodel] NewTagResponseList called")
	result := make([]TagResponse, 0, len(tags))
	for i := range tags {
		result = append(result, NewTagResponse(&tags[i]))
	}
	return result
}

func (r *RenameTagRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	return nil
}
