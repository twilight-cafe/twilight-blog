package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"blog-engine/internal/logging"
)

// EmailRepository 是「邮件发送留痕」的持久化契约。
// 记录只增不改不删，没有软删概念——它是发送动作已经发生的证据，不是待发队列。
type EmailRepository interface {
	Create(ctx context.Context, email *Email) error
	CountSince(ctx context.Context, since int64) (int64, error)
	List(ctx context.Context, p Pagination) ([]Email, int64, error)
	GetByID(ctx context.Context, id int64) (*Email, error)
}

type SQLiteEmailRepository struct {
	db *sql.DB
}

func NewSQLiteEmailRepository(db *sql.DB) *SQLiteEmailRepository {
	return &SQLiteEmailRepository{db: db}
}

// Create 插入一条发送记录并回填 ID。
func (r *SQLiteEmailRepository) Create(ctx context.Context, email *Email) error {
	logging.Debugf("[email-repo] Create called")
	res, err := r.db.ExecContext(ctx, `
INSERT INTO emails (from_email, to_email, subject, body, sent_at, created_at)
VALUES (?, ?, ?, ?, ?, ?)
`, email.FromEmail, email.ToEmail, email.Subject, email.Body, email.SentAt, email.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert email: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("get last insert id: %w", err)
	}
	email.ID = id
	return nil
}

// CountSince 统计 sent_at >= since 的邮件条数。
// email_service 以「今日零点」作为 since 调用它，用于每日发送配额判断。
func (r *SQLiteEmailRepository) CountSince(ctx context.Context, since int64) (int64, error) {
	logging.Debugf("[email-repo] CountSince since=%d", since)
	var count int64
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM emails
WHERE sent_at >= ?
`, since).Scan(&count); err != nil {
		return 0, fmt.Errorf("count emails since: %w", err)
	}
	return count, nil
}

// List 返回一页邮件审计记录，按发送时间倒序。
// total 与当页在同一个读事务里取出（见 query.go 的 readTx），两者不会不一致。
func (r *SQLiteEmailRepository) List(ctx context.Context, p Pagination) ([]Email, int64, error) {
	logging.Debugf("[email-repo] List called")

	var total int64
	emails := make([]Email, 0)

	err := readTx(ctx, r.db, func(ctx context.Context, q queryRunner) error {
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM emails`).Scan(&total); err != nil {
			return fmt.Errorf("count emails: %w", err)
		}

		rows, err := q.QueryContext(ctx, `
SELECT id, from_email, to_email, subject, body, sent_at, created_at
FROM emails
ORDER BY sent_at DESC, id DESC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
		if err != nil {
			return fmt.Errorf("list emails: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			email, err := scanEmail(rows)
			if err != nil {
				return err
			}
			emails = append(emails, *email)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate emails: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return emails, total, nil
}

func (r *SQLiteEmailRepository) GetByID(ctx context.Context, id int64) (*Email, error) {
	logging.Debugf("[email-repo] GetByID called")
	row := r.db.QueryRowContext(ctx, `
SELECT id, from_email, to_email, subject, body, sent_at, created_at
FROM emails
WHERE id = ?
`, id)

	email, err := scanEmail(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrEmailNotFound
		}
		return nil, err
	}
	return email, nil
}

func scanEmail(row scanner) (*Email, error) {
	var email Email
	if err := row.Scan(
		&email.ID,
		&email.FromEmail,
		&email.ToEmail,
		&email.Subject,
		&email.Body,
		&email.SentAt,
		&email.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &email, nil
}
