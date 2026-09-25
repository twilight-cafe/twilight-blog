package view_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

// 这个文件是「标签页下方的相关文章」在 HTTP 层的端到端守护。
//
// 为什么必须在 HTTP 这一层单独测一遍：这个功能曾经整块打不开，而**任何一层单测
// 都看不出来**。事故链条是——
//
//	标签页 → GET /api/tags/:id/articles → service.ListArticlesByTag
//	       → model.SQLiteTagRepository.ListArticlesByTag → scanArticle
//
// 加头图功能时 scanArticle 从 6 列扩到 7 列，article_repo.go 的三处 SELECT 都同步
// 补了 cover_media_id，唯独 tag_repo.go 那处漏了。SQL 本身完全合法，go build、
// go vet、model 包里其余全部测试都是绿的，只有真的请求这个端点的那个进程会在
// 运行时收到 "expected 6 destination arguments in Scan, not 7"，被 handler 兜成
// 500，前端 .catch 一到就显示「文章加载失败」。
//
// 因此这里的断言分两层，缺一不可：
//   - 第一层（形状）：200 + items/total，且 items[].cover_media_id 字段存在。
//     只断「调用成功」是不够的——见下。
//   - 第二层（语义）：带标签的那篇恰好是带图的那篇，且 tags 已装配。
//
// 关于「只断成功不够」：如果只是列数对上但列**顺序**错（比如把 cover_media_id 挪到
// deleted_at 后面），扫描不会报错，只是每个字段都错位——标题里塞进时间戳、
// 头图 id 读成 deleted_at。所以下面同时钉住 title 与 cover_media_id 的实际取值。

