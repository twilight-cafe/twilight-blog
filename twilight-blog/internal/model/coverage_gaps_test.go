package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 本文件补齐仓储层此前没有任何用例的常规 CRUD / 回收站路径。
// 它们不是安全边界，但都是会被 handler 直接调用、出错时静默丢数据的路径。

func TestEmailRepositoryRoundTrip(t *testing.T) {
	db := openTestDB(t)
	repo := NewSQLiteEmailRepository(db)
	ctx := context.Background()

	email := &Email{
		FromEmail: "from@example.com",
		ToEmail:   "to@example.com",
		Subject:   "主题",
		Body:      "正文",
		SentAt:    150,
		CreatedAt: 100,
	}
	if err := repo.Create(ctx, email); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if email.ID == 0 {
		t.Fatal("Create did not backfill the id")
	}

	if got, err := repo.CountSince(ctx, 100); err != nil || got != 1 {
		t.Fatalf("CountSince(100) = (%d, %v), want (1, nil)", got, err)
	}
	if got, err := repo.CountSince(ctx, 151); err != nil || got != 0 {
		t.Fatalf("CountSince(151) = (%d, %v), want (0, nil)", got, err)
	}

	list, total, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].ID != email.ID || list[0].Subject != "主题" {
		t.Fatalf("List = (%+v, %d), want the inserted email", list, total)
	}

	got, err := repo.GetByID(ctx, email.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ToEmail != "to@example.com" || got.Body != "正文" {
		t.Fatalf("GetByID = %+v", got)
	}
	if _, err := repo.GetByID(ctx, 4242); !errors.Is(err, ErrEmailNotFound) {
		t.Fatalf("GetByID(missing) error = %v, want ErrEmailNotFound", err)
	}
}

