// Package service 承载博客的业务规则，夹在 model（持久化）与 view/handler（HTTP）之间。
//
// 本层负责三件事：
//   - 校验与归一化输入（空值、长度、标签去重等），并把「客户端错误」与「服务端错误」
//     区分开：前者返回 *ValidationError，让 handler 应答 400 而不是 500；
//   - 生成时间戳：所有 CreatedAt/UpdatedAt 都在这里用 time.Now().Unix() 赋值，
//     model 层只负责存；
//   - 维护全文搜索索引：SQLite 落库成功后，尽力同步 Meilisearch。
//     这一步刻意做成「尽力而为」，失败只记日志、不回滚（见 indexArticle）。
//
// 本层不碰 HTTP（不解析请求、不写响应），也不拼 SQL。
package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"blog-engine/internal/logging"
	"blog-engine/internal/model"
)

// ArticleSearchIndexer 把外部 Meilisearch 索引与文章库的变更保持同步。
// 方法刻意保持最小，使 service 不依赖搜索模块的具体实现，便于测试时替换。
//
// 删除有单条与批量两个入口：批量删除必须走 DeleteArticles。用单条接口循环
// 会让成本与请求里的 id 个数成正比（每次都要同步等一个 Meilisearch 任务，
// 实测约 104ms/id），一个几 MB 的请求就能把服务占用数小时。
type ArticleSearchIndexer interface {
	IndexArticle(context.Context, *model.Article) error
	DeleteArticle(context.Context, int64) error
	DeleteArticles(context.Context, []int64) error
}

// ArticleMediaStore 是文章服务对媒体的最小依赖，实现是 *MediaService。
//
// 只需要两个方法：确认「这张图能不能当头图」（存在、且确实是图片），
// 以及在永久删除文章时把专属头图一并删掉。
// 接口留窄是有意的——文章服务不该因为「媒体还能转码、能列目录」而多出依赖。
type ArticleMediaStore interface {
	Get(ctx context.Context, id int64) (*model.Media, error)
	Delete(ctx context.Context, id int64) error
}

// ArticleWrite 是一次文章写入的意图，Create 与 Update 共用。
//
// 为什么用结构体而不是继续堆位置参数：这里已经有四个可选字段，其中两个是
// *int64（发布时间与头图媒体 id）。位置相邻、类型相同，传反了不会有任何编译错误，
// 只会把媒体 id 写进 created_at、把发布时间当成头图去查——一次静默的数据错位。
// 具名字段让每个调用点自解释，也让以后再加字段时不必再动所有调用方。
//
// 两个入口对「nil」的解释一致（都表示「调用方没提这个字段」），差别只在
// 各自把「没提」当成什么，见各字段的说明。
type ArticleWrite struct {
	Title string
	// Content 为 nil：Create 视为空正文；Update 视为「不改正文」。
	Content *string
	// TitleEN / ContentEN 与 Content 同一条规则：nil = Create 视为空 / Update 不改。
	TitleEN   *string
	ContentEN *string
	// AIGenerated / AIGeneratedEN 为 nil：Create 视为 false；Update 视为「不改标记」。
	// 两者**按语言独立**：中文标记不会影响英文，反之亦然。
	AIGenerated   *bool
	AIGeneratedEN *bool
	// Tags 为 nil：Create 视为无标签；Update 视为「不改标签」。
	Tags *[]string
	// PublishedAt 为 nil：Create 用当前时间；Update 保持原值。
	// 非 nil 时是 Unix 秒，直接写进 model.Article.CreatedAt——那一列是**发布时间**
	// （见该结构体的说明），所以这是一个正常的内容操作，会改变文章的排序位置。
	PublishedAt *int64
	// CoverSet 为 false 表示调用方没有提头图这件事（只有 Update 有实际意义），
	// 此时 CoverMediaID 必须为 nil，语义是「别动头图」。
	// 为 true 时由 CoverMediaID 决定结果：nil = 摘掉头图，非 nil = 换成这张媒体。
	CoverSet     bool
	CoverMediaID *int64
}

type ArticleService struct {
	repo   model.ArticleRepository
	search ArticleSearchIndexer
	media  ArticleMediaStore
}

func NewArticleService(repo model.ArticleRepository, search ArticleSearchIndexer, media ArticleMediaStore) *ArticleService {
	return &ArticleService{repo: repo, search: search, media: media}
}

// indexArticle 尽力把文章推送到搜索索引。
//
// 「尽力而为」是刻意的：数据库写入此刻已经成功，索引失败不该让整个请求失败，
// 因此只记 error 日志、不上抛、也不回滚。代价是索引可能落后于库
// （用户会搜不到刚发布的文章），目前没有重试或重建机制。
// s.search 为 nil 表示该部署未启用搜索，此时静默跳过。
func (s *ArticleService) indexArticle(ctx context.Context, article *model.Article) {
	if s.search == nil {
		return
	}
	if err := s.search.IndexArticle(ctx, article); err != nil {
		logging.Errorf("[article] search index update failed id=%d: %v", article.ID, err)
	}
}