// tagArticlesResponse 是 GET /api/tags/:id/articles 的响应形状。
//
// CoverMediaID 用 *int64 而不是 int64：没有头图时它是 JSON null，值类型会把 null
// 读成 0，就分不清「没头图」与「头图 id 是 0」。Tags 用「切片是否被赋值」来区分
// 字段缺失与空数组——json 包把缺失的字段留成 nil，把 [] 解成非 nil 空切片，
// 而前台对这二者的处理完全相反（缺失 → undefined 不渲染，[] → 渲染空态）。
type tagArticlesResponse struct {
	Items []struct {
		ID           int64    `json:"id"`
		Title        string   `json:"title"`
		CoverMediaID *int64   `json:"cover_media_id"`
		Tags         []string `json:"tags"`
	} `json:"items"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// listTagArticles 请求某个标签下的文章列表。
func listTagArticles(t *testing.T, admin *api, tagID int64) tagArticlesResponse {
	t.Helper()

	res := admin.call(http.MethodGet, "/api/tags/"+strconv.FormatInt(tagID, 10)+"/articles?page=1&page_size=12", nil, nil)
	expectStatus(t, res, http.StatusOK, "GET /api/tags/:id/articles")

	var page tagArticlesResponse
	decodeInto(t, res, &page)
	return page
}

// tagIDByName 从标签列表里找出指定名字的标签 id。
//
// page_size 写 12 而不是更大：handler 层对外的上限就是 12（比 model 的
// MaxPageSize=100 更严），超了会被 400 拒掉。本文件只需要「一个标签」，
// 12 个足够。
func tagIDByName(t *testing.T, admin *api, name string) int64 {
	t.Helper()

	res := admin.call(http.MethodGet, "/api/tags?page=1&page_size=12", nil, nil)
	expectStatus(t, res, http.StatusOK, "GET /api/tags")

	var page struct {
		Items []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	decodeInto(t, res, &page)

	for _, tag := range page.Items {
		if tag.Name == name {
			return tag.ID
		}
	}
	t.Fatalf("no tag named %q in %+v", name, page.Items)
	return 0
}

// TestTagArticlesEndToEnd 是「标签页下方相关文章不显示」的回归测试。
//
// 反证方式：删掉 model 层 ListArticlesByTag 的 SELECT 里的 a.cover_media_id
// → 本测试在第一个子用例上以 500 失败（而不是返回一个空列表）。
func TestTagArticlesEndToEnd(t *testing.T) {
	server := newCoverServer(t)
	admin := installFreshBlog(t, server)

	cover := uploadMedia(t, admin, "cover.png", pngBytes(t))

	// 带标签 + 带头图的那篇：两个特征叠在一起，才能同时验证
	// 「列对齐（cover 读得出来）」和「关联装配（tags 读得出来）」。
	//
	// 请求字段名是 snake_case 的 cover_media_id（与响应一致）。
	// 写错成 camelCase 不会有任何报错——viewmodel.OptionalID 把「字段缺失」
	// 理解成「本次不改动这把指针」，于是文章建出来了、头图静默为 null。
	tagged := createArticle(t, admin, map[string]any{
		"title":          "带标签的文章",
		"content":        "正文内容甲",
		"tags":           []string{"Go", "前端"},
		"cover_media_id": cover,
	})
	// 同一标签下的另一篇，没有头图。它承担两件事：让分页有第二行，
	// 以及钉住「无头图时该字段是 null 而不是 0」。
	plain := createArticle(t, admin, map[string]any{
		"title":   "另一篇",
		"content": "正文内容乙",
		"tags":    []string{"Go"},
	})

	tagID := tagIDByName(t, admin, "Go")

	t.Run("a tagged article with a cover comes back whole", func(t *testing.T) {
		page := listTagArticles(t, admin, tagID)

		if page.Total != 2 {
			t.Fatalf("total = %d, want 2", page.Total)
		}
		if len(page.Items) != 2 {
			t.Fatalf("got %d items, want 2", len(page.Items))
		}

		// createArticle 按 body 顺序发、created_at 相同，所以列表按 id 倒序：
		// plain（后建的 id 更大）在前，tagged 在后。
		var got struct {
			ID           int64
			Title        string
			CoverMediaID *int64
			Tags         []string
		}
		found := false
		for _, item := range page.Items {
			if item.ID == tagged.ID {
				got.ID, got.Title, got.CoverMediaID, got.Tags = item.ID, item.Title, item.CoverMediaID, item.Tags
				found = true
			}
		}
		if !found {
			t.Fatalf("the tagged article %d is missing from %+v", tagged.ID, page.Items)
		}

		// 这一条就是当初 500 的直接后果：列表整块空。
		if got.Title != "带标签的文章" {
			t.Fatalf("title = %q, want %q: scanArticle reads by position, so a drifted column list scrambles the fields", got.Title, "带标签的文章")
		}
		if got.CoverMediaID == nil {
			t.Fatal("cover_media_id = null on an article that has a cover: the SELECT in ListArticlesByTag dropped the column")
		}
		if *got.CoverMediaID != cover {
			t.Fatalf("cover_media_id = %d, want %d", *got.CoverMediaID, cover)
		}
		if !equalStringSlices(got.Tags, []string{"Go", "前端"}) {
			t.Fatalf("tags = %v, want [Go 前端] (the front end renders these under the article card)", got.Tags)
		}
	})

	t.Run("an article with no cover reports null, not zero", func(t *testing.T) {
		page := listTagArticles(t, admin, tagID)

		for _, item := range page.Items {
			if item.ID != plain.ID {
				continue
			}
			if item.CoverMediaID != nil {
				t.Fatalf("cover_media_id = %d, want null", *item.CoverMediaID)
			}
			if !equalStringSlices(item.Tags, []string{"Go"}) {
				t.Fatalf("tags = %v, want [Go]", item.Tags)
			}
			return
		}
		t.Fatalf("the article without a cover %d is missing", plain.ID)
	})

	t.Run("the article really has its cover set", func(t *testing.T) {
		// 排除「库里头图本来就没写进去，所以标签页读不到也正常」这种假绿。
		// 没有这一条，上面那条 nil 断言在写入路径整个坏掉时依然会通过。
		stored := getArticle(t, admin, tagged.ID)
		if stored.CoverMediaID == nil || *stored.CoverMediaID != cover {
			t.Fatalf("cover via GET /api/articles/:id = %v, want %d", stored.CoverMediaID, cover)
		}
	})

	t.Run("an unknown tag is 404, not an empty list", func(t *testing.T) {
		// 前台靠状态码区分「这个标签下暂时没文章」（渲染空态）与「标签不存在」
		// （渲染错误态）。都返回 200 的话，从别处点进来的失效链接会显示成
		// 「暂无文章」——一个看起来正常、实际上骗人的页面。
		//
		// 分页参数照给：它缺失时 handler 先返回 400，那条分支会把这个用例
		// 变成「测到了参数校验」，盖住真正想断的 404 语义。
		res := admin.call(http.MethodGet, "/api/tags/999999/articles?page=1&page_size=12", nil, nil)
		expectStatus(t, res, http.StatusNotFound, "GET /api/tags/:id/articles (unknown tag)")
	})

	t.Run("a tag with no visible articles returns an empty array", func(t *testing.T) {
		// 空数组而不是 null：前台对 items 直接 .map，null 就是一次白屏。
		lonely := tagIDByName(t, admin, "前端")
		page := listTagArticles(t, admin, lonely)
		if page.Total != 1 {
			t.Fatalf("total = %d, want 1", page.Total)
		}
		if page.Items == nil {
			t.Fatal("items = nil, want []")
		}
	})
}

// equalStringSlices 比较两个字符串切片（不作为断言函数，只返回布尔）。
func equalStringSlices(got, want []string) bool {
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

// 编译期确认上面那几处断言用到的 JSON 字段名没有写错——
// tagArticlesResponse 若与后端实际的 key 不一致，decodeInto 会静默解出零值，
// 测试就变成「断言零值等于零值」的假绿。
func TestTagArticlesResponseFieldsMatchTheWireFormat(t *testing.T) {
	raw := []byte(`{"items":[{"id":1,"title":"t","cover_media_id":7,"tags":["Go"]}],"total":1,"total_pages":1}`)

	var page tagArticlesResponse
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(page.Items))
	}
	item := page.Items[0]
	if item.ID != 1 || item.Title != "t" {
		t.Fatalf("id/title = %d/%q, want 1/t", item.ID, item.Title)
	}
	if item.CoverMediaID == nil || *item.CoverMediaID != 7 {
		t.Fatalf("cover_media_id = %v, want 7", item.CoverMediaID)
	}
	if page.Total != 1 || page.TotalPages != 1 {
		t.Fatalf("total/total_pages = %d/%d, want 1/1", page.Total, page.TotalPages)
	}
}