func TestPageRepositoryLifecycle(t *testing.T) {
	db := openTestDB(t)
	repo := NewSQLitePageRepository(db)
	ctx := context.Background()

	first := &Page{Title: "a", TitleEN: "a-en", Content: "one", ContentEN: "one-en", CreatedAt: 100, UpdatedAt: 100}
	second := &Page{Title: "b", Content: "two", CreatedAt: 200, UpdatedAt: 200}
	third := &Page{Title: "c", Content: "three", CreatedAt: 300, UpdatedAt: 300}
	for _, page := range []*Page{first, second, third} {
		if err := repo.Create(ctx, page); err != nil {
			t.Fatalf("Create(%q): %v", page.Title, err)
		}
	}

	if _, err := repo.GetByID(ctx, 999); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("GetByID(missing) error = %v, want ErrPageNotFound", err)
	}
	got, err := repo.GetByID(ctx, first.ID)
	if err != nil || got.Title != "a" {
		t.Fatalf("GetByID = (%+v, %v)", got, err)
	}
	// 英文字段随中文一起往返：页面双语不是「只多一列标题」，正文同样要持久化。
	if got.TitleEN != "a-en" || got.ContentEN != "one-en" {
		t.Fatalf("GetByID english = (%q, %q), want (a-en, one-en)", got.TitleEN, got.ContentEN)
	}
	// 只写中文的页面，英文列是空串而不是 NULL，读取侧无需再判空指针。
	if second.TitleEN != "" || second.ContentEN != "" {
		t.Fatalf("default english = (%q, %q), want empty", second.TitleEN, second.ContentEN)
	}

	// Update: 正常更新与回收站行不可更新。
	first.Title = "a2"
	first.TitleEN = "a2-en"
	first.ContentEN = "one2-en"
	first.UpdatedAt = 150
	if err := repo.Update(ctx, first); err != nil {
		t.Fatalf("Update: %v", err)
	}
	updated, err := repo.GetByID(ctx, first.ID)
	if err != nil || updated.TitleEN != "a2-en" || updated.ContentEN != "one2-en" {
		t.Fatalf("after Update english = (%+v, %v)", updated, err)
	}
	if err := repo.Update(ctx, &Page{ID: 999, Title: "missing", UpdatedAt: 1}); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("Update(missing) error = %v, want ErrPageNotFound", err)
	}

	// List: 按 created_at 升序、分页与 total。
	list, total, err := repo.List(ctx, Pagination{Page: 1, PageSize: 2})
	if err != nil || total != 3 || len(list) != 2 {
		t.Fatalf("List = (%v, %d, %v), want 2 of 3", list, total, err)
	}
	if list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("List order = [%d %d], want [%d %d]", list[0].ID, list[1].ID, first.ID, second.ID)
	}
	secondPage, _, err := repo.List(ctx, Pagination{Page: 2, PageSize: 2})
	if err != nil || len(secondPage) != 1 || secondPage[0].ID != third.ID {
		t.Fatalf("List page 2 = (%v, %v), want third only", secondPage, err)
	}

	// SoftDelete: 空集合、幂等与实际行数。
	if n, err := repo.SoftDelete(ctx, nil, 400); err != nil || n != 0 {
		t.Fatalf("SoftDelete(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := repo.SoftDelete(ctx, []int64{second.ID, third.ID}, 400); err != nil || n != 2 {
		t.Fatalf("SoftDelete = (%d, %v), want (2, nil)", n, err)
	}
	if n, err := repo.SoftDelete(ctx, []int64{second.ID}, 500); err != nil || n != 0 {
		t.Fatalf("SoftDelete(again) = (%d, %v), want (0, nil)", n, err)
	}
	if err := repo.Update(ctx, second); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("Update(deleted) error = %v, want ErrPageNotFound", err)
	}
	if _, err := repo.GetByID(ctx, second.ID); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("GetByID(deleted) error = %v, want ErrPageNotFound", err)
	}

	// ListDeleted: 只列回收站，并按 deleted_at 倒序。
	deleted, deletedTotal, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil || deletedTotal != 2 || len(deleted) != 2 {
		t.Fatalf("ListDeleted = (%v, %d, %v), want 2", deleted, deletedTotal, err)
	}
	if deleted[0].ID != third.ID || deleted[1].ID != second.ID {
		t.Fatalf("ListDeleted order = [%d %d], want [%d %d]", deleted[0].ID, deleted[1].ID, third.ID, second.ID)
	}

	// Restore: 活动行恢复失败，回收站行恢复成功且幂等。
	if err := repo.Restore(ctx, first.ID, 600); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("Restore(active) error = %v, want ErrPageNotFound", err)
	}
	if err := repo.Restore(ctx, second.ID, 600); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if err := repo.Restore(ctx, second.ID, 700); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("Restore(again) error = %v, want ErrPageNotFound", err)
	}

	// BatchRestore: 空集合、一次恢复多行、已活动行不再计数。
	if n, err := repo.BatchRestore(ctx, nil, 700); err != nil || n != 0 {
		t.Fatalf("BatchRestore(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := repo.BatchRestore(ctx, []int64{second.ID, third.ID}, 700); err != nil || n != 1 {
		t.Fatalf("BatchRestore = (%d, %v), want (1, nil)", n, err)
	}

	// HardDelete: 活动行删不掉，回收站行才能永久删除。
	if n, err := repo.HardDelete(ctx, nil); err != nil || n != 0 {
		t.Fatalf("HardDelete(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := repo.HardDelete(ctx, []int64{first.ID}); err != nil || n != 0 {
		t.Fatalf("HardDelete(active) = (%d, %v), want (0, nil)", n, err)
	}
	if _, err := repo.SoftDelete(ctx, []int64{third.ID}, 750); err != nil {
		t.Fatalf("SoftDelete before HardDelete: %v", err)
	}
	if n, err := repo.HardDelete(ctx, []int64{third.ID}); err != nil || n != 1 {
		t.Fatalf("HardDelete(deleted) = (%d, %v), want (1, nil)", n, err)
	}
	if err := repo.Restore(ctx, third.ID, 800); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("Restore(hard-deleted) error = %v, want ErrPageNotFound", err)
	}
}

