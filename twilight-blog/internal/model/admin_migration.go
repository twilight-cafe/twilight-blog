package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"blog-engine/internal/config"
	"blog-engine/internal/logging"
)

// MigrateAdminCredentials 把管理员凭据从 settings.json 搬进 users 表，
// 使管理员成为 is_admin = 1 的那一行。
//
// 为什么它住在 model 而不是 handle 提到的 database 包：database 包提供的是
// 「打开库 + 建表 + 补列」这套与业务无关的机制，它刻意不依赖 config 与 model
// （依赖方向是 database ← 上层）。而这个迁移正好相反——它要读 config 里的旧凭据、
// 要构造 User，属于**领域数据的搬运**，与仓储实现同层。
// 反过来放会让 database 依赖 model，而 model 自己的测试要 import database 建临时库，
// 于是形成 import cycle（`model` 的测试包 → `database` → `model`）。放在这里既避开
// 了循环，也符合「谁定义领域对象，谁负责搬运它」的分层。
//
// # 为什么这次迁移不能跳过
//
// 用户说过「不需要向前兼容」，但这一条是例外：管理员的密码哈希此前**只**存在于
// settings.json 里。如果直接把它从文件里删掉而不搬进库，升级后第一个启动瞬间
// 数据库里没有管理员行 → 「已安装」为假 → **未鉴权的 /install 重新开放**，
// 于是任何能访问站点的人都可以设一个新的管理员密码。那是一个真实的接管窗口。
//
// # 调用时机（硬约束）
//
// 必须严格早于 view.Register —— 也就是在进程开始对外服务**之前**。
// 晚一步就会留下上面那个窗口：第一个到达的请求会看到「未安装」并打开 /install。
// 见 main.go 里那条顺序注释。
//
// # 幂等
//
// 管理员行已经存在时**什么都不做**，绝不覆盖。否则一份陈旧的 settings.json 备份
// 会把已经轮换掉的旧密码复活，而且是在无人察觉的情况下——登录会成功，用的是
// 一个本该失效的密码。
//
// 返回 error 只用于真正的故障（查询或写入失败）；「没什么可迁的」不是错误：
// 全新安装、以及已经迁移过的库，都会走到这条正常路径上。
func MigrateAdminCredentials(ctx context.Context, db *sql.DB, cfg *config.Config) error {
	if cfg == nil {
		return nil
	}

	legacy := cfg.LegacyAdminCredentials()
	repo := NewSQLiteAdminRepository(db)

	// 幂等的第一道闸：库里已经有管理员行，就绝不碰它。
	//
	// 注意这一条要在「有没有可迁凭据」之前判断：老配置里可能还留着 password_hash，
	// 而此时用户早已通过 CLI 或 /install 换过密码——那种情况下文件里的是陈旧值。
	exists, err := repo.Exists(ctx)
	if err != nil {
		return fmt.Errorf("check for an existing administrator: %w", err)
	}
	if exists {
		logging.Debugf("[model] administrator row already present; skipping credential migration")
		return nil
	}

	// 没有哈希就说明老配置从未设过管理员密码，即博客本来就没装好：
	// 此时插入一个没有密码的管理员行反而会**把 /install 永久关掉**，
	// 而那个行谁也没法登录。所以这里必须直接返回。
	if strings.TrimSpace(legacy.PasswordHash) == "" {
		logging.Debugf("[model] no administrator credentials to migrate")
		return nil
	}

	username := strings.TrimSpace(legacy.Username)
	if username == "" {
		username = config.DefaultAdminName
	}
	nickname := strings.TrimSpace(legacy.Nickname)
	if nickname == "" {
		// 与 config.AdminDisplayName 的回退口径一致：昵称缺失时用登录名，
		// 而不是默认的 "admin"——后者会让「改了登录名却没设昵称」的站点
		// 显示成一个用户不认识的名字。
		nickname = username
	}

	now := time.Now().Unix()
	admin := &User{
		// 空串是哨兵：users.email 上有 NOT NULL UNIQUE，而 '' 是任何注册路径
		// 都无法产生的地址（normalizeAndValidateEmail 拒绝空值）。
		// 于是管理员天然不可注册，不需要任何额外的守卫代码。
		Email:        "",
		Username:     username,
		Nickname:     nickname,
		PasswordHash: legacy.PasswordHash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := repo.Create(ctx, admin); err != nil {
		// 撞上单管理员索引说明另一个进程（或并发启动的第二个实例）刚刚建好了行。
		// 那是幂等性的成功，不是失败。
		if errors.Is(err, ErrAdminAlreadyExists) {
			logging.Warnf("[model] another process created the administrator row first; keeping it")
			return nil
		}
		return fmt.Errorf("migrate administrator credentials: %w", err)
	}

	logging.Infof("[model] migrated the administrator account into the users table as user_id=%d username=%q; "+
		"settings.json no longer holds any password hash, so back up blog.db from now on", admin.ID, username)
	return nil
}
