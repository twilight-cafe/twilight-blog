package view

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/logging"
	"blog-engine/internal/mail"
	"blog-engine/internal/mediaconv"
	"blog-engine/internal/middleware"
	"blog-engine/internal/model"
	"blog-engine/internal/search"
	"blog-engine/internal/service"
	"blog-engine/internal/session"
	"blog-engine/internal/view/handler"
)

// Register 组装全部依赖并注册路由，是后端启动的最后一环。
//
// 顺序上有几处不能随意调换：
//   - 两个全局中间件（Authenticate → RequireCSRF）必须最先挂载，
//     否则后续 handler 拿不到会话上下文，写操作也失去了 CSRF 保护；
//   - 依赖的构造顺序是 repo → search 同步 → service → handler，
//     因为 service 要持有 repo、handler 要持有 service；
//   - registerWeb 放在最后注册：它注册的是 /* 通配路由，
//     即 SPA 回退，必须让所有 /api 路由先占据匹配优先级。
//
// 失败即返回 error 的有两处：mailer 初始化与前端资源注册——缺少这两样
// 服务无法正常工作，应当让进程启动失败而不是带病运行。
// 搜索索引同步失败则相反，只降级不阻断（见下方注释）。
func Register(e *echo.Echo, cfg *config.Config, db *sql.DB, settingsPath string, searchClient *search.Client) error {
	logging.Debugf("[router] registering HTTP routes")

	sessionStore := session.NewStore(db)
	trustForwarded := len(cfg.TrustedProxies) > 0

	// 会话解析必须排在任何依赖身份的中间件之前；CSRF 校验必须排在任何
	// 会改状态的 handler 之前。两者都在这里注册，因为它们需要 session store，
	// 而 store 是在本函数内构造的。
	e.Use(middleware.Authenticate(sessionStore))
	e.Use(middleware.RequireCSRF(sessionStore))

	articleRepo := model.NewSQLiteArticleRepository(db)
	tagRepo := model.NewSQLiteTagRepository(db)
	momentRepo := model.NewSQLiteMomentRepository(db)
	pageRepo := model.NewSQLitePageRepository(db)

	// 媒体服务必须排在文章服务之前构造：文章服务依赖它来校验头图、以及在永久
	// 删除文章时把专属头图一并删掉（见 service.ArticleMediaStore）。
	mediaConv := mediaconv.Detect(cfg.WebPTool, cfg.WebMTool)
	if mediaConv.WebPEnabled {
		logging.Infof("[media] WebP conversion enabled: using %s (%s)", mediaConv.WebPTool, mediaConv.WebPKind)
	} else {
		logging.Warnf("[media] WebP conversion disabled: no cwebp or ffmpeg found. Images will be stored as-is.")
	}
	if mediaConv.WebMEnabled {
		logging.Infof("[media] WebM conversion enabled: using %s", mediaConv.WebMTool)
	} else {
		logging.Warnf("[media] WebM conversion disabled: no ffmpeg found. Audio will be stored as-is.")
	}

	mediaRepo := model.NewSQLiteMediaRepository(db)
	mediaService := service.NewMediaService(mediaRepo, cfg.MediaDir, mediaConv)
	mediaDir := cfg.MediaDir
	if mediaDir == "" {
		mediaDir = "media"
	}
	// 0700 而不是 0755：媒体目录里会有尚未发布的素材，同机其他用户没有理由读它。
	// 与 settings.json（0600）、Meilisearch 数据目录（0700）保持同一口径。
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		logging.Warnf("[media] cannot create media directory %q: %v", mediaDir, err)
	}

	if searchClient != nil {
		logging.Debugf("[router] starting initial search index sync")
		if err := searchClient.Sync(context.Background(), articleRepo, pageRepo); err != nil {
			// 搜索是辅助能力，建索引失败不该阻断启动：文章与页面的写入会
			// 单独重索引对应文档，所以这里选择降级并交给后台重试。
			logging.Errorf("[router] initial search index sync failed; search may return incomplete results: %v", err)
			go retrySearchSync(searchClient, articleRepo, pageRepo)
		} else {
			logging.Debugf("[router] initial search index sync finished")
		}
	}

	// 过期会话与验证码没有别的清理途径：它们只在写入时被检查有效期，
	// 不会自动消失，所以需要一个定期清扫的任务。
	startPruneJanitor(db)

	// searchClient 是具体指针类型，直接传进 service 的接口参数会得到一个
	// 「非 nil 的接口包着 nil 指针」——service 里 `s.search == nil`（那条
	// 「本部署未启用搜索」的降级分支）就永远不会成立，nil 客户端会在
	// search.(*Client).do 读 c.baseURL 时 panic。auth_e2e_test.go 正是传 nil
	// 进来的，只是它没跑到写文章的路径才没炸。这里显式只在非 nil 时赋值，
	// 让接口的 nil 判断恢复本来的含义。
	var articleIndexer service.ArticleSearchIndexer
	var pageIndexer service.PageSearchIndexer
	if searchClient != nil {
		articleIndexer = searchClient
		pageIndexer = searchClient
	}

	articleService := service.NewArticleService(articleRepo, articleIndexer, mediaService)
	tagService := service.NewTagService(tagRepo)
	momentService := service.NewMomentService(momentRepo)
	pageService := service.NewPageService(pageRepo, pageIndexer)

	userRepo := model.NewSQLiteUserRepository(db)
	verificationRepo := model.NewSQLiteEmailVerificationRepository(db)
	commentRepo := model.NewSQLiteCommentRepository(db)

	mailCfg := mail.Config{}
	mailFrom := ""
	mailKeySource := "unset"
	if cfg.Mail != nil {
		// 密钥只从环境变量（或 env: 引用）解析，不从 settings.json 读字面量——
		// 理由见 config.MailConfig.ResolveAPIKey 的说明。
		apiKey, keySource := cfg.Mail.ResolveAPIKey()
		mailCfg = mail.Config{
			From:   cfg.Mail.From,
			APIKey: apiKey,
		}
		mailFrom = cfg.Mail.From
		mailKeySource = keySource
	}
	baseMailer, err := mail.New(mailCfg)
	if err != nil {
		return fmt.Errorf("initialize mailer: %w", err)
	}
	if mailCfg.APIKey == "" {
		logging.Warnf("[mail] Resend API key is not configured (checked %s); email sending is disabled", config.MailAPIKeyEnvVar)
	} else {
		logging.Infof("[mail] Resend mailer initialized for from=%q key_source=%s", mailCfg.From, mailKeySource)
	}
	emailRepo := model.NewSQLiteEmailRepository(db)
	emailService := service.NewEmailService(baseMailer, emailRepo, mailFrom)
	userService := service.NewUserService(userRepo, verificationRepo, emailService)

	// 管理员凭据的读写入口。它取代了以前散在 config 里的那一套
	// （IsInstalled / VerifyPassword / ...）：凭据现在住在 users 表里
	// is_admin = 1 那一行，config 包不该认识数据库。
	//
	// 注意它必须晚于 MigrateAdminCredentials —— 那个迁移由 main 完成，
	// 在 view.Register 之前（见 main.go 里的顺序注释）。
	//
	// 它还要一份媒体查询：站长头像（users.avatar_media_id）必须是图片，而
	// 「是不是图片」只有媒体服务答得出来。与文章头图共用同一条规则。
	adminRepo := model.NewSQLiteAdminRepository(db)
	adminAuth := service.NewAdminAuth(adminRepo, mediaService)

	// 评论目标必须真实存在（且未被软删），否则评论可以挂到任意 id 上——
	// 既能让内容下架后的评论继续被读出来，也能用不存在的 id 无限灌垃圾。
	// 三个仓储在这里收敛成一张 target_type → 查询函数 的表。
	commentTargets := service.NewCommentTargetLookups(articleRepo, momentRepo, pageRepo)
	commentService := service.NewCommentService(commentRepo, userRepo, commentTargets)

	authHandler := handler.NewAuthHandler(cfg, sessionStore, userService, adminAuth, trustForwarded)
	articleHandler := handler.NewArticleHandler(articleService)
	tagHandler := handler.NewTagHandler(tagService)
	momentHandler := handler.NewMomentHandler(momentService)
	pageHandler := handler.NewPageHandler(pageService)
	searchHandler := handler.NewSearchHandler(searchClient)
	userHandler := handler.NewUserHandler(userService)
	// 管理员发表的评论要标注为其展示名，因此把取名字的函数注入进去。
	// 与旧版不同，这个函数现在每次都会查一次库（展示名住在 users 表里）：
	// 刻意不做缓存——缓存会带来「改了昵称前台没变」这类 bug，而评论写入频率很低。
	commentHandler := handler.NewCommentHandler(commentService, authHandler.AdminName)

	mediaHandler := handler.NewMediaHandler(mediaService)
	emailHandler := handler.NewEmailHandler(emailService)
	installHandler := handler.NewInstallHandler(adminAuth, cfg)
	siteHandler := handler.NewSiteHandler(cfg, settingsPath, adminAuth)

	limits := cfg.EffectiveRateLimit()
	limiters := middleware.NewRateLimiters(middleware.RateLimitOptions{
		LoginPerMinute:   limits.LoginPerMinute,
		EmailPerMinute:   limits.EmailPerMinute,
		EmailPerHour:     limits.EmailPerHour,
		CommentPerMinute: limits.CommentPerMinute,
		VerifyPerMinute:  limits.VerifyPerMinute,
		InstallPerMinute: limits.InstallPerMinute,
		SearchPerMinute:  limits.SearchPerMinute,
	})
	logging.Infof("[security] rate limiting enabled=%t login=%d/min email=%d/min(+%d/h) comment=%d/min verify=%d/min search=%d/min",
		limits.IsEnabled(), limits.LoginPerMinute, limits.EmailPerMinute, limits.EmailPerHour, limits.CommentPerMinute, limits.VerifyPerMinute, limits.SearchPerMinute)

	// limiter 在限流被整体关闭时退化为空中间件，使下面的路由注册可以
	// 无条件写成 limiter(...)。限流开关来自配置，便于本地开发时关掉。
	limiter := func(mw echo.MiddlewareFunc) echo.MiddlewareFunc {
		if !limits.IsEnabled() {
			return func(next echo.HandlerFunc) echo.HandlerFunc { return next }
		}
		return mw
	}

	requireSession := middleware.RequireSession()

	// 安装与鉴权路由**不在**任何鉴权分组内——它们正是用来取得身份的入口，
	// 因此只能靠在各自 handler 内部识别状态（是否已安装）与限流来保护。
	// 注意 /install_status 连限流都没有：它只返回一个布尔，无副作用。
	// ---------------------------------------------------------------------
	// Installation
	// ---------------------------------------------------------------------
	e.GET("/install_status", installHandler.Status)
	e.POST("/install", installHandler.Install, limiter(limiters.Install))

	// ---------------------------------------------------------------------
	// Authentication
	// ---------------------------------------------------------------------
	// 登录只有一条路径，而且**没有任何前置分流**：一次带 COLLATE NOCASE 的用户名
	// 索引查找就回答了「这是谁」——站长也是 users 表里的一行（is_admin = 1）。
	// 从前的 isAdminLoginName（恒定时间字符串比较）与 loginAdmin 两支都删掉了：
	// 它们存在的唯一理由是把凭据留在 settings.json 里，而那个设计已经不存在。
	//
	// /api/auth/user/login 是**同义别名**，指向同一个 handler：老的客户端
	// （被缓存的前端产物、照着旧文档写的脚本）仍然能登录，但它不再享有另一套
	// 规则——两个入口曾经意味着一份要做两遍的安全推理，那正是漏洞的温床。
	e.POST("/api/auth/login", authHandler.Login, limiter(limiters.Login))
	e.POST("/api/auth/user/login", authHandler.Login, limiter(limiters.Login))
	e.POST("/api/auth/email-code", authHandler.RequestEmailCode, limiter(limiters.Email), limiter(limiters.EmailHour))
	e.POST("/api/auth/register", authHandler.Register, limiter(limiters.Verify))
	e.POST("/api/auth/reset-password", authHandler.ResetPassword, limiter(limiters.Verify))
	e.POST("/api/auth/logout", authHandler.Logout)

	// ---------------------------------------------------------------------
	// 自助资料：任何已登录身份（管理员或访客）都可访问，因此只要求有会话，
	// 不要求具体角色。具体是哪种身份由 handler 内部判断。
	// Self-service profile (any signed-in identity)
	// ---------------------------------------------------------------------
	e.GET("/api/me", authHandler.Me, requireSession)
	e.PATCH("/api/me", authHandler.UpdateOwnProfile, requireSession)
	e.PUT("/api/me/password", authHandler.ChangePassword, requireSession)

	// 公开内容：无需任何鉴权。这里**只允许 GET**——把写操作混进公开分组
	// 是这个项目最容易出的安全疏漏，新增路由时请归到 admin 组。
	//
	// 例外是评论：读取公开，写入（POST /api/comments、点赞）要求会话，
	// 并且额外挂了评论限流，因为它是唯一允许访客产生内容的入口。
	// ---------------------------------------------------------------------
	// Public content
	// ---------------------------------------------------------------------
	e.GET("/api/articles", articleHandler.List)
	e.GET("/api/articles/:id", articleHandler.Get)
	e.GET("/api/tags", tagHandler.List)
	e.GET("/api/tags/:id/articles", tagHandler.ListArticles)
	e.GET("/api/tags/:id/count", tagHandler.Count)
	e.GET("/api/moments", momentHandler.List)
	e.GET("/api/moments/:id", momentHandler.Get)
	e.GET("/api/pages", pageHandler.List)
	e.GET("/api/pages/:id", pageHandler.Get)
	// 搜索是公开只读的，但它每处理一个请求就要外呼一次 Meilisearch：
	// 不限量等于把后端处理能力按请求数免费送出去，所以它和评论一样挂限流器。
	// 搜索关闭（search_enabled=false）时路由本身不注册：「后端也不开放搜索 API」
	// 要求的是 404 而不是 503——关掉的端点应该让探测者以为它从来不存在，
	// 且 404 兜底还能统一回复本站不存在此资源，不必为它单独维护错误分支。
	if searchClient != nil {
		e.GET("/api/search", searchHandler.Search, limiter(limiters.Search))
	}
	e.GET("/api/media/:id", mediaHandler.Get) // 站点文案（网站描述）读取公开：它就是要展示给所有读者的那段话。
	// 写入在下面的 admin 分组里。
	e.GET("/api/site", siteHandler.Get)

	// Comments are readable by anyone; writing needs an account (or the admin).
	e.GET("/api/comments", commentHandler.List)
	e.POST("/api/comments", commentHandler.Create, requireSession, limiter(limiters.Comment))
	e.POST("/api/comments/:id/like", commentHandler.Like, requireSession, limiter(limiters.Comment))
	e.DELETE("/api/comments/:id/like", commentHandler.Unlike, requireSession, limiter(limiters.Comment))

	// ---------------------------------------------------------------------
	// Administrator
	// ---------------------------------------------------------------------
	// 管理员分组：RequireAdmin 在分组上挂一次，组内所有路由自动受保护。
	// 组内是唯一的写入口，涵盖新建/更新/软删/恢复/永久删除，以及媒体与邮件日志。
	//
	// 这些路由按资源分组排列，便于与 handler 文件对照；注意永久删除(/permanent)
	// 与批量接口都是独立路由，而不是给基础路由加参数——不可逆操作理应显式暴露。
	admin := e.Group("/api/admin", middleware.RequireAdmin())
	admin.PUT("/profile", authHandler.UpdateAdminProfile)
	// 站点设置：只允许管理员改，因为描述会出现在所有读者的首页与页脚上。
	admin.PUT("/site", siteHandler.Update)
	admin.GET("/users", userHandler.List)
	admin.PUT("/users/:id", userHandler.Update)

	admin.POST("/articles", articleHandler.Create)
	admin.PUT("/articles/:id", articleHandler.Update)
	admin.GET("/trash/articles", articleHandler.ListDeleted)
	admin.POST("/articles/:id/restore", articleHandler.Restore)
	admin.POST("/articles/batch-restore", articleHandler.BatchRestore)
	admin.DELETE("/articles/:id/permanent", articleHandler.PermanentDelete)
	admin.DELETE("/articles/batch-permanent", articleHandler.BatchPermanentDelete)
	admin.DELETE("/articles/:id", articleHandler.Delete)
	admin.DELETE("/articles", articleHandler.BatchDelete)
	admin.PUT("/tags/:id", tagHandler.Rename)
	admin.POST("/moments", momentHandler.Create)
	admin.PUT("/moments/:id", momentHandler.Update)
	admin.GET("/trash/moments", momentHandler.ListDeleted)
	admin.POST("/moments/:id/restore", momentHandler.Restore)
	admin.POST("/moments/batch-restore", momentHandler.BatchRestore)
	admin.DELETE("/moments/:id/permanent", momentHandler.PermanentDelete)
	admin.DELETE("/moments/batch-permanent", momentHandler.BatchPermanentDelete)
	admin.DELETE("/moments/:id", momentHandler.Delete)
	admin.DELETE("/moments", momentHandler.BatchDelete)
	admin.POST("/pages", pageHandler.Create)
	admin.PUT("/pages/:id", pageHandler.Update)
	admin.GET("/trash/pages", pageHandler.ListDeleted)
	admin.POST("/pages/:id/restore", pageHandler.Restore)
	admin.POST("/pages/batch-restore", pageHandler.BatchRestore)
	admin.DELETE("/pages/:id/permanent", pageHandler.PermanentDelete)
	admin.DELETE("/pages/batch-permanent", pageHandler.BatchPermanentDelete)
	admin.DELETE("/pages/:id", pageHandler.Delete)
	admin.DELETE("/pages", pageHandler.BatchDelete)
	admin.POST("/media", mediaHandler.Upload)
	admin.GET("/media", mediaHandler.List)
	admin.DELETE("/media/:id", mediaHandler.Delete)
	admin.GET("/emails", emailHandler.List)
	admin.GET("/emails/:id", emailHandler.Get)
	admin.GET("/comments", commentHandler.ListAll)
	admin.GET("/trash/comments", commentHandler.ListDeleted)
	admin.DELETE("/comments", commentHandler.BatchDelete)
	admin.POST("/comments/batch-restore", commentHandler.BatchRestore)
	admin.DELETE("/comments/batch-permanent", commentHandler.BatchPermanentDelete)
	admin.DELETE("/comments/:id", commentHandler.Delete)
	admin.POST("/comments/:id/restore", commentHandler.Restore)
	admin.DELETE("/comments/:id/permanent", commentHandler.PermanentDelete)

	if err := registerWeb(e, cfg); err != nil {
		return fmt.Errorf("register web routes: %w", err)
	}

	logging.Debugf("[router] HTTP routes registered")
	return nil
}

