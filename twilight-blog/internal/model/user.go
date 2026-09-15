package model

import "errors"

var (
	ErrUserNotFound = errors.New("user not found")
	// ErrEmailTaken 表示该邮箱已注册到另一个账号——包括管理员试图把某个邮箱
	// 改派给别人时。
	ErrEmailTaken = errors.New("email already registered")
	// ErrUsernameTaken 表示该用户名已被占用。用户名的比较不区分大小写。
	ErrUsernameTaken = errors.New("username already taken")
	// ErrAdminAlreadyExists 表示插入管理员行时撞上了单管理员唯一索引
	// （idx_users_single_admin）。它是数据库层的不变量，不是应用层的预检结果，
	// 所以并发创建第二个管理员时也一定会得到这个错误，而不是两个都成功。
	ErrAdminAlreadyExists = errors.New("an administrator account already exists")
	// ErrNotAVisitorAccount 表示目标行存在，但它不是访客账号（即 is_admin = 1）。
	//
	// 它存在的理由是一个**安全不变量**，不是一个界面细节：管理员行的 email 是
	// 空串哨兵（users.email 为 NOT NULL UNIQUE，而空串是任何注册路径都产生不了
	// 的地址），于是管理员天然不可注册、也不可经公开的「找回密码」流程触达。
	// 面向访客账号的写入一旦落到管理员行上，这条不变量就断了——所以相关的
	// UPDATE 都带 `AND is_admin = 0`，并在影响 0 行时返回本错误。
	ErrNotAVisitorAccount = errors.New("target account is not a visitor account")
)

type User struct {
	ID int64
	// Email 是账号的**联系方式与身份凭证**，从不向其他访客暴露；
	// 只有账号本人与管理员可见。它不参与登录——登录用 Username。
	// 但它是「确实是本人」的证明来源：注册与找回密码的验证码都发到这里。
	Email string
	// Username 是**登录标识**，全站唯一（不区分大小写）。
	//
	// 它就是登录框里要填的那个东西。空串表示该账号创建于这一列存在之前、
	// 且回填尚未跑到（正常启动流程里 backfillUsernames 会保证它非空）。
	Username string
	// Nickname 是**公开的展示名**，只用于前台呈现，绝不参与认证。
	//
	// 它与 Username 是两个独立字段，这一点是刻意的：昵称可以重复、可以随时改，
	// 用户名必须唯一且稳定。把两者合成一个字段会让「改昵称」变成「改登录名」，
	// 既容易撞名，也让历史上用昵称署名的评论失去可追溯性。
	Nickname string
	// PasswordHash 是 bcrypt 哈希。为空表示该账号创建于「还没有密码」的时期，
	// 且此后尚未设置密码。
	PasswordHash string
	// IsAdmin 标记这一行是不是站长本人。全站**最多只有一行**为 true，由部分
	// 唯一索引 idx_users_single_admin 在数据库层钉死（见 database/schema.go）。
	//
	// 管理员住在同一张表里，因此「用户名全站唯一」只需一个索引就能保证——
	// 这正是把凭据并进来（而不是让它留在 settings.json）的全部理由。
	// 它的 email 恒为空串哨兵：那是一个任何注册路径都无法产生的地址。
	IsAdmin bool
	// AvatarMediaID 是站长的头像那张媒体的 id；nil 表示没设头像。
	//
	// 只有 is_admin = 1 的那一行会用到它——当前没有「访客头像」这个需求，
	// 列开在 users 上是因为它就是「这一行账号的一个可空引用」，另起一张表
	// 只会多一层连接。
	//
	// 与 articles.cover_media_id 完全同构：**引用不是所有权**，换图/摘除都不删
	// 媒体文件（媒体是硬删、无回收站），只解除引用；真正会删文件的只有
	// 「在媒体库里删除那张图」这一个动作，届时靠 ON DELETE SET NULL 把这一列
	// 自动置空。
	AvatarMediaID *int64
	CreatedAt     int64
	UpdatedAt     int64
}

// HasPassword 报告该账号能否用密码登录。
func (u *User) HasPassword() bool { return u.PasswordHash != "" }
