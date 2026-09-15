package model

import (
	"context"
	"database/sql"
	"testing"
)

// 这组测试钉住「文章头图」在持久层的两条契约，两条都属于**静默失败**类型
// ——出错时不会有任何报错，只有数据悄悄不对：
//
//  1. cover_media_id 必须在 Create / Update / GetByID / List 四条路径上完整往返，
//     漏掉任何一条，头图就会在某个入口上凭空消失或复活；
//  2. 永久删除文章时，「哪些头图媒体可以顺手删掉」的判断必须只覆盖**真正没人再用**
//     的图。这道闸一旦放宽，删一篇文章就会连带毁掉别人的图（而且是通过外键的
//     SET NULL / CASCADE 静默发生）。
func newTestArticleRepos(t *testing.T) (*SQLiteArticleRepository, *SQLiteMediaRepository, *SQLiteMomentRepository, *sql.DB) {
	t.Helper()

	db := openTestDB(t)
	return NewSQLiteArticleRepository(db), NewSQLiteMediaRepository(db), NewSQLiteMomentRepository(db), db
}

func TestArticleCoverRoundTripsThroughEveryReadPath(t *testing.T) {
	repo, mediaRepo, _, _ := newTestArticleRepos(t)
	ctx := context.Background()

	cover := addMedia(t, mediaRepo, "cover.png", 100)
	other := addMedia(t, mediaRepo, "other.png", 100)

	article := &Article{Title: "with cover", CreatedAt: 100, UpdatedAt: 100, CoverMediaID: &cover}
	if err := repo.Create(ctx, article); err != nil {
		t.Fatalf("create article with cover: %v", err)
	}

	t.Run("GetByID", func(t *testing.T) {
		stored, err := repo.GetByID(ctx, article.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if stored.CoverMediaID == nil || *stored.CoverMediaID != cover {
			t.Fatalf("cover after GetByID = %v, want %d", stored.CoverMediaID, cover)
		}
	})

	t.Run("List", func(t *testing.T) {
		articles, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(articles) != 1 {
			t.Fatalf("got %d articles, want 1", len(articles))
		}
		if articles[0].CoverMediaID == nil || *articles[0].CoverMediaID != cover {
			t.Fatalf("cover after List = %v, want %d", articles[0].CoverMediaID, cover)
		}
	})

	t.Run("update replaces the cover", func(t *testing.T) {
		stored, err := repo.GetByID(ctx, article.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		stored.CoverMediaID = &other
		stored.UpdatedAt = 200
		if err := repo.Update(ctx, stored); err != nil {
			t.Fatalf("Update: %v", err)
		}
		reloaded, err := repo.GetByID(ctx, article.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if reloaded.CoverMediaID == nil || *reloaded.CoverMediaID != other {
			t.Fatalf("cover after replacing = %v, want %d", reloaded.CoverMediaID, other)
		}
	})

	t.Run("update clears the cover", func(t *testing.T) {
		stored, err := repo.GetByID(ctx, article.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		stored.CoverMediaID = nil
		if err := repo.Update(ctx, stored); err != nil {
			t.Fatalf("Update: %v", err)
		}
		reloaded, err := repo.GetByID(ctx, article.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if reloaded.CoverMediaID != nil {
			t.Fatalf("cover after clearing = %v, want nil", *reloaded.CoverMediaID)
		}
	})
}

// TestArticleWithoutCoverStaysNil 反向守住同一条契约：没有头图的文章读回来
// 必须是 nil，而不是「恰好等于 0 的指针」。id 从 1 开始让 0 看起来安全，
// 但那是隐式知识——真正拦住它的是扫描时的 sql.NullInt64。
func TestArticleWithoutCoverStaysNil(t *testing.T) {
	repo, _, _, _ := newTestArticleRepos(t)
	ctx := context.Background()

	article := &Article{Title: "no cover", CreatedAt: 100, UpdatedAt: 100}
	if err := repo.Create(ctx, article); err != nil {
		t.Fatalf("create article: %v", err)
	}

	stored, err := repo.GetByID(ctx, article.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.CoverMediaID != nil {
		t.Fatalf("cover = %d, want nil", *stored.CoverMediaID)
	}
}

// TestDeletingAMediaRowClearsTheCover 守护建表/补列语句里那句 ON DELETE SET NULL。
//
// 媒体库允许直接删掉任意一条媒体（DELETE /api/admin/media/:id），被删的那张完全
// 可能正被当作某篇文章的头图。没有这条外键动作，文章会留下一个指向不存在行的 id，
// 前台拿它去请求 /api/media/… 只会得到 404——一个坏掉的图位，而不是「没有头图」。
func TestDeletingAMediaRowClearsTheCover(t *testing.T) {
	repo, mediaRepo, _, _ := newTestArticleRepos(t)
	ctx := context.Background()

	cover := addMedia(t, mediaRepo, "gone.png", 100)
	article := &Article{Title: "loses its cover", CreatedAt: 100, UpdatedAt: 100, CoverMediaID: &cover}
	if err := repo.Create(ctx, article); err != nil {
		t.Fatalf("create article: %v", err)
	}

	if err := mediaRepo.Delete(ctx, cover); err != nil {
		t.Fatalf("delete media: %v", err)
	}

	stored, err := repo.GetByID(ctx, article.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.CoverMediaID != nil {
		t.Fatalf("cover = %d after the media was deleted, want nil (ON DELETE SET NULL did not fire)", *stored.CoverMediaID)
	}
}

// TestCoverMediaIDsToDeleteIsConservative 是这组测试里最承重的一条。
//
// 它逐项钉住「哪些头图媒体才算可以删」：
//   - 只有这一篇用它，且这一篇在回收站里 → 可以删；
//   - 还有别的文章（哪怕在回收站里）指着它 → 不删；
//   - 另外那篇文章也在本次删除名单里 → 可以删（它也要没了）；
//   - 文章还在活动态 → 不删（HardDelete 本来也删不掉它，顺手删图就变成了
//     「什么都没删掉却损坏了数据」）；
//   - 动态配图也在用它 → 不删。
//
// 反证方式：把任一 NOT EXISTS 或 deleted_at IS NOT NULL 条件删掉 → 对应子用例失败。
func TestCoverMediaIDsToDeleteIsConservative(t *testing.T) {
	repo, mediaRepo, momentRepo, _ := newTestArticleRepos(t)
	ctx := context.Background()

	// 每个场景用一张独立的图，互不干扰。
	onlyTrashed := addMedia(t, mediaRepo, "only-trashed.png", 100)
	sharedWithActive := addMedia(t, mediaRepo, "shared-active.png", 100)
	sharedWithTrashed := addMedia(t, mediaRepo, "shared-trashed.png", 100)
	usedByMoment := addMedia(t, mediaRepo, "moment.png", 100)
	activeOnly := addMedia(t, mediaRepo, "active-only.png", 100)

	newArticle := func(title string, cover *int64, trashed bool) int64 {
		t.Helper()
		article := &Article{Title: title, CreatedAt: 100, UpdatedAt: 100, CoverMediaID: cover}
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("create article %q: %v", title, err)
		}
		if trashed {
			if _, err := repo.SoftDelete(ctx, []int64{article.ID}, 200); err != nil {
				t.Fatalf("soft delete %q: %v", title, err)
			}
		}
		return article.ID
	}

	sole := newArticle("sole owner, trashed", &onlyTrashed, true)
	shared := newArticle("trashed, shares with an active one", &sharedWithActive, true)
	newArticle("active holder of the same cover", &sharedWithActive, false)

	firstSharers := newArticle("trashed A, shares with trashed B", &sharedWithTrashed, true)
	secondSharer := newArticle("trashed B", &sharedWithTrashed, true)

	withMoment := newArticle("trashed, its cover is a moment photo", &usedByMoment, true)
	moment := &Moment{Content: "uses the same file", MediaIDs: []int64{usedByMoment}, CreatedAt: 100, UpdatedAt: 100}
	if err := momentRepo.Create(ctx, moment); err != nil {
		t.Fatalf("create moment: %v", err)
	}

	untouched := newArticle("active article with a cover", &activeOnly, false)
	noCover := newArticle("trashed without a cover", nil, true)

	t.Run("empty ids yields an empty result", func(t *testing.T) {
		ids, err := repo.CoverMediaIDsToDelete(ctx, nil)
		if err != nil {
			t.Fatalf("CoverMediaIDsToDelete: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("ids = %v, want empty", ids)
		}
		// 非 nil 而不是 nil：这个切片会一路传到调用方的循环里。
		if ids == nil {
			t.Fatal("ids = nil, want an empty slice")
		}
	})

	t.Run("a cover nothing else uses is releasable", func(t *testing.T) {
		ids, err := repo.CoverMediaIDsToDelete(ctx, []int64{sole, noCover})
		if err != nil {
			t.Fatalf("CoverMediaIDsToDelete: %v", err)
		}
		if !equalIDs(ids, []int64{onlyTrashed}) {
			t.Fatalf("ids = %v, want [%d] (the article without a cover must contribute nothing)", ids, onlyTrashed)
		}
	})

	t.Run("a cover another article still points at is kept", func(t *testing.T) {
		ids, err := repo.CoverMediaIDsToDelete(ctx, []int64{shared})
		if err != nil {
			t.Fatalf("CoverMediaIDsToDelete: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("ids = %v, want empty: an active article still uses that media", ids)
		}
	})

	t.Run("deleting every holder at once releases the cover", func(t *testing.T) {
		ids, err := repo.CoverMediaIDsToDelete(ctx, []int64{firstSharers, secondSharer})
		if err != nil {
			t.Fatalf("CoverMediaIDsToDelete: %v", err)
		}
		// DISTINCT：两条行指向同一张图，结果里只能出现一次。
		if !equalIDs(ids, []int64{sharedWithTrashed}) {
			t.Fatalf("ids = %v, want [%d]", ids, sharedWithTrashed)
		}
	})

	t.Run("a cover that a moment uses is kept", func(t *testing.T) {
		ids, err := repo.CoverMediaIDsToDelete(ctx, []int64{withMoment})
		if err != nil {
			t.Fatalf("CoverMediaIDsToDelete: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("ids = %v, want empty: the media is also a moment photo", ids)
		}
	})

	t.Run("an article outside the recycle bin releases nothing", func(t *testing.T) {
		// 这正是 HardDelete 自己的前置条件。少了这一条，对活动文章调用本方法会
		// 返回它的头图，调用方随后删掉那张图——一次什么都没删掉的操作却造成了损坏。
		ids, err := repo.CoverMediaIDsToDelete(ctx, []int64{untouched})
		if err != nil {
			t.Fatalf("CoverMediaIDsToDelete: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("ids = %v, want empty: the article is still active", ids)
		}
	})
}

// TestCoverMediaIDsToDeleteSurfacesAFailedQuery 保证查询失败不会被当成
// 「没有需要清理的图」。两者的后果完全不同：前者必须让永久删除整件事停下来，
// 后者会让调用方继续删文章，从此再也问不出那几篇用了什么图。
//
// 用关掉的数据库制造失败——这是唯一不引入假仓储就能拿到的真实错误。
func TestCoverMediaIDsToDeleteSurfacesAFailedQuery(t *testing.T) {
	repo, _, _, db := newTestArticleRepos(t)

	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	if _, err := repo.CoverMediaIDsToDelete(context.Background(), []int64{1}); err == nil {
		t.Fatal("a failing query must be reported, not swallowed into an empty result")
	}
}
