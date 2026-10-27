package viewmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"blog-engine/internal/model"
)

// 这组测试守护两个**新入口字段**在 JSON 边界上的形状：头图（cover_media_id）
// 与发布时间（published_at）。
//
// 共同点是它们都能被「无害的输入」写坏：
//   - 头图塌成两态 → 「只改标题」会清空头图，或者用户摘掉的图在保存后复活；
//   - 发布时间放过一个毫秒值 → 前台把文章显示成公元 55835 年，而没有任何一步报错。
//
// 所以断言都打在**反序列化 / 序列化之后的形状**上，而不是 Go 结构体的取值：
// 只有形状是前端实际看到的那一份。

// TestUpdateArticleCoverIsThreeState 钉住 OptionalID 的三态。
//
// 为什么 *int64 不够：encoding/json 把「字段缺失」与 `null` 都只把指针留成 nil，
// 两种输入在反序列化之后**完全不可区分**。塌缩的方向无论倒向哪边都是数据损坏。
//
// 反证方式：把 CoverMediaID 的类型改成 *int64 并删掉 UnmarshalJSON →
// 「explicit null」与「omitted」两个用例会得到同样的结果，本条失败。
func TestUpdateArticleCoverIsThreeState(t *testing.T) {
	t.Run("an omitted field is not an instruction", func(t *testing.T) {
		var req UpdateArticleRequest
		if err := json.Unmarshal([]byte(`{"title":"只改标题"}`), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if req.CoverMediaID.Set {
			t.Fatal("an omitted cover_media_id must leave Set false, otherwise a title-only save would clear the cover")
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("a title-only update must be valid: %v", err)
		}
	})

	t.Run("an explicit null is an instruction to clear", func(t *testing.T) {
		var req UpdateArticleRequest
		if err := json.Unmarshal([]byte(`{"title":"摘掉头图","cover_media_id":null}`), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !req.CoverMediaID.Set {
			t.Fatal(`an explicit null must set Set=true — otherwise "clear the cover" cannot be expressed`)
		}
		if req.CoverMediaID.Value != nil {
			t.Fatalf("cover value = %v, want nil", req.CoverMediaID.Value)
		}
		// 「摘掉」是合法意图，不能被正数校验拦下。
		if err := req.Validate(); err != nil {
			t.Fatalf("clearing the cover must be valid: %v", err)
		}
	})

	t.Run("a number is an instruction to replace", func(t *testing.T) {
		var req UpdateArticleRequest
		if err := json.Unmarshal([]byte(`{"title":"换图","cover_media_id":7}`), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !req.CoverMediaID.Set || req.CoverMediaID.Value == nil || *req.CoverMediaID.Value != 7 {
			t.Fatalf("cover = {Set:%t Value:%v}, want {true 7}", req.CoverMediaID.Set, req.CoverMediaID.Value)
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("a positive cover id must be valid: %v", err)
		}
	})

	t.Run("a non-integer is rejected loudly", func(t *testing.T) {
		// 静默当成「没有这个字段」会让一次本想换图的操作变成一次**摘掉头图**。
		var req UpdateArticleRequest
		err := json.Unmarshal([]byte(`{"title":"坏值","cover_media_id":"7"}`), &req)
		if err == nil {
			t.Fatal(`a string cover_media_id must fail to parse, not be silently dropped`)
		}
		if !strings.Contains(err.Error(), "cover_media_id") {
			t.Fatalf("error %q should name the offending field", err.Error())
		}
	})

	t.Run("a non-positive number is rejected by Validate", func(t *testing.T) {
		zero := int64(0)
		req := UpdateArticleRequest{Title: "t", CoverMediaID: OptionalID{Set: true, Value: &zero}}
		if err := req.Validate(); err == nil {
			t.Fatal("cover_media_id = 0 must be rejected")
		}

		negative := int64(-1)
		req = UpdateArticleRequest{Title: "t", CoverMediaID: OptionalID{Set: true, Value: &negative}}
		if err := req.Validate(); err == nil {
			t.Fatal("a negative cover_media_id must be rejected")
		}
	})
}

// TestCreateArticleCoverStaysTwoState 说明新建为什么**不需要**三态：
// 新建时不存在「原来那张图」，「不指定」与「置空」都只是「没有头图」。
// 但它仍要挡住非正数——否则 0 会被写进外键列，INSERT 直接失败成 500。
func TestCreateArticleCoverStaysTwoState(t *testing.T) {
	var omitted CreateArticleRequest
	if err := json.Unmarshal([]byte(`{"title":"没有头图"}`), &omitted); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if omitted.CoverMediaID != nil {
		t.Fatalf("cover = %v, want nil", omitted.CoverMediaID)
	}
	if err := omitted.Validate(); err != nil {
		t.Fatalf("a cover-less create must be valid: %v", err)
	}

	var nulled CreateArticleRequest
	if err := json.Unmarshal([]byte(`{"title":"显式 null","cover_media_id":null}`), &nulled); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if nulled.CoverMediaID != nil {
		t.Fatalf("an explicit null cover = %v, want nil", nulled.CoverMediaID)
	}
	if err := nulled.Validate(); err != nil {
		t.Fatalf("an explicit null cover must be valid on create: %v", err)
	}

	zero := int64(0)
	bad := CreateArticleRequest{Title: "零", CoverMediaID: &zero}
	if err := bad.Validate(); err == nil {
		t.Fatal("cover_media_id = 0 must be rejected on create")
	}
}

// TestValidatePublishedAtBounds 钉住发布时间的取值区间。
//
// 上界不是随手取的：前端 lib/format.ts 用 1e12 作分界判断后端给的是秒还是毫秒
// （大于它就当毫秒）。一个 1.7e12 的**毫秒**值若被当成秒放行，会存进 created_at，
// 前台把文章显示成公元 55835 年——数据没坏，界面却一团乱，而且没有任何一步报错。
//
// 反证方式：把 validatePublishedAt 里的 MaxPublishedAt 判断删掉 →
// 「just below the cap」之后的用例仍通过，但「a millisecond timestamp」会通过本应失败的断言。
func TestValidatePublishedAtBounds(t *testing.T) {
	cases := []struct {
		name    string
		value   *int64
		wantErr bool
	}{
		{name: "omitted means «now»", value: nil, wantErr: false},
		{name: "a normal seconds timestamp", value: int64ptr(1_700_000_000), wantErr: false},
		{name: "epoch + 1", value: int64ptr(1), wantErr: false},
		{name: "zero is not a publish date", value: int64ptr(0), wantErr: true},
		{name: "negative", value: int64ptr(-1), wantErr: true},
		{name: "a millisecond timestamp", value: int64ptr(1_700_000_000_000), wantErr: true},
		{name: "just below the cap is allowed", value: int64ptr(MaxPublishedAt - 1), wantErr: false},
		{name: "at the cap is rejected", value: int64ptr(MaxPublishedAt), wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			createErr := (&CreateArticleRequest{Title: "t", PublishedAt: tc.value}).Validate()
			updateErr := (&UpdateArticleRequest{Title: "t", PublishedAt: tc.value}).Validate()

			if tc.wantErr {
				if createErr == nil {
					t.Fatal("CreateArticleRequest.Validate() accepted an out-of-range published_at")
				}
				if updateErr == nil {
					t.Fatal("UpdateArticleRequest.Validate() accepted an out-of-range published_at")
				}
				return
			}
			if createErr != nil {
				t.Fatalf("CreateArticleRequest.Validate() error = %v", createErr)
			}
			if updateErr != nil {
				t.Fatalf("UpdateArticleRequest.Validate() error = %v", updateErr)
			}
		})
	}
}

// TestPublishedAtErrorExplainsTheUnit 让错误消息自己说清楚该传什么单位。
// 400 的正文是唯一会被调用方看到的东西（响应体里没有别的诊断信息），
// 一句 "invalid" 会让人反复试错。
func TestPublishedAtErrorExplainsTheUnit(t *testing.T) {
	milliseconds := int64(1_700_000_000_000)
	err := (&CreateArticleRequest{Title: "t", PublishedAt: &milliseconds}).Validate()
	if err == nil {
		t.Fatal("a millisecond timestamp must be rejected")
	}
	if !strings.Contains(err.Error(), "seconds") {
		t.Fatalf("error %q should tell the caller the unit is seconds", err.Error())
	}
}

// TestArticleResponseAlwaysCarriesCoverMediaID 钉住响应的**形状**而不是取值。
//
// CoverMediaID 不带 omitempty 是刻意的：twilight-blog-api 的 Article 把它声明为
// 必填（`number | null`），ArticleSearchResult 又继承它。一个「有时不存在」的字段
// 会让前端需要多一层判空，而同类的坑在 tags 上踩过一次（那次直接白屏）。
//
// 反证方式：给 ArticleResponse.CoverMediaID 加上 omitempty → 第一个用例的
// `"cover_media_id"` 会整个消失，本条失败。
func TestArticleResponseAlwaysCarriesCoverMediaID(t *testing.T) {
	t.Run("no cover serializes as null, not as a missing field", func(t *testing.T) {
		encoded, err := json.Marshal(NewArticleResponse(&model.Article{ID: 1, Title: "无头图"}))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		raw, ok := decoded["cover_media_id"]
		if !ok {
			t.Fatalf("cover_media_id is missing from the response: %s", encoded)
		}
		if raw != nil {
			t.Fatalf("cover_media_id = %v, want null", raw)
		}
	})

	t.Run("a cover serializes as its id", func(t *testing.T) {
		cover := int64(9)
		encoded, err := json.Marshal(NewArticleResponse(&model.Article{ID: 1, CoverMediaID: &cover}))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded["cover_media_id"] != float64(9) {
			t.Fatalf("cover_media_id = %v, want 9", decoded["cover_media_id"])
		}
	})

	t.Run("the list form carries it too", func(t *testing.T) {
		cover := int64(4)
		list := NewArticleResponseList([]model.Article{
			{ID: 1},
			{ID: 2, CoverMediaID: &cover},
		})
		if len(list) != 2 {
			t.Fatalf("got %d responses, want 2", len(list))
		}
		if list[0].CoverMediaID != nil {
			t.Fatalf("first = %v, want nil", list[0].CoverMediaID)
		}
		if list[1].CoverMediaID == nil || *list[1].CoverMediaID != 4 {
			t.Fatalf("second = %v, want 4", list[1].CoverMediaID)
		}
	})
}

// TestPublishedAtSurvivesARoundTrip 确认字段名就是前端要发的那个。
// 用 `publishedAt`（驼峰）或 `publish_at` 都不会有编译错误，只会静默变成
// 「没传发布时间」——而新建时那意味着文章被排到「现在」。
func TestPublishedAtSurvivesARoundTrip(t *testing.T) {
	var req CreateArticleRequest
	if err := json.Unmarshal([]byte(`{"title":"补录","published_at":1600000000}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.PublishedAt == nil || *req.PublishedAt != 1_600_000_000 {
		t.Fatalf("published_at = %v, want 1600000000 (check the json tag)", req.PublishedAt)
	}

	var update UpdateArticleRequest
	if err := json.Unmarshal([]byte(`{"title":"改期","published_at":1700000000}`), &update); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if update.PublishedAt == nil || *update.PublishedAt != 1_700_000_000 {
		t.Fatalf("published_at = %v, want 1700000000 (check the json tag)", update.PublishedAt)
	}
}

func int64ptr(v int64) *int64 { return &v }

// TestArticleValidateRequiresATitle 钉住「标题必填」仍然在两端生效，
// 并确认 Validate 会**就地 TrimSpace**（handler 校验通过后直接使用该结构体，
// 回头再用原始输入就会把前后空白一起存进去）。
func TestArticleValidateRequiresATitle(t *testing.T) {
	if err := (&CreateArticleRequest{Title: "   "}).Validate(); err == nil {
		t.Fatal("a whitespace-only title must be rejected on create")
	}
	if err := (&UpdateArticleRequest{Title: ""}).Validate(); err == nil {
		t.Fatal("an empty title must be rejected on update")
	}

	created := &CreateArticleRequest{Title: "  带空格的标题  "}
	if err := created.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if created.Title != "带空格的标题" {
		t.Fatalf("title = %q, want it trimmed in place", created.Title)
	}

	updated := &UpdateArticleRequest{Title: "  另一个标题  "}
	if err := updated.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if updated.Title != "另一个标题" {
		t.Fatalf("title = %q, want it trimmed in place", updated.Title)
	}
}

// TestUpdateArticleTrimsTheEnglishTitle covers the optional half of the pair:
// title_en may be omitted (nil), but when present it is normalised in place so
// the handler does not store stray whitespace.
func TestUpdateArticleTrimsTheEnglishTitle(t *testing.T) {
	title := "  English Title  "
	updated := &UpdateArticleRequest{Title: "标题", TitleEN: &title}
	if err := updated.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if updated.TitleEN == nil || *updated.TitleEN != "English Title" {
		t.Fatalf("title_en = %v, want it trimmed", updated.TitleEN)
	}

	// 省略时保持 nil：那表示「不带英文标题」，不是空串。
	omitted := &UpdateArticleRequest{Title: "标题"}
	if err := omitted.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if omitted.TitleEN != nil {
		t.Fatalf("title_en = %v, want nil when omitted", omitted.TitleEN)
	}
}
