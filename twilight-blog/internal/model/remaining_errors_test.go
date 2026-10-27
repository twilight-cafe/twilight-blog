package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 本文件补齐剩余的分支：QueryRow 的 Scan 错误（用列改名制造）、
// 扫描结果里「有用户 id」的形态，以及关联替换在调用点的错误传播。

func TestQueryRowScanErrors(t *testing.T) {
	ctx := context.Background()

	scanFail := func(t *testing.T) {
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
	}

	t.Run("admin Get", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteAdminRepository(db)
		if err := repo.Create(ctx, &User{Username: "admin", Nickname: "Admin"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		scanFail(t)
		if _, err := repo.Get(ctx); err == nil {
			t.Fatal("a scan failure must surface")
		}
	})

	t.Run("article GetByID", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		article := &Article{Title: "t"}
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("seed: %v", err)
		}
		scanFail(t)
		if _, err := repo.GetByID(ctx, article.ID); err == nil {
			t.Fatal("a scan failure must surface")
		}
	})

	t.Run("comment GetByID", func(t *testing.T) {
		repo := newTestCommentRepo(t)
		id := addComment(t, repo, 1, nil, "root", 1)
		scanFail(t)
		if _, err := repo.GetByID(ctx, id); err == nil {
			t.Fatal("a scan failure must surface")
		}
	})

	t.Run("moment GetByID", func(t *testing.T) {
		repo, _, _ := newTestMomentRepo(t)
		moment := &Moment{Content: "m"}
		if err := repo.Create(ctx, moment); err != nil {
			t.Fatalf("seed: %v", err)
		}
		scanFail(t)
		if _, err := repo.GetByID(ctx, moment.ID); err == nil {
			t.Fatal("a scan failure must surface")
		}
	})

	t.Run("user lookups", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteUserRepository(db)
		user := &User{Username: "u", Nickname: "U", Email: "u@example.com"}
		if err := repo.Create(ctx, user); err != nil {
			t.Fatalf("seed: %v", err)
		}
		scanFail(t)
		if _, err := repo.FindByUsername(ctx, "u"); err == nil {
			t.Fatal("a scan failure must surface")
		}
		if _, err := repo.FindByEmail(ctx, "u@example.com"); err == nil {
			t.Fatal("a scan failure must surface")
		}
		if _, err := repo.GetByID(ctx, user.ID); err == nil {
			t.Fatal("a scan failure must surface")
		}
	})

	t.Run("verification lookup", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteEmailVerificationRepository(db)
		code := &EmailVerificationCode{Email: "a@b.co", CodeHash: "h", ExpiresAt: 100, CreatedAt: 1}
		if err := repo.Create(ctx, code); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// 这条查询直接 .Scan，不经过 scanValuesFn；改名让扫描本身失败。
		mustExec(t, db, `ALTER TABLE email_verification_codes RENAME COLUMN code_hash TO code_hash_moved`)
		if _, err := repo.FindLatestActiveByEmail(ctx, "a@b.co", 50); err == nil {
			t.Fatal("a scan failure must surface")
		}
	})
}

