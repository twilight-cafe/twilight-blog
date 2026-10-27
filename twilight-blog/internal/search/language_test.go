package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"blog-engine/internal/model"
)

func TestParseLanguageFallsBackToChinese(t *testing.T) {
	cases := map[string]Language{
		"zh": LanguageChinese,
		"en": LanguageEnglish,
		"":   LanguageChinese,
		"EN": LanguageChinese,
		"fr": LanguageChinese,
	}
	for input, want := range cases {
		if got := ParseLanguage(input); got != want {
			t.Fatalf("ParseLanguage(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestSearchRestrictsMatchingToTheActiveLanguage 钉住两套字段互不命中：
// 请求必须带 attributesToSearchOn，且英文结果也带英文标题/正文/标签。
func TestSearchRestrictsMatchingToTheActiveLanguage(t *testing.T) {
	var seen map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &seen)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": []any{map[string]any{
				"kind": "article", "id": float64(3),
				"title": "中文", "content": "正文",
				"title_en": "English", "content_en": "Body",
				"tags": []any{"Go"}, "tags_en": []any{"Golang"},
				"created_at": float64(1), "updated_at": float64(2),
			}},
			"totalHits": 1,
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	results, total, err := client.Search(context.Background(), "query", 1, 12, LanguageEnglish)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 1 || len(results) != 1 {
		t.Fatalf("results = %v total=%d", results, total)
	}
	if attrs, _ := seen["attributesToSearchOn"].([]any); len(attrs) != 2 || attrs[0] != "title_en" || attrs[1] != "content_en" {
		t.Fatalf("attributesToSearchOn = %v", seen["attributesToSearchOn"])
	}
	got := results[0]
	if got.TitleEN != "English" || got.ContentEN != "Body" {
		t.Fatalf("english fields = (%q, %q)", got.TitleEN, got.ContentEN)
	}
	if got.TagsEN == nil || len(*got.TagsEN) != 1 || (*got.TagsEN)[0] != "Golang" {
		t.Fatalf("tags_en = %v", got.TagsEN)
	}

	// 中文请求只匹配中文两列。
	if _, _, err := client.Search(context.Background(), "query", 1, 12, LanguageChinese); err != nil {
		t.Fatalf("Search(zh): %v", err)
	}
	if attrs, _ := seen["attributesToSearchOn"].([]any); len(attrs) != 2 || attrs[0] != "title" || attrs[1] != "content" {
		t.Fatalf("attributesToSearchOn = %v", seen["attributesToSearchOn"])
	}
}

// TestPageDocIndexesBothLanguages 确认页面英文标题/正文也会进入索引，
// 否则英文界面永远搜不到独立页面。
func TestPageDocIndexesBothLanguages(t *testing.T) {
	doc := pageDoc(&model.Page{ID: 1, Title: "关于", Content: "正文", TitleEN: "About", ContentEN: "Body"})
	if doc["title_en"] != "About" || doc["content_en"] != "Body" {
		t.Fatalf("page doc = %#v", doc)
	}
}
