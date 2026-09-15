package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/logging"
	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/session"
)

// 本文件实现 `<executable> admin set-password` 子命令。
//
// 它是「管理员忘记密码」的**标准恢复手段**。这一点是设计的直接后果：管理员行的
// email 是空串哨兵（一个任何注册路径都产生不了的地址），所以管理员**不能**走
// 「忘记密码」自助重置——那条路要求邮箱存在且能收信。密码哈希又只存在于 blog.db
// 里，编辑 settings.json 已经完全无效。于是恢复动作只能是本地跑这条命令。
//
// 安全上刻意做的三件事：
//   - 密码只从 **stdin** 读，绝不从 argv 读：argv 会进 shell history 与进程列表
//     （同一台机器上的任何用户 nproc/ps 就能看到）；
//   - 不提供「打印密码哈希」这类子命令：那会把凭据写进终端、日志与 scrollback；
//   - 改完调用 session.RevokeAll —— 密码变了，所有既有会话必须失效，
//     否则一个被盗的 token 会活得比密码还久。
const adminCommandUsage = `Usage:
  <executable> admin set-password [--username <name>] [--nickname <name>]

Reads the new password from stdin twice (it is never taken from the command line,
where it would end up in your shell history and in the process list). Use this when
the administrator password has been lost: it is the only recovery path, because the
administrator account has no reachable e-mail address.

Commands:
  set-password   Set (or create) the administrator password.
`

// 退出码。与 usage 里承诺的一致，脚本可以据此区分「你输错了」与「环境坏了」。
const (
	exitUsageOrValidation = 2
	exitFailure           = 1
)

// 三个流做成**变量**而不是直接调 os.Stdin/os.Stderr，是为了让测试能替换它们。
// 尤其是密码输入：测试必须能喂进一个假的 stdin，否则这条命令的分支（两次不一致、
// 空密码、强度不足）就完全测不到，而它们恰恰是最容易写错的部分。
//
// 与 email_command.go 的 parseTarget 不同，那里是纯函数、无需注入。
var (
	stdin  = func() io.Reader { return os.Stdin }
	stdout = func() io.Writer { return os.Stdout }
	stderr = func() io.Writer { return os.Stderr }
)

// runAdminCommand 分发 admin 子命令，返回进程退出码。
//
// 与 runEmailCommand 的差别是它**返回退出码**而不是 error：这条命令需要区分
// 「校验失败（2）」与「数据库失败（1）」，而 error 里没有这个信息。
func runAdminCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr(), adminCommandUsage)
		return exitUsageOrValidation
	}
	switch args[0] {
	case "set-password":
		return runAdminSetPassword(args[1:])
	default:
		fmt.Fprintf(stderr(), "unknown admin command %q\n\n%s", args[0], adminCommandUsage)
		return exitUsageOrValidation
	}
}

