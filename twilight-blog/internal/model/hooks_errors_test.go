package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 本文件用 db_hooks.go 里的间接层，把 SQLite 驱动在正常运行时几乎不会返回的
// 结果错误（RowsAffected / LastInsertId / Commit / Scan / Rows.Err）逐条触发。
// 没有这层间接，这些分支只能以「反正不会发生」留在代码里且永远不计入覆盖率。

func override[T any](t *testing.T, target *T, value T) {
	t.Helper()
	original := *target
	*target = value
	t.Cleanup(func() { *target = original })
}

func TestModelBeginTransactionErrors(t *testing.T) {
	ctx := context.Background()
	articleDB := openTestDB(t)
	articleRepo := NewSQLiteArticleRepository(articleDB)
	momentRepo, _, _ := newTestMomentRepo(t)
	commentRepo := newTestCommentRepo(t)
	tagRepo, _, _, _ := newTestTagRepo(t)
	pageRepo := NewSQLitePageRepository(articleDB)
	userRepo := NewSQLiteUserRepository(articleDB)
	mediaRepo := NewSQLiteMediaRepository(articleDB)
	emailRepo := NewSQLiteEmailRepository(articleDB)

	override(t, &beginTxFn, func(*sql.DB, context.Context) (*sql.Tx, error) {
		return nil, errors.New("begin failed")
	})

	calls := []struct {
		name string
		call func() error
	}{
		{"article create", func() error { return articleRepo.Create(ctx, &Article{}) }},
		{"article update", func() error { return articleRepo.Update(ctx, &Article{}) }},
		{"article list", func() error { _, _, err := articleRepo.List(ctx, Pagination{}); return err }},
		{"article list deleted", func() error { _, _, err := articleRepo.ListDeleted(ctx, Pagination{}); return err }},
		{"moment create", func() error { return momentRepo.Create(ctx, &Moment{}) }},
		{"moment update", func() error { return momentRepo.Update(ctx, &Moment{}) }},
		{"moment list", func() error { _, _, err := momentRepo.List(ctx, Pagination{}); return err }},
		{"comment list by target", func() error {
			_, _, err := commentRepo.ListByTarget(ctx, "article", 1, nil, Pagination{})
			return err
		}},
		{"tag list", func() error { _, _, err := tagRepo.List(ctx, Pagination{}); return err }},
		{"tag list articles", func() error { _, _, err := tagRepo.ListArticlesByTag(ctx, 1, Pagination{}); return err }},
		{"page list", func() error { _, _, err := pageRepo.List(ctx, Pagination{}); return err }},
		{"user list", func() error { _, _, err := userRepo.List(ctx, Pagination{}); return err }},
		{"media list", func() error { _, _, err := mediaRepo.List(ctx, Pagination{}); return err }},
		{"email list", func() error { _, _, err := emailRepo.List(ctx, Pagination{}); return err }},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatal("a failing BeginTx must surface")
			}
		})
	}
}

func TestModelLastInsertIDErrors(t *testing.T) {
	ctx := context.Background()
	articleRepo := NewSQLiteArticleRepository(openTestDB(t))
	momentRepo, _, _ := newTestMomentRepo(t)
	commentRepo := newTestCommentRepo(t)
	pageRepo := NewSQLitePageRepository(openTestDB(t))
	userRepo := NewSQLiteUserRepository(openTestDB(t))
	mediaRepo := NewSQLiteMediaRepository(openTestDB(t))
	emailRepo := NewSQLiteEmailRepository(openTestDB(t))
	codeRepo := NewSQLiteEmailVerificationRepository(openTestDB(t))

	override(t, &lastInsertIDFn, func(sql.Result) (int64, error) {
		return 0, errors.New("no id")
	})

	calls := []struct {
		name string
		call func() error
	}{
		{"article", func() error { return articleRepo.Create(ctx, &Article{Title: "t"}) }},
		{"moment", func() error { return momentRepo.Create(ctx, &Moment{Content: "c"}) }},
		{"comment", func() error {
			return commentRepo.Create(ctx, &Comment{
				AuthorType: "admin", AdminName: "站长", TargetType: "article", TargetID: 1, Content: "c",
			})
		}},
		{"page", func() error { return pageRepo.Create(ctx, &Page{Title: "t"}) }},
		{"user", func() error { return userRepo.Create(ctx, &User{Username: "u", Nickname: "U"}) }},
		{"media", func() error {
			return mediaRepo.Create(ctx, &Media{
				StorageName: "x.png", OriginalName: "x.png", MediaType: MediaTypeImage,
				MimeType: "image/png", Size: 1,
			})
		}},
		{"email", func() error { return emailRepo.Create(ctx, &Email{Subject: "s"}) }},
		{"verification", func() error {
			return codeRepo.Create(ctx, &EmailVerificationCode{Email: "a@b.co", CodeHash: "h"})
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatal("a failing LastInsertId must surface")
			}
		})
	}
}