// deleteArticles 一次从索引里移除多篇文章。
//
// 它取代了「对每个 id 调一次单条接口」的循环。那个写法在功能上正确，
// 成本却是 O(N) 次同步外呼：每次都要提交一个 Meilisearch 任务并等它结束，
// 而且**不存在的 id 也会照样外呼**。实测 300 个 id 耗时 31 秒（约 104ms/id），
// 而请求体上限 50MB 能装下上百万个 id——成本由此变成了一个可被单个请求触发的
// 数小时级占用。批量接口把 N 次往返压成 1 次。
//
// 取代之后那个单条包装就没有调用方了，已一并删掉：留着一个永远不会被执行的方法，
// 只会让下一个读代码的人以为「某处可能走单条路径」。
func (s *ArticleService) deleteArticles(ctx context.Context, ids []int64) {
	if s.search == nil || len(ids) == 0 {
		return
	}
	if err := s.search.DeleteArticles(ctx, ids); err != nil {
		logging.Errorf("[article] search batch delete failed count=%d: %v", len(ids), err)
	}
}

// Create 归一化标签后落库，随后尽力索引。
//
// 时间戳在这里生成：UpdatedAt 恒为「现在」，CreatedAt 默认也是「现在」，
// 但调用方可以用 PublishedAt 显式指定——那一列是发布时间，前台按它排序与展示。
// 允许指定是刻意的：把一篇旧文补录进博客、或把误排的日期改回来，都需要它。
//
// 头图在同一次写入里确定：只看 CoverMediaID（Create 上不存在「要不要动原图」，
// CoverSet 无意义）。非 nil 时会先确认那张媒体存在且是图片，
// 不成立就返回 *ValidationError，让 handler 应答 400 而不是留下一个坏指针。
func (s *ArticleService) Create(ctx context.Context, in ArticleWrite) (*model.Article, error) {
	tags := []string{}
	if in.Tags != nil {
		tags = normalizeTags(*in.Tags)
	}

	content := ""
	if in.Content != nil {
		content = *in.Content
	}
	titleEN := ""
	if in.TitleEN != nil {
		titleEN = *in.TitleEN
	}
	contentEN := ""
	if in.ContentEN != nil {
		contentEN = *in.ContentEN
	}
	// AI 声明按语言独立：中文版与英文版各存各的，互不覆盖。
	// 客户端没传的那一份按「未标记」处理。
	aiGeneratedZH := in.AIGenerated != nil && *in.AIGenerated
	aiGeneratedEN := in.AIGeneratedEN != nil && *in.AIGeneratedEN

	cover, err := s.usableCoverID(ctx, in.CoverMediaID)
	if err != nil {
		return nil, err
	}

	logging.Debugf("[article] creating article title=%q tags=%v cover=%v", in.Title, tags, cover)

	now := time.Now().Unix()
	publishedAt := now
	if in.PublishedAt != nil {
		publishedAt = *in.PublishedAt
	}

	article := &model.Article{
		Title:         in.Title,
		Content:       content,
		TitleEN:       titleEN,
		ContentEN:     contentEN,
		AIGenerated:   aiGeneratedZH,
		AIGeneratedEN: aiGeneratedEN,
		CreatedAt:     publishedAt,
		UpdatedAt:     now,
		CoverMediaID:  cover,
		Tags:          tags,
	}

	if err := s.repo.Create(ctx, article); err != nil {
		logging.Errorf("[article] create article failed title=%q: %v", in.Title, err)
		return nil, err
	}
	logging.Infof("[article] created id=%d title=%q", article.ID, article.Title)
	s.indexArticle(ctx, article)
	return article, nil
}

