package viewmodel

import (
	"encoding/json"
	"testing"

	"blog-engine/internal/model"
)

// TestArticleResponseKeepsAIFlagsPerLanguage pins the wire shape:
// 两个字段恒出现，且各自独立——中文标记不会被英文标记带成 true，反之亦然。
func TestArticleResponseKeepsAIFlagsPerLanguage(t *testing.T) {
	cases := []struct {
		name   string
		zh, en bool
	}{
		{name: "only english", zh: false, en: true},
		{name: "only chinese", zh: true, en: false},
		{name: "both", zh: true, en: true},
		{name: "neither", zh: false, en: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(NewArticleResponse(&model.Article{
				ID:            1,
				Title:         "手写中文",
				AIGenerated:   tc.zh,
				AIGeneratedEN: tc.en,
			}))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var body map[string]any
			if err := json.Unmarshal(encoded, &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			want := map[string]bool{"ai_generated": tc.zh, "ai_generated_en": tc.en}
			for key, expected := range want {
				if got, ok := body[key]; !ok || got != expected {
					t.Fatalf("%s = %v (present=%v), want %v", key, got, ok, expected)
				}
			}
		})
	}
}
