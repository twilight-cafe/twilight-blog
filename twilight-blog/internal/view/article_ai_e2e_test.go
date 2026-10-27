package view_test

import (
	"net/http"
	"strconv"
	"testing"
)

func itoa64(value int64) string { return strconv.FormatInt(value, 10) }

// articleAIResponse is the slice of the article shape these tests care about.
type articleAIResponse struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	AIGenerated   bool   `json:"ai_generated"`
	AIGeneratedEN bool   `json:"ai_generated_en"`
}

// TestAIFlagsArePerLanguageOverHTTP covers the whole path:
// AI 声明按语言独立存储与下发。只标英文时中文版本保持「非 AI」；
// 之后只改英文或只改中文都不会影响另一份。
func TestAIFlagsArePerLanguageOverHTTP(t *testing.T) {
	server, _, _ := newTestServer(t)
	admin := installFreshBlog(t, server)
	headers := admin.unsafe(nil)

	created := admin.call(http.MethodPost, "/api/admin/articles", map[string]any{
		"title":           "手写中文",
		"content":         "中文正文",
		"title_en":        "Translated",
		"content_en":      "English body",
		"ai_generated":    false,
		"ai_generated_en": true,
		"tags":            []string{},
	}, headers)
	expectStatus(t, created, http.StatusCreated, "POST /api/admin/articles")
	var article articleAIResponse
	decodeInto(t, created, &article)
	if article.AIGenerated || !article.AIGeneratedEN {
		t.Fatalf("created flags = zh:%v en:%v, want false/true", article.AIGenerated, article.AIGeneratedEN)
	}

	path := "/api/articles/" + itoa64(article.ID)
	read := admin.call(http.MethodGet, path, nil, nil)
	expectStatus(t, read, http.StatusOK, "GET "+path)
	decodeInto(t, read, &article)
	if article.AIGenerated || !article.AIGeneratedEN {
		t.Fatalf("read flags = zh:%v en:%v, want false/true", article.AIGenerated, article.AIGeneratedEN)
	}

	// 只提英文标记 false：中文本来就是 false，英文被单独摘掉。
	updateEN := admin.call(http.MethodPut, "/api/admin/articles/"+itoa64(article.ID), map[string]any{
		"title":           "手写中文",
		"ai_generated_en": false,
	}, headers)
	expectStatus(t, updateEN, http.StatusOK, "PUT /api/admin/articles/:id (en only)")
	decodeInto(t, updateEN, &article)
	if article.AIGenerated || article.AIGeneratedEN {
		t.Fatalf("after en-only clear = zh:%v en:%v, want false/false", article.AIGenerated, article.AIGeneratedEN)
	}

	// 只把中文置 true：英文仍保持 false。
	updateZH := admin.call(http.MethodPut, "/api/admin/articles/"+itoa64(article.ID), map[string]any{
		"title":        "手写中文",
		"ai_generated": true,
	}, headers)
	expectStatus(t, updateZH, http.StatusOK, "PUT /api/admin/articles/:id (zh only)")
	decodeInto(t, updateZH, &article)
	if !article.AIGenerated || article.AIGeneratedEN {
		t.Fatalf("after zh-only mark = zh:%v en:%v, want true/false", article.AIGenerated, article.AIGeneratedEN)
	}
}
