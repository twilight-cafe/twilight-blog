// Package model 定义博客的领域对象与持久化接口。
//
// 分层边界：本包只做「SQL ↔ 领域对象」的转换，不含业务规则——业务规则在
// internal/service，HTTP 编排在 internal/view/handler。唯一的例外是
// 同一实体的主子表写入，在这里用事务保证原子性（如文章与标签的关联）。
//
// 全局约定：
//   - 时间戳一律是 Unix 秒（int64），不是毫秒。
//   - 可空列用指针表示，nil 即 NULL；软删实体的 DeletedAt == nil 表示未删除。
//   - 「活数据」查询一律带 deleted_at IS NULL，回收站查询则相反。
//   - 列表方法返回 (rows, total, err)。total 与返回的当页取自同一个读事务
//     （见 query.go 的 readTx），两者不会互相矛盾。
package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"blog-engine/internal/logging"
)

// ErrNotFound 表示文章不存在，或已被软删（两者对外不作区分）。
//
// 名字是历史遗留：它只属于文章。页面、动态、评论、媒体、用户、标签各自有
// ErrPageNotFound / ErrMomentNotFound / … 等错误，调用方不要拿它去判断其它实体。
var ErrNotFound = errors.New("article not found")

// ArticleRepository 是文章的持久化契约。
//
// 软删与回收站语义（本包所有带软删的实体都遵循同一套）：
//   - SoftDelete 给一批 id 打删除标记，返回实际改动的行数。不存在或已是删除态的
//     id 会被静默跳过，因此重复调用是幂等的，且返回行数可能小于入参长度。
//   - Restore 只对当前处于删除态的行生效，否则返回 ErrNotFound。
//   - HardDelete 只删已经处于删除态的行。这是一条硬约束：永久删除无法绕过回收站
//     直接抹掉活数据（调用方必须先 SoftDelete）。
//   - 普通查询一律不返回已软删的行；读回收站请用 ListDeleted。
//
// 所有方法都接收 context，取消/超时由底层驱动向上传播为 error。
type ArticleRepository interface {
	Create(ctx context.Context, article *Article) error
	Update(ctx context.Context, article *Article) error
	SoftDelete(ctx context.Context, ids []int64, deletedAt int64) (int64, error)
	Restore(ctx context.Context, id int64, updatedAt int64) error
	BatchRestore(ctx context.Context, ids []int64, updatedAt int64) (int64, error)
	HardDelete(ctx context.Context, ids []int64) (int64, error)
	// CoverMediaIDsToDelete 给出「把 ids 这几篇永久删掉之后，还有哪些头图媒体
	// 失去了最后一个引用者」。调用方必须在 HardDelete **之前**取这些 id，
	// 之后再去删媒体（理由见实现处的说明）。
	CoverMediaIDsToDelete(ctx context.Context, ids []int64) ([]int64, error)
	GetByID(ctx context.Context, id int64) (*Article, error)
	List(ctx context.Context, p Pagination) ([]Article, int64, error)
	ListDeleted(ctx context.Context, p Pagination) ([]Article, int64, error)
}

// SQLiteArticleRepository 是 ArticleRepository 的 SQLite 实现。
// 除 *sql.DB 外不带任何状态，可被多个 goroutine 共用；实际并发度取决于连接池
// （database.Open 把最大连接数设为 1，因此对 SQLite 的访问是串行的）。
type SQLiteArticleRepository struct {
	db *sql.DB
}

func NewSQLiteArticleRepository(db *sql.DB) *SQLiteArticleRepository {
	return &SQLiteArticleRepository{db: db}
}