func TestMomentRepositoryRestoreAndListDeleted(t *testing.T) {
	repo, mediaRepo, db := newTestMomentRepo(t)
	ctx := context.Background()

	mediaID := addMedia(t, mediaRepo, "restore.png", 100)
	first := &Moment{Content: "first", MediaIDs: []int64{mediaID}, CreatedAt: 100, UpdatedAt: 100}
	second := &Moment{Content: "second", MediaIDs: []int64{mediaID}, CreatedAt: 200, UpdatedAt: 200}
	for _, moment := range []*Moment{first, second} {
		if err := repo.Create(ctx, moment); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if _, err := repo.SoftDelete(ctx, []int64{first.ID, second.ID}, 300); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	deleted, total, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil || total != 2 || len(deleted) != 2 {
		t.Fatalf("ListDeleted = (%v, %d, %v), want 2 rows", deleted, total, err)
	}
	if !equalIDs(deleted[0].MediaIDs, []int64{mediaID}) {
		t.Fatalf("ListDeleted media ids = %v, want [%d]", deleted[0].MediaIDs, mediaID)
	}
	if deleted[0].ID != second.ID || deleted[1].ID != first.ID {
		t.Fatalf("ListDeleted order = [%d %d], want [%d %d]", deleted[0].ID, deleted[1].ID, second.ID, first.ID)
	}

	if err := repo.Restore(ctx, 999, 400); !errors.Is(err, ErrMomentNotFound) {
		t.Fatalf("Restore(missing) error = %v, want ErrMomentNotFound", err)
	}
	if err := repo.Restore(ctx, first.ID, 400); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if err := repo.Restore(ctx, first.ID, 500); !errors.Is(err, ErrMomentNotFound) {
		t.Fatalf("Restore(active) error = %v, want ErrMomentNotFound", err)
	}

	if n, err := repo.BatchRestore(ctx, nil, 500); err != nil || n != 0 {
		t.Fatalf("BatchRestore(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := repo.BatchRestore(ctx, []int64{first.ID, second.ID}, 500); err != nil || n != 1 {
		t.Fatalf("BatchRestore = (%d, %v), want (1, nil)", n, err)
	}

	// ListDeleted 的空结果和 total。
	if _, err := repo.SoftDelete(ctx, []int64{second.ID}, 600); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if n, err := repo.HardDelete(ctx, []int64{second.ID}); err != nil || n != 1 {
		t.Fatalf("HardDelete = (%d, %v), want (1, nil)", n, err)
	}
	empty, emptyTotal, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil || emptyTotal != 0 || len(empty) != 0 {
		t.Fatalf("ListDeleted(empty) = (%v, %d, %v), want empty", empty, emptyTotal, err)
	}
	_ = db
}

func TestCommentRepositoryUnlikeRestoreAndBatchRestore(t *testing.T) {
	repo := newTestCommentRepo(t)
	ctx := context.Background()
	seedLiker(t, repo, 1)

	id := addComment(t, repo, 1, nil, "root", 100)
	if err := repo.AddLike(ctx, id, 1, 150); err != nil {
		t.Fatalf("AddLike: %v", err)
	}
	if err := repo.RemoveLike(ctx, id, 1); err != nil {
		t.Fatalf("RemoveLike: %v", err)
	}
	if got := countLikes(t, repo, id); got != 0 {
		t.Fatalf("likes after RemoveLike = %d, want 0", got)
	}
	// 再次删除是幂等的，不应把 like_count 压到负数。
	if err := repo.RemoveLike(ctx, id, 1); err != nil {
		t.Fatalf("RemoveLike(again): %v", err)
	}
	if c, err := repo.GetByID(ctx, id); err != nil || c.LikeCount != 0 {
		t.Fatalf("like_count = %d, err=%v, want 0", c.LikeCount, err)
	}

	if err := repo.Restore(ctx, 999, 200); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("Restore(missing) error = %v, want ErrCommentNotFound", err)
	}
	if err := repo.Restore(ctx, id, 200); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("Restore(active) error = %v, want ErrCommentNotFound", err)
	}
	if _, err := repo.SoftDelete(ctx, []int64{id}, 300); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if err := repo.Restore(ctx, id, 400); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if n, err := repo.BatchRestore(ctx, nil, 500); err != nil || n != 0 {
		t.Fatalf("BatchRestore(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if _, err := repo.SoftDelete(ctx, []int64{id}, 500); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if n, err := repo.BatchRestore(ctx, []int64{id, 999}, 600); err != nil || n != 1 {
		t.Fatalf("BatchRestore = (%d, %v), want (1, nil)", n, err)
	}
}

func TestTagRepositoryRename(t *testing.T) {
	tagRepo, articleRepo, _, _ := newTestTagRepo(t)
	ctx := context.Background()

	article := &Article{Title: "t", Content: "c", CreatedAt: 100, UpdatedAt: 100, Tags: []string{"Go", "React"}}
	if err := articleRepo.Create(ctx, article); err != nil {
		t.Fatalf("Create article: %v", err)
	}
	tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil || len(tags) != 2 {
		t.Fatalf("List tags = (%v, %v)", tags, err)
	}
	tagID := tags[0].ID
	otherID := tags[1].ID

	if err := tagRepo.Rename(ctx, 999, "missing", ""); !errors.Is(err, ErrTagNotFound) {
		t.Fatalf("Rename(missing) error = %v, want ErrTagNotFound", err)
	}
	if err := tagRepo.Rename(ctx, tagID, tags[1].Name, ""); !errors.Is(err, ErrTagNameExists) {
		t.Fatalf("Rename(duplicate) error = %v, want ErrTagNameExists", err)
	}
	if err := tagRepo.Rename(ctx, otherID, tags[1].Name, ""); err != nil {
		// Renaming to its own current name is allowed.
		t.Fatalf("Rename(same name) error = %v", err)
	}
	if err := tagRepo.Rename(ctx, tagID, "Golang", ""); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	renamed, err := tagRepo.GetByID(ctx, tagID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if renamed.Name != "Golang" {
		t.Fatalf("renamed tag = %q, want Golang", renamed.Name)
	}
}

var _ = sql.ErrNoRows
