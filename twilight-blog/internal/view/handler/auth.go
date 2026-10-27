package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/logging"
	"blog-engine/internal/middleware"
	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/session"
	"blog-engine/internal/viewmodel"
)

// AuthHandler 负责登录、登出与自助账号相关的端点。
// 约定：所有方法都通过 cookie 维持会话，绝不在响应体里返回 token。
type AuthHandler struct {
	config         *config.Config
	sessions       *session.Store
	users          *service.UserService
	admins         service.AdminCredentialsStore
	trustForwarded bool
}

func NewAuthHandler(
	cfg *config.Config,
	sessions *session.Store,
	users *service.UserService,
	admins service.AdminCredentialsStore,
	trustForwarded bool,
) *AuthHandler {
	return &AuthHandler{
		config:         cfg,
		sessions:       sessions,
		users:          users,
		admins:         admins,
		trustForwarded: trustForwarded,
	}
}

// AdminName 返回当前管理员显示名，评论区用它来标注「管理员」身份。
//
// 与旧版不同，它现在要查一次库：展示名住在 users 表里那一行的 nickname 上。
// 刻意**不做缓存**——缓存会带来「改了昵称前台没变」这类 bug，而评论写入频率很低，
// 单连接池下多一次本地查询可以忽略。
func (h *AuthHandler) AdminName(ctx context.Context) string {
	username, nickname, err := h.admins.Identity(ctx)
	if err != nil {
		if !errors.Is(err, model.ErrUserNotFound) {
			logging.Warnf("[auth] reading the administrator display name failed: %v", err)
		}
		return ""
	}
	_ = username
	return nickname
}

// cookieSecure 决定会话 cookie 是否带 Secure 标记。
//
// 只有确认走 HTTPS 时才加：直连 TLS，或请求来自可信代理且带了
// X-Forwarded-Proto（trustForwarded 为真）。开发环境用 http 却没有代理时，
// 若硬加 Secure 会导致 cookie 完全无法保存、登录看起来「没反应」。
func (h *AuthHandler) cookieSecure(c echo.Context) bool {
	return middleware.CookieSecureFor(c, h.config.CookieSecure, h.trustForwarded)
}

// Login 是**唯一**的登录入口：以「用户名 + 密码」认证，身份由账号本身决定。
//
// 从前这里是两条分支：先把输入与 settings.json 里的站长登录名做一次恒定时间比较，
// 命中就走 loginAdmin（对内存里的哈希），否则走 loginUser（查 users 表）。
// 那套分流连同它的理由一起消失了——现在站长也是 users 表里的一行，一次索引查找
// 就能回答「这是谁」，`user.IsAdmin` 再决定签发哪种会话。
//
// **一处刻意的能力变化（必须写进变更说明）**：站长登录名那一次
// subtle.ConstantTimeCompare 没有了。理由是它本来防的东西已经不存在：
// 现在这条路径对所有用户完全一致（一次 `username = ? COLLATE NOCASE` 的索引查找
// 加一次 bcrypt），耗时取决于索引结构与 bcrypt 本身，而不是「调用方猜对了几个
// 字节」，所以不再构成逐字节预言机；而且再没有「管理员特殊」的分支可供区分。
//
// 保留下来、不要顺手删掉的两处：
//   - **失败一律返回同一句话**，绝不区分「用户名错」与「密码错」；
//   - equalizeLoginTiming（在 service 层）：它对付的是「账号不存在 / 账号无密码
//     vs 密码错」，与本函数的改动无关，依然必要。
//
// 大小写不敏感由 users.username 的 COLLATE NOCASE 提供，与访客口径完全一致。
func (h *AuthHandler) Login(c echo.Context) error {
	logging.Debugf("[handler:auth] Login called path=%s", c.Request().URL.Path)

	var req viewmodel.LoginRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()
	user, err := h.users.LoginWithUsername(ctx, req.Identifier(), req.Password)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) {
			logging.Warnf("[auth] login failed remote=%s", c.RealIP())
			// 文案必须与改动前逐字相同："invalid username or password"。
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid username or password")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to sign in")
	}

	if user.IsAdmin {
		if err := h.startAdminSession(c, user.ID); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, h.adminMe(user))
	}

	if err := h.startUserSession(c, user.ID); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, viewmodel.NewUserMeResponse(user))
}

