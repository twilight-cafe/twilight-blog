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
// 双语与 AI 标记说明：
//
//   - Title / Content 是**中文**（也是唯一必填的一份）；TitleEN / ContentEN 是
//     附加的英文版本，空串表示没有。前台按当前语言取用，英文缺失时逐字段回退中文。
//   - AIGenerated / AIGeneratedEN 是**按语言独立**的 AI 声明：中文版与英文版
//     各有一个开关，互不影响。旧库只有一列 ai_generated，迁移时把它复制到
//     ai_generated_en，保持升级前的显示结果不变。
//   - TagsEN 与 Tags **等长同序**，由 repository 在同一次 JOIN 里装配；某一项为空串
//     表示该标签没有英文名。它不是表上的列（标签名住在 tags 表）。
type Article struct {
	ID            int64
	Title         string
	Content       string
	TitleEN       string
	ContentEN     string
	AIGenerated   bool
	AIGeneratedEN bool
	CreatedAt     int64
	UpdatedAt     int64
	DeletedAt     *int64
	CoverMediaID  *int64
	Tags          []string
	TagsEN        []string
}
