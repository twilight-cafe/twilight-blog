package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"blog-engine/internal/logging"
)

// CommentRepository 是评论的持久化契约。
//
// 软删/回收站语义与文章一致（SoftDelete 幂等并返回实际改动行数；Restore 只对已删除行
// 生效，否则 ErrCommentNotFound；HardDelete 只删回收站里的行）。此外还有两件评论特有的事：
//   - 评论是树形结构（parent_id 自引用），读列表要连子孙一起取；
//   - 点赞是「每用户一次」的幂等操作，用 comment_likes 唯一键配合冗余 like_count 实现。
type CommentRepository interface {
	Create(ctx context.Context, comment *Comment) error
	GetByID(ctx context.Context, id int64) (*Comment, error)
	GetByIDForUser(ctx context.Context, id int64, currentUserID *int64) (*Comment, error)
	// ListByTarget 返回「一页根评论 + 这些根评论的全部子孙」，以及根评论总数。
	// 分页在 SQL 里完成，热门文章不再需要每次请求都把全部评论读进内存。
	// 注意 total 统计的是根评论数，而返回切片同时含子孙评论，
	// 因此 len(返回切片) 通常大于 total，翻页要按 total 计算。
	ListByTarget(ctx context.Context, targetType string, targetID int64, currentUserID *int64, p Pagination) ([]Comment, int64, error)
	ListAll(ctx context.Context, p Pagination) ([]Comment, int64, error)
	ListDeleted(ctx context.Context, p Pagination) ([]Comment, int64, error)
	AddLike(ctx context.Context, commentID int64, userID int64, now int64) error
	RemoveLike(ctx context.Context, commentID int64, userID int64) error
	SoftDelete(ctx context.Context, ids []int64, deletedAt int64) (int64, error)
	Restore(ctx context.Context, id int64, updatedAt int64) error
	BatchRestore(ctx context.Context, ids []int64, updatedAt int64) (int64, error)
	HardDelete(ctx context.Context, ids []int64) (int64, error)
}

type SQLiteCommentRepository struct {
	db *sql.DB
}

func NewSQLiteCommentRepository(db *sql.DB) *SQLiteCommentRepository {
	return &SQLiteCommentRepository{db: db}
}

// commentColumns 是所有评论查询共用的投影列表。scanComment 期待严格按此顺序取值，
// 并且紧随其后还要有一个 liked_by_me 标志位。
const commentColumns = `c.id, c.author_type, c.user_id, c.admin_name, c.nickname,
       c.target_type, c.target_id, c.parent_id, c.content,
       c.created_at, c.updated_at, c.deleted_at,
       COALESCE(u.email, ''), COALESCE(c.like_count, 0)`

func (r *SQLiteCommentRepository) Create(ctx context.Context, comment *Comment) error {
	logging.Debugf("[comment-repo] Create called")
	res, err := r.db.ExecContext(ctx, `
INSERT INTO comments (author_type, user_id, admin_name, nickname, target_type, target_id, parent_id, content, created_at, updated_at, deleted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
`, comment.AuthorType, comment.UserID, comment.AdminName, comment.Nickname, comment.TargetType, comment.TargetID, comment.ParentID, comment.Content, comment.CreatedAt, comment.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert comment: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("get last insert id: %w", err)
	}
	comment.ID = id
	return nil
}

func (r *SQLiteCommentRepository) GetByID(ctx context.Context, id int64) (*Comment, error) {
	logging.Debugf("[comment-repo] GetByID called")
	row := r.db.QueryRowContext(ctx, `
SELECT `+commentColumns+`, 0
FROM comments c
LEFT JOIN users u ON u.id = c.user_id
WHERE c.id = ? AND c.deleted_at IS NULL
`, id)

	comment, err := scanComment(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrCommentNotFound
		}
		return nil, err
	}
	return comment, nil
}

// GetByIDForUser 读取单条评论，并附带「当前用户是否点过赞」。
// currentUserID 为 nil（未登录）时 liked_by_me 恒为 0，SQL 里用 ? IS NOT NULL 短路。
func (r *SQLiteCommentRepository) GetByIDForUser(ctx context.Context, id int64, currentUserID *int64) (*Comment, error) {
	logging.Debugf("[comment-repo] GetByIDForUser called")
	row := r.db.QueryRowContext(ctx, `
SELECT `+commentColumns+`,
       CASE WHEN ? IS NOT NULL AND EXISTS (
           SELECT 1 FROM comment_likes cl WHERE cl.comment_id = c.id AND cl.user_id = ?
       ) THEN 1 ELSE 0 END
FROM comments c
LEFT JOIN users u ON u.id = c.user_id
WHERE c.id = ? AND c.deleted_at IS NULL
`, currentUserID, currentUserID, id)

	comment, err := scanComment(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrCommentNotFound
		}
		return nil, err
	}
	return comment, nil
}

