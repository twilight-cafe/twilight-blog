package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"blog-engine/internal/logging"
)

// EmailVerificationRepository 是邮箱验证码的持久化契约。
// 同样只存哈希（CodeHash），明文验证码只存在于发给用户的那封邮件里。
// 已消费或已过期的记录由 database.PruneExpired 统一清理，这是它们唯一的清理路径。
type EmailVerificationRepository interface {
	Create(ctx context.Context, code *EmailVerificationCode) error
	FindLatestActiveByEmail(ctx context.Context, email string, now int64) (*EmailVerificationCode, error)
	// ClaimCode 原子地认领一枚验证码：仅当它仍未消费时才成功。
	//
	// 之所以叫「认领」而不是「标记」，是因为这一步承担的是**并发正确性**。
	// 上层走的是「先校验、后消费」两步（见 service.UserService 的 verifyCode /
	// claimCode），两步之间存在窗口。把它做成带条件的原子更新后，窗口内被抢先
	// 的那一次会拿到 ErrVerificationCodeConsumed，而不是静默成功——于是同一枚码
	// 不可能授权两次操作。
	ClaimCode(ctx context.Context, id int64, consumedAt int64) error
	// InvalidateActiveForEmail 作废该邮箱下所有仍然有效的验证码，使「签发新码」
	// 同时淘汰旧码。否则每次请求都会多出一个同时有效的码，
	// 把攻击者可用的猜测次数成倍放大。
	InvalidateActiveForEmail(ctx context.Context, email string, consumedAt int64) error
	// CountCreatedSince 统计该邮箱自 since 起签发过多少枚验证码（含已消费的）。
	//
	// 存在的理由是「按 IP 限流挡不住分布式刷某一个收件箱」：攻击者换 IP 就能
	// 重新拿到一份额度，而受害者只有一个邮箱。按地址计数是唯一能把这条滥用
	// 路径收住的维度，且不需要引入图形验证码那样的第三方依赖。
	CountCreatedSince(ctx context.Context, email string, since int64) (int64, error)
	// LastCreatedAt 返回该邮箱最近一次签发验证码的时刻（Unix 秒）。
	// 该邮箱从未签发过任何验证码时，第二个返回值为 false。
	//
	// 与 CountCreatedSince 的区别是「最近一次」vs「有多少次」：短间隔节流要问的是
	// 「上一封离现在够久了吗」，而不是「这段时间发了几封」。用计数去近似会很难看——
	// 1 分钟窗口里计数 ≥1 就等价于「间隔 < 60 秒」，但那要求调用方自己凑出一个
	// 与间隔等长的窗口，且窗口边界与真实间隔之间会差出一个取整误差。
	//
	// find=false 与 err 必须分开：前者是「从没发过，放行」，后者是「查不出来，
	// 应当失败」。把「查不出来」当成「没发过」，等于一次数据库故障就让节流失效。
	LastCreatedAt(ctx context.Context, email string) (int64, bool, error)
	// ReserveAttempt 原子地**预留一次猜测机会**：仅当这枚码仍未被认领、
	// 且已用次数还没到 limit 时才成功，成功返回 true。
	//
	// 它是「一枚码最多被猜 limit 次」这条不变量的**执行点**，所以必须在
	// 比较验证码**之前**调用。理由是一个真实的并发缺口：如果先比较、失败后再
	// 递增计数，N 个并发请求会各自读到「还没到上限」然后一起通过——上限被绕过，
	// 而串行时看起来完全正常（实测 200 并发放大到了 200 次猜测）。
	// 把「占用一次机会」做成先决条件，最多只有 limit 个请求能走到比较那一步。
	//
	// 未成功（返回 false）时调用方必须 fail closed，按「码无效/已失效」处理。
	ReserveAttempt(ctx context.Context, id int64, limit int) (bool, error)
}

type SQLiteEmailVerificationRepository struct {
	db *sql.DB
}

func NewSQLiteEmailVerificationRepository(db *sql.DB) *SQLiteEmailVerificationRepository {
	return &SQLiteEmailVerificationRepository{db: db}
}

