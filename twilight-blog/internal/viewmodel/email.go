package viewmodel

import (
	"blog-engine/internal/model"

	"blog-engine/internal/logging"
)

// EmailResponse 是邮件留痕的对外形状，包含完整正文。
// 收件人与正文都属于管理员可见范围，不要把它用到公开接口上。
type EmailResponse struct {
	ID        int64  `json:"id"`
	FromEmail string `json:"from_email"`
	ToEmail   string `json:"to_email"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	SentAt    int64  `json:"sent_at"`
	CreatedAt int64  `json:"created_at"`
}

func NewEmailResponse(email *model.Email) EmailResponse {
	logging.Debugf("[viewmodel] NewEmailResponse called")
	return EmailResponse{
		ID:        email.ID,
		FromEmail: email.FromEmail,
		ToEmail:   email.ToEmail,
		Subject:   email.Subject,
		Body:      email.Body,
		SentAt:    email.SentAt,
		CreatedAt: email.CreatedAt,
	}
}

func NewEmailResponseList(emails []model.Email) []EmailResponse {
	logging.Debugf("[viewmodel] NewEmailResponseList called")
	result := make([]EmailResponse, 0, len(emails))
	for i := range emails {
		result = append(result, NewEmailResponse(&emails[i]))
	}
	return result
}
