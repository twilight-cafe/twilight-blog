package service

import (
	"fmt"
	"strings"

	"blog-engine/internal/logging"
	"blog-engine/internal/model"
)

// CommentNotificationSender 是通知投递所需的最小接口。
//
// 只要一个 Send：通知**必须**走 EmailService，而不是直接拿 mail.Mailer——
// 前者会把每条通知写进 emails 表并受每日配额约束，后者什么记录都不留。
// 站长在后台能看到「今天到底发了多少封」，也才能在配额耗尽时明白
// 为什么收不到通知。这里声明成接口只是为了测试能注入假实现。
type CommentNotificationSender interface {
	Send(to, subject, body string) error
}

// CommentNotifier 在访客发表评论后给站长发一封提醒邮件。
//
// 三条不可动摇的规则（都与「通知是附属功能」这个定位有关）：
//
//  1. **只通知访客评论**。站长自己写下的评论不再给站长发一封——那只会训练
//     收件人忽略这个地址。调用方（handler）负责判断身份，这里只管发。
//  2. **失败绝不影响评论创建**。发送在独立 goroutine 里进行，错误只写日志；
//     评论的 201 已经在数据库落库后返回，不等待、也不因为发信失败而回滚。
//  3. **收件人每次现读**。站长在设置页改完地址立即生效，不需要重启进程。
//     空串表示「关闭通知」，此时连邮件内容都不拼。
type CommentNotifier struct {
	sender    CommentNotificationSender
	recipient func() string
}

// NewCommentNotifier 构造通知器。recipient 为 nil 时按「未配置」处理，
// 避免调用方漏传时在发表评论的路径上 panic。
func NewCommentNotifier(sender CommentNotificationSender, recipient func() string) *CommentNotifier {
	return &CommentNotifier{sender: sender, recipient: recipient}
}

// Notify 异步发出新评论通知。它不返回错误：调用方没有可做的补救动作，
// 而把错误抛回去只会让「评论是否成功」与「邮件是否送达」耦合在一起。
func (n *CommentNotifier) Notify(comment *model.Comment) {
	if comment == nil || n.recipient == nil {
		return
	}
	to := strings.TrimSpace(n.recipient())
	if to == "" {
		return
	}

	subject, body := commentNotification(comment)
	go func() {
		if err := n.sender.Send(to, subject, body); err != nil {
			// 只记日志。这里刻意不重试：评论通知的时效性以分钟计，
			// 重试只会把一条已经过时的提醒反复塞进配额，还可能刷爆收件人。
			logging.Errorf("[comment-notify] failed to notify %s about comment id=%d: %v", to, comment.ID, err)
		}
	}()
}

// commentNotification 组装主题与正文。
//
// 主题带上作者与目标，是为了让站长在收件箱列表里就能判断值不值得点开；
// 正文带上完整内容与目标 id，是为了不必先登录后台再找那条评论。
func commentNotification(comment *model.Comment) (subject, body string) {
	author := commentAuthorLabel(comment)
	subject = fmt.Sprintf("[Twilight] %s 评论了%s", author, commentTargetLabel(comment))
	body = fmt.Sprintf(
		"%s 在%s（#%d）发表了新评论：\n\n%s\n\n—— 这封邮件由站点自动发出，回复不会送达访客。",
		author,
		commentTargetLabel(comment),
		comment.TargetID,
		comment.Content,
	)
	return subject, body
}

// commentTargetLabel 把 target_type 翻成人话。未知类型原样带出，
// 这样日志与邮件里至少能看到服务端存了什么，而不是一句「某个地方」。
func commentTargetLabel(comment *model.Comment) string {
	switch comment.TargetType {
	case "article":
		return "文章"
	case "moment":
		return "微语"
	case "page":
		return "页面"
	default:
		return comment.TargetType
	}
}

// commentAuthorLabel 决定署名：站长用展示名，访客用昵称，历史数据（昵称为空）
// 回退到与前台一致的匿名署名——两处必须同一句话，否则通知里的名字
// 与评论区里的名字对不上。
func commentAuthorLabel(comment *model.Comment) string {
	if comment.AuthorType == "admin" {
		if name := strings.TrimSpace(comment.AdminName); name != "" {
			return name
		}
	}
	if name := strings.TrimSpace(comment.Nickname); name != "" {
		return name
	}
	return model.AnonymousDisplayName
}
