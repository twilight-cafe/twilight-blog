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
}

func NewAdminAuth(admins model.AdminRepository) *AdminAuth {
	return &AdminAuth{admins: admins}
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
// handler 只需要这四种能力，接口写窄一点让路由层的装配更明确，也让 handler 的单测
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
}

// 编译期断言：AdminAuth 必须始终满足窄接口，否则路由装配会在运行期才炸。
var _ AdminCredentialsStore = (*AdminAuth)(nil)