func (r *SQLiteEmailVerificationRepository) Create(ctx context.Context, code *EmailVerificationCode) error {
	logging.Debugf("[email-verification-repo] Create called")
	res, err := r.db.ExecContext(ctx, `
INSERT INTO email_verification_codes (email, code_hash, expires_at, consumed_at, attempts, created_at)
VALUES (?, ?, ?, NULL, 0, ?)
`, code.Email, code.CodeHash, code.ExpiresAt, code.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert email verification code: %w", err)
	}

	id, err := lastInsertIDFn(res)
	if err != nil {
		return fmt.Errorf("get last insert id: %w", err)
	}
	code.ID = id
	return nil
}

// FindLatestActiveByEmail 取该邮箱最近一条「未消费且未过期」的验证码。
// 没有这样的记录时返回 ErrVerificationCodeNotFound。
func (r *SQLiteEmailVerificationRepository) FindLatestActiveByEmail(ctx context.Context, email string, now int64) (*EmailVerificationCode, error) {
	logging.Debugf("[email-verification-repo] FindLatestActiveByEmail called")
	var code EmailVerificationCode
	var consumedAt sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
SELECT id, email, code_hash, expires_at, attempts, consumed_at, created_at
FROM email_verification_codes
WHERE email = ? COLLATE NOCASE AND consumed_at IS NULL AND expires_at > ?
ORDER BY created_at DESC, id DESC
LIMIT 1
`, email, now).Scan(
		&code.ID,
		&code.Email,
		&code.CodeHash,
		&code.ExpiresAt,
		&code.Attempts,
		&consumedAt,
		&code.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrVerificationCodeNotFound
		}
		return nil, fmt.Errorf("find active verification code: %w", err)
	}
	// 查询条件带 consumed_at IS NULL，所以这里永远扫到 NULL。保留列本身是因为
	// SELECT 的列顺序与结构体对齐；不再写一个永远不可达的赋值分支。
	_ = consumedAt
	return &code, nil
}

// ClaimCode 原子认领验证码：把 consumed_at 从未消费改为已消费，仅当它仍未被消费。
//
// 三个细节都不能省：
//   - `AND consumed_at IS NULL` 让这条 UPDATE 成为**比较并交换**。少了它，
//     两个并发请求会各自把同一行更新一次、都返回成功，同一枚码就授权了两次
//     操作（实测可复现，约 25% 的并发双请求命中）。
//   - `RowsAffected` 必须检查。受影响行数为 0 有两种含义——id 不存在，
//     或已经被别人认领；两种情况调用方都应当 fail closed。
//   - 与 RecordFailedAttempt 一样，递增与状态变更写在同一条语句里，不拆成两步。
func (r *SQLiteEmailVerificationRepository) ClaimCode(ctx context.Context, id int64, consumedAt int64) error {
	logging.Debugf("[email-verification-repo] ClaimCode called")
	res, err := r.db.ExecContext(ctx, `
UPDATE email_verification_codes
SET consumed_at = ?
WHERE id = ? AND consumed_at IS NULL
`, consumedAt, id)
	if err != nil {
		return fmt.Errorf("claim verification code: %w", err)
	}
	affected, err := rowsAffectedFn(res)
	if err != nil {
		return fmt.Errorf("claim verification code: %w", err)
	}
	if affected == 0 {
		logging.Warnf("[email-verification-repo] code already claimed or missing code_id=%d", id)
		return ErrVerificationCodeConsumed
	}
	return nil
}

func (r *SQLiteEmailVerificationRepository) InvalidateActiveForEmail(ctx context.Context, email string, consumedAt int64) error {
	logging.Debugf("[email-verification-repo] InvalidateActiveForEmail called")
	if _, err := r.db.ExecContext(ctx, `
UPDATE email_verification_codes
SET consumed_at = ?
WHERE email = ? COLLATE NOCASE AND consumed_at IS NULL
`, consumedAt, email); err != nil {
		return fmt.Errorf("invalidate active verification codes: %w", err)
	}
	return nil
}

// CountCreatedSince 数的是**签发次数**而不是「当前有效的码」：过期与已消费的
// 记录同样占配额。否则「等 10 分钟让它过期，再要一枚」就成了一条无限循环的
// 绕过路径。邮箱比较用 COLLATE NOCASE，与查找、作废两处保持一致
// ——大小写不同的同一个地址必须算作同一个收件箱。
func (r *SQLiteEmailVerificationRepository) CountCreatedSince(ctx context.Context, email string, since int64) (int64, error) {
	logging.Debugf("[email-verification-repo] CountCreatedSince called")
	var count int64
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM email_verification_codes
WHERE email = ? COLLATE NOCASE AND created_at >= ?
`, email, since).Scan(&count); err != nil {
		return 0, fmt.Errorf("count verification codes for address: %w", err)
	}
	return count, nil
}

