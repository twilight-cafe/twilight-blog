package model

import (
	"context"
	"database/sql"
	"testing"
)

// newTestTagRepo 返回标签仓储、文章仓储、媒体仓储，以及底层连接
// （关联表没有仓储级读取入口，要数关联行数就得直接走 SQL）。
func newTestTagRepo(t *testing.T) (*SQLiteTagRepository, *SQLiteArticleRepository, *SQLiteMediaRepository, *sql.DB) {
	t.Helper()

	db := openTestDB(t)
	return NewSQLiteTagRepository(db),
		NewSQLiteArticleRepository(db),
		NewSQLiteMediaRepository(db),
		db
}

// TestListArticlesByTagReturnsCoverMediaID 是标签页「相关文章」整块打不开的回归测试。
//
// 事故经过：加头图功能时 scanArticle 从 6 列扩到 7 列（多出 cover_media_id），
// article_repo.go 的 GetByID / List / ListDeleted 三处 SELECT 都同步补了列，
// **只有 tag_repo.go 的 ListArticlesByTag 漏了**。scanArticle 按位置扫描，
// 于是这条查询一执行就报 "expected 6 destination arguments in Scan, not 7"，
// 被 handler 兜成 500，前台的相关文章列表永远停在「文章加载失败」。
//
// 这个缺陷最阴的地方在于它**不被任何静态检查拦住**：SELECT 少一列，SQL 本身完全
// 合法，`go build`、`go vet`、其余仓储方法的测试全绿，只有真的调用这个端点才炸。
// 因此这里不满足于「调用成功」——必须是带 cover_media_id 的数据，否则把新列删掉
// 之后测试照样通过（NULL 少一列也只是数据错，不报错）。
//
// 反证方式：删掉 ListArticlesByTag 的 SELECT 里的 `a.cover_media_id` → 本测试失败。
func TestListArticlesByTagReturnsCoverMediaID(t *testing.T) {
	tagRepo, articleRepo, mediaRepo, _ := newTestTagRepo(t)
	ctx := context.Background()

	cover := addMedia(t, mediaRepo, "cover.png", 100)

	tagged := &Article{
		Title:        "带标签的文章",
		Content:      "正文内容甲",
		CreatedAt:    200,
		UpdatedAt:    200,
		CoverMediaID: &cover,
		Tags:         []string{"Go", "前端"},
	}
	if err := articleRepo.Create(ctx, tagged); err != nil {
		t.Fatalf("create tagged article: %v", err)
	}

	// 同一标签下的另一篇，没有头图也没有其它标签。
	// 它同时承担两个职责：凑出「分页里的第二篇」，以及钉住「无头图读回来是 nil」。
	plain := &Article{
		Title:     "另一篇",
		Content:   "正文内容乙",
		CreatedAt: 100,
		UpdatedAt: 100,
		Tags:      []string{"Go"},
	}
	if err := articleRepo.Create(ctx, plain); err != nil {
		t.Fatalf("create plain article: %v", err)
	}

	// 干扰项：用了同一张头图但**不带**这个标签，绝不能出现在结果里。
	// 少了它，把 SELECT 的 JOIN 条件写错（例如漏掉 at.tag_id = ?）也能通过。
	other := &Article{
		Title:        "不相干的文章",
		Content:      "正文内容丙",
		CreatedAt:    300,
		UpdatedAt:    300,
		CoverMediaID: &cover,
		Tags:         []string{"RSS"},
	}
	if err := articleRepo.Create(ctx, other); err != nil {
		t.Fatalf("create untagged article: %v", err)
	}

	tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List tags: %v", err)
	}
	var tagID int64
	for _, tag := range tags {
		if tag.Name == "Go" {
			tagID = tag.ID
		}
	}
	if tagID == 0 {
		t.Fatalf("no tag named Go in %v", tags)
	}

	articles, total, err := tagRepo.ListArticlesByTag(ctx, tagID, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		// 列数不匹配会在这里以 "expected N destination arguments in Scan, not 7" 出现。
		t.Fatalf("ListArticlesByTag: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(articles) != 2 {
		t.Fatalf("got %d articles, want 2 (the article without this tag must not be listed)", len(articles))
	}

	// 按 created_at 倒序：带标签的文章（200）在前，另一篇（100）在后。
	got := articles[0]
	if got.ID != tagged.ID {
		t.Fatalf("first article id = %d, want %d", got.ID, tagged.ID)
	}
	if got.CoverMediaID == nil {
		t.Fatal("CoverMediaID = nil: the SELECT in ListArticlesByTag no longer returns a.cover_media_id")
	}
	if *got.CoverMediaID != cover {
		t.Fatalf("CoverMediaID = %d, want %d", *got.CoverMediaID, cover)
	}
	if got.Title != tagged.Title {
		t.Fatalf("title = %q, want %q: scanArticle reads by position, so a misaligned column list scrambles the fields", got.Title, tagged.Title)
	}
	if got.Content != tagged.Content {
		t.Fatalf("content = %q, want %q", got.Content, tagged.Content)
	}
	if got.CreatedAt != tagged.CreatedAt || got.UpdatedAt != tagged.UpdatedAt {
		t.Fatalf("timestamps = (%d, %d), want (%d, %d)", got.CreatedAt, got.UpdatedAt, tagged.CreatedAt, tagged.UpdatedAt)
	}
	if got.DeletedAt != nil {
		t.Fatalf("DeletedAt = %d, want nil", *got.DeletedAt)
	}
	if !equalStrings(got.Tags, []string{"Go", "前端"}) {
		t.Fatalf("tags = %v, want [Go 前端] (ascending, case-insensitively)", got.Tags)
	}

	// 没有头图的那篇：nil 而不是「恰好为 0 的指针」。
	if articles[1].ID != plain.ID {
		t.Fatalf("second article id = %d, want %d", articles[1].ID, plain.ID)
	}
	if articles[1].CoverMediaID != nil {
		t.Fatalf("cover = %d on an article without one, want nil", *articles[1].CoverMediaID)
	}
	if !equalStrings(articles[1].Tags, []string{"Go"}) {
		t.Fatalf("tags = %v, want [Go]", articles[1].Tags)
	}
}

