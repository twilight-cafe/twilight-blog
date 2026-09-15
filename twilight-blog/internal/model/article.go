package model

// Article 是一篇文章。
//
// 时间字段一律是 Unix 秒（全项目统一，不是毫秒）。
// CreatedAt 是**发布时间**，不是「这条记录被插入的时刻」——后台可以在创建与编辑时
// 显式指定它，列表也按它倒序（见 SQLiteArticleRepository.List）。因此修改它是
// 一次正常的内容操作，会改变文章在首页与归档里的位置；UpdatedAt 才是「最近改过」。
// DeletedAt 为 nil 表示未删除；文章只做软删，要永久删除必须先软删
// （见 SQLiteArticleRepository.HardDelete 的 deleted_at IS NOT NULL 条件）。
// Tags 不是表上的列，查询路径由 repository 从 article_tags 装配；
// 写入路径（Create/Update）则把它当作「这篇文章期望的标签全集」。
//
// CoverMediaID 是头图那张媒体的 id，nil 表示没有头图。它**不是**「这张图归文章所有」
// 的声明，只是一个指针——同一张媒体理论上可以被多行指向（服务层不阻止，见
// ArticleService 的说明）。因此删除文章时不能无条件删掉它指向的媒体。
type Article struct {
	ID           int64
	Title        string
	Content      string
	CreatedAt    int64
	UpdatedAt    int64
	DeletedAt    *int64
	CoverMediaID *int64
	Tags         []string
}
