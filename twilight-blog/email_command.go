package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/logging"
	"blog-engine/internal/mail"
	"blog-engine/internal/model"
	"blog-engine/internal/service"
)

// 本文件实现 `<executable> email test` 子命令：用配置好的 Resend 账号
// 发一封测试邮件，用来验证发信链路是否打通（域名验证、API key、发件地址）。
//
// 它刻意走与主进程**完全相同**的构造路径（config.Load → database.Open →
// mail.New → EmailService），所以测试成功基本就说明服务端的发信配置是对的。
//
// 下面两个常量与 main.go 中同名，但作用域互不影响（那边是 main 的局部常量），
// 改路径时需要两处一起改。
const emailCommandUsage = `Usage:
  <executable> email test -target recipient@example.com

Commands:
  test   Send a test email through the configured Resend account.
`

const (
	settingsPath = "settings.json"
	databasePath = "blog.db"
)

// runEmailCommand 分发 email 子命令。
// 参数缺失或子命令未知时返回用法说明，由 main 打到 stderr 并以非零状态退出。
func runEmailCommand(args []string) error {
	if len(args) == 0 {
		return errors.New(emailCommandUsage)
	}
	switch args[0] {
	case "test":
		return runEmailTest(args[1:])
	default:
		return fmt.Errorf("unknown email command %q\n\n%s", args[0], emailCommandUsage)
	}
}

// newMailerForCommand 是 runEmailTest 构造发信器的间接层。
//
// 线上默认走 newMailer；测试里替换成假实现，就能在不访问 Resend 的前提下
// 验证「配置检查 → 组装 EmailService → 发送 → 留痕」整条命令路径。
var newMailerForCommand = func(cfg *config.Config) (mail.Mailer, error) {
	return newMailer(cfg)
}

// 配置/数据库读取的间接层：这些步骤的失败分支都对应运维会遇到的真实故障，
// 但命令自己打开文件与连接，测试只能靠替换这一层来触发。
var (
	configureEmailLogging = logging.Configure
	loadEmailSettings     = config.Load
	saveEmailSettings     = func(cfg *config.Config, path string) error { return cfg.Save(path) }
	openEmailDatabase     = database.Open
	migrateEmailAdmin     = model.MigrateAdminCredentials
	emailAdminExists      = func(db *sql.DB) (bool, error) {
		return model.NewSQLiteAdminRepository(db).Exists(context.Background())
	}
)

// runEmailTest 解析 -target 并发送一封测试邮件。
//
// 发送前逐项确认 mail.from 与 mail.api_key 已配置，给出的错误比
// 「发信失败」具体得多——这两项恰恰是最常见的漏配点。
func runEmailTest(args []string) error {
	target, err := parseTarget(args)
	if err != nil {
		return err
	}

	cfg, db, err := loadInstalledConfig()
	if err != nil {
		return err
	}
	defer db.Close()

	if strings.TrimSpace(cfg.Mail.From) == "" {
		return errors.New("mail.from is not configured in settings.json")
	}
	// 密钥只认环境变量引用，不再从 settings.json 读字面量。
	if apiKey, _ := cfg.Mail.ResolveAPIKey(); apiKey == "" {
		return fmt.Errorf("no Resend API key available: set the %s environment variable "+
			"(or point mail.api_key at one with \"env:NAME\")", config.MailAPIKeyEnvVar)
	}

	mailer, err := newMailerForCommand(cfg)
	if err != nil {
		return err
	}

	emailService := newEmailService(cfg, db, mailer)

	subject := "Test email from Twilight Blog"
	body := "This is a test email sent by the Twilight Blog server through Resend.\n\nIf you received this message, email delivery is working."

	if err := emailService.Send(target, subject, body); err != nil {
		return fmt.Errorf("send test email: %w", err)
	}

	fmt.Fprintf(stdout(), "Test email sent to %s\n", target)
	return nil
}