// adminMe 渲染站长的会话信息。
//
// 同时给出登录名与展示名，前端才能把「登录用哪个」「前台显示什么」分开呈现——
// 只给一个名字的话，界面就不得不猜，而那正是这个项目之前把两者混为一谈的起点。
// 集中在这里构造，避免登录与 Me 两处响应漂移。
//
// **响应形状刻意不变**（不变量 I2）：仍然只有 role / name / username，
// 没有 user 对象。管理员现在确实有账号行了，但把它塞进 user 字段会改变既有
// 客户端看到的形状，而那属于范围外的行为变更。
func (h *AuthHandler) adminMe(admin *model.User) viewmodel.MeResponse {
	return viewmodel.NewAdminMeResponse(admin.Nickname, admin.Username)
}

// Logout 吊销当前会话并清除 cookie。
// 即使请求里没有 token 也会清除 cookie（防御已失效会话的残留），固定返回 204。
func (h *AuthHandler) Logout(c echo.Context) error {
	logging.Debugf("[handler:auth] Logout called path=%s", c.Request().URL.Path)

	token := session.TokenFromRequest(c)
	if token == "" {
		session.ClearCookies(c, h.cookieSecure(c))
		return c.NoContent(http.StatusNoContent)
	}

	if err := h.sessions.Revoke(c.Request().Context(), token); err != nil {
		logging.Errorf("[auth] revoke session failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to sign out")
	}

	session.ClearCookies(c, h.cookieSecure(c))
	logging.Infof("[auth] session revoked remote=%s", c.RealIP())
	return c.NoContent(http.StatusNoContent)
}

// Me 返回当前登录身份，供 SPA 决定展示「管理员后台」还是「访客控制台」。
// 无会话或会话对应的账户已被删除时返回 401 并清 cookie（账户删除但会话仍残留的情况）。
func (h *AuthHandler) Me(c echo.Context) error {
	sess, ok := session.From(c)
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}

	// 管理员的会话角色是 role=admin，且 user_id 现在**也是**真实的账号 id
	// （迁移之前它是 NULL）。所以这里必须先按角色分支再判 user_id：
	// 反过来写的话，管理员会话会掉进访客分支，去 users 表里按 id 查一个
	// 它本来就有、却会被当作访客渲染的行。
	if sess.IsAdmin() {
		admin, err := h.admins.Get(c.Request().Context())
		if err != nil {
			// 管理员行消失了（例如被手工删掉），会话已经没有意义。
			session.ClearCookies(c, h.cookieSecure(c))
			return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
		}
		return c.JSON(http.StatusOK, h.adminMe(admin))
	}
	if sess.UserID == nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}

	user, err := h.users.GetByID(c.Request().Context(), *sess.UserID)
	if err != nil {
		// The account was removed while the session lingered.
		session.ClearCookies(c, h.cookieSecure(c))
		return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}
	return c.JSON(http.StatusOK, viewmodel.NewUserMeResponse(user))
}

// RequestEmailCode 发送注册或找回密码用的验证码邮件。
// 限流由路由层 Install/Email 限流器承担；具体错误（已注册、频率限制、邮件未配置等）
// 通过 mapUserAuthError 映射为对应状态码。
func (h *AuthHandler) RequestEmailCode(c echo.Context) error {
	logging.Debugf("[handler:auth] RequestEmailCode called path=%s", c.Request().URL.Path)

	var req viewmodel.EmailCodeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.users.RequestEmailCode(c.Request().Context(), req.Email); err != nil {
		return mapUserAuthError(err)
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "verification code sent"})
}

