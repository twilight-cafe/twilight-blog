package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/config"
	"blog-engine/internal/logging"
	"blog-engine/internal/model"
)

// AdminAvatarStore 是站长头像对媒体的**最小**依赖，实现是 *MediaService。
//
// 只需要「按 id 查一条媒体」：头像的校验规则与文章头图完全相同（存在，且
// media_type 是 image），不需要删除、不需要转码、不需要列表。接口留窄是有意的
// ——站长资料不该因为「媒体还能做别的」而多出依赖。
type AdminAvatarStore interface {
	Get(ctx context.Context, id int64) (*model.Media, error)
}

// AdminAuth 是管理员凭据的读写入口。
//
// 它取代了以前散在 config 包里的那一套（IsInstalled / VerifyPassword /
// SetPassword / PasswordHashValue）：凭据现在住在 users 表里 is_admin = 1 的
// 那一行，而 config 不该认识数据库。于是「读管理员那一行」「比对密码」
// 「改密码」这三件事集中到这里，handler 只依赖本类型。
//
// 密码强度校验复用 config.ValidatePasswordStrength：settings.json 不再是凭据的
// 权威存储，但**强度规则必须仍然只有一份**，否则 CLI、安装向导与自助改密会各自
// 演化出不同的下限。
type AdminAuth struct {
	admins model.AdminRepository
	// media 是头像校验用的媒体查询。**允许为 nil**：CLI 的 admin 子命令只做
	// 「建账号 / 改密码」，不涉及头像，让它去构造一个媒体目录没有意义。
	// 真的走到头像写入路径而 media 为 nil 时，usableAvatarID 会给出明确的
	// 校验错误，而不是空指针 panic。
	media AdminAvatarStore
}

func NewAdminAuth(admins model.AdminRepository, media AdminAvatarStore) *AdminAuth {
	return &AdminAuth{admins: admins, media: media}
}

// Exists 报告博客是否已经装好，即 users 表里有没有管理员行。
//
// 这是「已安装」的**唯一**权威答案（旧版看的是 settings.json 里有没有
// password_hash）。/install_status 与安装端点都问它。
func (a *AdminAuth) Exists(ctx context.Context) (bool, error) {
	return a.admins.Exists(ctx)
}

// Get 返回管理员行。未安装时返回 model.ErrUserNotFound。
func (a *AdminAuth) Get(ctx context.Context) (*model.User, error) {
	return a.admins.Get(ctx)
}

// Identity 返回管理员的登录名与展示名。
//
// 两者刻意分开返回：登录名参与认证，展示名只上屏。旧版把二者合成 admin_name，
// 于是「改个显示名」会连带改掉登录方式。
//
// 每次调用都查一次库，不做缓存。评论写入路径上的这一次本地查询在单连接池下
// 可以忽略，而缓存会带来「改了昵称前台没变」这类难以解释的 bug——那正是这段
// 代码要避免的东西。
func (a *AdminAuth) Identity(ctx context.Context) (username, nickname string, err error) {
	admin, err := a.admins.Get(ctx)
	if err != nil {
		return "", "", err
	}
	username = admin.Username
	nickname = admin.Nickname
	if strings.TrimSpace(nickname) == "" {
		// 昵称理论上非空（安装与迁移都会兜底），但历史数据可能例外。
		// 回退到登录名而不是一个写死的 "Admin"：至少显示的是本人认识的名字。
		nickname = username
	}
	return username, nickname, nil
}

// DisplayName 是 Identity 的便利包装，供评论署名这类只关心展示名的地方调用。
// 读不到管理员行时返回空串，由调用方决定回退文案。
func (a *AdminAuth) DisplayName(ctx context.Context) string {
	_, nickname, err := a.Identity(ctx)
	if err != nil {
		logging.Warnf("[admin-auth] reading the administrator display name failed: %v", err)
		return ""
	}
	return nickname
}

// AvatarMediaID 返回站长头像那张媒体的 id；没设头像（或读不到管理员行）时
// 第二个返回值为 false。
//
// 用 (int64, bool) 而不是返回 nil 指针：调用方（公开的 GET /api/site）只关心
// 「有没有」，而一个 nil 指针会诱使调用方去解引用它。
func (a *AdminAuth) AvatarMediaID(ctx context.Context) (int64, bool) {
	admin, err := a.admins.Get(ctx)
	if err != nil {
		// 未安装不算异常：那时的站点本来就没有管理员行、也就没有头像。
		if !errors.Is(err, model.ErrUserNotFound) {
			logging.Warnf("[admin-auth] reading the administrator avatar failed: %v", err)
		}
		return 0, false
	}
	if admin.AvatarMediaID == nil || *admin.AvatarMediaID <= 0 {
		return 0, false
	}
	return *admin.AvatarMediaID, true
}

