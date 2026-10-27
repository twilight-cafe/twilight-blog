package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"blog-engine/internal/logging"
)

// MomentRepository 是「动态」的持久化契约，软删/回收站语义与 ArticleRepository
// 完全一致：SoftDelete 幂等且返回实际改动行数；Restore 只对已删除的行生效，
// 否则返回 ErrMomentNotFound；HardDelete 只删回收站里的行。
// 差异只在关联对象是媒体而不是标签。
type MomentRepository interface {
	Create(ctx context.Context, moment *Moment) error
	Update(ctx context.Context, moment *Moment) error
	SoftDelete(ctx context.Context, ids []int64, deletedAt int64) (int64, error)
	Restore(ctx context.Context, id int64, updatedAt int64) error
	BatchRestore(ctx context.Context, ids []int64, updatedAt int64) (int64, error)
	HardDelete(ctx context.Context, ids []int64) (int64, error)
	GetByID(ctx context.Context, id int64) (*Moment, error)
	List(ctx context.Context, p Pagination) ([]Moment, int64, error)
	ListDeleted(ctx context.Context, p Pagination) ([]Moment, int64, error)
}

type SQLiteMomentRepository struct {
	db *sql.DB
}

func NewSQLiteMomentRepository(db *sql.DB) *SQLiteMomentRepository {
	return &SQLiteMomentRepository{db: db}
}