// Register 校验验证码后创建访客账户并自动登录，返回 201。
// 注意：注册不要求先登录；验证码错误/过期/邮件已注册等由 service 判定并映射状态码。
func (h *AuthHandler) Register(c echo.Context) error {
	logging.Debugf("[handler:auth] Register called path=%s", c.Request().URL.Path)

	var req viewmodel.RegisterRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	user, err := h.users.Register(c.Request().Context(), req.Email, req.Code, req.Username, req.Nickname, req.Password)
	if err != nil {
		return mapUserAuthError(err)
	}

	if err := h.startUserSession(c, user.ID); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, viewmodel.NewUserMeResponse(user))
}

// ResetPassword 用验证码重置密码，并吊销该账户的所有现有会话（挤掉盗用者）。
// 成功后仅清除本机 cookie，不自动登录——用户需重新登录，避免「改密即免验证登录」的隐患。
func (h *AuthHandler) ResetPassword(c echo.Context) error {
	logging.Debugf("[handler:auth] ResetPassword called path=%s", c.Request().URL.Path)

	var req viewmodel.ResetPasswordRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	user, err := h.users.ResetPassword(c.Request().Context(), req.Email, req.Code, req.Password)
	if err != nil {
		return mapUserAuthError(err)
	}

	// Anyone holding a stolen session for this account must be evicted.
	if err := h.sessions.RevokeUserSessions(c.Request().Context(), user.ID); err != nil {
		logging.Warnf("[auth] revoke sessions after reset failed user_id=%d: %v", user.ID, err)
	}
	session.ClearCookies(c, h.cookieSecure(c))

	return c.JSON(http.StatusOK, map[string]string{"message": "password updated"})
}

// UpdateOwnProfile 对应 PATCH /api/me，修改当前访客的**展示名**与/或**登录名**。
//
// 两个字段都是可选的（用指针区分「没传」与「传了空串」），一次请求可以只改其中一个：
//   - 传 nickname 只影响前台展示，登录方式不变；
//   - 传 username 会改变登录标识，本人已持有的会话不受影响（会话记的是 user_id）。
//
// 邮箱刻意不在这里开放修改：它是已验证的联系方式，只有管理员可以改派。
//
// 管理员仍然被拒绝（403），这条**故意没有放开**：管理员的资料走
// PUT /api/admin/profile，前端也是这么分工的。D-1=b 只覆盖密码——
// 把这一条也统一是自然的后续，但要单独一个变更，因为它会牵动前端的 IdentityCard。
//
// 注意 sess.IsAdmin() 这个条件是这次重构**必须**加的：管理员会话的 UserID 以前恒为
// nil，所以 `sess.UserID == nil` 恰好把管理员挡在外面；现在管理员有真实账号行了，
// 只判 UserID 会让它悄悄通过，从而获得改自己用户名的能力。这类「语义翻转」不会
// 报错、不会有编译失败，只会静默改变授权结果。
func (h *AuthHandler) UpdateOwnProfile(c echo.Context) error {
	sess, ok := session.From(c)
	if !ok || sess.IsAdmin() || sess.UserID == nil {
		return echo.NewHTTPError(http.StatusForbidden, "only visitor accounts can change their profile")
	}

	var req viewmodel.UpdateProfileRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var user *model.User
	var err error
	if req.Username != nil {
		user, err = h.users.UpdateOwnUsername(c.Request().Context(), *sess.UserID, *req.Username)
		if err != nil {
			return mapUserAuthError(err)
		}
	}
	if req.Nickname != nil {
		user, err = h.users.UpdateOwnNickname(c.Request().Context(), *sess.UserID, *req.Nickname)
		if err != nil {
			return mapUserAuthError(err)
		}
	}
	// 走到这里至少有一个字段被处理过：viewmodel 的 Validate 会拒绝
	// 「两个字段都没传」的请求，所以不存在 user == nil 的中间态。
	return c.JSON(http.StatusOK, viewmodel.NewUserResponse(user))
}

