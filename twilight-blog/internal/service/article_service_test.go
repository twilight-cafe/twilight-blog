package service

import (
	"context"
	"path/filepath"
	"testing"

	"blog-engine/internal/database"
	"blog-engine/internal/model"
)

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

	page, err := svc.Create(ctx, "关于", body)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := svc.Update(ctx, page.ID, "关于我们", nil); err != nil {
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
	if _, err := svc.Update(ctx, page.ID, "关于我们", &replacement); err != nil {
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