func (r *SQLiteMomentRepository) Create(ctx context.Context, moment *Moment) error {
	logging.Debugf("[moment-repo] Create called")
	tx, err := beginTxFn(r.db, ctx)
	if err != nil {
		return fmt.Errorf("begin create moment transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
INSERT INTO moments (content, created_at, updated_at, deleted_at)
VALUES (?, ?, ?, NULL)
`, moment.Content, moment.CreatedAt, moment.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert moment: %w", err)
	}

	id, err := lastInsertIDFn(res)
	if err != nil {
		return fmt.Errorf("get last insert id: %w", err)
	}
	moment.ID = id

	if err := replaceMomentMedia(ctx, tx, moment.ID, moment.MediaIDs); err != nil {
		return err
	}

	if err := commitTxFn(tx); err != nil {
		return fmt.Errorf("commit create moment: %w", err)
	}
	return nil
}

// Update 覆盖正文、刷新 updated_at，并在同一事务内重建与媒体的关联。
// WHERE 带 deleted_at IS NULL：回收站里的动态不可修改，这种情况返回 ErrMomentNotFound。
func (r *SQLiteMomentRepository) Update(ctx context.Context, moment *Moment) error {
	logging.Debugf("[moment-repo] Update called")
	tx, err := beginTxFn(r.db, ctx)
	if err != nil {
		return fmt.Errorf("begin update moment transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
UPDATE moments
SET content = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL
`, moment.Content, moment.UpdatedAt, moment.ID)
	if err != nil {
		return fmt.Errorf("update moment: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrMomentNotFound
	}

	if err := replaceMomentMedia(ctx, tx, moment.ID, moment.MediaIDs); err != nil {
		return err
	}

	if err := commitTxFn(tx); err != nil {
		return fmt.Errorf("commit update moment: %w", err)
	}
	return nil
}

func (r *SQLiteMomentRepository) SoftDelete(ctx context.Context, ids []int64, deletedAt int64) (int64, error) {
	logging.Debugf("[moment-repo] SoftDelete called")
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
UPDATE moments
SET deleted_at = ?, updated_at = ?
WHERE id IN (` + strings.Join(placeholders, ", ") + `) AND deleted_at IS NULL
`
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("soft delete moments: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (r *SQLiteMomentRepository) Restore(ctx context.Context, id int64, updatedAt int64) error {
	logging.Debugf("[moment-repo] Restore called")
	res, err := r.db.ExecContext(ctx, `
UPDATE moments
SET deleted_at = NULL, updated_at = ?
WHERE id = ? AND deleted_at IS NOT NULL
`, updatedAt, id)
	if err != nil {
		return fmt.Errorf("restore moment: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrMomentNotFound
	}
	return nil
}

func (r *SQLiteMomentRepository) BatchRestore(ctx context.Context, ids []int64, updatedAt int64) (int64, error) {
	logging.Debugf("[moment-repo] BatchRestore called")
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
UPDATE moments
SET deleted_at = NULL, updated_at = ?
WHERE id IN (` + strings.Join(placeholders, ", ") + `) AND deleted_at IS NOT NULL
`
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("batch restore moments: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

// HardDelete 永久删除动态，返回实际删除的行数，只作用于 deleted_at IS NOT NULL
// 的行（活动态删不掉，返回 0）。
//
// moment_media 的关联行由外键 ON DELETE CASCADE 自动清理，这里不需要显式删——
// database.Open 会在启动时校验 foreign_keys pragma 确实为 1（否则拒绝启动），
// 所以级联一定生效。文章侧的同名方法也是同样的写法。
//
// 早先这里有一句显式的 DELETE FROM moment_media，它是错的：那句没有 deleted_at
// 条件，因此对一条**未软删**的动态调用本方法时，动态本身删不掉（0 行），
// 但它的配图关联已经被清空了——一次「没删掉任何东西」的调用却造成了数据损坏。
// 交回给级联就没有这个问题：主体删不掉，关联自然也不会动。
func (r *SQLiteMomentRepository) HardDelete(ctx context.Context, ids []int64) (int64, error) {
	logging.Debugf("[moment-repo] HardDelete called")
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

	res, err := r.db.ExecContext(ctx, `
DELETE FROM moments
WHERE id IN (`+strings.Join(placeholders, ", ")+`) AND deleted_at IS NOT NULL
`, args...)
	if err != nil {
		return 0, fmt.Errorf("hard delete moments: %w", err)
	}

	n, err := rowsAffectedFn(res)
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (r *SQLiteMomentRepository) GetByID(ctx context.Context, id int64) (*Moment, error) {
	logging.Debugf("[moment-repo] GetByID called")
	row := r.db.QueryRowContext(ctx, `
SELECT id, content, created_at, updated_at, deleted_at
FROM moments
WHERE id = ? AND deleted_at IS NULL
`, id)

	moment, err := scanMoment(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMomentNotFound
		}
		return nil, err
	}

	media, err := loadMediaForMoment(ctx, r.db, moment.ID)
	if err != nil {
		return nil, err
	}
	moment.Media = media
	moment.MediaIDs = mediaIDsFromMedia(media)
	return moment, nil
}

func (r *SQLiteMomentRepository) List(ctx context.Context, p Pagination) ([]Moment, int64, error) {
	logging.Debugf("[moment-repo] List called")

	var total int64
	moments := make([]Moment, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM moments
WHERE deleted_at IS NULL
`).Scan(&total); err != nil {
			return fmt.Errorf("count moments: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT id, content, created_at, updated_at, deleted_at
FROM moments
WHERE deleted_at IS NULL
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list moments: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			moment, err := scanMoment(rows)
			if err != nil {
				return err
			}
			moments = append(moments, *moment)
		}
		if err := rowsErrFn(rows); err != nil {
			return fmt.Errorf("iterate moments: %w", err)
		}

		return loadMediaForMoments(ctx, q, moments)
	})
	if err != nil {
		return nil, 0, err
	}
	return moments, total, nil
}

func (r *SQLiteMomentRepository) ListDeleted(ctx context.Context, p Pagination) ([]Moment, int64, error) {
	logging.Debugf("[moment-repo] ListDeleted called")

	var total int64
	moments := make([]Moment, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM moments
WHERE deleted_at IS NOT NULL
`).Scan(&total); err != nil {
			return fmt.Errorf("count deleted moments: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT id, content, created_at, updated_at, deleted_at
FROM moments
WHERE deleted_at IS NOT NULL
ORDER BY deleted_at DESC, id DESC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list deleted moments: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			moment, err := scanMoment(rows)
			if err != nil {
				return err
			}
			moments = append(moments, *moment)
		}
		if err := rowsErrFn(rows); err != nil {
			return fmt.Errorf("iterate deleted moments: %w", err)
		}

		return loadMediaForMoments(ctx, q, moments)
	})
	if err != nil {
		return nil, 0, err
	}
	return moments, total, nil
}

func scanMoment(row scanner) (*Moment, error) {
	var moment Moment
	var deletedAt sql.NullInt64
	if err := scanValuesFn(row,
		&moment.ID,
		&moment.Content,
		&moment.CreatedAt,
		&moment.UpdatedAt,
		&deletedAt,
	); err != nil {
		return nil, err
	}

	if deletedAt.Valid {
		value := deletedAt.Int64
		moment.DeletedAt = &value
	}

	return &moment, nil
}

// replaceMomentMedia 用「先全删、再逐条重插」把动态的媒体关联替换为 mediaIDs。
// 与 replaceArticleTags 同构：入参是期望的媒体全集，而非增量，因此天然幂等。
//
// 时间戳取自 time.Now() 而不是调用方传入：关联行的 created_at 记的是「本次写入时刻」，
// 与动态自身的 CreatedAt 无关。这个字段决定了配图的展示顺序（见 loadMediaForMoment）。
func replaceMomentMedia(ctx context.Context, tx *sql.Tx, momentID int64, mediaIDs []int64) error {
	if _, err := tx.ExecContext(ctx, `
DELETE FROM moment_media
WHERE moment_id = ?
`, momentID); err != nil {
		return fmt.Errorf("delete moment media links: %w", err)
	}

	for _, mediaID := range mediaIDs {
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO moment_media (moment_id, media_id, created_at)
VALUES (?, ?, ?)
`, momentID, mediaID, time.Now().Unix()); err != nil {
			return fmt.Errorf("insert moment media link: %w", err)
		}
	}
	return nil
}

// loadMediaForMoment 读取一条动态关联的媒体，跳过已被删除的媒体，
// 按关联建立时间排序——即动态里配图的展示顺序。
func loadMediaForMoment(ctx context.Context, q queryRunner, momentID int64) ([]Media, error) {
	rows, err := q.QueryContext(ctx, `
SELECT m.id, m.storage_name, m.original_name, m.media_type, m.mime_type, m.size, m.converted, m.created_at, m.deleted_at
FROM media m
JOIN moment_media mm ON mm.media_id = m.id
WHERE mm.moment_id = ? AND m.deleted_at IS NULL
ORDER BY mm.created_at ASC, m.id ASC
`, momentID)
	if err != nil {
		return nil, fmt.Errorf("list moment media: %w", err)
	}
	defer rows.Close()

	media := make([]Media, 0)
	for rows.Next() {
		item, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		media = append(media, *item)
	}
	if err := rowsErrFn(rows); err != nil {
		return nil, fmt.Errorf("iterate moment media: %w", err)
	}
	return media, nil
}

// loadMediaForMoments 为一批动态装配媒体，**只用一次查询**。
//
// 早先是循环内逐条查询（N+1）：一页 12 条动态就是 12 次数据库往返。
// 改成一次 IN 查询、再按 moment_id 分组后，往返次数与页大小无关。
//
// 查询里 moment_id 排在 media 各列之前，因此扫描时要在 mediaRow 的目标前面
// 插一个 &momentID——这也是 mediaRow 存在的原因（见 media_repo.go）。
//
// 与 loadTagsForArticles 一样：没有配图的动态拿到的是**非 nil 的空切片**，
// 因为 Media 与 MediaIDs 都会被直接序列化成 JSON，nil 会变成 null。
func loadMediaForMoments(ctx context.Context, q queryRunner, moments []Moment) error {
	if len(moments) == 0 {
		return nil
	}

	// 动态拼 IN (?,?,?) 是为了让每个 id 都走参数绑定，避免把 ids 拼进 SQL 造成注入。
	placeholders := make([]string, 0, len(moments))
	args := make([]any, 0, len(moments))
	for i := range moments {
		placeholders = append(placeholders, "?")
		args = append(args, moments[i].ID)
	}

	rows, err := q.QueryContext(ctx, `
SELECT mm.moment_id,
       m.id, m.storage_name, m.original_name, m.media_type, m.mime_type,
       m.size, m.converted, m.created_at, m.deleted_at
FROM media m
JOIN moment_media mm ON mm.media_id = m.id
WHERE mm.moment_id IN (`+strings.Join(placeholders, ", ")+`) AND m.deleted_at IS NULL
ORDER BY mm.moment_id ASC, mm.created_at ASC, m.id ASC
`, args...)
	if err != nil {
		return fmt.Errorf("list moments media: %w", err)
	}
	defer rows.Close()

	byMoment := make(map[int64][]Media, len(moments))
	for rows.Next() {
		var momentID int64
		var item Media
		var raw mediaRow
		dests := append([]any{&momentID}, raw.dests(&item)...)
		if err := scanValuesFn(rows, dests...); err != nil {
			return fmt.Errorf("scan moment media: %w", err)
		}
		raw.apply(&item)
		byMoment[momentID] = append(byMoment[momentID], item)
	}
	if err := rowsErrFn(rows); err != nil {
		return fmt.Errorf("iterate moments media: %w", err)
	}

	// 分组内已按关联建立时间排序（见上面的 ORDER BY），即配图的展示顺序。
	for i := range moments {
		media, ok := byMoment[moments[i].ID]
		if !ok {
			media = []Media{}
		}
		moments[i].Media = media
		moments[i].MediaIDs = mediaIDsFromMedia(media)
	}
	return nil
}

// mediaIDsFromMedia 抽出媒体 id 列表，用于让 MediaIDs 与 Media 两个视图保持一致
// （前者供写路径使用，后者供读路径取元数据）。
func mediaIDsFromMedia(media []Media) []int64 {
	ids := make([]int64, 0, len(media))
	for i := range media {
		ids = append(ids, media[i].ID)
	}
	return ids
}
