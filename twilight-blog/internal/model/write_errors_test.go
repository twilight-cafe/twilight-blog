package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"blog-engine/internal/config"
)

func mustExec(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("exec %q: %v", statement, err)
	}
}

// TestArticleWriteAndListErrors 覆盖文章仓储里触发器/列改名才能制造的失败。
func TestArticleWriteAndListErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("insert fails", func(t *testing.T) {
		db := openTestDB(t)
		mustExec(t, db, `CREATE TRIGGER fail_article_insert BEFORE INSERT ON articles
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		repo := NewSQLiteArticleRepository(db)
		if err := repo.Create(ctx, &Article{Title: "t"}); err == nil {
			t.Fatal("a failing INSERT must surface")
		}
	})

	t.Run("update fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		article := &Article{Title: "t"}
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("seed: %v", err)
		}
		mustExec(t, db, `CREATE TRIGGER fail_article_update BEFORE UPDATE ON articles
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := repo.Update(ctx, article); err == nil {
			t.Fatal("a failing UPDATE must surface")
		}
	})

	t.Run("update of a missing row is not found", func(t *testing.T) {
		repo := NewSQLiteArticleRepository(openTestDB(t))
		if err := repo.Update(ctx, &Article{ID: 999, Title: "t"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("soft delete of an empty set", func(t *testing.T) {
		repo := NewSQLiteArticleRepository(openTestDB(t))
		if n, err := repo.SoftDelete(ctx, nil, 1); n != 0 || err != nil {
			t.Fatalf("SoftDelete(nil) = (%d, %v)", n, err)
		}
	})

	t.Run("cover media scan fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		article := &Article{Title: "t"}
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("seed: %v", err)
		}
		media := &Media{
			StorageName: "c.png", OriginalName: "c.png", MediaType: MediaTypeImage, MimeType: "image/png",
		}
		if err := NewSQLiteMediaRepository(db).Create(ctx, media); err != nil {
			t.Fatalf("seed media: %v", err)
		}
		article.CoverMediaID = &media.ID
		if err := repo.Update(ctx, article); err != nil {
			t.Fatalf("set cover: %v", err)
		}
		if _, err := repo.SoftDelete(ctx, []int64{article.ID}, 2); err != nil {
			t.Fatalf("soft delete: %v", err)
		}
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
		if _, err := repo.CoverMediaIDsToDelete(ctx, []int64{article.ID}); err == nil {
			t.Fatal("a failing Scan must surface")
		}
	})

	t.Run("cover media iteration fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		article := &Article{Title: "t"}
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("seed: %v", err)
		}
		media := &Media{
			StorageName: "c.png", OriginalName: "c.png", MediaType: MediaTypeImage, MimeType: "image/png",
		}
		if err := NewSQLiteMediaRepository(db).Create(ctx, media); err != nil {
			t.Fatalf("seed media: %v", err)
		}
		article.CoverMediaID = &media.ID
		if err := repo.Update(ctx, article); err != nil {
			t.Fatalf("set cover: %v", err)
		}
		if _, err := repo.SoftDelete(ctx, []int64{article.ID}, 2); err != nil {
			t.Fatalf("soft delete: %v", err)
		}
		override(t, &rowsErrFn, func(*sql.Rows) error { return errors.New("rows failed") })
		if _, err := repo.CoverMediaIDsToDelete(ctx, []int64{article.ID}); err == nil {
			t.Fatal("a failing Rows.Err must surface")
		}
	})

	t.Run("GetByID tag lookup fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		article := &Article{Title: "t", Tags: []string{"Go"}}
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("seed: %v", err)
		}
		mustExec(t, db, `DROP TABLE tags`)
		if _, err := repo.GetByID(ctx, article.ID); err == nil {
			t.Fatal("a failing tag lookup must surface")
		}
	})

	t.Run("List count fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		mustExec(t, db, `ALTER TABLE articles RENAME COLUMN deleted_at TO deleted_at_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("List query fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		mustExec(t, db, `ALTER TABLE articles RENAME COLUMN title TO title_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
	})
}

// TestMomentWriteAndListErrors 覆盖动态仓储的同类失败。
func TestMomentWriteAndListErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("insert fails", func(t *testing.T) {
		db := openTestDB(t)
		mustExec(t, db, `CREATE TRIGGER fail_moment_insert BEFORE INSERT ON moments
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := NewSQLiteMomentRepository(db).Create(ctx, &Moment{Content: "c"}); err == nil {
			t.Fatal("a failing INSERT must surface")
		}
	})

	t.Run("update fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteMomentRepository(db)
		moment := &Moment{Content: "c"}
		if err := repo.Create(ctx, moment); err != nil {
			t.Fatalf("seed: %v", err)
		}
		mustExec(t, db, `CREATE TRIGGER fail_moment_update BEFORE UPDATE ON moments
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := repo.Update(ctx, moment); err == nil {
			t.Fatal("a failing UPDATE must surface")
		}
	})

	t.Run("update of a missing row is not found", func(t *testing.T) {
		repo := NewSQLiteMomentRepository(openTestDB(t))
		if err := repo.Update(ctx, &Moment{ID: 999}); !errors.Is(err, ErrMomentNotFound) {
			t.Fatalf("err = %v, want ErrMomentNotFound", err)
		}
	})

	t.Run("GetByID media lookup fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteMomentRepository(db)
		moment := &Moment{Content: "c"}
		if err := repo.Create(ctx, moment); err != nil {
			t.Fatalf("seed: %v", err)
		}
		mustExec(t, db, `DROP TABLE media`)
		if _, err := repo.GetByID(ctx, moment.ID); err == nil {
			t.Fatal("a failing media lookup must surface")
		}
	})

	t.Run("List count fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteMomentRepository(db)
		mustExec(t, db, `ALTER TABLE moments RENAME COLUMN deleted_at TO deleted_at_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("List query fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteMomentRepository(db)
		mustExec(t, db, `ALTER TABLE moments RENAME COLUMN content TO content_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
	})
}

// TestCommentWriteAndListErrors 覆盖评论仓储的触发器与列改名失败。
func TestCommentWriteAndListErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("insert fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteCommentRepository(db)
		mustExec(t, db, `CREATE TRIGGER fail_comment_insert BEFORE INSERT ON comments
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := repo.Create(ctx, &Comment{
			AuthorType: "admin", AdminName: "站长", TargetType: "article", TargetID: 1, Content: "c",
		}); err == nil {
			t.Fatal("a failing INSERT must surface")
		}
	})

	t.Run("like insert fails", func(t *testing.T) {
		repo := newTestCommentRepo(t)
		seedLiker(t, repo, 1)
		id := addComment(t, repo, 1, nil, "root", 1)
		mustExec(t, repo.db, `CREATE TRIGGER fail_like_insert BEFORE INSERT ON comment_likes
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := repo.AddLike(ctx, id, 1, 2); err == nil {
			t.Fatal("a failing INSERT must surface")
		}
	})

	t.Run("like delete fails", func(t *testing.T) {
		repo := newTestCommentRepo(t)
		seedLiker(t, repo, 1)
		id := addComment(t, repo, 1, nil, "root", 1)
		if err := repo.AddLike(ctx, id, 1, 2); err != nil {
			t.Fatalf("seed like: %v", err)
		}
		mustExec(t, repo.db, `CREATE TRIGGER fail_like_delete BEFORE DELETE ON comment_likes
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := repo.RemoveLike(ctx, id, 1); err == nil {
			t.Fatal("a failing DELETE must surface")
		}
	})

	t.Run("like counter update fails", func(t *testing.T) {
		repo := newTestCommentRepo(t)
		seedLiker(t, repo, 1)
		id := addComment(t, repo, 1, nil, "root", 1)
		if err := repo.AddLike(ctx, id, 1, 2); err != nil {
			t.Fatalf("seed like: %v", err)
		}
		mustExec(t, repo.db, `CREATE TRIGGER fail_comment_count BEFORE UPDATE ON comments
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := repo.RemoveLike(ctx, id, 1); err == nil {
			t.Fatal("a failing UPDATE must surface")
		}
	})

	t.Run("list count fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteCommentRepository(db)
		mustExec(t, db, `ALTER TABLE comments RENAME COLUMN target_type TO target_type_moved`)
		if _, _, err := repo.ListAll(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("list query fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteCommentRepository(db)
		mustExec(t, db, `ALTER TABLE comments RENAME COLUMN content TO content_moved`)
		if _, _, err := repo.ListAll(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
	})
}

// TestPageUserTagMediaEmailWriteAndListErrors 覆盖其余仓储的同类分支。
func TestPageUserTagMediaEmailWriteAndListErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("page insert fails", func(t *testing.T) {
		db := openTestDB(t)
		mustExec(t, db, `CREATE TRIGGER fail_page_insert BEFORE INSERT ON pages
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := NewSQLitePageRepository(db).Create(ctx, &Page{Title: "t"}); err == nil {
			t.Fatal("a failing INSERT must surface")
		}
	})

	t.Run("page list query fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLitePageRepository(db)
		mustExec(t, db, `ALTER TABLE pages RENAME COLUMN title TO title_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
	})

	t.Run("page list count fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLitePageRepository(db)
		mustExec(t, db, `ALTER TABLE pages RENAME COLUMN deleted_at TO deleted_at_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("user insert fails", func(t *testing.T) {
		db := openTestDB(t)
		mustExec(t, db, `CREATE TRIGGER fail_user_insert BEFORE INSERT ON users
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := NewSQLiteUserRepository(db).Create(ctx, &User{Username: "u", Nickname: "U"}); err == nil {
			t.Fatal("a failing INSERT must surface")
		}
	})

	t.Run("user update profile fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteUserRepository(db)
		user := &User{Username: "u", Nickname: "U"}
		if err := repo.Create(ctx, user); err != nil {
			t.Fatalf("seed: %v", err)
		}
		mustExec(t, db, `CREATE TRIGGER fail_user_update BEFORE UPDATE ON users
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := repo.UpdateProfile(ctx, user.ID, "u2", "U2", "u2@example.com", 2); err == nil {
			t.Fatal("a failing UPDATE must surface")
		}
	})

	t.Run("user list query fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteUserRepository(db)
		mustExec(t, db, `ALTER TABLE users RENAME COLUMN username TO username_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
	})

	t.Run("tag list query fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteTagRepository(db)
		mustExec(t, db, `ALTER TABLE tags RENAME COLUMN name TO name_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
	})

	t.Run("tag rename update fails", func(t *testing.T) {
		tagRepo, articleRepo, _, _ := newTestTagRepo(t)
		if err := articleRepo.Create(ctx, &Article{Title: "t", Tags: []string{"Go"}}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
		if err != nil || len(tags) != 1 {
			t.Fatalf("tags = %v, %v", tags, err)
		}
		mustExec(t, tagRepo.db, `CREATE TRIGGER fail_tag_update BEFORE UPDATE ON tags
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := tagRepo.Rename(ctx, tags[0].ID, "Golang", ""); err == nil {
			t.Fatal("a failing UPDATE must surface")
		}
	})

	t.Run("media insert fails", func(t *testing.T) {
		db := openTestDB(t)
		mustExec(t, db, `CREATE TRIGGER fail_media_insert BEFORE INSERT ON media
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		repo := NewSQLiteMediaRepository(db)
		if err := repo.Create(ctx, &Media{
			StorageName: "x.png", OriginalName: "x.png", MediaType: MediaTypeImage, MimeType: "image/png",
		}); err == nil {
			t.Fatal("a failing INSERT must surface")
		}
	})

	t.Run("media list query fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteMediaRepository(db)
		mustExec(t, db, `ALTER TABLE media RENAME COLUMN storage_name TO storage_name_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
	})

	t.Run("email insert fails", func(t *testing.T) {
		db := openTestDB(t)
		mustExec(t, db, `CREATE TRIGGER fail_email_insert BEFORE INSERT ON emails
BEGIN SELECT RAISE(ABORT, 'no'); END`)
		if err := NewSQLiteEmailRepository(db).Create(ctx, &Email{Subject: "s"}); err == nil {
			t.Fatal("a failing INSERT must surface")
		}
	})

	t.Run("email list query fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteEmailRepository(db)
		mustExec(t, db, `ALTER TABLE emails RENAME COLUMN subject TO subject_moved`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
	})
}

// TestMigrationBranches 覆盖管理员凭据迁移的入口分支。
func TestMigrationBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("nil config is a no-op", func(t *testing.T) {
		db := openTestDB(t)
		if err := MigrateAdminCredentials(ctx, db, nil); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("existing administrator wins", func(t *testing.T) {
		db := openTestDB(t)
		if err := NewSQLiteAdminRepository(db).Create(ctx, &User{Username: "admin", Nickname: "Admin"}); err != nil {
			t.Fatalf("seed admin: %v", err)
		}
		cfg := configWithLegacy("hash", "other", "Other")
		if err := MigrateAdminCredentials(ctx, db, cfg); err != nil {
			t.Fatalf("err = %v", err)
		}
		admin, err := NewSQLiteAdminRepository(db).Get(ctx)
		if err != nil || admin.Username != "admin" {
			t.Fatalf("the existing admin must be preserved: %+v %v", admin, err)
		}
	})

	t.Run("missing hash is a no-op", func(t *testing.T) {
		db := openTestDB(t)
		if err := MigrateAdminCredentials(ctx, db, configWithLegacy("", "admin", "Admin")); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("first writer wins when another process inserted", func(t *testing.T) {
		db := openTestDB(t)
		mustExec(t, db, `CREATE TRIGGER fail_admin_insert BEFORE INSERT ON users
BEGIN SELECT RAISE(ABORT, 'UNIQUE constraint failed: users.is_admin'); END`)
		if err := MigrateAdminCredentials(ctx, db, configWithLegacy("hash", "admin", "")); err != nil {
			t.Fatalf("a concurrent insert is not an error: %v", err)
		}
	})

	t.Run("other insert failures surface", func(t *testing.T) {
		db := openTestDB(t)
		mustExec(t, db, `CREATE TRIGGER fail_admin_insert BEFORE INSERT ON users
BEGIN SELECT RAISE(ABORT, 'disk full'); END`)
		if err := MigrateAdminCredentials(ctx, db, configWithLegacy("hash", "", "")); err == nil {
			t.Fatal("a generic insert failure must surface")
		}
	})
}

// configWithLegacy 构造一份只含迁移来源的配置。
func configWithLegacy(hash, username, nickname string) *config.Config {
	cfg := &config.Config{PasswordHash: hash}
	cfg.AdminUsername = username
	cfg.AdminNickname = nickname
	return cfg
}