// ValidateAvatar 校验「这张媒体能不能当站长的头像」，规则与文章头图完全一致
// （必须存在，且 media_type 是 image）。mediaID 为 nil 表示「摘除头像」，
// 永远合法。校验失败返回 *ValidationError，由 handler 映射成 400。
//
// 它被单独导出，是为了让 handler 能在**任何写入之前**先跑一遍：
// 见 handler.UpdateAdminProfile 里关于写入顺序的说明。
func (a *AdminAuth) ValidateAvatar(ctx context.Context, mediaID *int64) error {
	_, err := a.usableAvatarID(ctx, mediaID)
	return err
}

// usableAvatarID 把「三态头像字段」翻译成可以直接落库的值。
//
// 三态由调用方（viewmodel.OptionalID）解析，这里收到的指针已经只有两态：
// nil = 摘除，非 nil = 换成这张。因此「字段缺失」不会走到这里，
// 也就不可能被误当成「摘除」。
func (a *AdminAuth) usableAvatarID(ctx context.Context, mediaID *int64) (*int64, error) {
	if mediaID == nil {
		return nil, nil
	}
	if *mediaID <= 0 {
		return nil, invalidf("avatar_media_id must be a positive integer")
	}
	if a.media == nil {
		// 只有 CLI 那种不接媒体的组装才会走到这里。给校验错误而不是 panic：
		// 这是一个「该配置不支持这个功能」的结果，不是一个程序缺陷。
		return nil, invalidf("avatar media is not available in this configuration")
	}
	media, err := a.media.Get(ctx, *mediaID)
	if err != nil {
		if errors.Is(err, model.ErrMediaNotFound) {
			return nil, invalidf("avatar media %d does not exist", *mediaID)
		}
		return nil, err
	}
	if media.MediaType != model.MediaTypeImage {
		return nil, invalidf("avatar media %d is %s, but an avatar must be an image", *mediaID, media.MediaType)
	}
	return mediaID, nil
}

