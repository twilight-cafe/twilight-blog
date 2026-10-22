// Package main 是博客后端的进程入口。启动遵循固定顺序：日志 → 配置 →
// 数据库迁移 → 启动内嵌 Meilisearch → 注册路由 → 监听 HTTP。前几步任一
// 失败都会使进程以非零状态退出，因为缺少其中任何一项都无法安全对外服务；
// 唯独“配置未安装”（settings.json 缺少 password_hash）不算失败——进程照常
// 启动，以便提供 /install 引导页让用户设置管理员密码。
//
// # 为什么启动链是可注入的
//
// 这条链上每一步都可能失败，而每一步的失败都是**启动即退出**——也就是说，
// 它们在任何测试里都跑不到，除非它们能单独替换。于是 runServe 的每一步都
// 收在一个 boot 结构里（见 serve.go 的 bootHooks），默认值指向真实实现。
// 代价是多一层间接；回报是「漏装 cwebp」「数据库打不开」「代理配置写错」
// 这些故障各自有了用例，而不再只能靠「装好环境跑一次」来验证。
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/logging"
	"blog-engine/internal/mediaconv"
	"blog-engine/internal/middleware"
	"blog-engine/internal/model"
	"blog-engine/internal/pidfile"
	"blog-engine/internal/search"
	"blog-engine/internal/session"
	"blog-engine/internal/view"
)

// searchStartAttempts / searchStartBackoff 控制内嵌 Meilisearch 的启动重试。
//
// 为什么需要重试：端口是「先探测空闲、再交给子进程」的（见 search.freePort），
// 两步之间存在交接窗口——本机任意进程都可能在此期间抢走那个端口，表现是子进程
// 绑定失败后立刻退出、waitReady 报「exited before becoming ready」。
//
// 这是一个**可自愈的偶发失败**，而启动路径原本直接 Fatalf：一次几百微秒级的
// 竞态被放大成「整站起不来」，在 systemd 下还会变成崩溃循环（每次重启都重新
// 掷一次骰子，而重启间隔通常长到让日志里只剩「起不来」）。
//
// 只重试固定次数、只在启动阶段生效：真正的配置类故障（二进制缺失、数据目录
// 不可写、端口全被占）会在三次内全部失败，仍然以非零状态退出，
// 不会把明确的错误拖成静默等待。
// searchStartAttempts 是固定的尝试次数；searchStartBackoff 是 var 而非 const：
// 它是整套测试里唯一「本该很慢」的时间参数，测试把它降到毫秒级，
// 从而在毫秒内验证「试三次再失败」而不必真的睡一秒半。
var (
	searchStartAttempts = 3
	searchStartBackoff  = 500 * time.Millisecond
)

// startWithRetries 是重试逻辑本体。start 与 backoff 都作为参数传入，
// 是为了让守护测试能在毫秒级、不碰真实进程的前提下验证调用次数与错误传播——
// 直接测 main 里的那段代码需要真的拉不起 Meilisearch，没人会那么写。
func startWithRetries(attempts int, backoff time.Duration, start func() (*search.Manager, error)) (*search.Manager, error) {
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		manager, err := start()
		if err == nil {
			if attempt > 1 {
				logging.Infof("[main] embedded meilisearch started on attempt %d/%d", attempt, attempts)
			}
			return manager, nil
		}
		lastErr = err
		logging.Warnf("[main] starting the embedded meilisearch failed (attempt %d/%d): %v", attempt, attempts, err)
		if attempt < attempts {
			time.Sleep(backoff)
		}
	}
	return nil, lastErr
}

// run 是 main 的可测本体：派发子命令，否则启动 HTTP 服务。
//
// 返回值就是进程退出码。子命令自己决定（2 = 用法或校验失败，1 = 运行失败），
// 服务启动路径上任何一步失败都返回 1。
// commands 是各个子命令的实现。抽出这一层只有一个理由：run 里「email 子命令
// 成功」这条分支在真实世界里的含义是「真的发出去一封邮件」，没有测试会去
// 真的发一封。命令实现作为参数传入之后，这条分支就有了不依赖外网的用例。
type commands struct {
	email        func(args []string) error
	admin        func(args []string) int
	backup       func(args []string) int
	restore      func(args []string) int
	upgradeMedia func(args []string) int
}

