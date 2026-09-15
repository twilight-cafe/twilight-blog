// Package mail 通过 Resend 发送事务邮件（目前只有验证码）。
//
// 与 search 不同，邮件是**可选依赖**：未配置 API key 或发件地址时，
// Send 返回哨兵错误，由 service 层翻译成「发信已禁用」，
// 相应接口返回 503，而不会影响博客其余部分的可用性。
//
// 本包不做重试：失败直接返回错误。验证码类邮件是用户可以主动重发的，
// 把重试放在服务端只会让「到底发出去了没有」变得更难判断。
package mail

import (
	"errors"
	"fmt"
	"strings"

	"github.com/resend/resend-go/v4"

	"blog-engine/internal/logging"
)

var (
	ErrResendNotConfigured = errors.New("Resend API key is not configured")
	ErrMailNotConfigured   = errors.New("mail.from is not configured")
)

// Config 是构造 Mailer 所需的配置。
// 两项都允许为空——真正的校验推迟到 Send，这样服务仍能启动，
// 并在用户请求发信时给出明确的「未配置」提示。
type Config struct {
	From   string
	APIKey string
}

// Mailer 是发信能力的抽象，便于在测试或未配置场景下替换实现。
type Mailer interface {
	Send(to, subject, body string) error
}

// ResendMailer 是基于 Resend HTTP SDK 的实现。
type ResendMailer struct {
	client *resend.Client
	from   string
}

// New 构造 Resend 客户端。
// API key 为空不会在这里报错——若此时就失败，服务将无法启动，
// 而「没配邮件」显然不该阻止整个博客运行。
func New(cfg Config) (*ResendMailer, error) {
	return &ResendMailer{
		client: resend.NewClient(cfg.APIKey),
		from:   strings.TrimSpace(cfg.From),
	}, nil
}

// Send 发送一封**纯文本**邮件（使用 Text 字段，不拼 HTML）。
//
// 两个前置检查分别返回不同的哨兵错误：缺发件地址属于配置不完整，
// 缺 API key 属于功能未启用。调用方据此能给出比「发送失败」有用的提示。
func (m *ResendMailer) Send(to, subject, body string) error {
	if m.from == "" {
		return ErrMailNotConfigured
	}
	if m.client == nil || strings.TrimSpace(m.client.ApiKey) == "" {
		return ErrResendNotConfigured
	}

	params := &resend.SendEmailRequest{
		From:    m.from,
		To:      []string{to},
		Subject: subject,
		Text:    body,
	}

	logging.Debugf("[mail] sending via Resend to=%s subject=%q", redactEmail(to), subject)
	sent, err := m.client.Emails.Send(params)
	if err != nil {
		logging.Errorf("[mail] Resend send failed to=%s subject=%q: %v", redactEmail(to), subject, err)
		return fmt.Errorf("send email via Resend: %w", err)
	}
	logging.Infof("[mail] email sent via Resend to=%s subject=%q id=%q", redactEmail(to), subject, sent.Id)
	return nil
}

// redactEmail 保留域名、抹掉本地部分。
//
// 实现已经搬到 logging.RedactEmail：它是日志卫生规则而不是邮件逻辑，而且
// service 层同样需要——此前两边各写各的，结果是 mail 脱敏、service 打完整地址。
// 这里保留一层薄封装，只是为了让本文件里的调用点读起来短一些。
func redactEmail(address string) string {
	return logging.RedactEmail(address)
}