// 评论树的读取上限。两者共同保证「一次请求的代价」有界。
//
// 为什么必须有：递归 CTE 原先只对**根评论**分页（LIMIT ? OFFSET ? 只出现在 roots
// 阶段），子孙阶段与最终 SELECT 都没有任何上限。于是一条 3000 层的回复链可以被
// 一次**无需认证**的 GET 全部拉回——实测 page_size=1 也返回 3000 条、耗时约 1.5s。
// 写入侧有限流（5/min）与邮箱验证，所以放大倍数受限；但**读取侧完全免费**。
//
// 取值依据：正常讨论极少超过 10 层；500 行远超一页（page_size 上限 12）实际需要的
// 数量，同时把单次响应体与查询代价钉在可预期范围内。
const (
	// maxCommentTreeDepth 是一条评论链允许的最大层数（根评论为第 1 层）。
	maxCommentTreeDepth = 20
	// maxCommentTreeRows 是一次「按目标读评论」最多返回的行数（根 + 全部子孙）。
	maxCommentTreeRows = 500
)

// ListByTarget 返回「一页根评论 + 这些根评论的子孙」，以及根评论总数。
//
// 分页在 SQL 内完成（roots CTE 带 ORDER BY/LIMIT），因此热门文章不必每次请求
// 把全部评论读进内存；递归的 tree CTE 再把选中根评论的子孙一并拉出，
// 保证回复不会因为分页而与父评论分离。
//
// 递归**同时受深度与行数两道上限约束**（见 maxCommentTreeDepth / maxCommentTreeRows，
// 以及那里的说明）：没有它们时，一条超深回复链能被一次匿名请求整体拉回。
// 代价是极深的讨论会被截断——这是有意的取舍，截断比无界放大更可接受。
//
// 注意两个容易踩的点：
//   - total 统计的是根评论数，而返回的切片同时包含子孙评论，
//     所以 len(切片) 通常大于 total，翻页必须按 total 算；
//   - 返回切片的顺序是 created_at 升序，roots 阶段的 like_count 排序只决定
//     「哪几条根评论进入本页」，并不会保留下来；真正的展示顺序由
//     service.orderCommentTree 重新整理（根评论按点赞降序，回复深度优先跟随父评论）。
func (r *SQLiteCommentRepository) ListByTarget(ctx context.Context, targetType string, targetID int64, currentUserID *int64, p Pagination) ([]Comment, int64, error) {
	logging.Debugf("[comment-repo] ListByTarget called")
	page := p.Normalized()

	var total int64
	comments := make([]Comment, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM comments
WHERE target_type = ? AND target_id = ? AND deleted_at IS NULL AND parent_id IS NULL
`, targetType, targetID).Scan(&total); err != nil {
			return fmt.Errorf("count root comments: %w", err)
		}
		if total == 0 {
			// 没有根评论就不必执行递归查询了。comments 保持空切片，
			// 与「执行了查询但没有结果」的返回值完全一致。
			return nil
		}

		// The roots CTE applies ORDER BY/LIMIT, so pagination happens in SQLite; the
		// recursive tree CTE then pulls in every descendant of the selected roots so
		// replies are never orphaned by paging.
		//
		// depth 从 0 开始并逐层 +1，使递归有明确的终止条件；maxCommentTreeRows
		// 则给最终结果集封顶。两个上限都在 SQL 里，不依赖调用方自觉。
		rows, err := q.QueryContext(ctx, `
WITH RECURSIVE roots AS (
    SELECT id
    FROM comments
    WHERE target_type = ? AND target_id = ? AND deleted_at IS NULL AND parent_id IS NULL
    ORDER BY like_count DESC, created_at ASC, id ASC
    LIMIT ? OFFSET ?
),
tree AS (
    SELECT id, 0 AS depth FROM roots
    UNION ALL
    SELECT c.id, t.depth + 1
    FROM comments c JOIN tree t ON c.parent_id = t.id
    WHERE c.deleted_at IS NULL AND t.depth + 1 < ?
)
SELECT `+commentColumns+`,
       CASE WHEN ? IS NOT NULL AND EXISTS (
           SELECT 1 FROM comment_likes cl WHERE cl.comment_id = c.id AND cl.user_id = ?
       ) THEN 1 ELSE 0 END
FROM comments c
JOIN tree t ON t.id = c.id
LEFT JOIN users u ON u.id = c.user_id
ORDER BY c.created_at ASC, c.id ASC
LIMIT ?
`, targetType, targetID, page.Limit(), page.Offset(), maxCommentTreeDepth, currentUserID, currentUserID, maxCommentTreeRows)
		if err != nil {
			return fmt.Errorf("list comments by target: %w", err)
		}
		defer rows.Close()

		comments, err = scanComments(rows)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return comments, total, nil
}

// ListAll 返回后台评论列表（各目标类型混在一起，按创建时间倒序）。
//
// 已软删的评论必须排除——它们不但不该出现在列表里，还带着评论者的邮箱，
// 漏掉这个条件就是一次数据泄露。这条过滤曾经缺失过，别把它删掉。
func (r *SQLiteCommentRepository) ListAll(ctx context.Context, p Pagination) ([]Comment, int64, error) {
	logging.Debugf("[comment-repo] ListAll called")

	var total int64
	comments := make([]Comment, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		// Soft-deleted comments must not appear (or be counted) in the active list;
		// they also carry the commenter's email, which made the omission a data leak.
		if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM comments
WHERE deleted_at IS NULL
`).Scan(&total); err != nil {
			return fmt.Errorf("count all comments: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT `+commentColumns+`, 0
FROM comments c
LEFT JOIN users u ON u.id = c.user_id
WHERE c.deleted_at IS NULL
ORDER BY c.created_at DESC, c.id DESC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list all comments: %w", err)
		}
		defer rows.Close()

		comments, err = scanComments(rows)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return comments, total, nil
}

// ListDeleted 返回回收站中的评论，按删除时间倒序。
func (r *SQLiteCommentRepository) ListDeleted(ctx context.Context, p Pagination) ([]Comment, int64, error) {
	logging.Debugf("[comment-repo] ListDeleted called")

	var total int64
	comments := make([]Comment, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM comments
WHERE deleted_at IS NOT NULL
`).Scan(&total); err != nil {
			return fmt.Errorf("count deleted comments: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT `+commentColumns+`, 0
FROM comments c
LEFT JOIN users u ON u.id = c.user_id
WHERE c.deleted_at IS NOT NULL
ORDER BY c.deleted_at DESC, c.id DESC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list deleted comments: %w", err)
		}
		defer rows.Close()

		comments, err = scanComments(rows)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return comments, total, nil
}

// AddLike 为评论点赞。只返回 error，因为「重复点赞」不视为失败。
//
// 实现要点：
//   - 先确认评论存在且未删除，否则 ErrCommentNotFound；
//   - 用 INSERT OR IGNORE 依赖 comment_likes 的 (comment_id, user_id) 主键去重；
//   - 只有当确实插入了新行（RowsAffected > 0）时才把冗余的 like_count +1，
//     因此重复点赞是幂等的，计数也不会被刷高。
//
// 整个过程在一个事务内完成。
func (r *SQLiteCommentRepository) AddLike(ctx context.Context, commentID int64, userID int64, now int64) error {
	logging.Debugf("[comment-repo] AddLike called")
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin add like: %w", err)
	}
	defer tx.Rollback()

	var exists int
	err = tx.QueryRowContext(ctx, `
SELECT 1 FROM comments
WHERE id = ? AND deleted_at IS NULL
`, commentID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCommentNotFound
	}
	if err != nil {
		return fmt.Errorf("check comment before like: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO comment_likes (comment_id, user_id, created_at)
VALUES (?, ?, ?)
`, commentID, userID, now)
	if err != nil {
		return fmt.Errorf("insert comment like: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("comment like rows affected: %w", err)
	}
	if n > 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE comments
SET like_count = like_count + 1
WHERE id = ?
`, commentID); err != nil {
			return fmt.Errorf("increment comment like count: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit add like: %w", err)
	}
	return nil
}

// RemoveLike 取消点赞。同样只在确实删除到行时才递减计数，
// 并用 MAX(like_count - 1, 0) 兜底，避免历史脏数据把计数减成负数。
func (r *SQLiteCommentRepository) RemoveLike(ctx context.Context, commentID int64, userID int64) error {
	logging.Debugf("[comment-repo] RemoveLike called")
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin remove like: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
DELETE FROM comment_likes
WHERE comment_id = ? AND user_id = ?
`, commentID, userID)
	if err != nil {
		return fmt.Errorf("delete comment like: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("comment unlike rows affected: %w", err)
	}
	if n > 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE comments
SET like_count = MAX(like_count - 1, 0)
WHERE id = ?
`, commentID); err != nil {
			return fmt.Errorf("decrement comment like count: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit remove like: %w", err)
	}
	return nil
}

// SoftDelete 标记删除一批评论，返回实际被标记的行数（幂等，语义同其它实体）。
//
// 注意连带效应：若删掉的是根评论，它的子孙评论本身并未被标记删除，仍留在表里，
// 但 ListByTarget 只从「未删除的根评论」向下遍历，于是这些子孙会一并从前台消失；
// 它们却仍会单独出现在回收站列表里。恢复根评论后即可重新可见。
func (r *SQLiteCommentRepository) SoftDelete(ctx context.Context, ids []int64, deletedAt int64) (int64, error) {
	logging.Debugf("[comment-repo] SoftDelete called")
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
UPDATE comments
SET deleted_at = ?, updated_at = ?
WHERE id IN (` + strings.Join(placeholders, ", ") + `) AND deleted_at IS NULL
`
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("soft delete comments: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (r *SQLiteCommentRepository) Restore(ctx context.Context, id int64, updatedAt int64) error {
	logging.Debugf("[comment-repo] Restore called")
	res, err := r.db.ExecContext(ctx, `
UPDATE comments
SET deleted_at = NULL, updated_at = ?
WHERE id = ? AND deleted_at IS NOT NULL
`, updatedAt, id)
	if err != nil {
		return fmt.Errorf("restore comment: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrCommentNotFound
	}
	return nil
}

func (r *SQLiteCommentRepository) BatchRestore(ctx context.Context, ids []int64, updatedAt int64) (int64, error) {
	logging.Debugf("[comment-repo] BatchRestore called")
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
UPDATE comments
SET deleted_at = NULL, updated_at = ?
WHERE id IN (` + strings.Join(placeholders, ", ") + `) AND deleted_at IS NOT NULL
`
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("batch restore comments: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

// HardDelete 永久删除回收站中的评论，只作用于 deleted_at IS NOT NULL 的行
// （活动态删不掉，返回 0）。
//
// comment_likes 的关联行由外键 ON DELETE CASCADE 自动清理，这里不需要显式删——
// database.Open 会在启动时校验 foreign_keys pragma 确实为 1（否则拒绝启动），
// 所以级联一定生效。
//
// 早先这里有一句显式的 DELETE FROM comment_likes，它是错的：那句没有 deleted_at
// 条件，因此对一条**未软删**的评论调用本方法时，评论本身删不掉（0 行），
// 但它的点赞记录已经被清空了。交回给级联就没有这个问题。
//
// 注意：这里不处理子评论。若永久删除的是一条仍有回复的父评论，那些回复会留在表里，
// 但再也无法经 ListByTarget 读到（它只能从活着的根评论往下走）。
func (r *SQLiteCommentRepository) HardDelete(ctx context.Context, ids []int64) (int64, error) {
	logging.Debugf("[comment-repo] HardDelete called")
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
DELETE FROM comments
WHERE id IN (`+strings.Join(placeholders, ", ")+`) AND deleted_at IS NOT NULL
`, args...)
	if err != nil {
		return 0, fmt.Errorf("hard delete comments: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func scanComments(rows *sql.Rows) ([]Comment, error) {
	comments := make([]Comment, 0)
	for rows.Next() {
		comment, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		comments = append(comments, *comment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate comments: %w", err)
	}
	return comments, nil
}

// scanComment 把一行结果扫描成 Comment：处理各可空列到指针/零值的转换，
// 并把 liked_by_me 的 0/1 转成 bool。
// 列顺序必须与 commentColumns 严格一致，改动那里就要同步改这里。
func scanComment(row scanner) (*Comment, error) {
	var comment Comment
	var userID sql.NullInt64
	var adminName sql.NullString
	var nickname sql.NullString
	var parentID sql.NullInt64
	var deletedAt sql.NullInt64
	var userEmail sql.NullString
	var likedByMe int64

	if err := row.Scan(
		&comment.ID,
		&comment.AuthorType,
		&userID,
		&adminName,
		&nickname,
		&comment.TargetType,
		&comment.TargetID,
		&parentID,
		&comment.Content,
		&comment.CreatedAt,
		&comment.UpdatedAt,
		&deletedAt,
		&userEmail,
		&comment.LikeCount,
		&likedByMe,
	); err != nil {
		return nil, err
	}

	if userID.Valid {
		value := userID.Int64
		comment.UserID = &value
	}
	if adminName.Valid {
		comment.AdminName = adminName.String
	}
	if nickname.Valid {
		comment.Nickname = nickname.String
	}
	if parentID.Valid {
		value := parentID.Int64
		comment.ParentID = &value
	}
	if deletedAt.Valid {
		value := deletedAt.Int64
		comment.DeletedAt = &value
	}
	if userEmail.Valid {
		comment.UserEmail = userEmail.String
	}
	comment.LikedByMe = likedByMe != 0

	return &comment, nil
}