// UpdateAvatar 换掉（或摘除）站长的头像，返回更新后的管理员行。
//
// 与 articles.cover_media_id 一样，**引用不是所有权**：换图/摘除都只改这一列，
// 绝不删媒体文件。媒体是硬删、无回收站，唯一会删文件的地方是媒体库里的删除操作。
//
// 它内部会再跑一遍 usableAvatarID：handler 为了写入顺序已经先校验过一次，
// 但把校验留在这里，任何新调用点都不可能写进一个不存在或非图片的 id。
// 两次校验共用同一个函数，因此不存在「两套规则各自演化」的问题。
func (a *AdminAuth) UpdateAvatar(ctx context.Context, mediaID *int64) (*model.User, error) {
	usable, err := a.usableAvatarID(ctx, mediaID)
	if err != nil {
		return nil, err
	}

	admin, err := a.admins.Get(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.admins.UpdateAvatar(ctx, admin.ID, usable, time.Now().Unix()); err != nil {
		return nil, err
	}

	admin.AvatarMediaID = usable
	logging.Infof("[admin-auth] administrator avatar updated user_id=%d cleared=%t", admin.ID, usable == nil)
	return admin, nil
}

// Verify 用 bcrypt 比对管理员密码。
//
// 无需额外包一层恒定时间比较：bcrypt.CompareHashAndPassword 内部就是用
// subtle.ConstantTimeCompare 比对派生密钥的。
//
// 未安装（没有管理员行）时返回 false 而不是错误：登录路径只关心「过/不过」，
// 把它变成 500 只会让攻击者更容易分辨服务状态。
func (a *AdminAuth) Verify(ctx context.Context, password string) bool {
	admin, err := a.admins.Get(ctx)
	if err != nil {
		if !errors.Is(err, model.ErrUserNotFound) {
			logging.Errorf("[admin-auth] loading the administrator for sign-in failed: %v", err)
		}
		return false
	}
	if !admin.HasPassword() {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(strings.TrimSpace(password))) == nil
}

// CreateAdmin 是首次安装的写入口：校验密码强度、生成 bcrypt 哈希、插入
// is_admin = 1 的那一行。
//
// 它把「强度校验 → 哈希 → 插入」收在一处，安装向导与 CLI 因此共用同一条路径：
// 两条各写一份的话，密码策略迟早会在其中一条上漂移。
//
// 已存在管理员时返回 model.ErrAdminAlreadyExists（由唯一索引保证，不是预检的结果）。
func (a *AdminAuth) CreateAdmin(ctx context.Context, username, nickname, password string) (*model.User, error) {
	if err := config.ValidatePasswordStrength(password); err != nil {
		return nil, invalidf("%s", err.Error())
	}
	username = strings.TrimSpace(username)
	if username == "" {
		username = config.DefaultAdminName
	}
	if _, err := model.NormalizeAndValidateUsername(username); err != nil {
		return nil, invalidf("%s", err.Error())
	}
	nickname = strings.TrimSpace(nickname)
	if nickname == "" {
		nickname = username
	}

	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	admin := &model.User{
		// 空串哨兵：什么注册路径都产生不了这个地址（见 model 包与 schema 的说明）。
		Email:        "",
		Username:     username,
		Nickname:     nickname,
		PasswordHash: hash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := a.admins.Create(ctx, admin); err != nil {
		return nil, err
	}

	logging.Infof("[admin-auth] administrator account created user_id=%d username=%q", admin.ID, username)
	return admin, nil
}

// SetPassword 校验强度后替换管理员密码哈希。
// 管理员行不存在时返回 model.ErrUserNotFound（调用方应据此报「未安装」）。
func (a *AdminAuth) SetPassword(ctx context.Context, password string) error {
	if err := config.ValidatePasswordStrength(password); err != nil {
		return err
	}

	hash, err := hashPassword(password)
	if err != nil {
		return err
	}

	admin, err := a.admins.Get(ctx)
	if err != nil {
		return err
	}
	if err := a.admins.SetPassword(ctx, admin.ID, hash, time.Now().Unix()); err != nil {
		return err
	}
	logging.Infof("[admin-auth] administrator password updated user_id=%d", admin.ID)
	return nil
}

// UpdateIdentity 改管理员的登录名与展示名。
//
// 登录名与访客用户名共用 users 表上的同一个唯一索引，所以「撞名」由数据库拦下，
// 返回 model.ErrUsernameTaken——不需要任何预检，也不需要一份「哪些名字被预留了」
// 的清单。改名**不会**影响既有会话：会话行存的是 user_id。
func (a *AdminAuth) UpdateIdentity(ctx context.Context, username, nickname string) (*model.User, error) {
	username = strings.TrimSpace(username)
	nickname = strings.TrimSpace(nickname)
	if username == "" {
		return nil, invalidf("login name is required")
	}
	if nickname == "" {
		return nil, invalidf("display name is required")
	}
	if _, err := model.NormalizeAndValidateUsername(username); err != nil {
		return nil, invalidf("%s", err.Error())
	}

	admin, err := a.admins.Get(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.admins.UpdateIdentity(ctx, admin.ID, username, nickname, time.Now().Unix()); err != nil {
		if errors.Is(err, model.ErrUsernameTaken) {
			return nil, ErrUsernameTaken
		}
		return nil, err
	}

	admin.Username = username
	admin.Nickname = nickname
	logging.Infof("[admin-auth] administrator identity updated user_id=%d", admin.ID)
	return admin, nil
}

// ChangePassword 为已登录的**管理员**改密码，必须先验证当前密码。
//
// 这是 D-1=b 的落地：管理员与访客走同一条自助改密路径。它顺带修掉一个线上 bug——
// 前端「账号」页早就渲染了改密表单，而后端一直对管理员返回 403，于是那个按钮
// 永远显示「修改失败」。
func (a *AdminAuth) ChangePassword(ctx context.Context, currentPassword, newPassword string) (*model.User, error) {
	admin, err := a.admins.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !admin.HasPassword() {
		// 不应发生：迁移只在哈希非空时建行，安装也一定会写哈希。
		// 真出现的话说明有人手工插入了一个没有密码的管理员行。
		return nil, invalidf("this account has no password yet")
	}
	if bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(strings.TrimSpace(currentPassword))) != nil {
		return nil, ErrInvalidCredentials
	}
	if err := config.ValidatePasswordStrength(newPassword); err != nil {
		return nil, invalidf("%s", err.Error())
	}

	hash, err := hashPassword(newPassword)
	if err != nil {
		return nil, err
	}
	if err := a.admins.SetPassword(ctx, admin.ID, hash, time.Now().Unix()); err != nil {
		return nil, err
	}

	admin.PasswordHash = hash
	logging.Infof("[admin-auth] administrator changed their own password user_id=%d", admin.ID)
	return admin, nil
}

// AdminCredentialsStore 是 AdminAuth 面向 handler 的窄接口。
//
// handler 只需要这几种能力，接口写窄一点让路由层的装配更明确，也让 handler 的单测
// 不必构造一整个 service。AdminAuth 直接满足它。
type AdminCredentialsStore interface {
	Exists(ctx context.Context) (bool, error)
	Get(ctx context.Context) (*model.User, error)
	Identity(ctx context.Context) (username, nickname string, err error)
	Verify(ctx context.Context, password string) bool
	CreateAdmin(ctx context.Context, username, nickname, password string) (*model.User, error)
	SetPassword(ctx context.Context, password string) error
	UpdateIdentity(ctx context.Context, username, nickname string) (*model.User, error)
	ChangePassword(ctx context.Context, currentPassword, newPassword string) (*model.User, error)
	// 头像三件套：读（公开的站点信息用）、校验（写入前预检）、写。
	AvatarMediaID(ctx context.Context) (int64, bool)
	ValidateAvatar(ctx context.Context, mediaID *int64) error
	UpdateAvatar(ctx context.Context, mediaID *int64) (*model.User, error)
}

// 编译期断言：AdminAuth 必须始终满足窄接口，否则路由装配会在运行期才炸。
var _ AdminCredentialsStore = (*AdminAuth)(nil)
