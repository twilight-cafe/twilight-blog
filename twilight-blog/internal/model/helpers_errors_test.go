package model

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestReplaceArticleTagsErrors(t *testing.T) {
	ctx := context.Background()

	seed := func(t *testing.T) (*sql.DB, int64) {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		item := &Article{Title: "t", Tags: []string{"Go"}}
		if err := repo.Create(ctx, item); err != nil {
			t.Fatalf("seed article: %v", err)
		}
		return db, item.ID
	}

	t.Run("delete links fails", func(t *testing.T) {
		db, id := seed(t)
		if _, err := db.Exec(`DROP TABLE article_tags`); err != nil {
			t.Fatalf("drop article_tags: %v", err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		if err := replaceArticleTags(ctx, tx, id, nil); err == nil {
			t.Fatal("a failing DELETE must surface")
		}
	})

	t.Run("inserting a tag fails", func(t *testing.T) {
		db, id := seed(t)
		if _, err := db.Exec(`CREATE TRIGGER fail_tag_insert BEFORE INSERT ON tags
BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		if err := replaceArticleTags(ctx, tx, id, []string{"New"}); err == nil {
			t.Fatal("a failing tag INSERT must surface")
		}
	})

	t.Run("selecting the tag id fails", func(t *testing.T) {
		db, id := seed(t)
		// 插入后立刻删掉：INSERT OR IGNORE 成功，但随后的 SELECT 查不到行。
		if _, err := db.Exec(`CREATE TRIGGER vanish_tag AFTER INSERT ON tags
BEGIN DELETE FROM tags WHERE id = NEW.id; END`); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		if err := replaceArticleTags(ctx, tx, id, []string{"Vanish"}); err == nil {
			t.Fatal("a missing tag id must surface")
		}
	})

	t.Run("linking the tag fails", func(t *testing.T) {
		db, id := seed(t)
		if _, err := db.Exec(`CREATE TRIGGER fail_link BEFORE INSERT ON article_tags
BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		if err := replaceArticleTags(ctx, tx, id, []string{"Go"}); err == nil {
			t.Fatal("a failing link INSERT must surface")
		}
	})
}

func TestLoadTagHelpersErrors(t *testing.T) {
	ctx := context.Background()

	type env struct {
		db      *sql.DB
		article *Article
	}
	seed := func(t *testing.T) env {
		db := openTestDB(t)
		repo := NewSQLiteArticleRepository(db)
		article := &Article{Title: "t", Tags: []string{"Go"}}
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("seed: %v", err)
		}
		return env{db: db, article: article}
	}

	t.Run("single article: query fails", func(t *testing.T) {
		e := seed(t)
		if _, err := e.db.Exec(`DROP TABLE tags`); err != nil {
			t.Fatalf("drop tags: %v", err)
		}
		if _, _, err := loadTagsForArticle(ctx, e.db, e.article.ID); err == nil {
			t.Fatal("a failing JOIN must surface")
		}
	})

	t.Run("single article: scan fails", func(t *testing.T) {
		e := seed(t)
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
		if _, _, err := loadTagsForArticle(ctx, e.db, e.article.ID); err == nil {
			t.Fatal("a failing Scan must surface")
		}
	})

	t.Run("single article: iteration fails", func(t *testing.T) {
		e := seed(t)
		override(t, &rowsErrFn, func(*sql.Rows) error { return errors.New("rows failed") })
		if _, _, err := loadTagsForArticle(ctx, e.db, e.article.ID); err == nil {
			t.Fatal("a failing Rows.Err must surface")
		}
	})

	t.Run("batch: query fails", func(t *testing.T) {
		e := seed(t)
		if _, err := e.db.Exec(`DROP TABLE article_tags`); err != nil {
			t.Fatalf("drop article_tags: %v", err)
		}
		if err := loadTagsForArticles(ctx, e.db, []Article{*e.article}); err == nil {
			t.Fatal("a failing JOIN must surface")
		}
	})

	t.Run("batch: scan fails", func(t *testing.T) {
		e := seed(t)
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
		if err := loadTagsForArticles(ctx, e.db, []Article{*e.article}); err == nil {
			t.Fatal("a failing Scan must surface")
		}
	})

	t.Run("batch: iteration fails", func(t *testing.T) {
		e := seed(t)
		override(t, &rowsErrFn, func(*sql.Rows) error { return errors.New("rows failed") })
		if err := loadTagsForArticles(ctx, e.db, []Article{*e.article}); err == nil {
			t.Fatal("a failing Rows.Err must surface")
		}
	})
}

func TestReplaceAndLoadMomentMediaErrors(t *testing.T) {
	ctx := context.Background()

	seed := func(t *testing.T) (*sql.DB, int64, int64) {
		repo, mediaRepo, db := newTestMomentRepo(t)
		mediaID := addMedia(t, mediaRepo, "m.png", 1)
		moment := &Moment{Content: "m", MediaIDs: []int64{mediaID}}
		if err := repo.Create(ctx, moment); err != nil {
			t.Fatalf("seed moment: %v", err)
		}
		return db, moment.ID, mediaID
	}

	t.Run("replace: delete fails", func(t *testing.T) {
		db, id, _ := seed(t)
		if _, err := db.Exec(`DROP TABLE moment_media`); err != nil {
			t.Fatalf("drop moment_media: %v", err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		if err := replaceMomentMedia(ctx, tx, id, nil); err == nil {
			t.Fatal("a failing DELETE must surface")
		}
	})

	t.Run("replace: insert fails", func(t *testing.T) {
		db, id, mediaID := seed(t)
		if _, err := db.Exec(`CREATE TRIGGER fail_moment_media BEFORE INSERT ON moment_media
BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		if err := replaceMomentMedia(ctx, tx, id, []int64{mediaID}); err == nil {
			t.Fatal("a failing INSERT must surface")
		}
	})

	t.Run("load one: query fails", func(t *testing.T) {
		db, id, _ := seed(t)
		if _, err := db.Exec(`DROP TABLE media`); err != nil {
			t.Fatalf("drop media: %v", err)
		}
		if _, err := loadMediaForMoment(ctx, db, id); err == nil {
			t.Fatal("a failing JOIN must surface")
		}
	})

	t.Run("load one: scan fails", func(t *testing.T) {
		db := openTestDB(t)
		mediaRepo := NewSQLiteMediaRepository(db)
		mediaID := addMedia(t, mediaRepo, "x.png", 1)
		momentRepo := NewSQLiteMomentRepository(db)
		moment := &Moment{Content: "m", MediaIDs: []int64{mediaID}}
		if err := momentRepo.Create(ctx, moment); err != nil {
			t.Fatalf("seed moment: %v", err)
		}
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
		if _, err := loadMediaForMoment(ctx, db, moment.ID); err == nil {
			t.Fatal("a failing Scan must surface")
		}
	})

	t.Run("load one: iteration fails", func(t *testing.T) {
		db := openTestDB(t)
		mediaRepo := NewSQLiteMediaRepository(db)
		mediaID := addMedia(t, mediaRepo, "x.png", 1)
		momentRepo := NewSQLiteMomentRepository(db)
		moment := &Moment{Content: "m", MediaIDs: []int64{mediaID}}
		if err := momentRepo.Create(ctx, moment); err != nil {
			t.Fatalf("seed moment: %v", err)
		}
		override(t, &rowsErrFn, func(*sql.Rows) error { return errors.New("rows failed") })
		if _, err := loadMediaForMoment(ctx, db, moment.ID); err == nil {
			t.Fatal("a failing Rows.Err must surface")
		}
	})

	t.Run("batch: query fails", func(t *testing.T) {
		db, id, _ := seed(t)
		if _, err := db.Exec(`DROP TABLE moment_media`); err != nil {
			t.Fatalf("drop moment_media: %v", err)
		}
		if err := loadMediaForMoments(ctx, db, []Moment{{ID: id}}); err == nil {
			t.Fatal("a failing JOIN must surface")
		}
	})

	t.Run("batch: scan fails", func(t *testing.T) {
		db, id, _ := seed(t)
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
		if err := loadMediaForMoments(ctx, db, []Moment{{ID: id}}); err == nil {
			t.Fatal("a failing Scan must surface")
		}
	})

	t.Run("batch: iteration fails", func(t *testing.T) {
		db, id, _ := seed(t)
		override(t, &rowsErrFn, func(*sql.Rows) error { return errors.New("rows failed") })
		if err := loadMediaForMoments(ctx, db, []Moment{{ID: id}}); err == nil {
			t.Fatal("a failing Rows.Err must surface")
		}
	})
}

func TestModelSmallHelpers(t *testing.T) {
	t.Run("HasPassword", func(t *testing.T) {
		if (&User{}).HasPassword() {
			t.Fatal("an empty hash means no password")
		}
		if !(&User{PasswordHash: "hash"}).HasPassword() {
			t.Fatal("a stored hash means the account has a password")
		}
	})

	t.Run("Pagination clamps both bounds", func(t *testing.T) {
		normalized := Pagination{Page: MaxPage + 1, PageSize: MaxPageSize + 1}.Normalized()
		if normalized.Page != MaxPage || normalized.PageSize != MaxPageSize {
			t.Fatalf("normalized = %+v", normalized)
		}
		normalized = Pagination{Page: -5, PageSize: -1}.Normalized()
		if normalized.Page != 1 || normalized.PageSize != DefaultPageSize {
			t.Fatalf("normalized = %+v", normalized)
		}
	})

	t.Run("mediaRow applies a deleted timestamp", func(t *testing.T) {
		var media Media
		raw := mediaRow{deletedAt: sql.NullInt64{Int64: 7, Valid: true}}
		raw.apply(&media)
		if media.DeletedAt == nil || *media.DeletedAt != 7 {
			t.Fatalf("deleted at = %v", media.DeletedAt)
		}
	})

	t.Run("uniqueViolationTarget", func(t *testing.T) {
		cases := []struct {
			err    error
			target string
			ok     bool
		}{
			{nil, "", false},
			{errors.New("disk full"), "", false},
			{errors.New("UNIQUE constraint failed: users.is_admin"), "admin", true},
			{errors.New("UNIQUE constraint failed: users.username"), "username", true},
			{errors.New("UNIQUE constraint failed: users.email"), "email", true},
			{errors.New("UNIQUE constraint failed: something_else"), "", true},
		}
		for _, tc := range cases {
			target, ok := uniqueViolationTarget(tc.err)
			if target != tc.target || ok != tc.ok {
				t.Fatalf("uniqueViolationTarget(%v) = (%q, %t), want (%q, %t)",
					tc.err, target, ok, tc.target, tc.ok)
			}
		}
	})

	t.Run("admin Get surfaces a scan failure", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteAdminRepository(db)
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
		if _, err := repo.Get(context.Background()); err == nil || !strings.Contains(err.Error(), "get administrator") {
			t.Fatalf("err = %v", err)
		}
	})
}
