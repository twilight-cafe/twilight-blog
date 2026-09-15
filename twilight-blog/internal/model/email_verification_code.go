package model

import "errors"

// 验证码查询与认领的哨兵错误。
//
// 这两个错误原先借用了 comment.go 的 ErrInvalidCommentCode（那是「评论验证码」
// 时代的命名遗留，见 email_verification_repo.go 的历史注释）。分开之后，
// 「这个邮箱没有有效的码」与「这枚码已经被用掉了」成为两件可区分的事：
// 前者是正常的过期路径，后者意味着并发争用或重放——两者的日志口径与告警
// 价值完全不同，不该共用一个错误值。
var (
	// ErrVerificationCodeNotFound 表示该邮箱没有「未消费且未过期」的验证码。
	ErrVerificationCodeNotFound = errors.New("verification code not found")
	// ErrVerificationCodeConsumed 表示这枚验证码已经被认领过了。
	// 它是**认领操作失败**的信号，不是查询结果：看到它就说明有另一个请求
	// 抢先消费了同一枚码（并发争用或重放），调用方必须 fail closed。
	ErrVerificationCodeConsumed = errors.New("verification code already consumed")
)

// EmailVerificationCode 是一次邮箱验证码的签发记录。
//
// CodeHash 存的是验证码的**慢哈希**（bcrypt，见 service.hashVerificationCode），
// 而不是明文、也不是无盐 SHA-256。
//
// 这一点是第三轮渗透测试改过来的：原先用无盐 SHA-256，而验证码只有 8 位十进制
// （当时是 6 位），整个搜索空间小到可以秒级穷举——「库里不存明文」对 10^8 的
// 空间等于没有任何保护，恰恰防不住下面 Attempts 那段所设想的攻击者。
// 之所以能沿用 SHA-256 的只有会话令牌那一类 256 位随机值。
//
// ConsumedAt 为 nil 表示尚未消费；已消费或已过期的记录由
// database.PruneExpired 统一清理（这是它们唯一的清理路径）。
type EmailVerificationCode struct {
	ID        int64
	Email     string
	CodeHash  string
	ExpiresAt int64
	// Attempts 记的是这枚码被猜错了多少次。它的有效期只有十分钟，
	// 而候选空间是 10^8——没有这个计数，一个足够大的分布式来源仍可能
	// 在有效期内把空间试完。达到上限后这枚码会被直接作废。
	Attempts   int
	ConsumedAt *int64
	CreatedAt  int64
}