// ChangePassword 对应 PUT /api/me/password，修改**当前账号**的密码并轮换会话：
// 先吊销该账号的全部会话（把其他设备挤下线），再给本机重新签发一个，
// 使本机不必重新登录。当前密码错误返回 401。
//
// D-1=b：管理员与访客走同一条路径。这顺带修掉一个线上 bug——前端「账号」页早就
// 渲染了改密表单（Profile.tsx 的 PasswordCard），而后端一直对管理员返回
// 403 "only visitor accounts have a password"，于是那个按钮永远显示「修改失败」。
// **前端工作量因此为零**：表单已经写好了，只是后端一直拒绝它。
//
// 必须配合同一个变更里的 reissueSession（T2）：管理员改完密码要按**它自己的角色**
// 重新签发会话。直接复用 startUserSession 会把管理员当场降级成访客——请求返回 204，
// 用户却发现自己被踢出了后台。
func (h *AuthHandler) ChangePassword(c echo.Context) error {
	sess, ok := session.From(c)
	if !ok {
		return echo.NewHTTPError(http.StatusForbidden, "only signed-in accounts have a password")
	}

	var req viewmodel.ChangePasswordRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()

	// 按身份分流到两条**只差存储位置**的改密路径，而不是按「有没有 user_id」——
	// 管理员现在也有 user_id 了。
	var account *model.User
	if sess.IsAdmin() {
		admin, err := h.admins.ChangePassword(ctx, req.CurrentPassword, req.NewPassword)
		if err != nil {
			if errors.Is(err, service.ErrInvalidCredentials) {
				return echo.NewHTTPError(http.StatusUnauthorized, "current password is incorrect")
			}
			return mapUserAuthError(err)
		}
		account = admin
	} else {
		if sess.UserID == nil {
			return echo.NewHTTPError(http.StatusForbidden, "only visitor accounts have a password")
		}
		user, err := h.users.ChangePassword(ctx, *sess.UserID, req.CurrentPassword, req.NewPassword)
		if err != nil {
			if errors.Is(err, service.ErrInvalidCredentials) {
				return echo.NewHTTPError(http.StatusUnauthorized, "current password is incorrect")
			}
			return mapUserAuthError(err)
		}
		account = user
	}

	// Rotate: drop every session for the account, then sign this device back in.
	// 管理员的会话行同样带真实 user_id，所以这一句对两种身份都真的生效
	// （见 startAdminSession 的说明）。
	if err := h.sessions.RevokeUserSessions(ctx, account.ID); err != nil {
		logging.Warnf("[auth] revoke sessions after password change failed user_id=%d: %v", account.ID, err)
	}
	if err := h.reissueSession(c, account); err != nil {
		return err
	}

	return c.NoContent(http.StatusNoContent)
}

