package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// AdminRepository 是**站长那一行**（is_admin = 1）的持久化契约。
//
// 它刻意不复用 UserRepository：那个接口描述的是「访客账号目录」——注册、按邮箱
// 查找、分页列表，这些操作对管理员都没有意义（管理员不自助注册、不用邮箱、
// 也不该出现在用户管理列表里）。共用一个接口会让每个访客方法都多出一句
// 「管理员行要不要算」的推理，而这类推理正是错的来源。
//
// 管理员与访客**共表**（都住在 users 里）是刻意的：那样「用户名全站唯一」
// 就由 idx_users_username 一个索引负责，不需要跨两个命名空间去比对。
//
// 全站最多一个管理员由 idx_users_single_admin 在数据库层保证，所以这里没有任何
// 「是不是已经存在」的预检——预检在并发下拦不住，而唯一索引一定拦得住：
// 撞上时 Create 返回 ErrAdminAlreadyExists。
type AdminRepository interface {
	// Create 插入管理员行（调用方负责把 User.IsAdmin 置为 true）。
	// 已存在管理员时返回 ErrAdminAlreadyExists。
	Create(ctx context.Context, admin *User) error
	// Get 返回当前管理员行；不存在时返回 ErrUserNotFound。
	Get(ctx context.Context) (*User, error)
	// Exists 报告博客是否已经装好（即管理员行是否存在）。
	Exists(ctx context.Context) (bool, error)
	// SetPassword 替换管理员的 bcrypt 哈希（校验旧密码是服务层的职责）。
	SetPassword(ctx context.Context, id int64, passwordHash string, updatedAt int64) error
	// UpdateIdentity 改管理员的登录名与展示名。用户名撞上任何账号
	// （访客或别的管理员）时返回 ErrUsernameTaken。
	UpdateIdentity(ctx context.Context, id int64, username, nickname string, updatedAt int64) error
	// UpdateAvatar 换掉（或摘除）管理员的头像。mediaID 为 nil 表示摘除。
	// 头像「必须是图片、必须存在」由服务层校验，这里只负责写。
	UpdateAvatar(ctx context.Context, id int64, mediaID *int64, updatedAt int64) error
}

// SQLiteAdminRepository 是 AdminRepository 的 SQLite 实现。
type SQLiteAdminRepository struct {
	users *SQLiteUserRepository
}

// NewSQLiteAdminRepository 构造管理员仓储。
//
// 它复用访客仓储而不是自己拿一份 *sql.DB：两条路径因此共用同一份 userColumns
// 与 scanUser，管理员行读出来的 IsAdmin 一定为 true——这是 T1（漏列导致管理员被
// 静默降级为访客）的最后一道结构性防线。
func NewSQLiteAdminRepository(db *sql.DB) *SQLiteAdminRepository {
	return &SQLiteAdminRepository{users: NewSQLiteUserRepository(db)}
}

// ErrAdminNotFound 是 ErrUserNotFound 的别名，让调用方在管理员语境下读起来更清楚。
// 值相同，所以 errors.Is 对两者都成立。
var ErrAdminNotFound = ErrUserNotFound

// Create 插入管理员行。第二个管理员会撞上 idx_users_single_admin，
// 由 uniqueViolationTarget 翻译成 ErrAdminAlreadyExists。
func (r *SQLiteAdminRepository) Create(ctx context.Context, admin *User) error {
	admin.IsAdmin = true
	return r.users.Create(ctx, admin)
}

// Get 读取唯一的管理员行。
//
// WHERE is_admin = 1 同时受唯一索引保护，所以 LIMIT 1 不会掩盖数据问题：
// 第二行根本插不进来。
func (r *SQLiteAdminRepository) Get(ctx context.Context) (*User, error) {
	row := r.users.db.QueryRowContext(ctx, `
SELECT `+userColumns+`
FROM users
WHERE is_admin = 1
LIMIT 1
`)

	admin, err := scanUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("get administrator: %w", err)
	}
	return admin, nil
}

// Exists 报告管理员行是否存在，供 /install_status 与安装端点判断「装没装过」。
//
// 用 SELECT EXISTS 而不是 Get：调用方只关心真假，不需要把整行（含哈希）读出来。
func (r *SQLiteAdminRepository) Exists(ctx context.Context) (bool, error) {
	var exists bool
	if err := r.users.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE is_admin = 1)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("check administrator: %w", err)
	}
	return exists, nil
}

// SetPassword 替换管理员的密码哈希。
// 管理员行不存在时返回 ErrUserNotFound（而不是静默成功）。
func (r *SQLiteAdminRepository) SetPassword(ctx context.Context, id int64, passwordHash string, updatedAt int64) error {
	res, err := r.users.db.ExecContext(ctx, `
UPDATE users
SET password_hash = ?, updated_at = ?
WHERE id = ? AND is_admin = 1
`, passwordHash, updatedAt, id)
	if err != nil {
		return fmt.Errorf("update administrator password: %w", err)
	}
	return requireAffected(res, ErrUserNotFound)
}

// UpdateIdentity 改管理员的登录名与展示名。
//
// 与访客路径共用同一个唯一索引，所以「管理员改名撞上访客的用户名」与
// 「访客注册撞上管理员的名字」都由数据库拦下，返回同一个 ErrUsernameTaken——
// 不需要任何一处预检，也不需要把管理员的登录名藏起来（见 user_service.go
// 对「预留用户名」那套机制的删除）。
func (r *SQLiteAdminRepository) UpdateIdentity(ctx context.Context, id int64, username, nickname string, updatedAt int64) error {
	res, err := r.users.db.ExecContext(ctx, `
UPDATE users
SET username = ?, nickname = ?, updated_at = ?
WHERE id = ? AND is_admin = 1
`, username, nickname, updatedAt, id)
	if err != nil {
		if target, ok := uniqueViolationTarget(err); ok {
			return uniqueTargetError(target)
		}
		return fmt.Errorf("update administrator identity: %w", err)
	}
	return requireAffected(res, ErrUserNotFound)
}

// UpdateAvatar 换掉（或摘除）管理员的头像。
//
// mediaID 为 nil 时写 NULL 而不是跳过这一列——「摘掉头像」是一个与「换成另一张」
// 平行的意图，两者都必须能表达。用 COALESCE(?, avatar_media_id) 之类的写法会把
// 「摘除」静默变成「不动」，用户就再也删不掉头像了（articles.cover_media_id 上
// 踩过同一个坑）。
//
// `AND is_admin = 1` 与其余管理员写入一致：访客行不该被这条路径碰到。
// 目标行不存在返回 ErrUserNotFound（而不是静默成功）。
func (r *SQLiteAdminRepository) UpdateAvatar(ctx context.Context, id int64, mediaID *int64, updatedAt int64) error {
	res, err := r.users.db.ExecContext(ctx, `
UPDATE users
SET avatar_media_id = ?, updated_at = ?
WHERE id = ? AND is_admin = 1
`, mediaID, updatedAt, id)
	if err != nil {
		return fmt.Errorf("update administrator avatar: %w", err)
	}
	return requireAffected(res, ErrUserNotFound)
}