// runAdminSetPassword 读取新密码、校验强度、写入管理员行，然后吊销全部会话。
func runAdminSetPassword(args []string) int {
	username, nickname, err := parseAdminIdentityArgs(args)
	if err != nil {
		fmt.Fprintln(stderr(), err)
		return exitUsageOrValidation
	}

	if err := logging.Configure(""); err != nil {
		fmt.Fprintf(stderr(), "configure logging: %v\n", err)
		return exitFailure
	}

	// 密码先读先校验，再碰数据库：输错两次不该产生任何副作用。
	password, err := readPasswordTwice(stdin(), stderr())
	if err != nil {
		fmt.Fprintln(stderr(), err)
		return exitUsageOrValidation
	}
	if err := config.ValidatePasswordStrength(password); err != nil {
		// 复用与服务端**同一份**强度规则。CLI 自己写一套的话，两条路径迟早漂移，
		// 而症状是「这个密码能登录但改不动」。
		fmt.Fprintf(stderr(), "weak password: %v\n", err)
		return exitUsageOrValidation
	}

	cfg, err := config.Load(settingsPath)
	if err != nil && !errors.Is(err, config.ErrNotInstalled) {
		fmt.Fprintf(stderr(), "load settings: %v\n", err)
		return exitFailure
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	db, err := database.Open(databasePath)
	if err != nil {
		fmt.Fprintf(stderr(), "open database: %v\n", err)
		return exitFailure
	}
	defer db.Close()

	// 顺带完成一次性迁移，与主进程和 email 子命令保持一致。跳过它会让一个
	// 尚未迁移过的老站点在这里被判成「没有管理员」，进而把新密码写到一个
	// 与 /install 将会创建的那一行**不同**的账号上。
	if err := model.MigrateAdminCredentials(context.Background(), db, cfg); err != nil {
		fmt.Fprintf(stderr(), "migrate administrator credentials: %v\n", err)
		return exitFailure
	}
	// 迁移成功之后必须把遗留凭据从 settings.json 里抹掉，与主进程
	// （main.go 的 ClearLegacyCredentials + Save）保持一致。
	//
	// 漏掉这一步的后果很隐蔽：库里已经是新密码，文件里却还留着**旧密码的
	// bcrypt 哈希**，直到下一次有人启动主程序或跑 email 子命令才被清掉。
	// 那段时间里「凭据只住在数据库里」这条不变量是假的，而它正是
	// blog.db 被当作唯一必须备份的文件的前提。
	cfg.ClearLegacyCredentials()
	if err := cfg.Save(settingsPath); err != nil {
		fmt.Fprintf(stderr(), "remove migrated credentials from settings: %v\n", err)
		return exitFailure
	}

	// 第二个参数是头像校验用的媒体查询，这里传 nil：admin 子命令只做
	// 「建账号 / 改密码」，不碰头像，去为它构造一个媒体目录没有意义
	// （真的走到头像路径时会得到一条明确的校验错误，而不是 panic）。
	auth := service.NewAdminAuth(model.NewSQLiteAdminRepository(db), nil)
	ctx := context.Background()

	exists, err := auth.Exists(ctx)
	if err != nil {
		fmt.Fprintf(stderr(), "check the administrator account: %v\n", err)
		return exitFailure
	}

	if !exists {
		// 还没有管理员：创建一行。这样一台全新的（或从未装好的）站点也能在
		// 命令行上完成安装，而不必先把 /install 暴露到公网上。
		if _, err := auth.CreateAdmin(ctx, username, nickname, password); err != nil {
			fmt.Fprintf(stderr(), "create the administrator account: %v\n", err)
			return exitFailure
		}
		fmt.Fprintf(stdout(), "Administrator account created with login name %q.\n", username)
	} else {
		if err := auth.SetPassword(ctx, password); err != nil {
			fmt.Fprintf(stderr(), "set the administrator password: %v\n", err)
			return exitFailure
		}
		fmt.Fprintln(stdout(), "Administrator password updated.")
	}

	// 密码变了，所有既有会话都必须失效。**包括管理员自己的**——这正是 RevokeAll
	// 而不是 RevokeUserSessions 的理由：后者只按 user_id 删，虽然现在管理员也有
	// user_id 了，但这条命令的语义是「凭据已轮换，所有人重新登录」，用 RevokeAll
	// 表达得更准确，也不会在未来某个角色变更时悄悄漏掉一类会话。
	if err := session.NewStore(db).RevokeAll(ctx); err != nil {
		fmt.Fprintf(stderr(), "revoke existing sessions: %v\n", err)
		return exitFailure
	}
	fmt.Fprintln(stdout(), "All existing sessions have been revoked; every device must sign in again.")
	return 0
}

// parseAdminIdentityArgs 解析 --username / --nickname，四种写法与 email 子命令一致。
//
// 两者都可省略：已存在管理员时它们会被**忽略**（set-password 不改身份，
// 改身份请走控制台的 PUT /api/admin/profile）；不存在时它们决定新行的身份。
func parseAdminIdentityArgs(args []string) (username, nickname string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var target *string
		var value string

		switch {
		case arg == "-username" || arg == "--username":
			if i+1 >= len(args) {
				return "", "", errors.New("missing value for -username")
			}
			i++
			value = args[i]
			target = &username
		case strings.HasPrefix(arg, "-username="):
			value = strings.TrimPrefix(arg, "-username=")
			target = &username
		case strings.HasPrefix(arg, "--username="):
			value = strings.TrimPrefix(arg, "--username=")
			target = &username
		case arg == "-nickname" || arg == "--nickname":
			if i+1 >= len(args) {
				return "", "", errors.New("missing value for -nickname")
			}
			i++
			value = args[i]
			target = &nickname
		case strings.HasPrefix(arg, "-nickname="):
			value = strings.TrimPrefix(arg, "-nickname=")
			target = &nickname
		case strings.HasPrefix(arg, "--nickname="):
			value = strings.TrimPrefix(arg, "--nickname=")
			target = &nickname
		default:
			return "", "", fmt.Errorf("unknown argument %q\n\n%s", arg, adminCommandUsage)
		}

		value = strings.TrimSpace(value)
		if value == "" {
			return "", "", errors.New("username and nickname must not be empty")
		}
		if *target != "" {
			return "", "", fmt.Errorf("%s specified more than once", strings.TrimLeft(arg, "-"))
		}
		*target = value
	}

	if username == "" {
		username = config.DefaultAdminName
	}
	if nickname == "" {
		nickname = username
	}
	// 用户名要过与访客同一套形状规则，否则这个命令能造出一个连登录框都过不了的
	// 登录名（那会把人锁在门外，而且只能再跑一次这条命令来救）。
	if _, err := model.NormalizeAndValidateUsername(username); err != nil {
		return "", "", fmt.Errorf("invalid username: %w", err)
	}
	return username, nickname, nil
}

// readPasswordTwice 从 in 读两次密码并要求一致。
//
// 读的是**行**而不是「无回显的终端输入」：Go 标准库没有跨平台的无回显读取，
// 为它引入一个依赖（或平台相关的 syscall）不值得。代价是密码会显示在终端上，
// 所以这里明确打印一行提示，让操作者知道该清屏、以及别在共享终端上跑。
func readPasswordTwice(in io.Reader, prompt io.Writer) (string, error) {
	reader := bufio.NewReader(in)

	fmt.Fprintln(prompt, "Reading the new password from stdin (it will be echoed; clear your terminal afterwards).")

	first, err := readPasswordLine(reader, prompt, "New password: ")
	if err != nil {
		return "", err
	}
	if first == "" {
		return "", errors.New("password cannot be empty")
	}

	second, err := readPasswordLine(reader, prompt, "Repeat new password: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("the two passwords do not match")
	}
	return first, nil
}

// readPasswordLine 读一行并去掉行尾的 CR/LF。
// CR 必须去掉：Windows 的终端回显与管道都会带上它，留着会让「两次输入一致」
// 这句判断在肉眼看上去完全相同的两个密码上失败。
func readPasswordLine(reader *bufio.Reader, prompt io.Writer, label string) (string, error) {
	fmt.Fprint(prompt, label)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" && errors.Is(err, io.EOF) {
		return "", errors.New("no password on stdin")
	}
	return line, nil
}