// UpdateAdminProfile 对应 PUT /api/admin/profile，修改站长的展示名、登录名与头像。
//
// 三个字段分开处理：`name` 只改前台显示，`username` 改的是登录标识，
// `avatar_media_id` 换的是头像（三态，见 viewmodel.OptionalID：缺失=不动、
// null=摘除、数字=换成这张）。旧版把前两者合成一个 admin_name，于是「换个显示名」
// 会连带换掉登录名——本版拆开后，只改 `name` 绝不会影响下一次怎么登录。
//
// 与旧版最大的差别：写的不再是 settings.json，而是 users 表里那一行。
// 于是「登录名会不会撞上某个访客的用户名」不再需要预检——登录名与访客用户名共用
// 同一个部分唯一索引，数据库直接拦下，service 把它翻译成 ErrUsernameTaken。
// 旧版那次 UsernameTakenByAccount 预检连同它要防的竞态一起删掉了。
//
// 也没有「先改内存、落盘失败再回滚」这一套了：唯一的状态就是数据库里的那一行。
//
// ⚠️ **写入顺序是有意的，不要调换**：先用 ValidateAvatar 预检头像，再写身份，
// 最后写头像。原因是这个项目跑在单连接池上（SetMaxOpenConns(1)），跨仓储事务被
// 明确禁止，因此两步写入之间没有原子性可言。把最可能被拒的一步（头像：媒体可能
// 不存在、也可能不是图片）放在**任何写入之前**，就能保证一次 4xx 真的意味着
// 「什么都没改」——反过来先写身份的话，用户会拿到一个错误提示，名字却已经变了。
// 剩下的窗口只有「登录名恰好被并发占用」，那时代码返回 409 而头像已换，
// 属于已知的、极小的不一致（见 UpdateAvatar 的说明）。
func (h *AuthHandler) UpdateAdminProfile(c echo.Context) error {
	var req viewmodel.AdminProfileRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()

	// 登录名没传就沿用当前值：改登录名是一件会影响下次登录方式的事，
	// 不该由「碰巧没传这个键」来触发。
	current, err := h.admins.Get(ctx)
	if err != nil {
		logging.Errorf("[auth] load administrator failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save the new profile")
	}
	username := current.Username
	if req.SetUsername() {
		username = *req.Username
	}

	// 头像预检：只校验，不写。失败时还没动过任何一行。
	if req.SetAvatarMediaID() {
		if err := h.admins.ValidateAvatar(ctx, req.AvatarMediaID.Value); err != nil {
			return adminProfileError(err)
		}
	}

	admin, err := h.admins.UpdateIdentity(ctx, username, req.Name)
	if err != nil {
		return adminProfileError(err)
	}

	if req.SetAvatarMediaID() {
		admin, err = h.admins.UpdateAvatar(ctx, req.AvatarMediaID.Value)
		if err != nil {
			// 预检已经通过，这里只剩存储类错误（或极端并发下媒体被删）。
			// 仍然按校验错误映射：对调用方来说「这张图现在不能用了」是同一件事。
			return adminProfileError(err)
		}
	}

	logging.Infof("[auth] administrator profile updated username_changed=%t avatar_changed=%t",
		req.SetUsername(), req.SetAvatarMediaID())
	return c.JSON(http.StatusOK, h.adminMe(admin))
}

// adminProfileError 把 UpdateAdminProfile 里两步写入的错误映射成 HTTP 响应。
//
// 与 mapUserAuthError 分开，是因为这里只有三种结果：取值不合法（400）、
// 登录名被占（409）、以及其余一律 500。共用一张大表会让阅读者以为这条路径
// 也会返回 401 / 429。
func adminProfileError(err error) error {
	var validationErr *service.ValidationError
	switch {
	case errors.As(err, &validationErr):
		return echo.NewHTTPError(http.StatusBadRequest, validationErr.Message)
	case errors.Is(err, service.ErrUsernameTaken):
		logging.Warnf("[auth] refused to move the administrator login name onto another account")
		return echo.NewHTTPError(http.StatusConflict, "username is already taken")
	default:
		logging.Errorf("[auth] persist administrator profile failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save the new profile")
	}
}

// startUserSession issues a visitor session and writes the cookies.
// startUserSession 为指定访客签发会话，并把 token 与 CSRF 写入 cookie。
// 失败时返回 500（由调用方直接透传），因为此时账户已创建/已验密，
// 无法回滚到「什么都没发生」的状态。
func (h *AuthHandler) startUserSession(c echo.Context, userID int64) error {
	return h.issueSession(c, session.RoleUser, userID, session.UserTTL)
}

// startAdminSession 为管理员签发会话。
//
// 与旧版的关键差别：userID 从 nil 变成了**真实的账号 id**。管理员现在有 users 行了，
// 而会话行必须指向它——否则 RevokeUserSessions(adminID) 匹配不到任何行，
// 「改密后其他设备立刻下线」这条保证就断了（这一点在 T2 里被特别点出，
// 因为它是那种「看起来没问题、但安全属性已经消失」的改动）。
//
// 会话仍然是 role=admin + AdminTTL(24h)，与旧版完全一致（不变量 I3）。
func (h *AuthHandler) startAdminSession(c echo.Context, adminUserID int64) error {
	return h.issueSession(c, session.RoleAdmin, adminUserID, session.AdminTTL)
}