func TestModelCommitErrors(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := newTestMomentRepo(t)
	articleRepo := NewSQLiteArticleRepository(openTestDB(t))
	commentRepo := newTestCommentRepo(t)

	article := &Article{Title: "t", Content: "c", CreatedAt: 1, UpdatedAt: 1}
	if err := articleRepo.Create(ctx, article); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	moment := &Moment{Content: "c", CreatedAt: 1, UpdatedAt: 1}
	if err := repo.Create(ctx, moment); err != nil {
		t.Fatalf("seed moment: %v", err)
	}
	seedLiker(t, commentRepo, 1)
	commentID := addComment(t, commentRepo, 1, nil, "root", 1)

	override(t, &commitTxFn, func(*sql.Tx) error { return errors.New("commit failed") })

	calls := []struct {
		name string
		call func() error
	}{
		{"article create", func() error { return articleRepo.Create(ctx, &Article{Title: "new"}) }},
		{"article update", func() error { return articleRepo.Update(ctx, article) }},
		{"moment create", func() error { return repo.Create(ctx, &Moment{Content: "new"}) }},
		{"moment update", func() error { return repo.Update(ctx, moment) }},
		{"comment add like", func() error { return commentRepo.AddLike(ctx, commentID, 1, 2) }},
		{"comment remove like", func() error { return commentRepo.RemoveLike(ctx, commentID, 1) }},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatal("a failing Commit must surface")
			}
		})
	}
}