// TestListArticlesByTagSkipsDeletedArticles 钉住两处 deleted_at IS NULL：
// 回收站里的文章既不该出现在列表里，也不该计入 total。
//
// 与上一条同源——都是「这个端点没有测试」的后果。少了 total 那条，翻页会算出一个
// 永远点不到内容的页码；少了列表那条，软删的文章会重新出现在前台。
func TestListArticlesByTagSkipsDeletedArticles(t *testing.T) {
	tagRepo, articleRepo, _, _ := newTestTagRepo(t)
	ctx := context.Background()

	kept := &Article{Title: "留下的", Content: "正文", CreatedAt: 100, UpdatedAt: 100, Tags: []string{"Go"}}
	trashed := &Article{Title: "进回收站的", Content: "正文", CreatedAt: 200, UpdatedAt: 200, Tags: []string{"Go"}}
	for _, article := range []*Article{kept, trashed} {
		if err := articleRepo.Create(ctx, article); err != nil {
			t.Fatalf("create article %q: %v", article.Title, err)
		}
	}
	if _, err := articleRepo.SoftDelete(ctx, []int64{trashed.ID}, 300); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List tags: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("got %d tags, want 1", len(tags))
	}
	// 标签的 ArticleCount 与 ListArticlesByTag 的 total 必须同口径，
	// 否则标签云上的数字和点进去看到的篇数对不上。
	if tags[0].ArticleCount != 1 {
		t.Fatalf("tag ArticleCount = %d, want 1", tags[0].ArticleCount)
	}

	articles, total, err := tagRepo.ListArticlesByTag(ctx, tags[0].ID, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListArticlesByTag: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1 (a trashed article must not be counted)", total)
	}
	if len(articles) != 1 || articles[0].ID != kept.ID {
		t.Fatalf("articles = %v, want only %d", articles, kept.ID)
	}
}

// TestListArticlesByTagReportsAMissingTag 钉住 404 语义：标签不存在时必须是
// ErrTagNotFound，而不是「空列表」。前台靠这个错误码区分「这个标签下暂时没文章」
// 与「这个标签根本不存在」，两者要显示的内容完全不同。
func TestListArticlesByTagReportsAMissingTag(t *testing.T) {
	tagRepo, _, _, _ := newTestTagRepo(t)

	articles, total, err := tagRepo.ListArticlesByTag(context.Background(), 999, Pagination{Page: 1, PageSize: 10})
	if err != ErrTagNotFound {
		t.Fatalf("err = %v, want ErrTagNotFound", err)
	}
	if articles != nil || total != 0 {
		t.Fatalf("got (%v, %d), want (nil, 0)", articles, total)
	}
}

