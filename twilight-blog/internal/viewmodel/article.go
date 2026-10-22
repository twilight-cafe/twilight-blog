// Package viewmodel 是 HTTP 层与领域层之间的装配层：请求 DTO、响应 DTO，
// 以及二者的校验与相互转换。
//
// 它存在的意义是让 JSON 的形状与数据库结构解耦——例如 model.Media 里的
// StorageName 从不出现在响应里，而响应会多出一个由服务端拼好的 URL。
//
// 两条容易忽略的约定：
//   - Validate() 返回的是普通的 fmt.Errorf 错误，而不是 service.ValidationError。
//     因此 handler 必须自己把这个错误映射成 400，别指望它能被 service 层的
//     错误分支识别；
//   - Validate() 会就地修改接收者（对各字段做 TrimSpace），
//     所以校验通过后应直接使用该结构体，不要回头再用原始输入。
package viewmodel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"blog-engine/internal/model"

	"blog-engine/internal/logging"
)

// CreateArticleRequest 是新建文章的请求体。Tags 缺失等价于空列表，
// 因为新建时本来就没有既有标签可言。
type CreateArticleRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	// TitleEN / ContentEN 是英文版本；省略或空串表示没有英文版。
	TitleEN   string `json:"title_en"`
	ContentEN string `json:"content_en"`
	// AIGenerated 由站长显式标记，服务端不做任何推断。
	AIGenerated bool     `json:"ai_generated"`
	Tags        []string `json:"tags"`
	// PublishedAt 是发布时间（Unix 秒）。缺失（或显式 null）表示「用服务器当前
	// 时间」——新建时「不指定」与「置空」没有区别，文章总得有个发布时间，
	// 因此这里不需要 UpdateArticleRequest.CoverMediaID 那种三态。
	PublishedAt *int64 `json:"published_at"`
	// CoverMediaID 是头图那张媒体的 id。缺失（或显式 null）表示这篇文章没有头图。
	// 同样是两态：新建时不存在「要不要动原头图」这个问题。
	CoverMediaID *int64 `json:"cover_media_id"`
}

type UpdateArticleRequest struct {
	Title string `json:"title"`
	// TitleEN / ContentEN 与 Content 同一条三态规则：nil = 不改，字符串（含空串）= 替换。
	// 用指针而不是 string 的理由与 Content 完全相同：只改标题的 PUT 不该清空英文正文。
	TitleEN   *string `json:"title_en"`
	ContentEN *string `json:"content_en"`
	// AIGenerated 是 nil 表示「这次不改」。用指针是为了让「没提这件事」与
	// 「明确取消 AI 标记」可区分。
	AIGenerated *bool `json:"ai_generated"`
	// Content 用指针的理由与 Tags 完全一致：让「字段缺失」与「显式传空串」可区分。
	// 非指针时期，一个只改标题的 PUT（前端保存草稿、脚本批量改标题都会这么发）
	// 会把正文静默清空并返回 200——数据没了、日志里也没有任何异常。
	// nil 表示「这次不动正文」。
	Content *string `json:"content"`
	// Tags 用指针，是为了让「字段缺失」与「显式传空数组」可区分。
	// 没有这层区分的话，一个根本没提标签的更新请求会静默删光该文章的所有标签。
	// 前端对应约定见 twilight-blog-api 的 updateArticle。
	Tags *[]string `json:"tags"`
	// PublishedAt 与上面两个同一条规则：缺失表示「不改发布时间」。
	// 这里用 *int64 就够了，不需要三态——把发布时间「置空」是没有意义的，
	// 文章必须有一个发布时间，所以不存在「摘掉」这个动作。
	PublishedAt *int64 `json:"published_at"`
	// CoverMediaID 需要三态，因此用 OptionalID 而不是 *int64：
	// 缺失 = 别动头图，显式 null = 摘掉头图，数字 = 换成这张。
	// *int64 会把它塌缩成两态，而塌缩的方向无论倒向哪边都是数据损坏
	// （要么用户摘掉的图在保存后复活，要么一次只改标题的保存静默删掉头图）。
	CoverMediaID OptionalID `json:"cover_media_id"`
}

