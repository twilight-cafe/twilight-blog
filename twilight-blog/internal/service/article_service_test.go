package service

import (
	"context"
	"path/filepath"
	"testing"

	"blog-engine/internal/database"
	"blog-engine/internal/model"
)

// TestAIMarkerIsPerLanguage 覆盖 AI 声明的按语言语义：
// 中文与英文各存各的，创建 / 更新其中一份不会影响另一份。
func TestAIMarkerIsPerLanguage(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(filepath.Join(t.TempDir(), "article-ai.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repo := model.NewSQLiteArticleRepository(db)
	svc := NewArticleService(repo, nil, nil)
	body := "手写中文"
	yes := true
	no := false

	// 创建时只标英文：中文仍是人工撰写。
	article, err := svc.Create(ctx, ArticleWrite{
		Title: "手写", Content: &body,
		AIGeneratedEN: &yes,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if article.AIGenerated || !article.AIGeneratedEN {
		t.Fatalf("create flags = zh:%v en:%v, want false/true", article.AIGenerated, article.AIGeneratedEN)
	}

	// 只把英文改回 false：中文本来就是 false，另一份不受影响。
	updated, err := svc.Update(ctx, article.ID, ArticleWrite{Title: "手写", AIGeneratedEN: &no})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.AIGenerated || updated.AIGeneratedEN {
		t.Fatalf("after en-only clear = zh:%v en:%v, want false/false", updated.AIGenerated, updated.AIGeneratedEN)
	}

	// 只把中文置 true：英文保持 false。
	updated, err = svc.Update(ctx, article.ID, ArticleWrite{Title: "手写", AIGenerated: &yes})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !updated.AIGenerated || updated.AIGeneratedEN {
		t.Fatalf("after zh-only mark = zh:%v en:%v, want true/false", updated.AIGenerated, updated.AIGeneratedEN)
	}

	// 再只把英文置 true：中文仍为 true。
	updated, err = svc.Update(ctx, article.ID, ArticleWrite{Title: "手写", AIGeneratedEN: &yes})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !updated.AIGenerated || !updated.AIGeneratedEN {
		t.Fatalf("after en-only mark = zh:%v en:%v, want true/true", updated.AIGenerated, updated.AIGeneratedEN)
	}

	// 创建路径只给中文标记时，英文保持未标记。
	chineseOnly, err := svc.Create(ctx, ArticleWrite{Title: "仅中文", AIGenerated: &yes})
	if err != nil {
		t.Fatalf("Create zh-only: %v", err)
	}
	if !chineseOnly.AIGenerated || chineseOnly.AIGeneratedEN {
		t.Fatalf("create zh-only flags = zh:%v en:%v, want true/false", chineseOnly.AIGenerated, chineseOnly.AIGeneratedEN)
	}
}

// TestUpdateKeepsTheBodyWhenContentIsOmitted 守护 R3-15 的承重契约：
// **省略 content 的更新不得改动正文**。
//
// 这类缺陷的特点是「静默」——请求返回 200、日志里没有任何异常、审计上看不出
// 谁把正文清空了，只有读者会看到一篇空文章。所以断言必须落在**库里的正文**，
// 而不是返回值：先写一段独有标记的正文，改完标题后再从库里读回来。
//
// 反证方式：把 `if content != nil` 去掉、无条件赋值 → 断言立刻失败。
func TestUpdateKeepsTheBodyWhenContentIsOmitted(t *testing.T) {
	ctx := context.Background()

	db, err := database.Open(filepath.Join(t.TempDir(), "articles.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repo := model.NewSQLiteArticleRepository(db)
	svc := NewArticleService(repo, nil, nil) // nil = 该部署未启用搜索、也不校验头图
	body := "正文标记-不许消失"

	article, err := svc.Create(ctx, ArticleWrite{Title: "原标题", Content: &body, Tags: &[]string{"t"}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	t.Run("title-only update keeps the body", func(t *testing.T) {
		// 只给标题：Content/Tags 都留 nil（= 这次不提这两个字段），PublishedAt 同理，
		// CoverSet 为 false（= 不动头图）。这正是「只改标题」的 PUT。
		if _, err := svc.Update(ctx, article.ID, ArticleWrite{Title: "新标题"}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		stored, err := repo.GetByID(ctx, article.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if stored.Title != "新标题" {
			t.Fatalf("title = %q, want 新标题", stored.Title)
		}
		if stored.Content != body {
			t.Fatalf("content = %q, want %q — an update that omits content must not touch the body", stored.Content, body)
		}
		// 标签同样没传，因此也不该被动过。
		if len(stored.Tags) != 1 || stored.Tags[0] != "t" {
			t.Fatalf("tags = %v, want [t]", stored.Tags)
		}
	})

	t.Run("an explicit body replaces it", func(t *testing.T) {
		replacement := "换掉正文"
		if _, err := svc.Update(ctx, article.ID, ArticleWrite{Title: "新标题", Content: &replacement}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		stored, err := repo.GetByID(ctx, article.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if stored.Content != replacement {
			t.Fatalf("content = %q, want %q", stored.Content, replacement)
		}
	})

	t.Run("an explicit empty body is honoured", func(t *testing.T) {
		// 显式传空串是调用方的明确意图，与「省略」不同，因此必须生效。
		// 若哪天有人把它也当成「不改」，这条会失败——那是有意的信号：
		// 那等于把「清空」变成一个无法表达的操作。
		empty := ""
		if _, err := svc.Update(ctx, article.ID, ArticleWrite{Title: "新标题", Content: &empty}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		stored, err := repo.GetByID(ctx, article.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if stored.Content != "" {
			t.Fatalf("content = %q, want empty (an explicit empty value must be applied)", stored.Content)
		}
	})
}

// TestPageUpdateKeepsTheBodyWhenContentIsOmitted 与文章同一条契约，独立覆盖页面：
// 两处是各自实现的，只测一处的话，另一处退化成非指针不会被发现。
func TestPageUpdateKeepsTheBodyWhenContentIsOmitted(t *testing.T) {
	ctx := context.Background()

	db, err := database.Open(filepath.Join(t.TempDir(), "pages.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repo := model.NewSQLitePageRepository(db)
	svc := NewPageService(repo, nil)
	const body = "页面正文标记-不许消失"

	page, err := svc.Create(ctx, "关于", "", body, "")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := svc.Update(ctx, page.ID, "关于我们", nil, nil, nil); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	stored, err := repo.GetByID(ctx, page.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.Title != "关于我们" {
		t.Fatalf("title = %q, want 关于我们", stored.Title)
	}
	if stored.Content != body {
		t.Fatalf("content = %q, want %q — an update that omits content must not touch the body", stored.Content, body)
	}

	replacement := "新的页面正文"
	if _, err := svc.Update(ctx, page.ID, "关于我们", nil, &replacement, nil); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	stored, err = repo.GetByID(ctx, page.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.Content != replacement {
		t.Fatalf("content = %q, want %q", stored.Content, replacement)
	}
}

// TestPageBilingualFieldsFollowTheArticleContract 验证页面的英文字段与文章
// 使用同一套三态语义：省略 = 保留，空串 = 清空。分开覆盖是因为两处实现独立，
// 只测文章的话，页面的指针分支可能默默退化成「缺省即清空」。
func TestPageBilingualFieldsFollowTheArticleContract(t *testing.T) {
	ctx := context.Background()

	db, err := database.Open(filepath.Join(t.TempDir(), "bilingual-pages.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repo := model.NewSQLitePageRepository(db)
	svc := NewPageService(repo, nil)

	page, err := svc.Create(ctx, "关于", "About", "中文正文", "English body")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if page.TitleEN != "About" || page.ContentEN != "English body" {
		t.Fatalf("created english = (%q, %q)", page.TitleEN, page.ContentEN)
	}

	// 只改中文标题：英文两份都必须原样保留。
	if _, err := svc.Update(ctx, page.ID, "关于我们", nil, nil, nil); err != nil {
		t.Fatalf("Update(title only) error = %v", err)
	}
	stored, err := repo.GetByID(ctx, page.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.TitleEN != "About" || stored.ContentEN != "English body" {
		t.Fatalf("english after title-only update = (%q, %q)", stored.TitleEN, stored.ContentEN)
	}

	// 显式清空英文标题（空串），英文正文保持不变：两份字段的指针互相独立。
	empty := ""
	if _, err := svc.Update(ctx, page.ID, "关于我们", &empty, nil, nil); err != nil {
		t.Fatalf("Update(clear title_en) error = %v", err)
	}
	stored, err = repo.GetByID(ctx, page.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.TitleEN != "" || stored.ContentEN != "English body" {
		t.Fatalf("after clearing title_en = (%q, %q)", stored.TitleEN, stored.ContentEN)
	}

	// 只替换英文正文。
	translated := "Rewritten body"
	if _, err := svc.Update(ctx, page.ID, "关于我们", nil, nil, &translated); err != nil {
		t.Fatalf("Update(content_en) error = %v", err)
	}
	stored, err = repo.GetByID(ctx, page.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.Content != "中文正文" || stored.ContentEN != translated {
		t.Fatalf("bodies = (%q, %q), want (中文正文, %q)", stored.Content, stored.ContentEN, translated)
	}
}