func TestModelRowsAffectedErrors(t *testing.T) {
	ctx := context.Background()
	articleDB := openTestDB(t)
	articleRepo := NewSQLiteArticleRepository(articleDB)
	momentRepo, _, _ := newTestMomentRepo(t)
	commentRepo := newTestCommentRepo(t)
	pageRepo := NewSQLitePageRepository(openTestDB(t))
	userRepo := NewSQLiteUserRepository(openTestDB(t))
	adminRepo := NewSQLiteAdminRepository(openTestDB(t))
	codeRepo := NewSQLiteEmailVerificationRepository(openTestDB(t))

	article := &Article{Title: "t", Content: "c", CreatedAt: 1, UpdatedAt: 1}
	deletedArticle := &Article{Title: "d", Content: "c", CreatedAt: 2, UpdatedAt: 2}
	for _, item := range []*Article{article, deletedArticle} {
		if err := articleRepo.Create(ctx, item); err != nil {
			t.Fatalf("seed article: %v", err)
		}
	}
	if _, err := articleRepo.SoftDelete(ctx, []int64{deletedArticle.ID}, 3); err != nil {
		t.Fatalf("soft delete article: %v", err)
	}
	moment := &Moment{Content: "m", CreatedAt: 1, UpdatedAt: 1}
	if err := momentRepo.Create(ctx, moment); err != nil {
		t.Fatalf("seed moment: %v", err)
	}
	if _, err := momentRepo.SoftDelete(ctx, []int64{moment.ID}, 2); err != nil {
		t.Fatalf("soft delete moment: %v", err)
	}
	seedLiker(t, commentRepo, 1)
	commentID := addComment(t, commentRepo, 1, nil, "root", 1)
	if _, err := commentRepo.SoftDelete(ctx, []int64{commentID}, 2); err != nil {
		t.Fatalf("soft delete comment: %v", err)
	}
	page := &Page{Title: "p", Content: "c", CreatedAt: 1, UpdatedAt: 1}
	if err := pageRepo.Create(ctx, page); err != nil {
		t.Fatalf("seed page: %v", err)
	}
	if _, err := pageRepo.SoftDelete(ctx, []int64{page.ID}, 2); err != nil {
		t.Fatalf("soft delete page: %v", err)
	}
	user := &User{Username: "u", Nickname: "U", Email: "u@example.com", CreatedAt: 1, UpdatedAt: 1}
	if err := userRepo.Create(ctx, user); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	admin := &User{IsAdmin: true, Username: "admin", Nickname: "站长", PasswordHash: "h", CreatedAt: 1, UpdatedAt: 1}
	if err := adminRepo.Create(ctx, admin); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	code := &EmailVerificationCode{Email: "a@b.co", CodeHash: "h", ExpiresAt: 99999, CreatedAt: 1}
	if err := codeRepo.Create(ctx, code); err != nil {
		t.Fatalf("seed code: %v", err)
	}

	override(t, &rowsAffectedFn, func(sql.Result) (int64, error) { return 0, errors.New("no affected count") })

	calls := []struct {
		name string
		call func() error
	}{
		{"article update", func() error { return articleRepo.Update(ctx, article) }},
		{"article soft delete", func() error {
			_, err := articleRepo.SoftDelete(ctx, []int64{article.ID}, 3)
			return err
		}},
		{"article restore", func() error { return articleRepo.Restore(ctx, deletedArticle.ID, 4) }},
		{"article batch restore", func() error {
			_, err := articleRepo.BatchRestore(ctx, []int64{deletedArticle.ID}, 4)
			return err
		}},
		{"article hard delete", func() error {
			_, err := articleRepo.HardDelete(ctx, []int64{deletedArticle.ID})
			return err
		}},
		{"moment soft delete", func() error {
			_, err := momentRepo.SoftDelete(ctx, []int64{moment.ID}, 3)
			return err
		}},
		{"moment restore", func() error { return momentRepo.Restore(ctx, moment.ID, 4) }},
		{"comment add like", func() error { return commentRepo.AddLike(ctx, commentID, 1, 3) }},
		{"comment remove like", func() error { return commentRepo.RemoveLike(ctx, commentID, 1) }},
		{"comment restore", func() error { return commentRepo.Restore(ctx, commentID, 4) }},
		{"comment batch restore", func() error {
			_, err := commentRepo.BatchRestore(ctx, []int64{commentID}, 4)
			return err
		}},
		{"comment hard delete", func() error {
			_, err := commentRepo.HardDelete(ctx, []int64{commentID})
			return err
		}},
		{"page update", func() error { return pageRepo.Update(ctx, page) }},
		{"page restore", func() error { return pageRepo.Restore(ctx, page.ID, 4) }},
		{"page batch restore", func() error {
			_, err := pageRepo.BatchRestore(ctx, []int64{page.ID}, 4)
			return err
		}},
		{"page hard delete", func() error {
			_, err := pageRepo.HardDelete(ctx, []int64{page.ID})
			return err
		}},
		{"user update nickname", func() error { return userRepo.UpdateNickname(ctx, user.ID, "U2", 2) }},
		{"user update username", func() error { return userRepo.UpdateUsername(ctx, user.ID, "u2", 2) }},
		{"user update password", func() error { return userRepo.UpdatePassword(ctx, user.ID, "h2", 2) }},
		{"user update profile", func() error {
			return userRepo.UpdateProfile(ctx, user.ID, "u3", "U3", "u3@example.com", 3)
		}},
		{"admin set password", func() error { return adminRepo.SetPassword(ctx, admin.ID, "h2", 2) }},
		{"admin update identity", func() error { return adminRepo.UpdateIdentity(ctx, admin.ID, "root", "Root", 2) }},
		{"admin update avatar", func() error { return adminRepo.UpdateAvatar(ctx, admin.ID, nil, 2) }},
		{"verification claim", func() error { return codeRepo.ClaimCode(ctx, code.ID, 2) }},
		{"verification reserve", func() error { _, err := codeRepo.ReserveAttempt(ctx, code.ID, 5); return err }},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatal("a failing RowsAffected must surface")
			}
		})
	}
}