func TestScanCommentWithUserAuthor(t *testing.T) {
	db := openTestDB(t)
	userRepo := NewSQLiteUserRepository(db)
	user := &User{Username: "u", Nickname: "U", Email: "u@example.com", CreatedAt: 1, UpdatedAt: 1}
	if err := userRepo.Create(ctxBg(), user); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	repo := NewSQLiteCommentRepository(db)
	comment := &Comment{
		AuthorType: "user", UserID: &user.ID, TargetType: "article", TargetID: 1,
		Content: "c", CreatedAt: 1, UpdatedAt: 1,
	}
	if err := repo.Create(ctxBg(), comment); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	got, err := repo.GetByID(ctxBg(), comment.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.UserID == nil || *got.UserID != user.ID {
		t.Fatalf("user id = %v", got.UserID)
	}
}

func ctxBg() context.Context { return context.Background() }

func TestListDeletedScanAndRowErrors(t *testing.T) {
	ctx := context.Background()

	scanFail := func(t *testing.T) {
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
	}
	rowsFail := func(t *testing.T) {
		override(t, &rowsErrFn, func(*sql.Rows) error { return errors.New("rows failed") })
	}

	t.Run("article ListDeleted scan", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		article := &Article{Title: "t"}
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := repo.SoftDelete(ctx, []int64{article.ID}, 2); err != nil {
			t.Fatalf("delete: %v", err)
		}
		scanFail(t)
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a scan failure must surface")
		}
	})

	t.Run("article ListDeleted iteration", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		article := &Article{Title: "t"}
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := repo.SoftDelete(ctx, []int64{article.ID}, 2); err != nil {
			t.Fatalf("delete: %v", err)
		}
		rowsFail(t)
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a Rows.Err failure must surface")
		}
	})

	t.Run("moment ListDeleted scan", func(t *testing.T) {
		repo, _, _ := newTestMomentRepo(t)
		moment := &Moment{Content: "m"}
		if err := repo.Create(ctx, moment); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := repo.SoftDelete(ctx, []int64{moment.ID}, 2); err != nil {
			t.Fatalf("delete: %v", err)
		}
		scanFail(t)
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a scan failure must surface")
		}
	})

	t.Run("page ListDeleted scan", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLitePageRepository(db)
		page := &Page{Title: "p"}
		if err := repo.Create(ctx, page); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := repo.SoftDelete(ctx, []int64{page.ID}, 2); err != nil {
			t.Fatalf("delete: %v", err)
		}
		scanFail(t)
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a scan failure must surface")
		}
	})

	t.Run("media upgrade iteration", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteMediaRepository(db)
		if err := repo.Create(ctx, &Media{
			StorageName: "x.png", OriginalName: "x.png", MediaType: MediaTypeImage, MimeType: "image/png",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		rowsFail(t)
		if _, err := repo.ListImagesForWebPUpgrade(ctx); err == nil {
			t.Fatal("a Rows.Err failure must surface")
		}
	})
}