func defaultCommands() commands {
	return commands{
		email:        runEmailCommand,
		admin:        runAdminCommand,
		backup:       runBackupCommand,
		restore:      runRestoreCommand,
		upgradeMedia: runUpgradeMediaCommand,
	}
}

func run(args []string, hooks bootHooks, cli commands) int {
	if len(args) > 0 {
		switch args[0] {
		case "email":
			if err := cli.email(args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return 0
		case "admin":
			return cli.admin(args[1:])
		case "backup":
			return cli.backup(args[1:])
		case "restore":
			return cli.restore(args[1:])
		case "upgrade-media":
			return cli.upgradeMedia(args[1:])
		}
	}
	if err := serve(hooks); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// exitFunc 让 main() 可以在测试里被执行而不结束测试进程：真机上它是 os.Exit。
var exitFunc = os.Exit

func main() {
	exitFunc(run(os.Args[1:], bootHooksProvider(), defaultCommands()))
}

// bootHooks 是启动链上每一步的实现。默认值指向真实实现，测试按需替换其中的
// 某几步来制造故障。
type bootHooks struct {
	configureLogging func(level string) error
	acquireLock      func() (func(), error)
	loadSettings     func(path string) (*config.Config, error)
	saveSettings     func(cfg *config.Config, path string) error
	detectMedia      func(webpTool, webmTool string) (*mediaconv.Converter, error)
	openDatabase     func(path string) (*sql.DB, error)
	migrateAdmin     func(ctx context.Context, db *sql.DB, cfg *config.Config) error
	revokeSessions   func(ctx context.Context, db *sql.DB) (int64, error)
	installed        func(ctx context.Context, db *sql.DB) (bool, error)
	startSearch      func(ctx context.Context, dataDir string) (*search.Manager, error)
	parseProxies     func(entries []string) (middleware.TrustedProxies, error)
	registerRoutes   func(*echo.Echo, *config.Config, *sql.DB, string, *search.Client, *mediaconv.Converter) error
	newEcho          func() *echo.Echo
	listen           func(e *echo.Echo, addr string) error
	sleep            func(time.Duration)
	exit             func(code int)
	nowSignal        func() <-chan os.Signal
}

// searchStarter 是 search.Start 的间接层：默认值是真实启动，而「Meilisearch
// 拉不起来」这条分支的测试不能真的去下载一个二进制。
var searchStarter = search.Start

// bootHooksProvider 是一个变量（而不是函数）：main() 要能在测试里被整体替换掉，
// 否则「main 执行了什么」就只能靠真的起一个服务来观察。
var bootHooksProvider = defaultBootHooks

func defaultBootHooks() bootHooks {
	return bootHooks{
		configureLogging: logging.Configure,
		acquireLock: func() (func(), error) {
			handle, err := pidfile.Acquire(pidfile.FileName)
			if err != nil {
				return nil, err
			}
			return func() { handle.Release() }, nil
		},
		loadSettings: config.Load,
		saveSettings: func(cfg *config.Config, path string) error { return cfg.Save(path) },
		detectMedia:  mediaconv.Detect,
		openDatabase: database.Open,
		migrateAdmin: model.MigrateAdminCredentials,
		revokeSessions: func(ctx context.Context, db *sql.DB) (int64, error) {
			return session.NewStore(db).RevokeLegacySessions(ctx)
		},
		installed: func(ctx context.Context, db *sql.DB) (bool, error) {
			return model.NewSQLiteAdminRepository(db).Exists(ctx)
		},
		startSearch: func(ctx context.Context, dataDir string) (*search.Manager, error) {
			return searchStarter(ctx, dataDir)
		},
		parseProxies:   middleware.ParseTrustedProxies,
		registerRoutes: view.Register,
		newEcho:        echo.New,
		listen:         func(e *echo.Echo, addr string) error { return e.Start(addr) },
		sleep:          time.Sleep,
		exit:           func(code int) { exitFunc(code) },
		nowSignal: func() <-chan os.Signal {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
			return sigCh
		},
	}
}

// serve 执行完整的启动链，任一步失败即返回错误（调用方据此以非零码退出）。
//
// 顺序是硬约束，见各步骤上方的注释；这里只把它们排成一条可测的链。
func serve(hooks bootHooks) error {
	ctx := context.Background()

	// 在读取配置**之前**就打开默认日志文件，这样连最早期的配置错误
	// 也能落到 log.txt，而不是只在控制台上闪一下。
	if err := hooks.configureLogging(""); err != nil {
		return fmt.Errorf("configure default logging: %w", err)
	}
	logging.Debugf("[main] default log file opened")

	// 记下「本站正在运行」，供命令行工具判断（见 internal/pidfile）。
	//
	// 备份并不依赖它——VACUUM INTO 拿的是一致快照，边服务边备份是安全的。但
	// **还原依赖它**：还原会替换 blog.db、删掉它的 -wal/-shm、把媒体目录整个
	// 换掉，而一个还在跑的进程会继续往旧的文件句柄里写。没有这个文件，还原
	// 就只能靠猜（比如「-wal 存在」，而它在崩溃后也会留下）。
	//
	// 写失败只告警、不中断启动：磁盘满或目录只读时站点仍然该能对外服务，
	// 代价仅仅是还原命令退回到「没有锁文件」那条路（它仍会告警）。
	release, lockErr := hooks.acquireLock()
	if lockErr != nil {
		logging.Warnf("[main] could not write %s: %v; "+
			"`restore` will not be able to tell that this instance is running", pidfile.FileName, lockErr)
	} else {
		defer release()
	}

	// 是否已安装取决于 settings.json 是否存在且含非空 password_hash：
	// 只有 settings.json 而没有密码哈希，只是一个默认模板，不算装好。
	// 注意数据库不在这里打开——由下面的 database.Open 负责建库与迁移。
	logging.Debugf("[main] loading settings from %s", settingsPath)
	cfg, err := hooks.loadSettings(settingsPath)
	if err != nil && !isNotInstalled(err) {
		return fmt.Errorf("load settings: %w", err)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	if err := hooks.configureLogging(cfg.LogLevel); err != nil {
		return fmt.Errorf("configure log level: %w", err)
	}
	logging.Infof("log level set to %s", logging.CurrentLevel())

	// 图片必须在落盘前转成 WebP，而这个承诺只能靠真正的编码器兑现——Go 标准库与
	// golang.org/x/image 都只有 WebP 解码器，没有编码器（见 internal/mediaconv）。
	// 所以**先探测再决定要不要继续启动**：没有编码器就直接退出，绝不带病运行。
	//
	// 位置是刻意的：排在打开数据库与启动 Meilisearch **之前**。这样一次「漏装 cwebp」
	// 只打印一条能照着做的错误就结束，而不是先把索引子进程拉起来、跑完迁移，最后才在
	// 第一次上传时以「图片被存成了 jpg」的形式暴露——那正是这条链路上曾经失效的样子
	// （settings.json 里钉着另一台机器的 webp_tool 绝对路径时就会这样）。
	//
	// 探测结果同时交给 view.Register，避免同一次判断在启动路径上出现两份。
	mediaConv, err := hooks.detectMedia(cfg.WebPTool, cfg.WebMTool)
	if err != nil {
		return fmt.Errorf("media conversion is required: %w", err)
	}
	logging.Infof("[media] WebP conversion enabled: using %s (%s)", mediaConv.WebPTool, mediaConv.WebPKind)

	// 读完配置立刻回写一次，让 settings.json 始终包含完整的键：
	// 已有的值原样保留，缺的补默认值。这样用户打开文件就能看到全部可选项。
	//
	// 这一步**不能**顺手把管理员凭据从文件里抹掉：下面的 MigrateAdminCredentials
	// 正是从文件里把它们读出来的（见 config.LegacyAdminCredentials）。凭据的清除
	// 发生在迁移成功之后的第二次 Save。
	if err := hooks.saveSettings(cfg, settingsPath); err != nil {
		return fmt.Errorf("update settings: %w", err)
	}

	logging.Debugf("[main] opening database %s", databasePath)
	db, err := hooks.openDatabase(databasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	logging.Debugf("[main] database opened: %s", databasePath)

	// ⚠️ 顺序约束（T4，改动前务必读完）：管理员凭据必须在**开始对外服务之前**
	// 从 settings.json 搬进 users 表。
	//
	// 原因：搬完之后 settings.json 就不再含密码哈希，而「是否已安装」从此由
	// 「users 表里有没有 is_admin = 1 的行」回答。如果这一步晚于 view.Register，
	// 第一个到达的请求会看到「未安装」，于是未鉴权的 /install 重新开放——
	// 任何能访问站点的人都能设一个新的管理员密码。那是一个真实的接管窗口。
	//
	// 因此这里是启动链里唯一正确的位置：database.Open 之后（表已建好）、
	// view.Register 之前（还没开始服务）。
	if err := hooks.migrateAdmin(ctx, db, cfg); err != nil {
		return fmt.Errorf("migrate administrator credentials: %w", err)
	}

	// 凭据已经进库，现在把它们从配置与文件里抹掉。
	// 这一步之后 settings.json 不再含任何密码哈希，admin_username / admin_nickname
	// 也一并清空——管理员的登录名与展示名从此只从 users 表读。
	// 注意仍在 view.Register 之前：管理员的登录名不能在配置文件里"多活"一会儿。
	cfg.ClearLegacyCredentials()
	if err := hooks.saveSettings(cfg, settingsPath); err != nil {
		return fmt.Errorf("remove migrated administrator credentials from settings: %w", err)
	}

	// 凭据轮换必须让旧会话一并失效。
	//
	// 迁移前的管理员会话 user_id 为 NULL（那时管理员没有 users 行），而
	// RequireAdmin 只看 role、RevokeUserSessions 又按 user_id 删——两者都够不到
	// 这类行。结果是：凭据从 settings.json 搬进 users 表之后，旧 cookie 仍然是
	// 有效的管理员凭证，而且事后改密码也吊销不掉它。
	//
	// 位置与迁移同样严格：必须在开始对外服务之前。
	revoked, err := hooks.revokeSessions(ctx, db)
	if err != nil {
		return fmt.Errorf("revoke pre-migration sessions: %w", err)
	}
	if revoked > 0 {
		logging.Infof("[main] revoked %d pre-migration session(s)", revoked)
	}

	// 「是否已安装」的权威答案在数据库里（users 表有没有 is_admin = 1 的行）。
	// 这里只是把状态写进日志，好让运维一眼看出该去 /install 还是该去登录页。
	installed, err := hooks.installed(ctx, db)
	if err != nil {
		return fmt.Errorf("check installation state: %w", err)
	}
	if !installed {
		logging.Infof("not installed yet; open /install to set the admin password")
	}

	// 内嵌 Meilisearch 只在开关打开时启动。关闭时不只是不启动子进程：
	// searchClient 保持 nil 会让 router 跳过 /api/search 的注册与索引同步，
	// 三处（子进程 / 路由 / 前端入口）一起消失才算「关闭搜索」。
	var meili *search.Manager
	if cfg.SearchEnabledValue() {
		logging.Debugf("[main] starting embedded meilisearch")
		manager, err := startWithRetries(searchStartAttempts, searchStartBackoff, func() (*search.Manager, error) {
			return hooks.startSearch(ctx, "")
		})
		if err != nil {
			return fmt.Errorf("start meilisearch: %w", err)
		}
		meili = manager
		defer meili.Stop()
		logging.Debugf("[main] embedded meilisearch started")
	} else {
		logging.Infof("[main] embedded meilisearch disabled by configuration (search_enabled=false)")
	}

	// 优雅退出：捕获 SIGINT/SIGTERM 后显式停掉 Meilisearch 子进程再退出，
	// 避免留下孤儿进程。
	//
	// 之所以要**显式**调用，是因为这里走的是 os.Exit，它不执行 defer，
	// 上面的 defer meili.Stop() / defer db.Close() 在这条路径上都不生效。
	// 代价是退出不等待在途请求完成，属于偏简单的关闭策略。
	go func() {
		<-hooks.nowSignal()
		logging.Infof("received shutdown signal, stopping meilisearch")
		if meili != nil {
			meili.Stop()
		}
		_ = db.Close()
		hooks.exit(0)
	}()

	e := hooks.newEcho()
	e.HideBanner = true
	logWriter := logging.Writer()
	e.Logger.SetOutput(logWriter)
	e.StdLogger.SetOutput(logWriter)

	// 必须在任何依赖客户端 IP 的东西之前定好它：限流与安全日志都看 RealIP()，
	// 而 Echo 的默认实现会直接采信可伪造的转发头。
	//
	// 解析一次、两处共用：IP 提取与 OriginCheck 都要回答「本次请求的对端是不是
	// 我们配置的代理」，各写一份判断迟早会漂移（改动前 OriginCheck 用的就是
	// 「配置里有没有代理」这个全局布尔量，与按请求判定的口径并不一致）。
	trustedProxies, err := hooks.parseProxies(cfg.TrustedProxies)
	if err != nil {
		return fmt.Errorf("configure trusted proxies: %w", err)
	}
	e.IPExtractor = middleware.BuildIPExtractor(trustedProxies)

	// 这几个超时限制了单条连接可以占住一个 goroutine 的时长。
	// ReadHeaderTimeout 是防 slowloris 的主力；读写超时给得宽松
	// （媒体上传下载可能很慢），但仍必须是有限值。
	e.Server.ReadHeaderTimeout = 10 * time.Second
	e.Server.ReadTimeout = 5 * time.Minute
	e.Server.WriteTimeout = 5 * time.Minute
	e.Server.IdleTimeout = 2 * time.Minute

	e.Use(echomw.Logger())
	e.Use(echomw.Recover())
	e.Use(middleware.SecureHeaders())
	e.Use(middleware.OriginCheck(cfg.AllowedOrigins, trustedProxies))
	if cors := middleware.CORS(cfg.AllowedOrigins); cors != nil {
		logging.Infof("[security] CORS enabled for %d configured origin(s)", len(cfg.AllowedOrigins))
		e.Use(cors)
	}

	// 上传体积上限的第一道闸。为 0（未配置）时兜底成默认值——
	// 配置校验已保证它不会是负数。媒体类型与内容的校验在 service 层。
	maxUploadSize := cfg.MaxUploadSize
	if maxUploadSize <= 0 {
		maxUploadSize = config.DefaultMaxUploadSize
	}
	e.Use(echomw.BodyLimit(fmt.Sprintf("%dM", maxUploadSize)))
	logging.Debugf("[main] upload body limit set to %dM", maxUploadSize)
	logging.Debugf("[main] echo server initialized")

	// 搜索关闭时 meili 为 nil：不能对 nil 解引用 Client()，只能传 nil client，
	// router 的既有分支（跳过同步 / 不注册路由）正是为它准备的。
	var searchClient *search.Client
	if meili != nil {
		searchClient = meili.Client()
	}
	if err := hooks.registerRoutes(e, cfg, db, settingsPath, searchClient, mediaConv); err != nil {
		if meili != nil {
			meili.Stop()
		}
		return fmt.Errorf("register routes: %w", err)
	}
	logging.Debugf("[main] HTTP routes registered")

	// 监听地址由 server_name + port 拼成。server_name 为空会监听所有网卡，
	// 这里默认打 Warning 而不是静默：在「本机开发」与「公网暴露」之间，
	// 这里只差一次误操作。
	port := cfg.Port
	if port == 0 {
		port = config.DefaultPort
	}
	serverName := cfg.ServerName
	if serverName == "" {
		logging.Warnf("[main] server_name is empty: binding to all interfaces. Set server_name (for example 127.0.0.1) in settings.json unless this host is meant to be public.")
	}
	addr := fmt.Sprintf("%s:%d", serverName, port)
	logging.Infof("blog engine listening on %s", addr)
	logging.Debugf("[main] starting HTTP server")
	if err := hooks.listen(e, addr); err != nil {
		if meili != nil {
			meili.Stop()
		}
		return fmt.Errorf("server stopped: %w", err)
	}
	return nil
}

// isNotInstalled 把「还没装过」这一种非致命错误单独认出来，其余一律致命。
func isNotInstalled(err error) bool {
	return err != nil && errors.Is(err, config.ErrNotInstalled)
}
