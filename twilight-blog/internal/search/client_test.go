package search

import (
	"encoding/json"
	"strings"
	"testing"

	"blog-engine/internal/model"
)

// 这组测试守护的是搜索结果的**线上契约**，不是内部实现细节。
//
// 背景：前端 ArticleSearchResult 继承 Article，把 tags 声明为必填，而搜索页
// 直接读 `result.tags.length`。一旦后端在「文章没有标签」时省略该字段，
// 搜索页就会抛 TypeError 并整页白屏（曾经真实发生）。所以这里断言的是
// 序列化之后的 JSON 形状，而不是 Go 结构体的取值——只有前者是前端看到的。
func TestResultFromHitTagsContract(t *testing.T) {
	t.Run("article without tags still serializes an empty array", func(t *testing.T) {
		hit := map[string]any{
			"kind":  "article",
			"id":    float64(7),
			"title": "无标签文章",
		}

		encoded, err := json.Marshal(resultFromHit(hit))
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}

		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}

		raw, ok := decoded["tags"]
		if !ok {
			t.Fatalf("article hit must always carry tags, got json: %s", encoded)
		}
		list, ok := raw.([]any)
		if !ok {
			t.Fatalf("tags must be an array, got %T (%v)", raw, raw)
		}
		if len(list) != 0 {
			t.Fatalf("expected empty tags, got %v", list)
		}
	})

	t.Run("article with tags keeps them in order", func(t *testing.T) {
		hit := map[string]any{
			"kind": "article",
			"id":   float64(1),
			"tags": []any{"alpha", "beta", 42},
		}

		result := resultFromHit(hit)
		if result.Tags == nil {
			t.Fatal("article hit must set Tags")
		}
		if len(*result.Tags) != 2 {
			t.Fatalf("non-string tags must be dropped, got %v", *result.Tags)
		}
		if (*result.Tags)[0] != "alpha" || (*result.Tags)[1] != "beta" {
			t.Fatalf("tag order must be preserved, got %v", *result.Tags)
		}
	})

	t.Run("page hit omits tags entirely", func(t *testing.T) {
		// A page has no tags at all (see pageDoc), so the field must not appear.
		// Emitting `"tags": null` here would be just as wrong as omitting it for
		// an article: the two shapes are documented as different.
		encoded, err := json.Marshal(resultFromHit(map[string]any{
			"kind":  "page",
			"id":    float64(3),
			"title": "关于",
		}))
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		if strings.Contains(string(encoded), `"tags"`) {
			t.Fatalf("page hit must not carry a tags field, got json: %s", encoded)
		}
	})
}

// TestResultFromHitCoverContract 与上面那组同源，守护头图在搜索结果里的形状。
//
// 与 tags 相反，cover_media_id **不带** omitempty：twilight-blog-api 的
// ArticleSearchResult 继承 Article，而 Article 把它声明为必填（`number | null`）。
// 所以「没有头图」的正确形状是 `"cover_media_id": null`，**不是**省略这个字段。
// 反证方式：给 Result.CoverMediaID 加上 omitempty → 第二条用例失败。
func TestResultFromHitCoverContract(t *testing.T) {
	t.Run("an article with a cover carries its id", func(t *testing.T) {
		result := resultFromHit(map[string]any{
			"kind":           "article",
			"id":             float64(7),
			"title":          "带头图",
			"cover_media_id": float64(12),
		})
		if result.CoverMediaID == nil || *result.CoverMediaID != 12 {
			t.Fatalf("CoverMediaID = %v, want 12", result.CoverMediaID)
		}
	})

	t.Run("an article without a cover still emits the field as null", func(t *testing.T) {
		encoded, err := json.Marshal(resultFromHit(map[string]any{
			"kind":  "article",
			"id":    float64(7),
			"title": "无头图",
		}))
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		raw, ok := decoded["cover_media_id"]
		if !ok {
			t.Fatalf("article hits must always carry cover_media_id, got json: %s", encoded)
		}
		if raw != nil {
			t.Fatalf("cover_media_id = %v, want null", raw)
		}
	})

	t.Run("zero is treated as «no cover»", func(t *testing.T) {
		// 媒体的自增 id 从 1 开始，0 永远不是一张真实的图。索引里出现的 0 只可能
		// 来自「字段被显式写成 0」或「文档过期」，两者都应当读成「没有头图」——
		// 而不是让前端去拼一个 /api/media/0。
		result := resultFromHit(map[string]any{
			"kind":           "article",
			"id":             float64(7),
			"cover_media_id": float64(0),
		})
		if result.CoverMediaID != nil {
			t.Fatalf("CoverMediaID = %v, want nil for 0", *result.CoverMediaID)
		}
	})

	t.Run("a page hit also carries a null cover", func(t *testing.T) {
		// 这是刻意的：PageSearchResult 不声明该字段，多一个 null 不会让任何调用方
		// 出错，而为一个类型分支增加一种字段形状的代价更大。
		encoded, err := json.Marshal(resultFromHit(map[string]any{
			"kind":  "page",
			"id":    float64(3),
			"title": "关于",
		}))
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		raw, ok := decoded["cover_media_id"]
		if !ok || raw != nil {
			t.Fatalf("page hits must carry a null cover_media_id, got json: %s", encoded)
		}
	})
}

