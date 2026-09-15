package model

import "errors"

var ErrEmailNotFound = errors.New("email not found")

// Email 是一条邮件发送记录（用于后台的邮件列表与发送量统计）。
// 它是「已发送留痕」，不是待发队列——写入即代表发送动作已经发生。
type Email struct {
	ID        int64
	FromEmail string
	ToEmail   string
	Subject   string
	Body      string
	SentAt    int64
	CreatedAt int64
}
