package view_test

import (
	"net/http"
	"strconv"
	"testing"
)

// TestMomentMediaItemsAreServedOnThePublicFeed 钉住「带配图的微语在公开列表里
// 带着可直接渲染的媒体地址」这条契约。
//
// 为什么必须在 **HTTP 这一层**再钉一遍（service / viewmodel 已有单测）：
// 公开微语页曾经只渲染正文、完全不读 item.media_items，表现是「后台卡片看得到图，
// 前台的微语页只剩文字」。前端已经修好（改用与后台同一套 MediaGallery），
// 但只要列表响应里少了 media_items——某次改动漏掉 loadMediaForMoments、或者
// 响应的 JSON 键从 media_items 漂成别的名字——同一处故障会立刻回来，而
// service / viewmodel 的单测**全都还是绿的**：它们断言的是 Go 结构体字段，
// 不是 HTTP 上真正的键名与地址。
//
// 所以这里比对的是前端真正读取的那两个值：`media_items` 数组、以及每项的 `url`
// （前端把它塞进 <img src>）。地址形状由 viewmodel.MediaURL 决定，前端不做拼接。
//
// 反证方式：把 model.SQLiteMomentRepository.List 末尾的 loadMediaForMoments 删掉，
// 或把 viewmodel.MomentResponse.MediaItems 的 json 标签改掉 → 本用例失败。
func TestMomentMediaItemsAreServedOnThePublicFeed(t *testing.T) {
	server, db, _ := newTestServer(t)
	admin := installFreshBlog(t, server)

	// 直接落一行媒体记录：本用例要钉的是「微语 → 媒体」的响应形状，
	// 上传接口本身的形状由 media_e2e_test.go 负责。
	mediaID := seedMediaRow(t, db, "moment-cover.png")

	created := admin.call(http.MethodPost, "/api/admin/moments", map[string]any{
		"content": "带图微语", "media_ids": []int64{mediaID},
	}, admin.unsafe(nil))
	expectStatus(t, created, http.StatusCreated, "POST /api/admin/moments with media")

	var page struct {
		Items []struct {
			ID         int64 `json:"id"`
			MediaItems []struct {
				ID  int64  `json:"id"`
				URL string `json:"url"`
			} `json:"media_items"`
		} `json:"items"`
	}
	decodeInto(t, admin.call(http.MethodGet, "/api/moments?page=1&page_size=12", nil, nil), &page)

	if len(page.Items) != 1 {
		t.Fatalf("the public feed served %d moment(s), want exactly 1", len(page.Items))
	}
	if len(page.Items[0].MediaItems) != 1 {
		t.Fatalf("the public feed served %d media item(s) for the moment, want 1: "+
			"the moments page renders <img src> from exactly this array",
			len(page.Items[0].MediaItems))
	}

	got := page.Items[0].MediaItems[0]
	if got.ID != mediaID {
		t.Fatalf("media item id = %d, want the linked media %d", got.ID, mediaID)
	}
	if want := "/api/media/" + strconv.FormatInt(mediaID, 10); got.URL != want {
		t.Fatalf("media item url = %q, want %q — this string is the src the moments page renders",
			got.URL, want)
	}
}