// retrySearchSync 用指数退避重试初始索引构建，使一次临时性的 Meilisearch
// 故障能在无人干预的情况下自愈。
//
// 共尝试 5 次（15s、30s、60s、120s、240s），全部失败后放弃并记 Error，
// 提示运维「保存一篇文章即可触发重新索引」——因为写入路径会单独重索引文档。
// 失败不会让进程退出，只是搜索结果可能不完整。
func retrySearchSync(client *search.Client, articleRepo model.ArticleRepository, pageRepo model.PageRepository) {
	const attempts = 5
	delay := 15 * time.Second

	for attempt := 1; attempt <= attempts; attempt++ {
		time.Sleep(delay)
		if err := client.Sync(context.Background(), articleRepo, pageRepo); err == nil {
			logging.Infof("[router] background search index sync succeeded on attempt %d", attempt)
			return
		} else {
			logging.Warnf("[router] background search index sync attempt %d/%d failed: %v", attempt, attempts, err)
		}
		delay *= 2
	}

	logging.Errorf("[router] abandoned background search index sync after %d attempts; use an article save to re-index", attempts)
}

// startPruneJanitor 启动定期清理：立即跑一次，之后每小时一次。
//
// 清理过期会话与验证码。在独立的 goroutine 中运行，全程不阻塞启动，
// 失败也只记 Warn（清理是尽力而为，下次 tick 会重试）。
func startPruneJanitor(db *sql.DB) {
	prune := func() {
		if _, err := database.PruneExpired(context.Background(), db); err != nil {
			logging.Warnf("[router] pruning expired rows failed: %v", err)
		}
	}

	go func() {
		prune()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			prune()
		}
	}()
}