// Update 用给定字段覆盖文章。tags 为 nil 表示调用方没有传这个字段，
// 此时保留原有标签；传一个空的切片（非 nil）则表示「清空标签」。
// 两者语义不同，不要把它简化成 len(tags) == 0 的判断。
//
// content 同理，而且更危险：它是文章的正文。content 为 nil 表示「这次不改正文」，
// 非 nil 才替换。非指针时期，一个只改标题的 PUT 会把正文清空成空串——返回 200、
// 日志无异常，只有读者会发现文章空了。**不要**为了省事把它退回 string。
//
// publishedAt 为 nil 表示「不改发布时间」。它写的是 CreatedAt，因此这一条
// 会改变文章在列表里的位置——那正是「修改发布时间」这个功能的意义。
//
// 头图是三态的（见 ArticleWrite.CoverSet）：不提供就不动，显式提供 nil 才摘掉。
// 摘掉只是把指针置空，**不删媒体**——那张图可能还被别处引用，而且用户很可能
// 只是换图，旧图应当留在媒体库里等他决定。
//
// 这里先读后写（GetByID 再 Update），存在读-改-写竞态：两个并发更新可能互相覆盖。
// 单连接池把窗口压得很小，但这不是乐观锁。
func (s *ArticleService) Update(ctx context.Context, id int64, in ArticleWrite) (*model.Article, error) {
	logging.Debugf("[article] updating article id=%d title=%q content_provided=%t tags_provided=%t published_at_provided=%t cover_provided=%t",
		id, in.Title, in.Content != nil, in.Tags != nil, in.PublishedAt != nil, in.CoverSet)
	article, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logging.Errorf("[article] get article for update failed id=%d: %v", id, err)
		return nil, err
	}

	article.Title = in.Title
	if in.Content != nil {
		article.Content = *in.Content
	}
	if in.TitleEN != nil {
		article.TitleEN = *in.TitleEN
	}
	if in.ContentEN != nil {
		article.ContentEN = *in.ContentEN
	}
	// AI 标记与正文一样是三态（nil = 不改），但**两种语言各自独立**：
	// 只改英文不会影响中文的声明，反之亦然。
	if in.AIGenerated != nil {
		article.AIGenerated = *in.AIGenerated
	}
	if in.AIGeneratedEN != nil {
		article.AIGeneratedEN = *in.AIGeneratedEN
	}
	if in.Tags != nil {
		article.Tags = normalizeTags(*in.Tags)
	}
	if in.PublishedAt != nil {
		article.CreatedAt = *in.PublishedAt
	}
	if in.CoverSet {
		// 校验排在赋值之前：图不存在或不是图片时直接返回，库里那篇文章
		// 一个字段都不会被动到（此刻所有改动都还只在内存里）。
		cover, err := s.usableCoverID(ctx, in.CoverMediaID)
		if err != nil {
			return nil, err
		}
		article.CoverMediaID = cover
	}
	article.UpdatedAt = time.Now().Unix()

	if err := s.repo.Update(ctx, article); err != nil {
		logging.Errorf("[article] update article failed id=%d: %v", id, err)
		return nil, err
	}
	logging.Infof("[article] updated id=%d title=%q", article.ID, article.Title)
	s.indexArticle(ctx, article)
	return article, nil
}

// usableCoverID 校验「这张图能不能当某篇文章的头图」，通过时返回它的 id。
//
// id 为 nil 直接返回 nil（表示「没有头图 / 摘掉头图」），因此调用方不必先判空。
// 三种失败都返回 *ValidationError（→ 400），因为这三种都是**客户端给错了**，
// 不是服务端故障：
//   - id 非正数；
//   - 媒体不存在（已删或从未存在）；
//   - 媒体存在但不是图片——`<img src>` 指向一段视频只会得到一个破图，
//     与其让它在读者面前坏掉，不如在写文章的那一刻就说清楚。
//
// 返回的是**副本**而不是入参指针：调用方会把它存进结构体，共享指针意味着
// 此后任何一处改动都会被另一处看到。
func (s *ArticleService) usableCoverID(ctx context.Context, id *int64) (*int64, error) {
	if id == nil {
		return nil, nil
	}
	if *id <= 0 {
		return nil, invalidf("cover_media_id must be a positive integer")
	}

	media, err := s.media.Get(ctx, *id)
	if err != nil {
		if errors.Is(err, model.ErrMediaNotFound) {
			return nil, invalidf("cover media %d does not exist", *id)
		}
		return nil, err
	}
	if media.MediaType != model.MediaTypeImage {
		return nil, invalidf("cover media %d is %s, but a cover must be an image", *id, media.MediaType)
	}

	value := *id
	return &value, nil
}

// SoftDelete 软删一批文章，并把它们从索引中一次移除。ids 为空时直接返回。
// 注意是「先改库、再同步索引」，不是原子操作。
func (s *ArticleService) SoftDelete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	logging.Debugf("[article] soft-deleting ids=%v", ids)
	if _, err := s.repo.SoftDelete(ctx, ids, time.Now().Unix()); err != nil {
		logging.Errorf("[article] soft-delete failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[article] soft-deleted ids=%v", ids)
	s.deleteArticles(ctx, ids)
	return nil
}