func TestModelScanAndRowsErrors(t *testing.T) {
	ctx := context.Background()
	articleDB := openTestDB(t)
	articleRepo := NewSQLiteArticleRepository(articleDB)
	momentRepo, mediaRepo, _ := newTestMomentRepo(t)
	commentRepo := newTestCommentRepo(t)
	pageRepo := NewSQLitePageRepository(openTestDB(t))
	tagRepo, tagArticleRepo, _, _ := newTestTagRepo(t)
	userRepo := NewSQLiteUserRepository(openTestDB(t))
	mediaStore := NewSQLiteMediaRepository(openTestDB(t))
	emailRepo := NewSQLiteEmailRepository(openTestDB(t))

	article := &Article{Title: "t", Content: "c", CreatedAt: 1, UpdatedAt: 1, Tags: []string{"Go"}}
	if err := articleRepo.Create(ctx, article); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	momentMedia := addMedia(t, mediaRepo, "m.png", 1)
	moment := &Moment{Content: "m", MediaIDs: []int64{momentMedia}, CreatedAt: 1, UpdatedAt: 1}
	if err := momentRepo.Create(ctx, moment); err != nil {
		t.Fatalf("seed moment: %v", err)
	}
	commentID := addComment(t, commentRepo, 1, nil, "root", 1)
	page := &Page{Title: "p", Content: "c", CreatedAt: 1, UpdatedAt: 1}
	if err := pageRepo.Create(ctx, page); err != nil {
		t.Fatalf("seed page: %v", err)
	}
	if err := tagArticleRepo.Create(ctx, &Article{Title: "tagged", Tags: []string{"Go"}}); err != nil {
		t.Fatalf("seed tagged article: %v", err)
	}
	user := &User{Username: "u", Nickname: "U", Email: "u@example.com"}
	if err := userRepo.Create(ctx, user); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	media := &Media{StorageName: "x.png", OriginalName: "x.png", MediaType: MediaTypeImage, MimeType: "image/png"}
	if err := mediaStore.Create(ctx, media); err != nil {
		t.Fatalf("seed media: %v", err)
	}
	email := &Email{Subject: "s"}
	if err := emailRepo.Create(ctx, email); err != nil {
		t.Fatalf("seed email: %v", err)
	}

	t.Run("scan failures", func(t *testing.T) {
		// 先让底层 Scan 真正执行，释放查询持有的连接；再返回合成的错误。
		// 直接跳过 Scan 会让 *sql.Row 永远不归还连接，单连接池随即死锁。
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})

		calls := []struct {
			name string
			call func() error
		}{
			{"article get", func() error { _, err := articleRepo.GetByID(ctx, article.ID); return err }},
			{"article list", func() error { _, _, err := articleRepo.List(ctx, Pagination{Page: 1, PageSize: 10}); return err }},
			{"moment get", func() error { _, err := momentRepo.GetByID(ctx, moment.ID); return err }},
			{"moment list", func() error { _, _, err := momentRepo.List(ctx, Pagination{Page: 1, PageSize: 10}); return err }},
			{"comment get", func() error { _, err := commentRepo.GetByID(ctx, commentID); return err }},
			{"comment list", func() error {
				_, _, err := commentRepo.ListByTarget(ctx, "article", 1, nil, Pagination{Page: 1, PageSize: 10})
				return err
			}},
			{"page get", func() error { _, err := pageRepo.GetByID(ctx, page.ID); return err }},
			{"tag get", func() error {
				tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
				if err != nil {
					return err
				}
				_, err = tagRepo.GetByID(ctx, tags[0].ID)
				return err
			}},
			{"user find", func() error { _, err := userRepo.FindByUsername(ctx, "u"); return err }},
			{"user list", func() error { _, _, err := userRepo.List(ctx, Pagination{Page: 1, PageSize: 10}); return err }},
			{"media get", func() error { _, err := mediaStore.GetByID(ctx, media.ID); return err }},
			{"email get", func() error { _, err := emailRepo.GetByID(ctx, email.ID); return err }},
		}
		for _, tc := range calls {
			t.Run(tc.name, func(t *testing.T) {
				if err := tc.call(); err == nil {
					t.Fatal("a failing Scan must surface")
				}
			})
		}
	})

	t.Run("rows.Err failures", func(t *testing.T) {
		override(t, &rowsErrFn, func(*sql.Rows) error { return errors.New("iteration failed") })

		calls := []struct {
			name string
			call func() error
		}{
			{"article list", func() error { _, _, err := articleRepo.List(ctx, Pagination{Page: 1, PageSize: 10}); return err }},
			{"article list deleted", func() error { _, _, err := articleRepo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); return err }},
			{"moment list", func() error { _, _, err := momentRepo.List(ctx, Pagination{Page: 1, PageSize: 10}); return err }},
			{"comment list", func() error {
				_, _, err := commentRepo.ListAll(ctx, Pagination{Page: 1, PageSize: 10})
				return err
			}},
			{"page list", func() error { _, _, err := pageRepo.List(ctx, Pagination{Page: 1, PageSize: 10}); return err }},
			{"tag list", func() error { _, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10}); return err }},
			{"user list", func() error { _, _, err := userRepo.List(ctx, Pagination{Page: 1, PageSize: 10}); return err }},
			{"media list", func() error { _, _, err := mediaStore.List(ctx, Pagination{Page: 1, PageSize: 10}); return err }},
			{"email list", func() error { _, _, err := emailRepo.List(ctx, Pagination{Page: 1, PageSize: 10}); return err }},
		}
		for _, tc := range calls {
			t.Run(tc.name, func(t *testing.T) {
				if err := tc.call(); err == nil {
					t.Fatal("a failing Rows.Err must surface")
				}
			})
		}
	})
}
