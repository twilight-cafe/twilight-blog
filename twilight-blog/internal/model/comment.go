package model

import "errors"

var (
	ErrCommentNotFound      = errors.New("comment not found")
	ErrInvalidCommentTarget = errors.New("invalid comment target")
	ErrInvalidCommentCode   = errors.New("invalid verification code")

	// ErrCommentTargetNotFound 表示 target_type 合法、但那个目标内容并不存在
	// 或已被软删。与 ErrInvalidCommentTarget 刻意分开：前者是「形状不对」
	// （未知类型、非正数 id），后者是「指向了不存在的东西」——两者对调用方
	// 的含义不同（400 与 404），也免得排障时把「类型写错」和「id 过期」混为一谈。
	ErrCommentTargetNotFound = errors.New("comment target not found")
)

// 访客内容的长度上限。文章与页面的正文刻意不设上限，因为只有站长能写；
// 评论与昵称面向公众，必须设限，否则单个请求就能写入一行任意大的数据
// （仅靠 HTTP 请求体上限，仍允许到几十 MB 量级）。
const (
	MaxCommentContentLength  = 2000
	MaxCommentNicknameLength = 24
	// AnonymousDisplayName 是账号昵称为空时的兜底署名（昵称字段引入之前
	// 创建的历史数据）。放在 model 里，是为了让 service 与 viewmodel 两层
	// 引用同一个字面量——两处各写一份迟早会漂移，读评论的人就会看到
	// 同一条评论在不同接口下署名不同。
	AnonymousDisplayName = "访客"
)

type Comment struct {
	ID         int64
	AuthorType string
	UserID     *int64
	AdminName  string
	TargetType string
	TargetID   int64
	ParentID   *int64
	Content    string
	CreatedAt  int64
	UpdatedAt  int64
	DeletedAt  *int64

	// Nickname 是访客发表评论时自选的显示名。
	// 它是唯一对其他访客公开的作者身份；邮箱只留给后台管理端看。
	Nickname string

	// UserEmail 从 users 表带出，用于访客评论。
	// 它只在管理员已鉴权的响应中才会被序列化输出。
	UserEmail string

	// LikeCount 是冗余存在评论行上的点赞数（不是实时 COUNT）。
	LikeCount int64

	// LikedByMe 仅在查询时提供了当前用户 id 的情况下才会被填充。
	LikedByMe bool
}