// OptionalID 是一个「可以显式置空」的可选 id 字段。
//
// 三态必须可区分：字段缺失（Set 为 false，别动）、显式 null（Set 为 true 且
// Value 为 nil，置空）、给了数字（Set 为 true 且 Value 非 nil，换成它）。
//
// 为什么不能用 *int64 表达：encoding/json 把「字段缺失」与 `null` 都只把指针
// 留成 nil，两种输入在反序列化之后**完全不可区分**。这与 UpdateArticleRequest
// 里 Content / Tags 用指针的动机是同一条——那些字段自己顶得住（nil 与零值不同），
// 而一个可空的 id 顶不住，所以要多带一个「客户端提过这个字段」的标记。
type OptionalID struct {
	// Set 表示客户端在 JSON 里确实出现了这个字段（哪怕值是 null）。
	Set bool
	// Value 是解析出的 id；只有 Set 为 true 时才有意义。
	Value *int64
}

// UnmarshalJSON 让 OptionalID 自己消化三种输入。只有 **指针接收者** 才满足
// json.Unmarshaler，因此字段必须是可取地址的（结构体字段天然满足）。
func (o *OptionalID) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(bytes.TrimSpace(data)) == "null" {
		o.Value = nil
		return nil
	}
	var id int64
	if err := json.Unmarshal(data, &id); err != nil {
		return fmt.Errorf("cover_media_id must be an integer or null")
	}
	o.Value = &id
	return nil
}

// MaxBatchIDs 是一次批量操作允许携带的 id 个数上限。
//
// 存在的理由是**成本模型**，不是存储：批量删除会为每个 id 向 Meilisearch 发一次
// 请求（即使现在改成了批量接口，逐条的重索引/清理仍然与 id 数成正比），而请求体
// 上限是 50MB（main.go 的 BodyLimit）。一个约 6MB 的请求就能塞进上百万个 id，
// 在服务端变成以小时计的串行外呼。1000 已经远高于任何真实后台操作的需要
// （前端一次最多选中一页），超出即 400，让调用方分页处理而不是把服务挂住。
const MaxBatchIDs = 1000

// BatchDeleteRequest 是批量操作（软删 / 恢复 / 永久删除）共用的请求体。
type BatchDeleteRequest struct {
	IDs []int64 `json:"ids"`
}

