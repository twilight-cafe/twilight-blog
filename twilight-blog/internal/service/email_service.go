package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"blog-engine/internal/logging"
	"blog-engine/internal/mail"
	"blog-engine/internal/model"
)

// ErrEmailSendingDisabled 表示本部署没有可用的发信配置（缺 API key 或缺发件人）。
//
// ErrDailyEmailLimitReached 表示当日发送配额已用尽。注意它的文案会直接展示给访客，
// 所以刻意写成了人话而不是工程语言——改文案前先确认这是面向用户的内容。
var (
	ErrEmailSendingDisabled   = errors.New("email sending is disabled")
	ErrDailyEmailLimitReached = errors.New("博主买不起更多的邮件了，请明天再来。")
)

// DailyEmailLimit 是每日发送上限。配额按自然日重置（以服务器本地时区计）。
const DailyEmailLimit = 100

// EmailSender 是「需要发信的服务」所依赖的接口。EmailService 实现它，
// 从而让成功发出的邮件能够被留痕记录。
//
// SendVerification 是 Send 的受控变体：这封信的正文里含有一枚验证码。
// 两者分开而不是给 Send 加一个「密钥」参数，是为了让调用方必须**明说**
// 「这封信里有秘密」——留痕时打码是编译期就无法绕过的义务，
// 而不是一个「实现者记得就做、忘了就静默泄露」的可选动作。
type EmailSender interface {
	Send(to, subject, body string) error
	SendVerification(to, subject, body, code string) error
}

type EmailService struct {
	mailer mail.Mailer
	repo   model.EmailRepository
	from   string
}

func NewEmailService(mailer mail.Mailer, repo model.EmailRepository, from string) *EmailService {
	return &EmailService{
		mailer: mailer,
		repo:   repo,
		from:   from,
	}
}

// Send 发送一封邮件并留痕。顺序是「查当日配额 → 发信 → 写记录」。
//
// 几点值得注意：
//   - 配额按「今日零点起已发送条数 >= DailyEmailLimit」判断，跨自然日重置；
//   - 判断与发送之间不是原子的，并发请求可能一起越过上限。配额是软约束，不是安全边界；
//   - 写留痕用 context.Background() 而不是调用方的 ctx——信已经发出去了，
//     不能因为请求上下文取消就把这条记录丢掉；
//   - 「没配 API key」与「没配发件人」对调用方是同一件事：本部署发不了信。
//     因此统一归成 ErrEmailSendingDisabled，让接口能回 503，
//     而不是把一个配置细节当成莫名其妙的 500 抛出去。
func (s *EmailService) Send(to, subject, body string) error {
	return s.deliver(to, subject, body, body)
}

// SendVerification 发送一枚验证码，并保证它**不进入留痕记录**。
//
// 正文照原样发出（收件人当然要看得到验证码），但落库的那一份会把验证码
// 换成占位符。这条边界值得单独写一个方法：邮件日志是可通过
// GET /api/admin/emails/:id 读到的，原样存正文等于把「重置任意账号密码」
// 所需要的最后一样东西摆在了日志里——验证码的有效期只有 10 分钟，
// 但日志里的那一份是永久的。
func (s *EmailService) SendVerification(to, subject, body, code string) error {
	return s.deliver(to, subject, body, redactSecret(body, code))
}

// redactSecret 生成留痕用的正文：把出现过的密钥替换成占位符。
//
// 若正文里根本找不到这枚密钥（调用方把两者对错了），就整段作废而不是原样保存：
// 留痕要证明的是「这封信发出去了」，不是「原文写了什么」，
// 而原样保存的代价是把一个未知的秘密写进了数据库。
func redactSecret(body, secret string) string {
	if secret == "" {
		return body
	}
	redacted := strings.ReplaceAll(body, secret, "[redacted]")
	if redacted == body {
		logging.Warnf("[email] verification body did not contain the supplied code; storing a redacted placeholder instead")
		return "[redacted: the stored body did not contain the supplied verification code]"
	}
	return redacted
}

// deliver 是 Send 与 SendVerification 共用的投递路径。body 用于真正发信，
// storedBody 用于落库——两者分开就是为了让「发出去的内容」与「留下的记录」
// 可以不同，且这个不同点只由调用方选择的方法决定。
func (s *EmailService) deliver(to, subject, body, storedBody string) error {
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	count, err := s.repo.CountSince(context.Background(), startOfDay)
	if err != nil {
		logging.Errorf("[email] count daily sent emails failed: %v", err)
		return fmt.Errorf("count daily sent emails: %w", err)
	}
	if count >= DailyEmailLimit {
		logging.Warnf("[email] daily email limit reached count=%d limit=%d", count, DailyEmailLimit)
		return ErrDailyEmailLimitReached
	}

	if err := s.mailer.Send(to, subject, body); err != nil {
		// Both "no API key" and "no from address" mean the same thing to a caller:
		// this deployment cannot send mail at all. Reporting them as one sentinel
		// keeps the API answering 503 instead of surfacing a configuration detail
		// as an unexplained 500.
		if errors.Is(err, mail.ErrResendNotConfigured) || errors.Is(err, mail.ErrMailNotConfigured) {
			return ErrEmailSendingDisabled
		}
		return err
	}

	nowUnix := now.Unix()
	email := &model.Email{
		FromEmail: s.from,
		ToEmail:   to,
		Subject:   subject,
		Body:      storedBody,
		SentAt:    nowUnix,
		CreatedAt: nowUnix,
	}
	if err := s.repo.Create(context.Background(), email); err != nil {
		logging.Errorf("[email] store sent email failed to=%q subject=%q: %v", logging.RedactEmail(to), subject, err)
		return fmt.Errorf("store sent email: %w", err)
	}
	logging.Infof("[email] stored sent email id=%d to=%q subject=%q", email.ID, logging.RedactEmail(to), subject)
	return nil
}

func (s *EmailService) List(ctx context.Context, p model.Pagination) ([]model.Email, int64, error) {
	logging.Debugf("[email] list page=%d page_size=%d", p.Page, p.PageSize)
	return s.repo.List(ctx, p)
}

func (s *EmailService) Get(ctx context.Context, id int64) (*model.Email, error) {
	logging.Debugf("[email] get id=%d", id)
	return s.repo.GetByID(ctx, id)
}
