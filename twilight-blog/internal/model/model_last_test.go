package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 最后一组「不存在 / 空集合 / 单行扫描」分支。它们都是正常业务里常见的结局
// （查不到、没东西可删），只是此前的用例集中在上成功路径。

func TestMissingRowBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("admin missing", func(t *testing.T) {
		repo := NewSQLiteAdminRepository(openTestDB(t))
		if _, err := repo.Get(ctx); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("err = %v, want ErrUserNotFound", err)
		}
	})

	t.Run("article missing", func(t *testing.T) {
		repo := NewSQLiteArticleRepository(openTestDB(t))
		if _, err := repo.GetByID(ctx, 999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("comment missing", func(t *testing.T) {
		repo := NewSQLiteCommentRepository(openTestDB(t))
		if _, err := repo.GetByID(ctx, 999); !errors.Is(err, ErrCommentNotFound) {
			t.Fatalf("err = %v, want ErrCommentNotFound", err)
		}
		if _, err := repo.GetByIDForUser(ctx, 999, nil); !errors.Is(err, ErrCommentNotFound) {
			t.Fatalf("err = %v, want ErrCommentNotFound", err)
		}
	})

	t.Run("moment missing", func(t *testing.T) {
		repo := NewSQLiteMomentRepository(openTestDB(t))
		if _, err := repo.GetByID(ctx, 999); !errors.Is(err, ErrMomentNotFound) {
			t.Fatalf("err = %v, want ErrMomentNotFound", err)
		}
	})

	t.Run("verification missing", func(t *testing.T) {
		repo := NewSQLiteEmailVerificationRepository(openTestDB(t))
		if _, err := repo.FindLatestActiveByEmail(ctx, "none@example.com", 100); !errors.Is(err, ErrVerificationCodeNotFound) {
			t.Fatalf("err = %v, want ErrVerificationCodeNotFound", err)
		}
	})

	t.Run("user missing", func(t *testing.T) {
		repo := NewSQLiteUserRepository(openTestDB(t))
		if _, err := repo.FindByUsername(ctx, "nobody"); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("err = %v", err)
		}
		if _, err := repo.FindByEmail(ctx, "none@example.com"); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("err = %v", err)
		}
		if _, err := repo.GetByID(ctx, 999); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestEmptySetBranches(t *testing.T) {
	ctx := context.Background()
	commentRepo := NewSQLiteCommentRepository(openTestDB(t))
	momentRepo := NewSQLiteMomentRepository(openTestDB(t))
	pageRepo := NewSQLitePageRepository(openTestDB(t))

	if n, err := commentRepo.SoftDelete(ctx, nil, 1); n != 0 || err != nil {
		t.Fatalf("comment SoftDelete(nil) = (%d, %v)", n, err)
	}
	if n, err := commentRepo.BatchRestore(ctx, nil, 1); n != 0 || err != nil {
		t.Fatalf("comment BatchRestore(nil) = (%d, %v)", n, err)
	}
	if n, err := commentRepo.HardDelete(ctx, nil); n != 0 || err != nil {
		t.Fatalf("comment HardDelete(nil) = (%d, %v)", n, err)
	}
	if n, err := momentRepo.BatchRestore(ctx, nil, 1); n != 0 || err != nil {
		t.Fatalf("moment BatchRestore(nil) = (%d, %v)", n, err)
	}
	if n, err := momentRepo.HardDelete(ctx, nil); n != 0 || err != nil {
		t.Fatalf("moment HardDelete(nil) = (%d, %v)", n, err)
	}
	if n, err := pageRepo.SoftDelete(ctx, nil, 1); n != 0 || err != nil {
		t.Fatalf("page SoftDelete(nil) = (%d, %v)", n, err)
	}
	if n, err := pageRepo.BatchRestore(ctx, nil, 1); n != 0 || err != nil {
		t.Fatalf("page BatchRestore(nil) = (%d, %v)", n, err)
	}
	if n, err := pageRepo.HardDelete(ctx, nil); n != 0 || err != nil {
		t.Fatalf("page HardDelete(nil) = (%d, %v)", n, err)
	}
}

func TestCommentListByTargetEmptyShortCircuit(t *testing.T) {
	repo := NewSQLiteCommentRepository(openTestDB(t))
	comments, total, err := repo.ListByTarget(context.Background(), "article", 1, nil, Pagination{Page: 1, PageSize: 10})
	if err != nil || total != 0 || len(comments) != 0 {
		t.Fatalf("ListByTarget(empty) = (%v, %d, %v)", comments, total, err)
	}
}

func TestMomentHappyUpdateAndRowErrors(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := newTestMomentRepo(t)
	moment := &Moment{Content: "before"}
	if err := repo.Create(ctx, moment); err != nil {
		t.Fatalf("seed: %v", err)
	}
	moment.Content = "after"
	if err := repo.Update(ctx, moment); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := repo.GetByID(ctx, moment.ID)
	if err != nil || got.Content != "after" {
		t.Fatalf("GetByID = (%+v, %v)", got, err)
	}

	override(t, &rowsAffectedFn, func(sql.Result) (int64, error) { return 0, errors.New("no count") })
	if err := repo.Update(ctx, moment); err == nil {
		t.Fatal("a failing RowsAffected must surface")
	}
}

func TestEmailListScanAndCountErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("count fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteEmailRepository(db)
		mustExec(t, db, `DROP TABLE emails`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("scan fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteEmailRepository(db)
		if err := repo.Create(ctx, &Email{Subject: "s"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing Scan must surface")
		}
	})
}

func TestTagListAndRenameErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("List count fails", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteTagRepository(db)
		mustExec(t, db, `DROP TABLE tags`)
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("ListArticlesByTag count fails", func(t *testing.T) {
		tagRepo, articleRepo, _, _ := newTestTagRepo(t)
		if err := articleRepo.Create(ctx, &Article{Title: "t", Tags: []string{"Go"}}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
		if err != nil || len(tags) != 1 {
			t.Fatalf("tags = %v, %v", tags, err)
		}
		mustExec(t, tagRepo.db, `ALTER TABLE articles RENAME COLUMN deleted_at TO deleted_at_moved`)
		if _, _, err := tagRepo.ListArticlesByTag(ctx, tags[0].ID, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("ListArticlesByTag query fails", func(t *testing.T) {
		tagRepo, articleRepo, _, _ := newTestTagRepo(t)
		if err := articleRepo.Create(ctx, &Article{Title: "t", Tags: []string{"Go"}}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
		if err != nil || len(tags) != 1 {
			t.Fatalf("tags = %v, %v", tags, err)
		}
		mustExec(t, tagRepo.db, `ALTER TABLE articles RENAME COLUMN title TO title_moved`)
		if _, _, err := tagRepo.ListArticlesByTag(ctx, tags[0].ID, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing list query must surface")
		}
	})

	t.Run("ListArticlesByTag scan fails", func(t *testing.T) {
		tagRepo, articleRepo, _, _ := newTestTagRepo(t)
		if err := articleRepo.Create(ctx, &Article{Title: "t", Tags: []string{"Go"}}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
		if err != nil || len(tags) != 1 {
			t.Fatalf("tags = %v, %v", tags, err)
		}
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
		if _, _, err := tagRepo.ListArticlesByTag(ctx, tags[0].ID, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing Scan must surface")
		}
	})

	t.Run("ListArticlesByTag iteration fails", func(t *testing.T) {
		tagRepo, articleRepo, _, _ := newTestTagRepo(t)
		if err := articleRepo.Create(ctx, &Article{Title: "t", Tags: []string{"Go"}}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
		if err != nil || len(tags) != 1 {
			t.Fatalf("tags = %v, %v", tags, err)
		}
		override(t, &rowsErrFn, func(*sql.Rows) error { return errors.New("rows failed") })
		if _, _, err := tagRepo.ListArticlesByTag(ctx, tags[0].ID, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing Rows.Err must surface")
		}
	})

	t.Run("CountByTag fails", func(t *testing.T) {
		tagRepo, articleRepo, _, _ := newTestTagRepo(t)
		if err := articleRepo.Create(ctx, &Article{Title: "t", Tags: []string{"Go"}}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
		if err != nil || len(tags) != 1 {
			t.Fatalf("tags = %v, %v", tags, err)
		}
		mustExec(t, tagRepo.db, `ALTER TABLE articles RENAME COLUMN deleted_at TO deleted_at_moved`)
		if _, err := tagRepo.CountByTag(ctx, tags[0].ID); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})
}

func TestModelHookCoverageForRemainingBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("comment ListAll count", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteCommentRepository(db)
		mustExec(t, db, `ALTER TABLE comments RENAME COLUMN deleted_at TO deleted_at_moved`)
		if _, _, err := repo.ListAll(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
		}
	})

	t.Run("comment AddLike rows affected", func(t *testing.T) {
		repo := newTestCommentRepo(t)
		seedLiker(t, repo, 1)
		id := addComment(t, repo, 1, nil, "root", 1)
		override(t, &rowsAffectedFn, func(sql.Result) (int64, error) { return 0, errors.New("no count") })
		if err := repo.AddLike(ctx, id, 1, 2); err == nil {
			t.Fatal("a failing RowsAffected must surface")
		}
	})

	t.Run("comment SoftDelete rows affected", func(t *testing.T) {
		repo := newTestCommentRepo(t)
		id := addComment(t, repo, 1, nil, "root", 1)
		override(t, &rowsAffectedFn, func(sql.Result) (int64, error) { return 0, errors.New("no count") })
		if _, err := repo.SoftDelete(ctx, []int64{id}, 2); err == nil {
			t.Fatal("a failing RowsAffected must surface")
		}
	})

	t.Run("moment BatchRestore rows affected", func(t *testing.T) {
		repo, _, _ := newTestMomentRepo(t)
		moment := &Moment{Content: "m"}
		if err := repo.Create(ctx, moment); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := repo.SoftDelete(ctx, []int64{moment.ID}, 2); err != nil {
			t.Fatalf("delete: %v", err)
		}
		override(t, &rowsAffectedFn, func(sql.Result) (int64, error) { return 0, errors.New("no count") })
		if _, err := repo.BatchRestore(ctx, []int64{moment.ID}, 3); err == nil {
			t.Fatal("a failing RowsAffected must surface")
		}
	})

	t.Run("moment HardDelete rows affected", func(t *testing.T) {
		repo, _, _ := newTestMomentRepo(t)
		moment := &Moment{Content: "m"}
		if err := repo.Create(ctx, moment); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := repo.SoftDelete(ctx, []int64{moment.ID}, 2); err != nil {
			t.Fatalf("delete: %v", err)
		}
		override(t, &rowsAffectedFn, func(sql.Result) (int64, error) { return 0, errors.New("no count") })
		if _, err := repo.HardDelete(ctx, []int64{moment.ID}); err == nil {
			t.Fatal("a failing RowsAffected must surface")
		}
	})

	t.Run("moment media batch scan", func(t *testing.T) {
		db := openTestDB(t)
		mediaRepo := NewSQLiteMediaRepository(db)
		mediaID := addMedia(t, mediaRepo, "m.png", 1)
		momentRepo := NewSQLiteMomentRepository(db)
		moment := &Moment{Content: "m", MediaIDs: []int64{mediaID}}
		if err := momentRepo.Create(ctx, moment); err != nil {
			t.Fatalf("seed: %v", err)
		}
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
		if err := loadMediaForMoments(ctx, db, []Moment{{ID: moment.ID}}); err == nil {
			t.Fatal("a failing Scan must surface")
		}
	})

	t.Run("page SoftDelete rows affected", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLitePageRepository(db)
		page := &Page{Title: "p"}
		if err := repo.Create(ctx, page); err != nil {
			t.Fatalf("seed: %v", err)
		}
		override(t, &rowsAffectedFn, func(sql.Result) (int64, error) { return 0, errors.New("no count") })
		if _, err := repo.SoftDelete(ctx, []int64{page.ID}, 2); err == nil {
			t.Fatal("a failing RowsAffected must surface")
		}
	})

	t.Run("moment ListDeleted iteration", func(t *testing.T) {
		repo, _, _ := newTestMomentRepo(t)
		moment := &Moment{Content: "m"}
		if err := repo.Create(ctx, moment); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := repo.SoftDelete(ctx, []int64{moment.ID}, 2); err != nil {
			t.Fatalf("delete: %v", err)
		}
		override(t, &rowsErrFn, func(*sql.Rows) error { return errors.New("rows failed") })
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing Rows.Err must surface")
		}
	})

	t.Run("page List scan", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLitePageRepository(db)
		if err := repo.Create(ctx, &Page{Title: "p"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
		if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing Scan must surface")
		}
	})

	t.Run("page ListDeleted count", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLitePageRepository(db)
		mustExec(t, db, `ALTER TABLE pages RENAME COLUMN deleted_at TO deleted_at_moved`)
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing COUNT must surface")
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
		override(t, &scanValuesFn, func(s scanner, dest ...any) error {
			_ = s.Scan(dest...)
			return errors.New("scan failed")
		})
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing Scan must surface")
		}
	})

	t.Run("page ListDeleted iteration", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLitePageRepository(db)
		page := &Page{Title: "p"}
		if err := repo.Create(ctx, page); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := repo.SoftDelete(ctx, []int64{page.ID}, 2); err != nil {
			t.Fatalf("delete: %v", err)
		}
		override(t, &rowsErrFn, func(*sql.Rows) error { return errors.New("rows failed") })
		if _, _, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("a failing Rows.Err must surface")
		}
	})

	t.Run("tag count hooks", func(t *testing.T) {
		tagRepo, articleRepo, _, _ := newTestTagRepo(t)
		if err := articleRepo.Create(ctx, &Article{Title: "t", Tags: []string{"Go"}}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
		if err != nil || len(tags) != 1 {
			t.Fatalf("tags = %v, %v", tags, err)
		}
		override(t, &countArticlesByTagFn, func(context.Context, queryRunner, int64) (int64, error) {
			return 0, errors.New("count failed")
		})
		if _, _, err := tagRepo.ListArticlesByTag(ctx, tags[0].ID, Pagination{Page: 1, PageSize: 10}); err == nil {
			t.Fatal("ListArticlesByTag must surface a count error")
		}
		if _, err := tagRepo.CountByTag(ctx, tags[0].ID); err == nil {
			t.Fatal("CountByTag must surface a count error")
		}
	})

	t.Run("tag duplicate check hook", func(t *testing.T) {
		tagRepo, articleRepo, _, _ := newTestTagRepo(t)
		if err := articleRepo.Create(ctx, &Article{Title: "t", Tags: []string{"Go"}}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
		if err != nil || len(tags) != 1 {
			t.Fatalf("tags = %v, %v", tags, err)
		}
		override(t, &tagNameOwnerFn, func(context.Context, queryRunner, string, int64) (int64, error) {
			return 0, errors.New("check failed")
		})
		if err := tagRepo.Rename(ctx, tags[0].ID, "Golang", ""); err == nil {
			t.Fatal("Rename must surface a duplicate-check error")
		}
	})

	t.Run("tag helper functions fail", func(t *testing.T) {
		db := openTestDB(t)
		mustExec(t, db, `DROP TABLE article_tags`)
		if _, err := countArticlesByTag(ctx, db, 1); err == nil {
			t.Fatal("countArticlesByTag must fail without its join table")
		}
		mustExec(t, db, `DROP TABLE tags`)
		if _, err := tagNameOwner(ctx, db, "Go", 1); err == nil {
			t.Fatal("tagNameOwner must fail without its table")
		}
	})

	t.Run("user classification hooks", func(t *testing.T) {
		db := openTestDB(t)
		repo := NewSQLiteUserRepository(db)
		user := &User{Username: "u", Nickname: "U", Email: "u@example.com"}
		if err := repo.Create(ctx, user); err != nil {
			t.Fatalf("seed: %v", err)
		}
		override(t, &rowsAffectedFn, func(sql.Result) (int64, error) { return 0, nil })

		override(t, &classifyUpdateTargetFn, func(context.Context, *sql.DB, int64) (int, error) {
			return 0, errors.New("classify failed")
		})
		if err := repo.UpdateProfile(ctx, user.ID, "u2", "U2", "u2@example.com", 2); err == nil {
			t.Fatal("UpdateProfile must surface a classification error")
		}

		override(t, &classifyUpdateTargetFn, func(context.Context, *sql.DB, int64) (int, error) {
			return 1, nil
		})
		if err := repo.UpdateProfile(ctx, user.ID, "u3", "U3", "u3@example.com", 3); !errors.Is(err, ErrNotAVisitorAccount) {
			t.Fatalf("err = %v, want ErrNotAVisitorAccount", err)
		}

		override(t, &classifyUpdateTargetFn, func(context.Context, *sql.DB, int64) (int, error) {
			return 0, nil
		})
		if err := repo.UpdateProfile(ctx, user.ID, "u4", "U4", "u4@example.com", 4); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("err = %v, want ErrUserNotFound", err)
		}
	})
}

func TestUserUpdateProfileClassification(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := NewSQLiteUserRepository(db)
	user := &User{Username: "u", Nickname: "U", Email: "u@example.com"}
	if err := repo.Create(ctx, user); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// affected == 0 但行存在：分类查询必须把它当成「不可达的保守失败」。
	override(t, &rowsAffectedFn, func(sql.Result) (int64, error) { return 0, nil })
	if err := repo.UpdateProfile(ctx, user.ID, "u2", "U2", "u2@example.com", 2); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}

	// 分类查询本身失败。
	db2 := openTestDB(t)
	repo2 := NewSQLiteUserRepository(db2)
	user2 := &User{Username: "u", Nickname: "U", Email: "u@example.com"}
	if err := repo2.Create(ctx, user2); err != nil {
		t.Fatalf("seed: %v", err)
	}
	override(t, &rowsAffectedFn, func(sql.Result) (int64, error) { return 0, nil })
	mustExec(t, db2, `ALTER TABLE users RENAME COLUMN is_admin TO is_admin_moved`)
	if err := repo2.UpdateProfile(ctx, user2.ID, "u3", "U3", "u3@example.com", 3); err == nil {
		t.Fatal("a failing classification query must surface")
	}
}

func TestUserListCountFailure(t *testing.T) {
	db := openTestDB(t)
	repo := NewSQLiteUserRepository(db)
	mustExec(t, db, `ALTER TABLE users RENAME COLUMN is_admin TO is_admin_moved`)
	if _, _, err := repo.List(context.Background(), Pagination{Page: 1, PageSize: 10}); err == nil {
		t.Fatal("a failing COUNT must surface")
	}
}