// TestListArticlesByTagPaginates 钉住分页边界：LIMIT/OFFSET 绑定的是 p 而不是常量，
// 且 total 是「符合条件的总数」而非「本页条数」——前台靠它算总页数。
func TestListArticlesByTagPaginates(t *testing.T) {
	tagRepo, articleRepo, _, _ := newTestTagRepo(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		article := &Article{
			Title:     string(rune('A' + i)),
			Content:   "正文",
			CreatedAt: int64(100 + i),
			UpdatedAt: int64(100 + i),
			Tags:      []string{"Go"},
		}
		if err := articleRepo.Create(ctx, article); err != nil {
			t.Fatalf("create article %c: %v", 'A'+i, err)
		}
	}

	tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List tags: %v", err)
	}
	tagID := tags[0].ID

	first, total, err := tagRepo.ListArticlesByTag(ctx, tagID, Pagination{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("ListArticlesByTag page 1: %v", err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5 (total counts every match, not just this page)", total)
	}
	if len(first) != 2 {
		t.Fatalf("page 1 has %d articles, want 2", len(first))
	}
	// created_at 倒序：E(104)、D(103)。
	if first[0].Title != "E" || first[1].Title != "D" {
		t.Fatalf("page 1 = [%s %s], want [E D]", first[0].Title, first[1].Title)
	}

	second, total, err := tagRepo.ListArticlesByTag(ctx, tagID, Pagination{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatalf("ListArticlesByTag page 2: %v", err)
	}
	if total != 5 {
		t.Fatalf("total on page 2 = %d, want 5", total)
	}
	if len(second) != 2 || second[0].Title != "C" || second[1].Title != "B" {
		t.Fatalf("page 2 = %v, want [C B]", second)
	}

	// 最后一页只剩一条：OFFSET 越界只是返回更少的行，不是错误。
	third, _, err := tagRepo.ListArticlesByTag(ctx, tagID, Pagination{Page: 3, PageSize: 2})
	if err != nil {
		t.Fatalf("ListArticlesByTag page 3: %v", err)
	}
	if len(third) != 1 || third[0].Title != "A" {
		t.Fatalf("page 3 = %v, want [A]", third)
	}
}

// TestListArticlesByTagReturnsAnEmptySlice 钉住空结果的形状：articles 必须是
// 非 nil 空切片。它会被序列化成 JSON 的 []，nil 会变成 null，前台对 null 调
// .map 就是一次白屏。这也是 handler 层 200 响应的实际形态。
func TestListArticlesByTagReturnsAnEmptySlice(t *testing.T) {
	tagRepo, articleRepo, _, _ := newTestTagRepo(t)
	ctx := context.Background()

	// 造一个「标签存在、但一篇活动文章都没有」的局面：发一篇再软删。
	// 软删只解除「前台可见」，article_tags 的关联行仍然留着，
	// 所以标签本身还在（这也正是回收站能按标签筛选的前提）。
	article := &Article{Title: "唯一的", Content: "正文", CreatedAt: 100, UpdatedAt: 100, Tags: []string{"Go"}}
	if err := articleRepo.Create(ctx, article); err != nil {
		t.Fatalf("create article: %v", err)
	}
	if _, err := articleRepo.SoftDelete(ctx, []int64{article.ID}, 200); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List tags: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("got %d tags, want 1", len(tags))
	}

	articles, total, err := tagRepo.ListArticlesByTag(ctx, tags[0].ID, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListArticlesByTag: %v", err)
	}
	if total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
	if articles == nil {
		t.Fatal("articles = nil, want an empty slice (nil marshals to JSON null, and the page then crashes on .map)")
	}
	if len(articles) != 0 {
		t.Fatalf("articles = %v, want empty", articles)
	}
}

// TestCountByTagMatchesListArticlesByTag 钉住两条独立 SQL 的口径一致。
// CountByTag 走的是自己的 COUNT 语句，与列表的 total 是两份实现——
// 一旦其中一份漏掉 deleted_at IS NULL，标签页的数字就会与列表对不上。
func TestCountByTagMatchesListArticlesByTag(t *testing.T) {
	tagRepo, articleRepo, _, db := newTestTagRepo(t)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		article := &Article{
			Title:     string(rune('a' + i)),
			Content:   "正文",
			CreatedAt: int64(100 + i),
			UpdatedAt: int64(100 + i),
			Tags:      []string{"Go"},
		}
		if err := articleRepo.Create(ctx, article); err != nil {
			t.Fatalf("create article %c: %v", 'a'+i, err)
		}
		if i%2 == 0 {
			if _, err := articleRepo.SoftDelete(ctx, []int64{article.ID}, 500); err != nil {
				t.Fatalf("soft delete: %v", err)
			}
		}
	}

	tags, _, err := tagRepo.List(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List tags: %v", err)
	}
	tagID := tags[0].ID

	count, err := tagRepo.CountByTag(ctx, tagID)
	if err != nil {
		t.Fatalf("CountByTag: %v", err)
	}
	_, total, err := tagRepo.ListArticlesByTag(ctx, tagID, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListArticlesByTag: %v", err)
	}
	if count != total {
		t.Fatalf("CountByTag = %d but ListArticlesByTag total = %d", count, total)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2 (two of the four are trashed)", count)
	}

	if _, err := tagRepo.CountByTag(ctx, 999); err != ErrTagNotFound {
		t.Fatalf("CountByTag on a missing tag = %v, want ErrTagNotFound", err)
	}

	// 直接对数行数，确认两侧都不是「恰好都算错了同一个数」。
	if got := countRows(t, db, `SELECT COUNT(*) FROM article_tags WHERE tag_id = ?`, tagID); got != 4 {
		t.Fatalf("article_tags rows = %d, want 4 (the trashed ones keep their links)", got)
	}
}