// loadInstalledConfig 走与主进程相同的配置加载流程，但额外强制「必须已安装」：
// 未安装（还没有管理员）时发测试邮件没有意义。
// 它同样会回写 settings.json，保持与主进程一致的行为。
//
// **它会打开数据库并执行一次迁移**，然后返回那个连接（调用方负责关闭）。
// 这是这次重构带来的必要副作用：管理员凭据住在 users 表里，而「装没装过」
// 的权威答案也只能从那里读——settings.json 已经不再回答这个问题了。
// 顺带的好处是 `main.exe email ...` 也会把老的 settings.json 凭据搬进库，
// 与主进程行为一致。
func loadInstalledConfig() (*config.Config, *sql.DB, error) {
	if err := configureEmailLogging(""); err != nil {
		return nil, nil, fmt.Errorf("configure default logging: %w", err)
	}
	cfg, err := loadEmailSettings(settingsPath)
	if err != nil && !errors.Is(err, config.ErrNotInstalled) {
		return nil, nil, fmt.Errorf("load settings: %w", err)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	if err := configureEmailLogging(cfg.LogLevel); err != nil {
		return nil, nil, fmt.Errorf("configure log level: %w", err)
	}
	// Keep settings.json synchronized even when the email subcommand is used.
	// 与 main 一样，这一步**不能**清掉管理员凭据：下面的迁移还要读它们。
	if err := saveEmailSettings(cfg, settingsPath); err != nil {
		return nil, nil, fmt.Errorf("sync settings: %w", err)
	}

	db, err := openEmailDatabase(databasePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}

	// 与 main.go 相同的一条顺序约束：凭据必须在任何「已安装吗」的判断之前搬进库。
	// 这里的调用方只是发邮件，但如果跳过它，一个还没迁移过的老站点会被判成
	// 「未安装」而被拒绝——而那与真实状态不符。
	if err := migrateEmailAdmin(context.Background(), db, cfg); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("migrate administrator credentials: %w", err)
	}
	cfg.ClearLegacyCredentials()
	if err := saveEmailSettings(cfg, settingsPath); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("remove migrated credentials from settings: %w", err)
	}

	installed, err := emailAdminExists(db)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("check installation state: %w", err)
	}
	if !installed {
		db.Close()
		return nil, nil, errors.New("the blog is not installed; run /install first")
	}
	return cfg, db, nil
}

// newMailer 从配置构造 Resend 客户端；cfg.Mail 为 nil 时按空配置处理。
// 密钥同样只经 ResolveAPIKey 解析（环境变量优先），不从 settings.json 读字面量。
func newMailer(cfg *config.Config) (*mail.ResendMailer, error) {
	mailCfg := mail.Config{}
	if cfg.Mail != nil {
		apiKey, _ := cfg.Mail.ResolveAPIKey()
		mailCfg = mail.Config{
			From:   cfg.Mail.From,
			APIKey: apiKey,
		}
	}
	return mail.New(mailCfg)
}

// newEmailService 组装一个最小的 EmailService，只依赖邮件仓库与 mailer。
// 这样测试路径与线上路径共用同一套发信与留痕逻辑。
func newEmailService(cfg *config.Config, db *sql.DB, mailer mail.Mailer) *service.EmailService {
	emailRepo := model.NewSQLiteEmailRepository(db)
	from := ""
	if cfg.Mail != nil {
		from = cfg.Mail.From
	}
	return service.NewEmailService(mailer, emailRepo, from)
}

// parseTarget 解析 -target 参数，接受 -target X、--target X、-target=X、
// --target=X 四种写法。
//
// 遇到不认识的参数直接报错而不是忽略：静默忽略一个拼错的参数，
// 会让人误以为邮件已经发出去了。
// 最后用 net/mail 解析地址，既校验格式，也把 "名字 <地址>" 归一成纯地址。
func parseTarget(args []string) (string, error) {
	target := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var value string
		switch {
		case arg == "-target" || arg == "--target":
			if i+1 >= len(args) {
				return "", errors.New("missing value for -target")
			}
			i++
			value = args[i]
		case strings.HasPrefix(arg, "-target="):
			value = strings.TrimPrefix(arg, "-target=")
		case strings.HasPrefix(arg, "--target="):
			value = strings.TrimPrefix(arg, "--target=")
		default:
			return "", fmt.Errorf("unknown argument %q\n\n%s", arg, emailCommandUsage)
		}

		value = strings.TrimSpace(value)
		if value == "" {
			return "", errors.New("target email must not be empty")
		}
		if target != "" {
			return "", errors.New("target email specified more than once")
		}
		target = value
	}

	if target == "" {
		return "", errors.New("missing -target recipient\n\n" + emailCommandUsage)
	}

	parsed, err := netmail.ParseAddress(target)
	if err != nil {
		return "", fmt.Errorf("invalid target email %q: %w", target, err)
	}
	return parsed.Address, nil
}
