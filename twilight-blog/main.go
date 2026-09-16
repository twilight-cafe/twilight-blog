// Package main 是博客后端的进程入口。启动遵循固定顺序：日志 → 配置 →
// 数据库迁移 → 启动内嵌 Meilisearch → 注册路由 → 监听 HTTP。前几步任一
// 失败都会使进程以非零状态退出（logging.Fatalf），因为缺少其中任何一项
// 都无法安全对外服务；唯独“配置未安装”（settings.json 缺少 password_hash）
// 不算失败——进程照常启动，以便提供 /install 引导页让用户设置管理员密码。
package main

import (
	"context"
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
	"blog-engine/internal/middleware"
	"blog-engine/internal/model"
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
// 不可写、端口全被占）会在三次内全部失败，仍然以 Fatalf 收场，
// 不会把明确的错误拖成静默等待。
const (
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

// main 是进程入口：先处理 email / admin 子命令，否则走启动 HTTP 服务的主路径。
// 启动顺序与失败策略见包注释；任何一步失败都以非零状态退出。
func main() {
	if len(os.Args) > 1 && os.Args[1] == "email" {
		if err := runEmailCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	// admin 子命令自己决定退出码（2 = 用法或校验失败，1 = 运行失败），
	// 所以这里直接用它返回的值。
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		os.Exit(runAdminCommand(os.Args[2:]))
	}

	const settingsPath = "settings.json"
	const databasePath = "blog.db"

	// 在读取配置**之前**就打开默认日志文件，这样连最早期的配置错误
	// 也能落到 log.txt，而不是只在控制台上闪一下。
	if err := logging.Configure(""); err != nil {
		logging.Fatalf("configure default logging: %v", err)
	}
	logging.Debugf("[main] default log file opened")

	// 是否已安装取决于 settings.json 是否存在且含非空 password_hash：
	// 只有 settings.json 而没有密码哈希，只是一个默认模板，不算装好。
	// 注意数据库不在这里打开——由下面的 database.Open 负责建库与迁移。
	logging.Debugf("[main] loading settings from %s", settingsPath)
	cfg, err := config.Load(settingsPath)
	if err != nil && !errors.Is(err, config.ErrNotInstalled) {
		logging.Fatalf("load settings: %v", err)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	if err := logging.Configure(cfg.LogLevel); err != nil {
		logging.Fatalf("configure log level: %v", err)
	}
	logging.Infof("log level set to %s", logging.CurrentLevel())
	// 读完配置立刻回写一次，让 settings.json 始终包含完整的键：
	// 已有的值原样保留，缺的补默认值。这样用户打开文件就能看到全部可选项。
	//
	// 这一步**不能**顺手把管理员凭据从文件里抹掉：下面的 MigrateAdminCredentials
	// 正是从文件里把它们读出来的（见 config.LegacyAdminCredentials）。凭据的清除
	// 发生在迁移成功之后的第二次 Save。
	if err := cfg.Save(settingsPath); err != nil {
		logging.Fatalf("update settings: %v", err)
	}

	logging.Debugf("[main] opening database %s", databasePath)
	db, err := database.Open(databasePath)
	if err != nil {
		logging.Fatalf("open database: %v", err)
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
	// 因此这里是 main 里唯一正确的位置：database.Open 之后（表已建好）、
	// view.Register 之前（还没开始服务）。
	if err := model.MigrateAdminCredentials(context.Background(), db, cfg); err != nil {
		logging.Fatalf("migrate administrator credentials: %v", err)
	}

	// 凭据已经进库，现在把它们从配置与文件里抹掉。
	// 这一步之后 settings.json 不再含任何密码哈希，admin_username / admin_nickname
	// 也一并清空——管理员的登录名与展示名从此只从 users 表读。
	// 注意仍在 view.Register 之前：管理员的登录名不能在配置文件里"多活"一会儿。
	cfg.ClearLegacyCredentials()
	if err := cfg.Save(settingsPath); err != nil {
		logging.Fatalf("remove migrated administrator credentials from settings: %v", err)
	}

	// 凭据轮换必须让旧会话一并失效。
	//
	// 迁移前的管理员会话 user_id 为 NULL（那时管理员没有 users 行），而
	// RequireAdmin 只看 role、RevokeUserSessions 又按 user_id 删——两者都够不到
	// 这类行。结果是：凭据从 settings.json 搬进 users 表之后，旧 cookie 仍然是
	// 有效的管理员凭证，而且事后改密码也吊销不掉它。
	//
	// 位置与迁移同样严格：必须在开始对外服务之前。
	if revoked, err := session.NewStore(db).RevokeLegacySessions(context.Background()); err != nil {
		logging.Fatalf("revoke pre-migration sessions: %v", err)
	} else if revoked > 0 {
		logging.Infof("[main] revoked %d pre-migration session(s)", revoked)
	}

	// 「是否已安装」的权威答案在数据库里（users 表有没有 is_admin = 1 的行）。
	// 这里只是把状态写进日志，好让运维一眼看出该去 /install 还是该去登录页。
	installed, err := model.NewSQLiteAdminRepository(db).Exists(context.Background())
	if err != nil {
		logging.Fatalf("check installation state: %v", err)
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
		var err error
		meili, err = startWithRetries(searchStartAttempts, searchStartBackoff, func() (*search.Manager, error) {
			return search.Start(context.Background(), "")
		})
		if err != nil {
			logging.Fatalf("start meilisearch: %v", err)
		}
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
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		logging.Infof("received shutdown signal, stopping meilisearch")
		if meili != nil {
			meili.Stop()
		}
		_ = db.Close()
		os.Exit(0)
	}()

	e := echo.New()
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
	trustedProxies, err := middleware.ParseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		logging.Fatalf("configure trusted proxies: %v", err)
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
	if err := view.Register(e, cfg, db, settingsPath, searchClient); err != nil {
		if meili != nil {
			meili.Stop()
		}
		logging.Fatalf("register routes: %v", err)
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
	if err := e.Start(addr); err != nil {
		meili.Stop()
		logging.Fatalf("server stopped: %v", err)
	}
}
