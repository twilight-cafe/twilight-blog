package model

import (
	"context"
	"database/sql"
	"testing"
)

func newTestArticleRepo(t *testing.T) (*SQLiteArticleRepository, *sql.DB) {
	t.Helper()

	db := openTestDB(t)
	return NewSQLiteArticleRepository(db), db
}

// TestAIGeneratedFlagsRoundTripPerLanguage 钉住两列各自独立：手写中文 + AI 翻译
// 英文时数据库里必须是 false/true，更新后也必须能分别改回来。
func TestAIGeneratedFlagsRoundTripPerLanguage(t *testing.T) {
	repo, _ := newTestArticleRepo(t)
	ctx := context.Background()

	article := &Article{
		Title: "双语", Content: "正文", TitleEN: "Bilingual", ContentEN: "body",
		AIGenerated: false, AIGeneratedEN: true, CreatedAt: 1, UpdatedAt: 1,
	}
	if err := repo.Create(ctx, article); err != nil {
		t.Fatalf("Create: %v", err)
	}

	stored, err := repo.GetByID(ctx, article.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.AIGenerated || !stored.AIGeneratedEN {
		t.Fatalf("flags after create = zh:%v en:%v, want false/true", stored.AIGenerated, stored.AIGeneratedEN)
	}

	stored.AIGenerated = true
	stored.AIGeneratedEN = false
	if err := repo.Update(ctx, stored); err != nil {
		t.Fatalf("Update: %v", err)
	}
	updated, err := repo.GetByID(ctx, article.ID)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if !updated.AIGenerated || updated.AIGeneratedEN {
		t.Fatalf("flags after update = zh:%v en:%v, want true/false", updated.AIGenerated, updated.AIGeneratedEN)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestListAssemblesTagsForAllArticles 覆盖批量装配改成一次 IN 查询后的分组正确性。
//
// 三个点：每篇只拿到自己的标签；标签按名称排序（不区分大小写）；没有标签的文章
// 拿到的是**非 nil 空切片**而不是 nil——Tags 会直接序列化成 JSON，nil 是 null。
func TestListAssemblesTagsForAllArticles(t *testing.T) {
	repo, db := newTestArticleRepo(t)
	ctx := context.Background()

	for _, article := range []*Article{
		{Title: "two tags", Tags: []string{"Go", "RSS"}, CreatedAt: 100, UpdatedAt: 100},
		{Title: "one tag", Tags: []string{"go"}, CreatedAt: 200, UpdatedAt: 200},
		{Title: "no tag", CreatedAt: 300, UpdatedAt: 300},
	} {
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("create article %q: %v", article.Title, err)
		}
	}

	articles, total, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(articles) != 3 {
		t.Fatalf("got %d articles, want 3", len(articles))
	}

	// List 按 created_at 倒序：no tag、one tag、two tags。
	if articles[0].Tags == nil {
		t.Fatal("untagged article got nil Tags, want an empty slice (nil marshals to JSON null)")
	}
	if !equalStrings(articles[0].Tags, []string{}) {
		t.Fatalf("untagged article tags = %v, want []", articles[0].Tags)
	}
	// 第二篇写的是 "go"，但 tags.name 是 COLLATE NOCASE UNIQUE，所以它命中的
	// 是已有的那行、名字仍是 "Go"。这条断言把该语义钉住。
	if !equalStrings(articles[1].Tags, []string{"Go"}) {
		t.Fatalf("second article tags = %v, want [Go]", articles[1].Tags)
	}
	if !equalStrings(articles[2].Tags, []string{"Go", "RSS"}) {
		t.Fatalf("third article tags = %v, want [Go RSS]", articles[2].Tags)
	}

	// 大小写不同不应产生第二行标签。
	if got := countRows(t, db, `SELECT COUNT(*) FROM tags`); got != 2 {
		t.Fatalf("tags rows = %d, want 2 (Go and RSS)", got)
	}
}
