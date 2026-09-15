package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"blog-engine/internal/logging"
)

// TagRepository 是标签的「只读 + 改名」契约。
//
// 刻意没有 Create 与 Delete：标签由文章正文里的标签名隐式产生，删除文章只解除关联。
// 一旦提供「删标签」，就必须定义「还有文章在引用它时怎么办」，那是个产品决策，
// 目前不做。唯一允许的改动是改名。
type TagRepository interface {
	List(ctx context.Context, p Pagination) ([]Tag, int64, error)
	GetByID(ctx context.Context, id int64) (*Tag, error)
	ListArticlesByTag(ctx context.Context, tagID int64, p Pagination) ([]Article, int64, error)
	CountByTag(ctx context.Context, tagID int64) (int64, error)
	Rename(ctx context.Context, tagID int64, newName string) error
}

type SQLiteTagRepository struct {
	db *sql.DB
}

func NewSQLiteTagRepository(db *sql.DB) *SQLiteTagRepository {
	return &SQLiteTagRepository{db: db}
}

// List 返回一页标签，并带上「引用它的未删除文章数」。
//
// 用两次 LEFT JOIN 而非子查询，是为了让计数和分页在同一条 SQL 里完成。
// 注意 JOIN articles 的条件里带了 a.deleted_at IS NULL，
// 因此回收站里的文章不计入 ArticleCount。
func (r *SQLiteTagRepository) List(ctx context.Context, p Pagination) ([]Tag, int64, error) {
	logging.Debugf("[tag-repo] List called")

	var total int64
	tags := make([]Tag, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM tags
`).Scan(&total); err != nil {
			return fmt.Errorf("count tags: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT t.id, t.name, COUNT(a.id)
FROM tags t
LEFT JOIN article_tags at ON at.tag_id = t.id
LEFT JOIN articles a ON a.id = at.article_id AND a.deleted_at IS NULL
GROUP BY t.id, t.name
ORDER BY t.name COLLATE NOCASE
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list tags: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var tag Tag
			if err := rows.Scan(&tag.ID, &tag.Name, &tag.ArticleCount); err != nil {
				return fmt.Errorf("scan tag: %w", err)
			}
			tags = append(tags, tag)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate tags: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return tags, total, nil
}

func (r *SQLiteTagRepository) GetByID(ctx context.Context, id int64) (*Tag, error) {
	logging.Debugf("[tag-repo] GetByID called")
	return getTagByID(ctx, r.db, id)
}

// getTagByID 是 GetByID 的实现，接收 queryRunner 而非 *sql.DB，
// 好让 ListArticlesByTag 能把这次存在性检查也放进自己的读事务里。
func getTagByID(ctx context.Context, q queryRunner, id int64) (*Tag, error) {
	var tag Tag
	err := q.QueryRowContext(ctx, `
SELECT t.id, t.name, COUNT(a.id)
FROM tags t
LEFT JOIN article_tags at ON at.tag_id = t.id
LEFT JOIN articles a ON a.id = at.article_id AND a.deleted_at IS NULL
WHERE t.id = ?
GROUP BY t.id, t.name
`, id).Scan(&tag.ID, &tag.Name, &tag.ArticleCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTagNotFound
		}
		return nil, fmt.Errorf("get tag by id: %w", err)
	}
	return &tag, nil
}

// ListArticlesByTag 返回该标签下未删除的文章及其总数。
// 开头先做一次标签存在性检查，纯粹是为了让「标签不存在」返回 ErrTagNotFound，
// 多一次查询换取清晰的 404 语义。检查、计数与取页都在同一个读事务里完成。
func (r *SQLiteTagRepository) ListArticlesByTag(ctx context.Context, tagID int64, p Pagination) ([]Article, int64, error) {
	logging.Debugf("[tag-repo] ListArticlesByTag called")

	var total int64
	articles := make([]Article, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if _, err := getTagByID(ctx, q, tagID); err != nil {
			return err
		}

		if err := q.QueryRowContext(ctx, `
SELECT COUNT(a.id)
FROM article_tags at
JOIN articles a ON a.id = at.article_id AND a.deleted_at IS NULL
WHERE at.tag_id = ?
`, tagID).Scan(&total); err != nil {
			return fmt.Errorf("count articles by tag: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT a.id, a.title, a.content, a.created_at, a.updated_at, a.deleted_at
FROM articles a
JOIN article_tags at ON at.article_id = a.id
WHERE at.tag_id = ? AND a.deleted_at IS NULL
ORDER BY a.created_at DESC, a.id DESC
LIMIT ? OFFSET ?
`, tagID, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list articles by tag: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			article, err := scanArticle(rows)
			if err != nil {
				return err
			}
			articles = append(articles, *article)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate articles by tag: %w", err)
		}

		return loadTagsForArticles(ctx, q, articles)
	})
	if err != nil {
		return nil, 0, err
	}
	return articles, total, nil
}

// CountByTag 返回该标签下未删除的文章数，同样先做一次存在性检查。
func (r *SQLiteTagRepository) CountByTag(ctx context.Context, tagID int64) (int64, error) {
	logging.Debugf("[tag-repo] CountByTag called")
	if _, err := r.GetByID(ctx, tagID); err != nil {
		return 0, err
	}

	var count int64
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(a.id)
FROM article_tags at
JOIN articles a ON a.id = at.article_id AND a.deleted_at IS NULL
WHERE at.tag_id = ?
`, tagID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count articles by tag: %w", err)
	}
	return count, nil
}

// Rename 修改标签名。有三点值得注意：
//
//  1. 重名检查带 COLLATE NOCASE，与表上的 UNIQUE 约束口径一致；
//  2. 改名只需更新 tags 一行——article_tags 关联的是 tag_id，所以「改名会立刻
//     反映到所有引用它的文章上」是天然成立的，不需要批量更新关联表；
//  3. 检查与更新之间没有包事务，存在竞态窗口：两个并发改名理论上可能互相覆盖
//     或绕过重名检查。单连接池（SetMaxOpenConns(1)）把窗口压得很小，但并非不存在。
func (r *SQLiteTagRepository) Rename(ctx context.Context, tagID int64, newName string) error {
	logging.Debugf("[tag-repo] Rename called")
	var currentName string
	err := r.db.QueryRowContext(ctx, `
SELECT name
FROM tags
WHERE id = ?
`, tagID).Scan(&currentName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTagNotFound
		}
		return fmt.Errorf("get tag name: %w", err)
	}

	var existingID int64
	err = r.db.QueryRowContext(ctx, `
SELECT id
FROM tags
WHERE name = ? COLLATE NOCASE AND id <> ?
LIMIT 1
`, newName, tagID).Scan(&existingID)
	if err == nil {
		return ErrTagNameExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check duplicate tag name: %w", err)
	}

	if _, err := r.db.ExecContext(ctx, `
UPDATE tags
SET name = ?
WHERE id = ?
`, newName, tagID); err != nil {
		return fmt.Errorf("rename tag: %w", err)
	}
	return nil
}