// TestArticleDocOnlyCarriesACoverWhenThereIsOne 钉住索引文档那一侧：
// 没有头图时**整个字段省略**，而不是写成 0 或 null。
//
// 理由与读取侧不同——Meilisearch 的 POST /documents 是「有则整体替换」，
// 所以把字段从文档里删掉再索引一次，旧值会随之消失；而写一个 0 进去反而会
// 留下一个「看起来像 id 的 0」。两条路径的取舍不同，因此要各自钉住。
//
// 反证方式：把 articleDoc 里的 `if article.CoverMediaID != nil` 去掉、
// 无条件写 doc["cover_media_id"] = 0 → 「没有头图」用例失败。
func TestArticleDocOnlyCarriesACoverWhenThereIsOne(t *testing.T) {
	withoutCover := articleDoc(&model.Article{ID: 1, Title: "无头图", Tags: []string{}})
	if _, exists := withoutCover["cover_media_id"]; exists {
		t.Fatalf("an article without a cover must not carry cover_media_id, got %v", withoutCover["cover_media_id"])
	}

	cover := int64(42)
	withCover := articleDoc(&model.Article{ID: 2, Title: "带头图", Tags: []string{}, CoverMediaID: &cover})
	if got, ok := withCover["cover_media_id"].(int64); !ok || got != 42 {
		t.Fatalf("cover_media_id = %v (%T), want int64(42)", withCover["cover_media_id"], withCover["cover_media_id"])
	}
}

// TestArticleDocNormalizesNilTags 钉住 tags 为 nil 时写成空数组。
// Meilisearch 不接受 null：写成 null 会让索引请求被拒，整篇文章进不了搜索。
func TestArticleDocNormalizesNilTags(t *testing.T) {
	doc := articleDoc(&model.Article{ID: 1, Title: "无标签"}) // Tags 为 nil
	tags, ok := doc["tags"].([]string)
	if !ok {
		t.Fatalf("tags = %v (%T), want []string{}", doc["tags"], doc["tags"])
	}
	if len(tags) != 0 {
		t.Fatalf("tags = %v, want empty", tags)
	}
}

// TestResultFromHitKeepsHighlights 覆盖 _formatted 的搬运：搜索页靠它给命中的
// 片段加高亮，字段名丢了不会有报错，只是高亮静默消失。
func TestResultFromHitKeepsHighlights(t *testing.T) {
	formatted := map[string]any{"title": "<em>Go</em>"}
	result := resultFromHit(map[string]any{
		"kind":       "article",
		"id":         float64(1),
		"_formatted": formatted,
	})
	if result.Highlight["title"] != "<em>Go</em>" {
		t.Fatalf("Highlight = %v, want the _formatted map to be carried over", result.Highlight)
	}

	// 空 map 与缺失同义：都不要产生一个空的 highlight 字段。
	empty := resultFromHit(map[string]any{"kind": "article", "id": float64(1), "_formatted": map[string]any{}})
	if empty.Highlight != nil {
		t.Fatalf("Highlight = %v, want nil for an empty _formatted", empty.Highlight)
	}
}

