package viewmodel

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"blog-engine/internal/model"

	"blog-engine/internal/logging"
)

// CreateCommentRequest 是发表评论的请求体。
//
// 这里**没有昵称字段**：评论署名取自作者身份（访客账号的 nickname 或站长的
// 展示名），不由请求指定。曾经放开过它，结果是同一个人可以顶着不同名字发言，
// 而「昵称」也随之有了两个互相矛盾的来源。
type CreateCommentRequest struct {
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`
	ParentID   *int64 `json:"parent_id"`
	Content    string `json:"content"`
}

// CommentAuthorResponse 是评论作者的对外形状，按作者类型互斥填充：
// 访客评论填 Nickname（管理员鉴权的响应里额外带 Email），站长评论填 Name。
//
// 为什么不共用一个字段：拆开后客户端仅凭 JSON 形状就能区分两种作者，
// 不需要再去比对某个字段的取值来猜。
type CommentAuthorResponse struct {
	Type string `json:"type"`
	ID   *int64 `json:"id,omitempty"`
	// Email 只在管理员已鉴权的响应里填充。公开视图刻意不包含它：
	// 把评论者邮箱暴露给匿名读者曾是一次隐私泄露。
	Email string `json:"email,omitempty"`
	// Name 是站长作者的显示名。
	Name string `json:"name,omitempty"`
	// Nickname 是访客作者的显示名。它与 Name 是两个独立字段，
	// 好让客户端凭 JSON 形状区分作者类型，而不必去检视某个共用字段的取值。
	Nickname string `json:"nickname,omitempty"`
}

// CommentResponse 是评论的对外形状。
// LikedByMe 是「相对于当前请求者」的字段：未登录时恒为 false，
// 也就是说同一个评论对象对不同用户会呈现不同的取值。
type CommentResponse struct {
	ID         int64                 `json:"id"`
	Author     CommentAuthorResponse `json:"author"`
	TargetType string                `json:"target_type"`
	TargetID   int64                 `json:"target_id"`
	ParentID   *int64                `json:"parent_id,omitempty"`
	Content    string                `json:"content"`
	LikeCount  int64                 `json:"like_count"`
	LikedByMe  bool                  `json:"liked_by_me"`
	CreatedAt  int64                 `json:"created_at"`
	UpdatedAt  int64                 `json:"updated_at"`
	DeletedAt  *int64                `json:"deleted_at,omitempty"`
}

// anonDisplayName 用于昵称为空的评论（昵称字段引入之前创建的历史数据）。
// 它刻意不回退成邮箱的本地部分——那会泄露半个私人邮箱地址。
//
// 取值与 service 层的兜底共用 model.AnonymousDisplayName：两处各写一份字面量，
// 一旦漂移，同一条评论在「刚发表」与「再次拉取」两个响应里会显示不同的名字。
const anonDisplayName = model.AnonymousDisplayName

// NewCommentResponse 渲染一条评论。
// includeEmail 只有在调用方是「已鉴权的管理员」时才可为 true。
func NewCommentResponse(comment *model.Comment, includeEmail bool) CommentResponse {
	logging.Debugf("[viewmodel] NewCommentResponse called")
	author := CommentAuthorResponse{
		Type: comment.AuthorType,
	}
	if comment.AuthorType == "user" {
		author.ID = comment.UserID
		author.Nickname = strings.TrimSpace(comment.Nickname)
		if author.Nickname == "" {
			author.Nickname = anonDisplayName
		}
		if includeEmail {
			author.Email = comment.UserEmail
		}
	} else {
		author.Name = comment.AdminName
		if author.Name == "" {
			author.Name = "Admin"
		}
	}

	return CommentResponse{
		ID:         comment.ID,
		Author:     author,
		TargetType: comment.TargetType,
		TargetID:   comment.TargetID,
		ParentID:   comment.ParentID,
		Content:    comment.Content,
		LikeCount:  comment.LikeCount,
		LikedByMe:  comment.LikedByMe,
		CreatedAt:  comment.CreatedAt,
		UpdatedAt:  comment.UpdatedAt,
		DeletedAt:  comment.DeletedAt,
	}
}

func NewCommentResponseList(comments []model.Comment, includeEmail bool) []CommentResponse {
	logging.Debugf("[viewmodel] NewCommentResponseList called")
	result := make([]CommentResponse, 0, len(comments))
	for i := range comments {
		result = append(result, NewCommentResponse(&comments[i], includeEmail))
	}
	return result
}

func (r *CreateCommentRequest) Validate() error {
	r.TargetType = strings.TrimSpace(r.TargetType)
	r.Content = strings.TrimSpace(r.Content)

	if r.TargetType == "" {
		return fmt.Errorf("target_type is required")
	}
	if r.TargetID <= 0 {
		return fmt.Errorf("target_id must be a positive integer")
	}
	if r.Content == "" {
		return fmt.Errorf("content is required")
	}
	if utf8.RuneCountInString(r.Content) > model.MaxCommentContentLength {
		return fmt.Errorf("content must be at most %d characters", model.MaxCommentContentLength)
	}
	return nil
}