// LastCreatedAt 取该邮箱最近一次签发验证码的时刻。
//
// 用 MAX(created_at) 而不是 ORDER BY ... LIMIT 1：只关心时间戳，不需要把整行
// 拉回来；MAX 在 idx_email_verification_codes_email 上走索引即可。
//
// 邮箱比较用 COLLATE NOCASE，与查找、作废、计数三条路径保持一致——大小写不同的
// 同一个地址必须算作同一个收件箱，否则大小写变体就成了绕过短间隔节流的口子。
//
// 没有记录时返回 (0, false, nil)：sql.ErrNoRows 在这里不是错误，因为「从未签发」
// 正是节流要放行的情况。注意返回值的语义顺序——第一个是时刻，第二个是「是否存在」。
func (r *SQLiteEmailVerificationRepository) LastCreatedAt(ctx context.Context, email string) (int64, bool, error) {
	logging.Debugf("[email-verification-repo] LastCreatedAt called")
	var last sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `
SELECT MAX(created_at) FROM email_verification_codes
WHERE email = ? COLLATE NOCASE
`, email).Scan(&last); err != nil {
		return 0, false, fmt.Errorf("get last verification code time for address: %w", err)
	}
	if !last.Valid {
		return 0, false, nil
	}
	return last.Int64, true, nil
}

// ReserveAttempt 原子地占用一次猜测机会。
//
// 一条 UPDATE 里完成两件事：递增计数，并用 WHERE 把「已被认领」与「次数已用尽」
// 都变成失败条件。两者必须同语句——拆开就会重新出现「读的时候还没到上限、
// 写的时候已经超了」的窗口，而那正是这个并发缺口本身。
//
// ⚠️ 这里**刻意不设置 consumed_at**，尽管「达到上限即作废」读起来更自然。
// 原因是它会打断一条合法路径：如果第 limit 次预留顺手把 consumed_at 写成非 NULL，
// 那么「第 limit 次猜对了」的用户会在随后的 ClaimCode（带 AND consumed_at IS NULL）
// 上被拒绝——一个正确验证码反而登不进去。上限本身由 `attempts < limit` 单独保证，
// 已经用尽的码对后续请求一律 fail closed，所以作废是**隐含**的，不需要额外标记。
// 记录本身会在下一次 RequestEmailCode 的 InvalidateActiveForEmail、或 TTL 到期时
// 被清理，不会长期残留。
//
// limit <= 0 视为「不允许任何猜测」，直接 fail closed——配错成 0 时宁可让流程
// 不可用，也不要静默变成无限次。
func (r *SQLiteEmailVerificationRepository) ReserveAttempt(ctx context.Context, id int64, limit int) (bool, error) {
	logging.Debugf("[email-verification-repo] ReserveAttempt called")
	if limit <= 0 {
		return false, nil
	}

	res, err := r.db.ExecContext(ctx, `
UPDATE email_verification_codes
SET attempts = attempts + 1
WHERE id = ? AND consumed_at IS NULL AND attempts < ?
`, id, limit)
	if err != nil {
		return false, fmt.Errorf("reserve verification attempt: %w", err)
	}

	affected, err := rowsAffectedFn(res)
	if err != nil {
		return false, fmt.Errorf("reserve verification attempt rows affected: %w", err)
	}
	if affected == 0 {
		// 两种含义：这枚码已经被别人认领，或猜测次数已经用尽。
		// 两者调用方都应当 fail closed，所以不必区分。
		logging.Warnf("[email-verification-repo] verification attempt refused (already claimed or exhausted) code_id=%d", id)
		return false, nil
	}
	return true, nil
}