// TestResultFromHitAIContract 守护搜索结果里的 AI 声明形状。
//
// ArticleSearchResult 继承 Article，而 Article 把 ai_generated 与
// ai_generated_en 都声明为必填布尔。搜索命中若省略它们，类型声明就与实际
// 载荷不符——tags 与 cover_media_id 都踩过同类的坑，这里提前钉住。
func TestResultFromHitAIContract(t *testing.T) {
	t.Run("article keeps both language flags", func(t *testing.T) {
		encoded, err := json.Marshal(resultFromHit(map[string]any{
			"kind":            "article",
			"id":              float64(7),
			"ai_generated":    true,
			"ai_generated_en": false,
		}))
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if decoded["ai_generated"] != true {
			t.Fatalf("ai_generated = %v, want true (json: %s)", decoded["ai_generated"], encoded)
		}
		if decoded["ai_generated_en"] != false {
			t.Fatalf("ai_generated_en = %v, want false (json: %s)", decoded["ai_generated_en"], encoded)
		}
	})

	t.Run("missing or non-boolean flags still serialize as false", func(t *testing.T) {
		encoded, err := json.Marshal(resultFromHit(map[string]any{
			"kind":         "article",
			"id":           float64(7),
			"ai_generated": "yes", // 类型不对：按 false 处理，而不是让整次搜索失败
		}))
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		raw, ok := decoded["ai_generated"]
		if !ok || raw != false {
			t.Fatalf("ai_generated must be present and false, got json: %s", encoded)
		}
		rawEN, ok := decoded["ai_generated_en"]
		if !ok || rawEN != false {
			t.Fatalf("ai_generated_en must be present and false, got json: %s", encoded)
		}
	})

	t.Run("page hit also carries both flags as false", func(t *testing.T) {
		encoded, err := json.Marshal(resultFromHit(map[string]any{
			"kind":  "page",
			"id":    float64(3),
			"title": "关于",
		}))
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if decoded["ai_generated"] != false || decoded["ai_generated_en"] != false {
			t.Fatalf("page hit must carry both flags as false, got json: %s", encoded)
		}
	})
}

// TestArticleDocCarriesBothAIFlags 钉住索引文档那一侧。
//
// 两个标记必须一起进索引：只写中文标记时，英文搜索结果的
// ai_generated_en 会永远读成 false，于是英文界面的 AI 声明静默消失。
func TestArticleDocCarriesBothAIFlags(t *testing.T) {
	doc := articleDoc(&model.Article{ID: 1, AIGenerated: true, AIGeneratedEN: false})
	if doc["ai_generated"] != true {
		t.Fatalf("ai_generated = %v, want true", doc["ai_generated"])
	}
	if doc["ai_generated_en"] != false {
		t.Fatalf("ai_generated_en = %v, want false", doc["ai_generated_en"])
	}
}

// TestSourceInt64AcceptsEveryDecodedNumberType 钉住取数助手的三条解析路径。
//
// 为什么三种数字类型都要吃：同一份数据走的是不同的解码路径——HTTP 响应体经
// encoding/json 解码得到 float64，而带 UseNumber 的解码器给出 json.Number，
// 直接构造的文档（如测试与同步逻辑）则是 int64。少认一种，头图 id 就会静默变成
// 0，而 0 在渲染层等价于「没有头图」——卡片左侧空着，不报任何错。
func TestSourceInt64AcceptsEveryDecodedNumberType(t *testing.T) {
	source := map[string]any{
		"from_float":  float64(42),
		"from_number": json.Number("43"),
		"from_int":    int64(44),
		// 非数字与缺失都归 0：搜索结果的字段类型不该让整个查询失败。
		"from_string": "45",
	}

	cases := []struct {
		key  string
		want int64
	}{
		{"from_float", 42},
		{"from_number", 43},
		{"from_int", 44},
		// 非数字与缺失都归 0。
		{"from_string", 0},
		{"missing", 0},
	}
	for _, testCase := range cases {
		if got := sourceInt64(source, testCase.key); got != testCase.want {
			t.Fatalf("sourceInt64(%q) = %d, want %d", testCase.key, got, testCase.want)
		}
	}
}