// issueSession 是两种身份共用的签发实现，只有角色与 TTL 不同。
func (h *AuthHandler) issueSession(c echo.Context, role session.Role, userID int64, ttl time.Duration) error {
	userIDCopy := userID
	token, csrf, expiresAt, err := h.sessions.Create(c.Request().Context(), role, &userIDCopy, ttl)
	if err != nil {
		logging.Errorf("[auth] create %s session failed: %v", role, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create session")
	}
	session.SetCookies(c, h.cookieSecure(c), token, csrf, expiresAt)
	return nil
}

// reissueSession 按账号的**真实角色**重新签发会话。
//
// 存在的唯一理由是一个静默失败：startUserSession 硬编码 session.RoleUser，
// 管理员改完自己的密码后会被当场降级成访客会话——请求返回 204，用户却发现自己
// 再也进不了后台，而且没有任何错误信息指向这里。它在 D-1=b（放开管理员自助改密）
// 之后才会发生，所以守卫测试必须断言「改密后的会话仍然是 role=admin」。
func (h *AuthHandler) reissueSession(c echo.Context, account *model.User) error {
	if account.IsAdmin {
		return h.startAdminSession(c, account.ID)
	}
	return h.startUserSession(c, account.ID)
}

// mapUserAuthError converts service errors into HTTP responses.
// mapUserAuthError 把 service 层的账号相关错误统一映射成 HTTP 响应。
//
// 这是 auth 域**唯一**的错误映射表：新增错误类型时只需改这一处；
// 漏掉的会落到 default 分支，被记成 Error 日志并返回 500。
// 匹配顺序有讲究——ValidationError 是结构体匹配（errors.As），
// 其余是哨兵错误比较（errors.Is），前者必须排在前面。
func mapUserAuthError(err error) error {
	if err == nil {
		return nil
	}

	var validationErr *service.ValidationError
	switch {
	case errors.As(err, &validationErr):
		return echo.NewHTTPError(http.StatusBadRequest, validationErr.Message)
	case errors.Is(err, service.ErrInvalidVerificationCode):
		return echo.NewHTTPError(http.StatusBadRequest, "invalid or expired verification code")
	case errors.Is(err, service.ErrEmailAlreadyRegistered):
		return echo.NewHTTPError(http.StatusConflict, "email is already registered")
	case errors.Is(err, service.ErrUsernameTaken):
		return echo.NewHTTPError(http.StatusConflict, "username is already taken")
	case errors.Is(err, service.ErrAccountNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "account not found")
	case errors.Is(err, service.ErrInvalidCredentials):
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	case errors.Is(err, service.ErrDailyEmailLimitReached):
		return echo.NewHTTPError(http.StatusTooManyRequests, strings.TrimSpace(err.Error()))
	case errors.Is(err, service.ErrEmailCodeAddressLimit):
		return echo.NewHTTPError(http.StatusTooManyRequests, strings.TrimSpace(err.Error()))
	// 与上面那条同样是 429，但语义不同：那条是「今天要得太多了」，这条是
	// 「离上一封不到 60 秒」。两者都是**限流**而不是**非法请求**，所以都不是 400
	// ——用 400 会让前端把它当成表单填写错误，弹出与输入框相关的提示。
	// 前端还会读这个 429 来决定是否启动倒计时。
	case errors.Is(err, service.ErrEmailCodeTooFrequent):
		return echo.NewHTTPError(http.StatusTooManyRequests, strings.TrimSpace(err.Error()))
	case errors.Is(err, service.ErrEmailSendingDisabled):
		return echo.NewHTTPError(http.StatusServiceUnavailable, "email sending is disabled")
	default:
		logging.Errorf("[auth] unhandled account error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "request failed")
	}
}
