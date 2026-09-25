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
	UpdateStorage(ctx context.Context, id int64, storageName, mimeType string, size int64, converted bool) error
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

// UpdateStorage 就地改写一条媒体记录的落盘信息（storage_name / mime_type / size /
// converted 四列）。
//
// 它存在的理由见 upgrade-media 子命令：把历史上的原格式图片重新编码为 WebP 时
// **不新建记录**，而是改写这一条。id 保持不变，于是所有指向它的引用
// （articles.cover_media_id、users.avatar_media_id、moment_media.media_id）一个都
// 不用动——这正是选择「就地更新」而不是「新建一条再改引用」的全部原因：后者要在
// 一个事务里改三张表，漏改任何一处都会留下指向旧图的引用（而且它看起来很正常，
// 直到有人删掉那条旧记录、外键把引用置空为止）。
//
// 只动这四列是有意的：original_name 是用户上传时的原始名（留档用，不随转码改变），
// media_type 与 created_at 也与本次转码无关。
//
// 找不到行时返回 ErrMediaNotFound 而不是静默成功：静默成功会让调用方以为图片已经
// 登记在案，随后把它当成「已升级」。
func (r *SQLiteMediaRepository) UpdateStorage(ctx context.Context, id int64, storageName, mimeType string, size int64, converted bool) error {
	logging.Debugf("[media-repo] UpdateStorage called id=%d", id)
	flag := 0
	if converted {
		flag = 1
	}

	res, err := r.db.ExecContext(ctx, `
UPDATE media
SET storage_name = ?, mime_type = ?, size = ?, converted = ?
WHERE id = ? AND deleted_at IS NULL
`, storageName, mimeType, size, flag, id)
	if err != nil {
		return fmt.Errorf("update media storage: %w", err)
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

// ListImagesForWebPUpgrade 列出媒体库里所有的图片记录，供 upgrade-media 逐条
// 重新编码为 WebP。
//
// 为什么一次全读而不是分页：需要升级的是**存量**（通常是几十到几千行），而分页会
// 给「边遍历边改写」多加一层偏移量失效的风险——升级过程中 mime_type 会变，
// 于是下一页的边界会滑动，可能漏掉或重复处理某些行。一次读进内存没有这个问题。
//
// 只读、且在返回前就把 rows 关掉：单连接池下「遍历 rows 的同时写库」是静默死锁，
// 所以调用方必须在**拿完整个切片之后**才开始写（upgrade-media 正是这个顺序）。
// 排序按 id 升序，让中断后的重跑容易与日志对照。
func (r *SQLiteMediaRepository) ListImagesForWebPUpgrade(ctx context.Context) ([]Media, error) {
	logging.Debugf("[media-repo] ListImagesForWebPUpgrade called")
	rows, err := r.db.QueryContext(ctx, `
SELECT id, storage_name, original_name, media_type, mime_type, size, converted, created_at, deleted_at
FROM media
WHERE deleted_at IS NULL AND media_type = ?
ORDER BY id
`, MediaTypeImage)
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	defer rows.Close()

	images := make([]Media, 0)
	for rows.Next() {
		media, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		images = append(images, *media)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate images: %w", err)
	}
	return images, nil
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