// Restore 恢复一篇文章并重新索引。
//
// 若恢复已成功、但随后重新读取失败，这里只记日志仍然返回 nil——
// 因为恢复本身已经生效，不该向调用方报失败。
func (s *ArticleService) Restore(ctx context.Context, id int64) error {
	logging.Debugf("[article] restoring id=%d", id)
	if err := s.repo.Restore(ctx, id, time.Now().Unix()); err != nil {
		logging.Errorf("[article] restore failed id=%d: %v", id, err)
		return err
	}
	logging.Infof("[article] restored id=%d", id)
	if restored, err := s.repo.GetByID(ctx, id); err == nil {
		s.indexArticle(ctx, restored)
	} else {
		logging.Errorf("[article] reindex after restore failed id=%d: %v", id, err)
	}
	return nil
}

// BatchRestore 批量恢复，做法与 Restore 相同，只是逐条重新索引。
func (s *ArticleService) BatchRestore(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	logging.Debugf("[article] batch-restoring ids=%v", ids)
	if _, err := s.repo.BatchRestore(ctx, ids, time.Now().Unix()); err != nil {
		logging.Errorf("[article] batch-restore failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[article] batch-restored ids=%v", ids)
	for _, id := range ids {
		if restored, err := s.repo.GetByID(ctx, id); err == nil {
			s.indexArticle(ctx, restored)
		} else {
			logging.Errorf("[article] reindex after batch restore failed id=%d: %v", id, err)
		}
	}
	return nil
}

// HardDelete 永久删除文章、把随之失去最后一个引用者的专属头图一并删掉，
// 并从索引中一次移除。
//
// 只有回收站里的文章能被真正删掉（约束在 model 层），活动态调用会静默无效果。
//
// 顺序是刻意的，三步都不可调换：
//  1. 先问出「删完之后哪些头图会成为孤儿」——删完那几行，cover_media_id 就没了，
//     再也问不出来（model.CoverMediaIDsToDelete 的 WHERE 与这里的前置条件一致，
//     所以对活动文章调用不会问出任何东西）；
//  2. 再删文章；
//  3. 最后删媒体。
//
// 反过来（先删媒体）一旦删文章失败，图已经没了而文章还在——那是不可逆的损失。
// 现在这个顺序最坏是「文章删了、媒体没删成」：留下一个无人引用的文件，
// 日志里有记录、可以人工清理，而且没有任何界面会再引用到它。
//
// 为什么头图要在这一步删、而不是软删（进回收站）时就删：回收站的存在意义就是
// 「删错了能找回来」。软删时顺手销毁头图，会让「恢复文章」变成一个会丢数据的
// 操作，而它看上去只是一个可以撤销的动作。media 本身没有回收站（见
// MediaRepository 的说明），所以真正的销毁只能挂在**永久删除**这条路上。
func (s *ArticleService) HardDelete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	logging.Debugf("[article] permanently deleting ids=%v", ids)

	covers, err := s.repo.CoverMediaIDsToDelete(ctx, ids)
	if err != nil {
		logging.Errorf("[article] resolving covers for permanent delete failed ids=%v: %v", ids, err)
		return err
	}

	if _, err := s.repo.HardDelete(ctx, ids); err != nil {
		logging.Errorf("[article] permanent delete failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[article] permanently deleted ids=%v", ids)
	s.deleteArticles(ctx, ids)

	// 清理用 context.Background() 而不是请求的 ctx：文章此刻已经永久删除，
	// 一个中途断开的连接不该让磁盘上留下一份再也无人引用的文件。与 handler/media.go
	// 里批量上传的补偿回滚同一条理由——收尾动作必须跑完。
	// 失败只记 Warn、不上抛：接口已经成功，为一个残留文件报错会让调用方以为
	// 删除失败并去重试，而重试时文章已经不存在了。
	for _, mediaID := range covers {
		if err := s.media.Delete(context.Background(), mediaID); err != nil {
			logging.Warnf("[article] removing cover media failed media_id=%d article_ids=%v: %v", mediaID, ids, err)
		}
	}
	return nil
}

func (s *ArticleService) Get(ctx context.Context, id int64) (*model.Article, error) {
	logging.Debugf("[article] get id=%d", id)
	return s.repo.GetByID(ctx, id)
}

func (s *ArticleService) List(ctx context.Context, p model.Pagination) ([]model.Article, int64, error) {
	logging.Debugf("[article] list page=%d page_size=%d", p.Page, p.PageSize)
	return s.repo.List(ctx, p)
}

func (s *ArticleService) ListDeleted(ctx context.Context, p model.Pagination) ([]model.Article, int64, error) {
	logging.Debugf("[article] list deleted page=%d page_size=%d", p.Page, p.PageSize)
	return s.repo.ListDeleted(ctx, p)
}

// normalizeTags 归一化标签列表：去掉首尾空白、丢弃空串、按小写去重。
//
// 去重时保留首次出现的原始大小写（"Go" 与 "go" 视为同一个标签，存先出现的那个写法），
// 与数据库 tags.name 的 COLLATE NOCASE UNIQUE 口径一致。
func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, tag)
	}
	return result
}