func (r *SQLiteArticleRepository) Create(ctx context.Context, article *Article) error {
	logging.Debugf("[article-repo] Create called")
	tx, err := beginTxFn(r.db, ctx)
	if err != nil {
		return fmt.Errorf("begin create transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
INSERT INTO articles (title, content, title_en, content_en, ai_generated, ai_generated_en, created_at, updated_at, cover_media_id, deleted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
`, article.Title, article.Content, article.TitleEN, article.ContentEN,
		boolToInt(article.AIGenerated), boolToInt(article.AIGeneratedEN),
		article.CreatedAt, article.UpdatedAt, nullableID(article.CoverMediaID))
	if err != nil {
		return fmt.Errorf("insert article: %w", err)
	}

	id, err := lastInsertIDFn(res)
	if err != nil {
		return fmt.Errorf("get last insert id: %w", err)
	}
	article.ID = id

	if err := replaceArticleTags(ctx, tx, article.ID, article.Tags); err != nil {
		return err
	}

	if err := commitTxFn(tx); err != nil {
		return fmt.Errorf("commit create article: %w", err)
	}
	return nil
}

// Update 覆盖标题、正文、发布时间与头图，刷新 updated_at，并重建标签关联。
//
// created_at 在这里是**发布时间**，也一并写回。它必须写：服务层是「这次要不要改
// 发布时间」的唯一决策者，它先读出当前行、只在客户端显式给了 published_at 时才
// 覆盖该字段，再把最终值交下来。所以传进来的 CreatedAt 永远是「这一行该有的值」。
//
// 反过来（不写 created_at）并不安全，只是看起来安全：接口会拿内存里的新值回应
// 200，库里却还是旧日期——「修改发布时间」整个功能静默失效，而响应在撒谎。
// 这个坑真踩过，由 service 的 TestUpdatePublishedAtChangesTheSortPosition 守护。
//
// WHERE 带 deleted_at IS NULL：不能修改已在回收站里的文章，这种情况返回 ErrNotFound。
// 标签关联的替换与主表更新在同一事务内，要么全成、要么全不成。
func (r *SQLiteArticleRepository) Update(ctx context.Context, article *Article) error {
	logging.Debugf("[article-repo] Update called")
	tx, err := beginTxFn(r.db, ctx)
	if err != nil {
		return fmt.Errorf("begin update transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
UPDATE articles
SET title = ?, content = ?, title_en = ?, content_en = ?, ai_generated = ?, ai_generated_en = ?,
    created_at = ?, updated_at = ?, cover_media_id = ?
WHERE id = ? AND deleted_at IS NULL
`, article.Title, article.Content, article.TitleEN, article.ContentEN,
		boolToInt(article.AIGenerated), boolToInt(article.AIGeneratedEN),
		article.CreatedAt, article.UpdatedAt, nullableID(article.CoverMediaID), article.ID)
	if err != nil {
		return fmt.Errorf("update article: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}

	if err := replaceArticleTags(ctx, tx, article.ID, article.Tags); err != nil {
		return err
	}

	if err := commitTxFn(tx); err != nil {
		return fmt.Errorf("commit update article: %w", err)
	}
	return nil
}

// SoftDelete 标记删除一批文章，返回实际被标记的行数。
//
// 已是删除态或不存在的 id 会被静默跳过，所以返回值可能小于 len(ids)；ids 为空时
// 直接返回 (0, nil) 而不发查询，重复调用天然幂等。
// 动态拼 IN (?,?,?) 是为了让每个 id 都走参数绑定，避免把 ids 拼进 SQL 造成注入。
func (r *SQLiteArticleRepository) SoftDelete(ctx context.Context, ids []int64, deletedAt int64) (int64, error) {
	logging.Debugf("[article-repo] SoftDelete called")
	if len(ids) == 0 {
		return 0, nil
	}

	placeholders := make([]string, 0, len(ids))
	for range ids {
		placeholders = append(placeholders, "?")
	}

	args := make([]any, 0, len(ids)+2)
	args = append(args, deletedAt, deletedAt)
	for _, id := range ids {
		args = append(args, id)
	}

	query := `
UPDATE articles
SET deleted_at = ?, updated_at = ?
WHERE id IN (` + strings.Join(placeholders, ", ") + `) AND deleted_at IS NULL
`
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("soft delete articles: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

// Restore 把一篇文章从回收站恢复。只对当前处于删除态的行生效；
// 行不存在、或本来就未被删除时返回 ErrNotFound（因此恢复是「有向」操作，不幂等）。
func (r *SQLiteArticleRepository) Restore(ctx context.Context, id int64, updatedAt int64) error {
	logging.Debugf("[article-repo] Restore called")
	res, err := r.db.ExecContext(ctx, `
UPDATE articles
SET deleted_at = NULL, updated_at = ?
WHERE id = ? AND deleted_at IS NOT NULL
`, updatedAt, id)
	if err != nil {
		return fmt.Errorf("restore article: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteArticleRepository) BatchRestore(ctx context.Context, ids []int64, updatedAt int64) (int64, error) {
	logging.Debugf("[article-repo] BatchRestore called")
	if len(ids) == 0 {
		return 0, nil
	}

	placeholders := make([]string, 0, len(ids))
	for range ids {
		placeholders = append(placeholders, "?")
	}

	args := make([]any, 0, len(ids)+1)
	args = append(args, updatedAt)
	for _, id := range ids {
		args = append(args, id)
	}

	query := `
UPDATE articles
SET deleted_at = NULL, updated_at = ?
WHERE id IN (` + strings.Join(placeholders, ", ") + `) AND deleted_at IS NOT NULL
`
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("batch restore articles: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

// HardDelete 永久删除文章，返回实际删除的行数。
//
// 关键不变量：WHERE 带 deleted_at IS NOT NULL——只有回收站里的文章能被永久删除，
// 活文章一律删不掉。这是防止误删的最后一道闸。
//
// article_tags 的关联行不需要手动清理：外键声明了 ON DELETE CASCADE，而
// database.Open 会在启动时校验 foreign_keys pragma 确实为 1（否则拒绝启动），
// 所以级联一定生效，不会留下孤儿关联行。
func (r *SQLiteArticleRepository) HardDelete(ctx context.Context, ids []int64) (int64, error) {
	logging.Debugf("[article-repo] HardDelete called")
	if len(ids) == 0 {
		return 0, nil
	}

	placeholders := make([]string, 0, len(ids))
	for range ids {
		placeholders = append(placeholders, "?")
	}

	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}

	query := `
DELETE FROM articles
WHERE id IN (` + strings.Join(placeholders, ", ") + `) AND deleted_at IS NOT NULL
`
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("hard delete articles: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

// CoverMediaIDsToDelete 返回「把 ids 这几篇永久删掉之后，还有哪些头图媒体
// 失去了最后一个引用者」。
//
// 为什么叫「可以删」而不是「它们的头图」：头图是**指向媒体的一根指针**，
// 不是所有权声明。同一张媒体可能被多行文章指向，也可能被动态配图引用
// （接口不阻止这种用法，前端只是碰巧每条都用新图）。无条件删掉指针指向的媒体
// 会让**别人**的图消失——而且是通过外键的 SET NULL / CASCADE 静默消失，
// 一个与本次操作无关的页面跟着坏掉。所以两个 NOT EXISTS 就是这道闸：
// 还有人指着它（哪怕那篇文章正在回收站里）就一律不删，宁可留下一个没人引用的
// 文件，也不制造一次不可逆的连带损失。
//
// WHERE 里的 deleted_at IS NOT NULL 与 HardDelete 的前置条件**必须一致**：
// 永久删除只作用于回收站里的行，活动文章一篇也删不掉。若这里不加上同样的条件，
// 对一篇活动文章调用本方法会返回它的头图 id，调用方随后就会把图删掉——
// 一次「什么都没删掉」的操作却造成了数据损坏（同类的坑见 moment_repo.HardDelete
// 的注释，那里踩过一模一样的形态）。
//
// 必须在 HardDelete **之前**调用：删完之后这些行的 cover_media_id 就没了，
// 再也问不出「刚才那几篇用的是什么图」。这也决定了调用顺序是
// 「先取 id → 删文章 → 删媒体」——反过来一旦删文章失败，图已经没了。
//
// 关于单连接池：本方法自己读完并关闭 rows 才返回，调用方是在它返回之后才去
// 删文章的，因此不存在「一边遍历 rows 一边写库」的死锁。
func (r *SQLiteArticleRepository) CoverMediaIDsToDelete(ctx context.Context, ids []int64) ([]int64, error) {
	logging.Debugf("[article-repo] CoverMediaIDsToDelete called")
	if len(ids) == 0 {
		return []int64{}, nil
	}

	// ids 在语句里出现两次（一次限定候选行，一次从「幸存者」里排除它们），
	// 因此参数也要绑两遍。动态拼 IN (?,?,?) 是为了让每个 id 走参数绑定。
	placeholders := make([]string, 0, len(ids))
	for range ids {
		placeholders = append(placeholders, "?")
	}
	list := strings.Join(placeholders, ", ")

	args := make([]any, 0, len(ids)*2)
	for _, id := range ids {
		args = append(args, id)
	}
	for _, id := range ids {
		args = append(args, id)
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT DISTINCT a.cover_media_id
FROM articles a
WHERE a.id IN (`+list+`)
  AND a.deleted_at IS NOT NULL
  AND a.cover_media_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM articles other
      WHERE other.cover_media_id = a.cover_media_id
        AND other.id NOT IN (`+list+`)
  )
  AND NOT EXISTS (
      SELECT 1 FROM moment_media mm
      WHERE mm.media_id = a.cover_media_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM users u
      WHERE u.avatar_media_id = a.cover_media_id
  )
ORDER BY a.cover_media_id
`, args...)
	if err != nil {
		return nil, fmt.Errorf("select cover media: %w", err)
	}
	defer rows.Close()

	mediaIDs := make([]int64, 0)
	for rows.Next() {
		var mediaID int64
		if err := scanValuesFn(rows, &mediaID); err != nil {
			return nil, fmt.Errorf("scan cover media id: %w", err)
		}
		mediaIDs = append(mediaIDs, mediaID)
	}
	if err := rowsErrFn(rows); err != nil {
		return nil, fmt.Errorf("iterate cover media: %w", err)
	}
	return mediaIDs, nil
}

// GetByID 读取一篇未删除的文章，并装配其标签。
// 不存在或已软删一律返回 ErrNotFound，调用方无法区分二者。
func (r *SQLiteArticleRepository) GetByID(ctx context.Context, id int64) (*Article, error) {
	logging.Debugf("[article-repo] GetByID called")
	row := r.db.QueryRowContext(ctx, `
SELECT id, title, content, title_en, content_en, ai_generated, ai_generated_en, created_at, updated_at, cover_media_id, deleted_at
FROM articles
WHERE id = ? AND deleted_at IS NULL
`, id)

	article, err := scanArticle(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	tags, tagsEN, err := loadTagsForArticle(ctx, r.db, article.ID)
	if err != nil {
		return nil, err
	}
	article.Tags = tags
	article.TagsEN = tagsEN
	return article, nil
}

// List 返回一页未删除的文章（按创建时间倒序）以及总数。
//
// total 与当页在同一个读事务里取出，因此不会出现「总数与页内容对不上」的
// 情形——并发写入不会插到两次查询之间。这一点由 query.go 的 readTx 保证。
// p 越界会被 Pagination.Normalized() 夹到安全区间（见 pagination.go）。
func (r *SQLiteArticleRepository) List(ctx context.Context, p Pagination) ([]Article, int64, error) {
	logging.Debugf("[article-repo] List called")

	var total int64
	articles := make([]Article, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM articles
WHERE deleted_at IS NULL
`).Scan(&total); err != nil {
			return fmt.Errorf("count articles: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT id, title, content, title_en, content_en, ai_generated, ai_generated_en, created_at, updated_at, cover_media_id, deleted_at
FROM articles
WHERE deleted_at IS NULL
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list articles: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			article, err := scanArticle(rows)
			if err != nil {
				return err
			}
			articles = append(articles, *article)
		}
		if err := rowsErrFn(rows); err != nil {
			return fmt.Errorf("iterate articles: %w", err)
		}

		return loadTagsForArticles(ctx, q, articles)
	})
	if err != nil {
		return nil, 0, err
	}
	return articles, total, nil
}

// ListDeleted 返回一页回收站中的文章，按删除时间倒序（最近删除的排在最前）。
func (r *SQLiteArticleRepository) ListDeleted(ctx context.Context, p Pagination) ([]Article, int64, error) {
	logging.Debugf("[article-repo] ListDeleted called")

	var total int64
	articles := make([]Article, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM articles
WHERE deleted_at IS NOT NULL
`).Scan(&total); err != nil {
			return fmt.Errorf("count deleted articles: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT id, title, content, title_en, content_en, ai_generated, ai_generated_en, created_at, updated_at, cover_media_id, deleted_at
FROM articles
WHERE deleted_at IS NOT NULL
ORDER BY deleted_at DESC, id DESC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list deleted articles: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			article, err := scanArticle(rows)
			if err != nil {
				return err
			}
			articles = append(articles, *article)
		}
		if err := rowsErrFn(rows); err != nil {
			return fmt.Errorf("iterate deleted articles: %w", err)
		}

		return loadTagsForArticles(ctx, q, articles)
	})
	if err != nil {
		return nil, 0, err
	}
	return articles, total, nil
}

// scanner 抽象出 *sql.Row 与 *sql.Rows 的公共方法，
// 让单行查询与多行查询可以复用同一个扫描函数。
type scanner interface {
	Scan(dest ...any) error
}

// scanArticle 把一行结果扫描成 Article。deleted_at 与 cover_media_id 都先走
// sql.NullInt64 再转指针，这样「未删除」与「恰好删除于第 0 秒」、「没有头图」与
// 「头图 id 恰好是 0」不会被混为一谈（id 从 1 开始，但依赖这一点是隐式的）。
func scanArticle(row scanner) (*Article, error) {
	var article Article
	var deletedAt sql.NullInt64
	var coverMediaID sql.NullInt64
	var aiGenerated int
	var aiGeneratedEN int
	if err := scanValuesFn(row,
		&article.ID,
		&article.Title,
		&article.Content,
		&article.TitleEN,
		&article.ContentEN,
		&aiGenerated,
		&aiGeneratedEN,
		&article.CreatedAt,
		&article.UpdatedAt,
		&coverMediaID,
		&deletedAt,
	); err != nil {
		return nil, err
	}

	if deletedAt.Valid {
		value := deletedAt.Int64
		article.DeletedAt = &value
	}
	if coverMediaID.Valid {
		value := coverMediaID.Int64
		article.CoverMediaID = &value
	}
	article.AIGenerated = aiGenerated != 0
	article.AIGeneratedEN = aiGeneratedEN != 0

	return &article, nil
}

// nullableID 把可空的 id 转成 database/sql 认得的驱动值：nil → NULL。
//
// 直接传 *int64 给 ExecContext 是行不通的——database/sql 的默认转换器只认
// nil 与实现了 driver.Valuer 的类型，一个非 nil 的 *int64 会得到
// "unsupported type" 错误。这是本项目里唯一的「指针 ↔ NULL」转换点，
// 所有可空 id 的写入都应当经过它。
func nullableID(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

// replaceArticleTags 用「先全删、再逐条重插」把文章的标签关联替换为 tags。
//
// 为什么是全删重插而不是差量对比：调用方传入的 tags 是这篇文章期望的标签全集
// （而非增量），全删重插天然幂等，也省去求差集的复杂度。
// 标签本身用 INSERT OR IGNORE 隐式创建——本项目没有独立的「新建标签」入口；
// 随后按 name COLLATE NOCASE 取回 id，这样大小写不同但实为同一标签时也能命中
// （tags.name 在 schema 里就是 NOCASE UNIQUE）。
// 必须在调用方的事务内执行，否则中途失败会留下一半关联。
func replaceArticleTags(ctx context.Context, tx *sql.Tx, articleID int64, tags []string) error {
	if _, err := tx.ExecContext(ctx, `
DELETE FROM article_tags
WHERE article_id = ?
`, articleID); err != nil {
		return fmt.Errorf("delete article tags: %w", err)
	}

	for _, name := range tags {
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO tags (name)
VALUES (?)
`, name); err != nil {
			return fmt.Errorf("insert tag: %w", err)
		}

		var tagID int64
		if err := tx.QueryRowContext(ctx, `
SELECT id
FROM tags
WHERE name = ? COLLATE NOCASE
`, name).Scan(&tagID); err != nil {
			return fmt.Errorf("select tag id: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO article_tags (article_id, tag_id)
VALUES (?, ?)
`, articleID, tagID); err != nil {
			return fmt.Errorf("link article tag: %w", err)
		}
	}
	return nil
}

// loadTagsForArticle 读取一篇文章的标签名（中文名与英文名各一份），按名称排序。
// 两个切片按索引对应；英文名为空串表示该标签没有英文译名。
func loadTagsForArticle(ctx context.Context, q queryRunner, articleID int64) ([]string, []string, error) {
	rows, err := q.QueryContext(ctx, `
SELECT t.name, t.name_en
FROM tags t
JOIN article_tags at ON at.tag_id = t.id
WHERE at.article_id = ?
ORDER BY t.name COLLATE NOCASE
`, articleID)
	if err != nil {
		return nil, nil, fmt.Errorf("list article tags: %w", err)
	}
	defer rows.Close()

	tags := make([]string, 0)
	tagsEN := make([]string, 0)
	for rows.Next() {
		var name, nameEN string
		if err := scanValuesFn(rows, &name, &nameEN); err != nil {
			return nil, nil, fmt.Errorf("scan article tag: %w", err)
		}
		tags = append(tags, name)
		tagsEN = append(tagsEN, nameEN)
	}
	if err := rowsErrFn(rows); err != nil {
		return nil, nil, fmt.Errorf("iterate article tags: %w", err)
	}
	return tags, tagsEN, nil
}

// loadTagsForArticles 为一批文章装配标签，**只用一次查询**。
//
// 这里刻意不逐篇查一次：那是典型的 N+1，一页 12 篇就是 12 次数据库往返，
// 而每篇的标签通常只有几个。改成一次 IN 查询、再在内存里按 article_id 分组后，
// 往返次数与页大小无关。
//
// 调用方传入的 articles 必须有互不相同的 ID（分页查询天然如此）——若同一个 ID
// 出现两次，两处会共享同一个切片。这一点无实际影响，因为两者内容本就相同。
//
// 没有标签的文章拿到的是**非 nil 的空切片**而不是 nil：Tags 会被直接序列化成
// JSON，nil 会渲染成 null，空切片才是 []。
func loadTagsForArticles(ctx context.Context, q queryRunner, articles []Article) error {
	if len(articles) == 0 {
		return nil
	}

	// 动态拼 IN (?,?,?) 是为了让每个 id 都走参数绑定，避免把 ids 拼进 SQL 造成注入。
	placeholders := make([]string, 0, len(articles))
	args := make([]any, 0, len(articles))
	for i := range articles {
		placeholders = append(placeholders, "?")
		args = append(args, articles[i].ID)
	}

	rows, err := q.QueryContext(ctx, `
SELECT at.article_id, t.name, t.name_en
FROM article_tags at
JOIN tags t ON t.id = at.tag_id
WHERE at.article_id IN (`+strings.Join(placeholders, ", ")+`)
ORDER BY at.article_id ASC, t.name COLLATE NOCASE
`, args...)
	if err != nil {
		return fmt.Errorf("list article tags: %w", err)
	}
	defer rows.Close()

	byArticle := make(map[int64][]string, len(articles))
	byArticleEN := make(map[int64][]string, len(articles))
	for rows.Next() {
		var articleID int64
		var name, nameEN string
		if err := scanValuesFn(rows, &articleID, &name, &nameEN); err != nil {
			return fmt.Errorf("scan article tag: %w", err)
		}
		byArticle[articleID] = append(byArticle[articleID], name)
		byArticleEN[articleID] = append(byArticleEN[articleID], nameEN)
	}
	if err := rowsErrFn(rows); err != nil {
		return fmt.Errorf("iterate article tags: %w", err)
	}

	// 分组内已按名称排序（见上面的 ORDER BY），因此无需再排一次。
	for i := range articles {
		tags, ok := byArticle[articles[i].ID]
		if !ok {
			tags = []string{}
		}
		articles[i].Tags = tags
		tagsEN, ok := byArticleEN[articles[i].ID]
		if !ok {
			tagsEN = []string{}
		}
		articles[i].TagsEN = tagsEN
	}
	return nil
}