func TestAssociationReplacementCallSites(t *testing.T) {
	ctx := context.Background()

	t.Run("article create propagates tag failure", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		mustExec(t, db, `CREATE TRIGGER fail_tag_insert BEFORE INSERT ON tags
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := repo.Create(ctx, &Article{Title: "t", Tags: []string{"Go"}}); err == nil {
			t.Fatal("a tag failure must surface from Create")
		}
	})

	t.Run("article update propagates tag failure", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		article := &Article{Title: "t"}
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("seed: %v", err)
		}
		mustExec(t, db, `CREATE TRIGGER fail_tag_insert BEFORE INSERT ON tags
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		article.Tags = []string{"New"}
		if err := repo.Update(ctx, article); err == nil {
			t.Fatal("a tag failure must surface from Update")
		}
	})

	t.Run("moment create propagates media failure", func(t *testing.T) {
		repo, mediaRepo, db := newTestMomentRepo(t)
		mediaID := addMedia(t, mediaRepo, "m.png", 1)
		mustExec(t, db, `CREATE TRIGGER fail_moment_media BEFORE INSERT ON moment_media
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := repo.Create(ctx, &Moment{Content: "m", MediaIDs: []int64{mediaID}}); err == nil {
			t.Fatal("a media failure must surface from Create")
		}
	})

	t.Run("moment update propagates media failure", func(t *testing.T) {
		repo, mediaRepo, db := newTestMomentRepo(t)
		mediaID := addMedia(t, mediaRepo, "m.png", 1)
		moment := &Moment{Content: "m", MediaIDs: []int64{mediaID}}
		if err := repo.Create(ctx, moment); err != nil {
			t.Fatalf("seed: %v", err)
		}
		mustExec(t, db, `CREATE TRIGGER fail_moment_media BEFORE INSERT ON moment_media
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		moment.MediaIDs = []int64{mediaID}
		if err := repo.Update(ctx, moment); err == nil {
			t.Fatal("a media failure must surface from Update")
		}
	})

	t.Run("moment soft delete of an empty set", func(t *testing.T) {
		repo, _, _ := newTestMomentRepo(t)
		if n, err := repo.SoftDelete(ctx, nil, 1); n != 0 || err != nil {
			t.Fatalf("SoftDelete(nil) = (%d, %v)", n, err)
		}
	})

	t.Run("moment update of a missing row", func(t *testing.T) {
		repo, _, _ := newTestMomentRepo(t)
		if err := repo.Update(ctx, &Moment{ID: 999}); !errors.Is(err, ErrMomentNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCommentLikeAndDeleteErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("like existence query fails", func(t *testing.T) {
		repo := newTestCommentRepo(t)
		id := addComment(t, repo, 1, nil, "root", 1)
		mustExec(t, repo.db, `ALTER TABLE comments RENAME COLUMN deleted_at TO deleted_at_moved`)
		if err := repo.AddLike(ctx, id, 1, 2); err == nil {
			t.Fatal("a failing existence check must surface")
		}
	})

	t.Run("like counter update fails", func(t *testing.T) {
		repo := newTestCommentRepo(t)
		seedLiker(t, repo, 1)
		id := addComment(t, repo, 1, nil, "root", 1)
		mustExec(t, repo.db, `CREATE TRIGGER fail_comment_count BEFORE UPDATE ON comments
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := repo.AddLike(ctx, id, 1, 2); err == nil {
			t.Fatal("a failing counter update must surface")
		}
	})

	t.Run("soft delete fails", func(t *testing.T) {
		repo := newTestCommentRepo(t)
		id := addComment(t, repo, 1, nil, "root", 1)
		mustExec(t, repo.db, `CREATE TRIGGER fail_comment_update BEFORE UPDATE ON comments
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if _, err := repo.SoftDelete(ctx, []int64{id}, 2); err == nil {
			t.Fatal("a failing soft delete must surface")
		}
	})

	t.Run("hard delete fails", func(t *testing.T) {
		repo := newTestCommentRepo(t)
		id := addComment(t, repo, 1, nil, "root", 1)
		if _, err := repo.SoftDelete(ctx, []int64{id}, 2); err != nil {
			t.Fatalf("soft delete: %v", err)
		}
		mustExec(t, repo.db, `CREATE TRIGGER fail_comment_delete BEFORE DELETE ON comments
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if _, err := repo.HardDelete(ctx, []int64{id}); err == nil {
			t.Fatal("a failing hard delete must surface")
		}
	})
}

func TestRemainingListCountErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("comment ListByTarget count", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteCommentRepository(db)
		mustExec(t, db, `ALTER TABLE comments RENAME COLUMN target_type TO target_type_moved`)
		if _, _, err := repo.ListByTarget(ctx, "article", 1, nil, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("comment ListByTarget query", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteCommentRepository(db)
		// 先放一条根评论：total == 0 时 ListByTarget 会跳过递归查询。
		addComment(t, repo, 1, nil, "root", 1)
		mustExec(t, db, `ALTER TABLE comments RENAME COLUMN content TO content_moved`)
		if _, _, err := repo.ListByTarget(ctx, "article", 1, nil, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
	})

	t.Run("comment ListDeleted count", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteCommentRepository(db)
		mustExec(t, db, `ALTER TABLE comments RENAME COLUMN deleted_at TO deleted_at_moved`)
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("tag List count", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteTagRepository(db)
		mustExec(t, db, `ALTER TABLE tags RENAME COLUMN name TO name_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
	})

	t.Run("tag ListArticlesByTag count", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteTagRepository(db)
		mustExec(t, db, `ALTER TABLE article_tags RENAME COLUMN tag_id TO tag_id_moved`)
		if _, _, err := repo.ListArticlesByTag(ctx, 1, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("tag CountByTag query", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteTagRepository(db)
		mustExec(t, db, `ALTER TABLE article_tags RENAME COLUMN tag_id TO tag_id_moved`)
		if _, err := repo.CountByTag(ctx, 1); err == nil {
			t.Fatal("a failing query must surface")
		}
	})

	t.Run("email List count", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteEmailRepository(db)
		mustExec(t, db, `ALTER TABLE emails RENAME COLUMN sent_at TO sent_at_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("migration existence check fails", func(t *testing.T) {
		db := openTestDB(t)
		if err := db.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if err := MigrateAdminCredentials(ctx, db, configWithLegacy("hash", "admin", "Admin")); err == nil {
			t.Fatal("a failing Exists must surface")
		}
	})
}
