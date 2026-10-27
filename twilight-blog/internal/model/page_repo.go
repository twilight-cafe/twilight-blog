package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"blog-engine/internal/logging"
)

// PageRepository 是独立页面的持久化契约，软删/回收站语义与 ArticleRepository 一致
// （SoftDelete 幂等并返回实际改动行数；Restore 只对已删除行生效，否则 ErrPageNotFound；
// HardDelete 只删回收站里的行）。页面没有关联表，因此写入路径不需要事务。
type PageRepository interface {
	Create(ctx context.Context, page *Page) error
	Update(ctx context.Context, page *Page) error
	SoftDelete(ctx context.Context, ids []int64, deletedAt int64) (int64, error)
	Restore(ctx context.Context, id int64, updatedAt int64) error
	BatchRestore(ctx context.Context, ids []int64, updatedAt int64) (int64, error)
	HardDelete(ctx context.Context, ids []int64) (int64, error)
	GetByID(ctx context.Context, id int64) (*Page, error)
	List(ctx context.Context, p Pagination) ([]Page, int64, error)
	ListDeleted(ctx context.Context, p Pagination) ([]Page, int64, error)
}

type SQLitePageRepository struct {
	db *sql.DB
}

func NewSQLitePageRepository(db *sql.DB) *SQLitePageRepository {
	return &SQLitePageRepository{db: db}
}

func (r *SQLitePageRepository) Create(ctx context.Context, page *Page) error {
	logging.Debugf("[page-repo] Create called")
	res, err := r.db.ExecContext(ctx, `
INSERT INTO pages (title, content, title_en, content_en, created_at, updated_at, deleted_at)
VALUES (?, ?, ?, ?, ?, ?, NULL)
`, page.Title, page.Content, page.TitleEN, page.ContentEN, page.CreatedAt, page.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert page: %w", err)
	}

	id, err := lastInsertIDFn(res)
	if err != nil {
		return fmt.Errorf("get last insert id: %w", err)
	}
	page.ID = id
	return nil
}

func (r *SQLitePageRepository) Update(ctx context.Context, page *Page) error {
	logging.Debugf("[page-repo] Update called")
	res, err := r.db.ExecContext(ctx, `
UPDATE pages
SET title = ?, content = ?, title_en = ?, content_en = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL
`, page.Title, page.Content, page.TitleEN, page.ContentEN, page.UpdatedAt, page.ID)
	if err != nil {
		return fmt.Errorf("update page: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrPageNotFound
	}
	return nil
}

func (r *SQLitePageRepository) SoftDelete(ctx context.Context, ids []int64, deletedAt int64) (int64, error) {
	logging.Debugf("[page-repo] SoftDelete called")
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
UPDATE pages
SET deleted_at = ?, updated_at = ?
WHERE id IN (` + strings.Join(placeholders, ", ") + `) AND deleted_at IS NULL
`
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("soft delete pages: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (r *SQLitePageRepository) Restore(ctx context.Context, id int64, updatedAt int64) error {
	logging.Debugf("[page-repo] Restore called")
	res, err := r.db.ExecContext(ctx, `
UPDATE pages
SET deleted_at = NULL, updated_at = ?
WHERE id = ? AND deleted_at IS NOT NULL
`, updatedAt, id)
	if err != nil {
		return fmt.Errorf("restore page: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrPageNotFound
	}
	return nil
}

func (r *SQLitePageRepository) BatchRestore(ctx context.Context, ids []int64, updatedAt int64) (int64, error) {
	logging.Debugf("[page-repo] BatchRestore called")
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
UPDATE pages
SET deleted_at = NULL, updated_at = ?
WHERE id IN (` + strings.Join(placeholders, ", ") + `) AND deleted_at IS NOT NULL
`
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("batch restore pages: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (r *SQLitePageRepository) HardDelete(ctx context.Context, ids []int64) (int64, error) {
	logging.Debugf("[page-repo] HardDelete called")
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
DELETE FROM pages
WHERE id IN (` + strings.Join(placeholders, ", ") + `) AND deleted_at IS NOT NULL
`
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("hard delete pages: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (r *SQLitePageRepository) GetByID(ctx context.Context, id int64) (*Page, error) {
	logging.Debugf("[page-repo] GetByID called")
	row := r.db.QueryRowContext(ctx, `
SELECT id, title, content, title_en, content_en, created_at, updated_at, deleted_at
FROM pages
WHERE id = ? AND deleted_at IS NULL
`, id)

	page, err := scanPage(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPageNotFound
		}
		return nil, err
	}
	return page, nil
}

// List 返回一页未删除的页面。
//
// 排序是 created_at 升序——与文章、动态的「最新在前」相反。这是有意的：
// 页面（关于、友链…）通常按人工维护的先后顺序讲解或导航，而不是按时间倒排。
// total 与当页在同一个读事务里取出（见 query.go 的 readTx），两者不会不一致。
func (r *SQLitePageRepository) List(ctx context.Context, p Pagination) ([]Page, int64, error) {
	logging.Debugf("[page-repo] List called")

	var total int64
	pages := make([]Page, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM pages
WHERE deleted_at IS NULL
`).Scan(&total); err != nil {
			return fmt.Errorf("count pages: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT id, title, content, title_en, content_en, created_at, updated_at, deleted_at
FROM pages
WHERE deleted_at IS NULL
ORDER BY created_at ASC, id ASC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list pages: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			page, err := scanPage(rows)
			if err != nil {
				return err
			}
			pages = append(pages, *page)
		}
		if err := rowsErrFn(rows); err != nil {
			return fmt.Errorf("iterate pages: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return pages, total, nil
}

func (r *SQLitePageRepository) ListDeleted(ctx context.Context, p Pagination) ([]Page, int64, error) {
	logging.Debugf("[page-repo] ListDeleted called")

	var total int64
	pages := make([]Page, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM pages
WHERE deleted_at IS NOT NULL
`).Scan(&total); err != nil {
			return fmt.Errorf("count deleted pages: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT id, title, content, title_en, content_en, created_at, updated_at, deleted_at
FROM pages
WHERE deleted_at IS NOT NULL
ORDER BY deleted_at DESC, id DESC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list deleted pages: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			page, err := scanPage(rows)
			if err != nil {
				return err
			}
			pages = append(pages, *page)
		}
		if err := rowsErrFn(rows); err != nil {
			return fmt.Errorf("iterate deleted pages: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return pages, total, nil
}

func scanPage(row scanner) (*Page, error) {
	var page Page
	var deletedAt sql.NullInt64
	if err := scanValuesFn(row,
		&page.ID,
		&page.Title,
		&page.Content,
		&page.TitleEN,
		&page.ContentEN,
		&page.CreatedAt,
		&page.UpdatedAt,
		&deletedAt,
	); err != nil {
		return nil, err
	}

	if deletedAt.Valid {
		value := deletedAt.Int64
		page.DeletedAt = &value
	}

	return &page, nil
}
