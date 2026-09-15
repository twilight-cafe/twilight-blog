package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"blog-engine/internal/logging"
)

// MediaRepository 是媒体记录的持久化契约。
//
// 与文章/页面/动态不同，媒体目前只有 Delete（硬删），没有软删与回收站：
// 表上虽有 deleted_at 列、Media 结构体也有 DeletedAt 字段，但本仓库没有任何
// 写路径会设置它，而查询却一律带 deleted_at IS NULL。也就是说「软删」这套目前
// 只是预留，真正生效的只有硬删。改这块之前先想清楚磁盘文件的生命周期。
type MediaRepository interface {
	Create(ctx context.Context, media *Media) error
	GetByID(ctx context.Context, id int64) (*Media, error)
	List(ctx context.Context, p Pagination) ([]Media, int64, error)
	Delete(ctx context.Context, id int64) error
}

type SQLiteMediaRepository struct {
	db *sql.DB
}

func NewSQLiteMediaRepository(db *sql.DB) *SQLiteMediaRepository {
	return &SQLiteMediaRepository{db: db}
}

// Create 插入媒体记录并回填 ID。Converted 是 bool，落库时显式转成 0/1。
func (r *SQLiteMediaRepository) Create(ctx context.Context, media *Media) error {
	logging.Debugf("[media-repo] Create called")
	converted := 0
	if media.Converted {
		converted = 1
	}

	res, err := r.db.ExecContext(ctx, `
INSERT INTO media (storage_name, original_name, media_type, mime_type, size, converted, created_at, deleted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, NULL)
`, media.StorageName, media.OriginalName, media.MediaType, media.MimeType, media.Size, converted, media.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert media: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("get last insert id: %w", err)
	}
	media.ID = id
	return nil
}

func (r *SQLiteMediaRepository) GetByID(ctx context.Context, id int64) (*Media, error) {
	logging.Debugf("[media-repo] GetByID called")
	row := r.db.QueryRowContext(ctx, `
SELECT id, storage_name, original_name, media_type, mime_type, size, converted, created_at, deleted_at
FROM media
WHERE id = ? AND deleted_at IS NULL
`, id)

	media, err := scanMedia(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMediaNotFound
		}
		return nil, err
	}
	return media, nil
}

func (r *SQLiteMediaRepository) List(ctx context.Context, p Pagination) ([]Media, int64, error) {
	logging.Debugf("[media-repo] List called")

	var total int64
	mediaList := make([]Media, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM media
WHERE deleted_at IS NULL
`).Scan(&total); err != nil {
			return fmt.Errorf("count media: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT id, storage_name, original_name, media_type, mime_type, size, converted, created_at, deleted_at
FROM media
WHERE deleted_at IS NULL
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list media: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			media, err := scanMedia(rows)
			if err != nil {
				return err
			}
			mediaList = append(mediaList, *media)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate media: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return mediaList, total, nil
}

// Delete 硬删除一条媒体记录（不可恢复）。
//
// 只删数据库行，不动磁盘文件——文件清理由服务层负责（见 media_service）。
// 删除会经外键级联清掉 moment_media 里的关联行，所以引用该媒体的动态会少一张图，
// 而不是整条动态消失。
func (r *SQLiteMediaRepository) Delete(ctx context.Context, id int64) error {
	logging.Debugf("[media-repo] Delete called")
	res, err := r.db.ExecContext(ctx, `
DELETE FROM media
WHERE id = ?
`, id)
	if err != nil {
		return fmt.Errorf("delete media: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrMediaNotFound
	}
	return nil
}

// mediaRow 保存一行 media 结果里需要后处理的中间值。
//
// 把它和列清单拆出来，是为了让「media 各列 + 自己的附加列」的查询能复用同一份
// 扫描逻辑：loadMediaForMoments 要在 media 各列之前多带一个 moment_id，
// 若没有这个抽象就只能复制一遍列清单，而列顺序是与 schema 绑定的——
// 复制一份就多一个失配点。
type mediaRow struct {
	converted int
	deletedAt sql.NullInt64
}

// dests 返回与 scanMedia 所期待的顺序完全一致的扫描目标。
func (r *mediaRow) dests(media *Media) []any {
	return []any{
		&media.ID,
		&media.StorageName,
		&media.OriginalName,
		&media.MediaType,
		&media.MimeType,
		&media.Size,
		&r.converted,
		&media.CreatedAt,
		&r.deletedAt,
	}
}

// apply 把中间值转换到 Media 上。
func (r *mediaRow) apply(media *Media) {
	media.Converted = r.converted != 0
	if r.deletedAt.Valid {
		value := r.deletedAt.Int64
		media.DeletedAt = &value
	}
}

// scanMedia 把一行扫描成 Media。converted 在库里是 0/1 整数，这里显式转 bool，
// 避免把「0」当成结构体里的假值语义来猜。
func scanMedia(row scanner) (*Media, error) {
	var media Media
	var raw mediaRow
	if err := row.Scan(raw.dests(&media)...); err != nil {
		return nil, err
	}
	raw.apply(&media)
	return &media, nil
}