// ArticleResponse 是文章的对外形状。
// DeletedAt 带 omitempty，因此未删除时该字段会整个消失——
// 客户端应按「字段可能不存在」处理，而不是假设它一定有值。
//
// CoverMediaID **不带** omitempty：它恒出现在响应里，没有头图时是 null。
// 这是刻意的——twilight-blog-api 的 Article 把它声明为必填（`number | null`），
// 而 ArticleSearchResult 继承 Article，所以搜索结果的 Result 里也有同样一个
// 恒输出的字段。前端据此直接判断 `cover_media_id !== null` 取图，
// 一个「有时不存在」的字段会让它需要多一层判空。
type ArticleResponse struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
	// TitleEN / ContentEN 恒输出（空串表示没有英文版）。前端按语言选择，
	// 英文缺失时回退到中文，因此不能让字段缺席。
	TitleEN      string   `json:"title_en"`
	ContentEN    string   `json:"content_en"`
	AIGenerated  bool     `json:"ai_generated"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
	DeletedAt    *int64   `json:"deleted_at,omitempty"`
	CoverMediaID *int64   `json:"cover_media_id"`
	Tags         []string `json:"tags"`
	// TagsEN 与 Tags 等长，空串表示该标签没有英文名。
	TagsEN []string `json:"tags_en"`
}

// NewArticleResponse 把领域对象转成响应。
// Tags 会从 nil 规范成空数组，免得客户端拿到 null 还要额外判空。
func NewArticleResponse(article *model.Article) ArticleResponse {
	logging.Debugf("[viewmodel] NewArticleResponse called")
	tags := article.Tags
	if tags == nil {
		tags = []string{}
	}
	// 两个切片必须等长：前端按索引取英文名，短一个就会把「最后一个标签」
	// 与「没有英文名」混在一起。
	tagsEN := article.TagsEN
	if tagsEN == nil {
		tagsEN = []string{}
	}
	for len(tagsEN) < len(tags) {
		tagsEN = append(tagsEN, "")
	}
	return ArticleResponse{
		ID:           article.ID,
		Title:        article.Title,
		Content:      article.Content,
		TitleEN:      article.TitleEN,
		ContentEN:    article.ContentEN,
		AIGenerated:  article.AIGenerated,
		CreatedAt:    article.CreatedAt,
		UpdatedAt:    article.UpdatedAt,
		DeletedAt:    article.DeletedAt,
		CoverMediaID: article.CoverMediaID,
		Tags:         tags,
		TagsEN:       tagsEN,
	}
}

// NewArticleResponseList 批量转换，结果恒为非 nil 切片。
func NewArticleResponseList(articles []model.Article) []ArticleResponse {
	logging.Debugf("[viewmodel] NewArticleResponseList called")
	result := make([]ArticleResponse, 0, len(articles))
	for i := range articles {
		result = append(result, NewArticleResponse(&articles[i]))
	}
	return result
}

// MaxPublishedAt 是发布时间允许的上界（不含）。
//
// 它不是随手取的：前端 lib/format.ts 用 1e12 作分界判断后端给的时间戳到底是秒
// 还是毫秒（大于它就当毫秒，乘以 1000 会溢出成 Invalid Date）。一个 1.7e12 的
// **毫秒**值如果被当成秒放行，会存进 created_at，于是前台把文章显示成公元
// 55835 年——数据没坏，界面却是一团乱，而且没有任何一步报错。与其让展示层去猜，
// 不如在入口把「这不像一个秒级时间戳」的值挡回去。1e12 秒约等于公元 33658 年，
// 远在任何真实发布时间之外，不会误伤。
const MaxPublishedAt = 1_000_000_000_000

// validatePublishedAt 校验客户端给的发布时间。
// nil 表示「这次不提这个字段」，由调用方决定默认值，因此直接放行。
func validatePublishedAt(value *int64) error {
	if value == nil {
		return nil
	}
	if *value <= 0 {
		return fmt.Errorf("published_at must be a positive unix timestamp in seconds")
	}
	if *value >= MaxPublishedAt {
		return fmt.Errorf("published_at must be unix seconds (values >= %d look like milliseconds)", int64(MaxPublishedAt))
	}
	return nil
}

func (r *CreateArticleRequest) Validate() error {
	r.Title = strings.TrimSpace(r.Title)
	r.TitleEN = strings.TrimSpace(r.TitleEN)
	if r.Title == "" {
		return fmt.Errorf("title is required")
	}
	// 英文标题与正文各自独立、都可为空：允许先补标题，稍后再补正文。
	if err := validatePublishedAt(r.PublishedAt); err != nil {
		return err
	}
	if r.CoverMediaID != nil && *r.CoverMediaID <= 0 {
		return fmt.Errorf("cover_media_id must be a positive integer")
	}
	return nil
}

func (r *UpdateArticleRequest) Validate() error {
	r.Title = strings.TrimSpace(r.Title)
	if r.Title == "" {
		return fmt.Errorf("title is required")
	}
	if r.TitleEN != nil {
		trimmed := strings.TrimSpace(*r.TitleEN)
		r.TitleEN = &trimmed
	}
	if err := validatePublishedAt(r.PublishedAt); err != nil {
		return err
	}
	// 只有「客户端真的给了数字」时才检查正负；Set 为 true 且 Value 为 nil
	// 是「摘掉头图」，那是一个合法意图。
	if r.CoverMediaID.Set && r.CoverMediaID.Value != nil && *r.CoverMediaID.Value <= 0 {
		return fmt.Errorf("cover_media_id must be a positive integer")
	}
	return nil
}

func (r *BatchDeleteRequest) Validate() error {
	if len(r.IDs) == 0 {
		return fmt.Errorf("ids is required")
	}
	if len(r.IDs) > MaxBatchIDs {
		return fmt.Errorf("ids must contain at most %d entries", MaxBatchIDs)
	}
	for _, id := range r.IDs {
		if id <= 0 {
			return fmt.Errorf("id must be a positive integer")
		}
	}
	return nil
}
