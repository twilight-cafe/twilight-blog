package view_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"blog-engine/internal/config"
)

// 这个文件是「文章头图」与「修改发布时间」两条新功能在 HTTP 层的端到端守护。
//
// 为什么必须在 HTTP 这一层再测一遍（service / viewmodel 已经各自测过）：
// 三层之间是**透传**关系，任何一处漏传字段，下层测得的契约就与用户实际走到的
// 路径无关。头图尤其如此——它要穿过 viewmodel 的三态解析、handler 的
// CoverSet/CoverMediaID 拆分、service 的可用性校验，最后落进一条带外键的列。
// 单独测任何一层都看不出「前端传了 null，后端当成没传」这类问题。
//
// 两处务必注意的**副作用**：
//   - 上传媒体会把文件写进 cfg.MediaDir，所以这里的 server 必须把 MediaDir
//     指到临时目录，否则测试会往仓库里拉屎（默认值是相对路径 "media"）。
//   - 永久删除文章会连带删掉头图文件，因此那条用例的顺序（软删 → 恢复 → 再软删
//     → 永久删）不能颠倒：HardDelete 的前置条件就是「已经在回收站里」。

// newCoverServer 起一个把媒体目录指向临时目录的服务器。
func newCoverServer(t *testing.T) *httptest.Server {
	t.Helper()

	server, _ := newCoverServerWithDir(t)
	return server
}

// newCoverServerWithDir 同上，并把媒体目录一并交出来——需要断言「磁盘上的文件
// 真的没了」的用例必须知道目录在哪。
func newCoverServerWithDir(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	mediaDir := filepath.Join(t.TempDir(), "media")
	server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.MediaDir = mediaDir
	})
	return server, mediaDir
}

// mediaFiles 列出媒体目录下的文件名（排序后），目录还不存在时返回空。
func mediaFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read media dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// pngBytes 生成一张真实的小 PNG。
//
// 刻意用编码器生成而不是硬编码一段 base64：媒体服务按**内容嗅探**判定类型，
// 一段抄错的字节会变成「上传被拒」，而症状看起来像校验逻辑坏了。
func pngBytes(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// uploadMedia 走真实的 POST /api/admin/media（multipart，字段名 "files"），
// 返回新建媒体的 id。走 HTTP 而不是插库，是因为本文件的断言全都关于
// 「上传出来的媒体能不能当头图」。
func uploadMedia(t *testing.T, admin *api, filename string, content []byte) int64 {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("files", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, admin.base+"/api/admin/media", &buf)
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	// 写请求必须带 CSRF（管理员浏览器在这一步已经持有会话）。
	req.Header.Set("X-CSRF-Token", admin.csrf())

	resp, err := admin.client.Do(req)
	if err != nil {
		t.Fatalf("upload media: %v", err)
	}
	defer resp.Body.Close()

	var raw bytes.Buffer
	if _, err := raw.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read upload response: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload media: status = %d, want 201 (body: %s)", resp.StatusCode, truncate(raw.Bytes()))
	}

	var created []struct {
		ID        int64  `json:"id"`
		MediaType string `json:"media_type"`
	}
	if err := json.Unmarshal(raw.Bytes(), &created); err != nil {
		t.Fatalf("decode upload response %q: %v", truncate(raw.Bytes()), err)
	}
	if len(created) != 1 {
		t.Fatalf("upload returned %d media items, want 1", len(created))
	}
	if created[0].ID == 0 {
		t.Fatalf("the uploaded media has no id: %s", truncate(raw.Bytes()))
	}
	if created[0].MediaType != "image" {
		t.Fatalf("uploaded media_type = %q, want image", created[0].MediaType)
	}
	return created[0].ID
}

// articleResponse 是本文件关心的那部分文章形状。CoverMediaID 用 *int64
// 而不是 int64：响应里没有头图时它是 null，用值类型会把 null 读成 0，
// 断言就分不清「没有头图」与「头图 id 是 0」。
type articleResponse struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	CreatedAt    int64  `json:"created_at"`
	CoverMediaID *int64 `json:"cover_media_id"`
}

func createArticle(t *testing.T, admin *api, body map[string]any) articleResponse {
	t.Helper()

	res := admin.call(http.MethodPost, "/api/admin/articles", body, admin.unsafe(nil))
	expectStatus(t, res, http.StatusCreated, "POST /api/admin/articles")

	var article articleResponse
	decodeInto(t, res, &article)
	return article
}

// getArticle 通过公开接口读回文章，断言的是**用户能看到的那一份**，
// 而不是写请求的响应（后者来自内存里的结构体，可能根本没落库）。
func getArticle(t *testing.T, admin *api, id int64) articleResponse {
	t.Helper()

	res := admin.call(http.MethodGet, "/api/articles/"+strconv.FormatInt(id, 10), nil, nil)
	expectStatus(t, res, http.StatusOK, "GET /api/articles/:id")

	var article articleResponse
	decodeInto(t, res, &article)
	return article
}

// listArticles 读公开列表（首页与归档用的就是它）。
func listArticles(t *testing.T, admin *api) []articleResponse {
	t.Helper()

	res := admin.call(http.MethodGet, "/api/articles?page=1&page_size=12", nil, nil)
	expectStatus(t, res, http.StatusOK, "GET /api/articles")

	var page struct {
		Items []articleResponse `json:"items"`
	}
	decodeInto(t, res, &page)
	return page.Items
}

// TestArticleCoverEndToEnd 覆盖头图在管理端与公开端之间的完整往返。
func TestArticleCoverEndToEnd(t *testing.T) {
	server := newCoverServer(t)
	admin := installFreshBlog(t, server)

	cover := uploadMedia(t, admin, "cover.png", pngBytes(t))
	replacement := uploadMedia(t, admin, "replacement.png", pngBytes(t))

	t.Run("a created article carries the cover back", func(t *testing.T) {
		article := createArticle(t, admin, map[string]any{
			"title": "带头图的文章", "content": "正文", "tags": []string{},
			"cover_media_id": cover,
		})
		if article.CoverMediaID == nil || *article.CoverMediaID != cover {
			t.Fatalf("cover in the create response = %v, want %d", article.CoverMediaID, cover)
		}
		// 公开读取路径也必须带上它——文章详情页正是从这里取图。
		if stored := getArticle(t, admin, article.ID); stored.CoverMediaID == nil || *stored.CoverMediaID != cover {
			t.Fatalf("cover on GET = %v, want %d", stored.CoverMediaID, cover)
		}
		// 头图必须是媒体库里的真实一条，公开 URL 打得开。
		expectStatus(t, admin.call(http.MethodGet, "/api/media/"+strconv.FormatInt(cover, 10), nil, nil),
			http.StatusOK, "GET /api/media/:id of the cover")
	})

	t.Run("the cover reaches the public list used by the home page", func(t *testing.T) {
		found := false
		for _, item := range listArticles(t, admin) {
			if item.CoverMediaID != nil && *item.CoverMediaID == cover {
				found = true
			}
		}
		if !found {
			t.Fatalf("no listed article carries the cover %d", cover)
		}
	})

	article := createArticle(t, admin, map[string]any{
		"title": "三态用文章", "content": "正文", "tags": []string{},
		"cover_media_id": cover,
	})
	path := "/api/admin/articles/" + strconv.FormatInt(article.ID, 10)

	t.Run("a title-only update leaves the cover alone", func(t *testing.T) {
		res := admin.call(http.MethodPut, path, map[string]any{"title": "改了标题"}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusOK, "PUT with a title only")

		if stored := getArticle(t, admin, article.ID); stored.CoverMediaID == nil || *stored.CoverMediaID != cover {
			t.Fatalf("a title-only PUT cleared the cover (got %v); the field must be optional, not absent", stored.CoverMediaID)
		}
	})

	t.Run("an explicit null clears the cover", func(t *testing.T) {
		res := admin.call(http.MethodPut, path,
			map[string]any{"title": "摘掉头图", "cover_media_id": nil}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusOK, "PUT with an explicit null cover")

		if stored := getArticle(t, admin, article.ID); stored.CoverMediaID != nil {
			t.Fatalf("cover = %v after an explicit null, want it cleared", stored.CoverMediaID)
		}
		// 摘掉指针不等于删媒体：这张图必须还在库里（用户可能只是换图或反悔）。
		expectStatus(t, admin.call(http.MethodGet, "/api/media/"+strconv.FormatInt(cover, 10), nil, nil),
			http.StatusOK, "GET /api/media/:id after clearing the cover")
	})

	t.Run("a provided id replaces the cover", func(t *testing.T) {
		res := admin.call(http.MethodPut, path,
			map[string]any{"title": "换成新图", "cover_media_id": replacement}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusOK, "PUT with a new cover")

		if stored := getArticle(t, admin, article.ID); stored.CoverMediaID == nil || *stored.CoverMediaID != replacement {
			t.Fatalf("cover = %v, want the replacement %d", stored.CoverMediaID, replacement)
		}
	})
}

// TestArticleCoverValidationOverHTTP 覆盖 handler 的错误映射：
// 客户端给错 → 400，目标文章不存在 → 404，两者不能混为一谈。
//
// 非图片的那一条直接往库里的 media 表插一行 video：走上传接口造不出音视频
// （需要一个真实可嗅探的容器文件），而这里要验的是「service 会拒绝非图片」，
// 与媒体是怎么进来的无关。
func TestArticleCoverValidationOverHTTP(t *testing.T) {
	server, db, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.MediaDir = filepath.Join(t.TempDir(), "media")
	})
	admin := installFreshBlog(t, server)

	if _, err := db.Exec(`
INSERT INTO media (id, storage_name, original_name, media_type, mime_type, size, created_at)
VALUES (77, 'clip.mp4', 'clip.mp4', 'video', 'video/mp4', 10, 1);
`); err != nil {
		t.Fatalf("seed a non-image media row: %v", err)
	}

	article := createArticle(t, admin, map[string]any{
		"title": "校验用文章", "content": "正文", "tags": []string{},
	})

	t.Run("an unknown cover is refused on create", func(t *testing.T) {
		res := admin.call(http.MethodPost, "/api/admin/articles", map[string]any{
			"title": "不存在的头图", "content": "", "tags": []string{},
			"cover_media_id": 999999,
		}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "POST with a cover that does not exist")
	})

	t.Run("a non-image cover is refused on create", func(t *testing.T) {
		res := admin.call(http.MethodPost, "/api/admin/articles", map[string]any{
			"title": "视频当头图", "content": "", "tags": []string{},
			"cover_media_id": 77,
		}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "POST with a video as the cover")
	})

	t.Run("a zero cover is refused", func(t *testing.T) {
		res := admin.call(http.MethodPost, "/api/admin/articles", map[string]any{
			"title": "零头图", "content": "", "tags": []string{},
			"cover_media_id": 0,
		}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "POST with cover_media_id = 0")
	})

	t.Run("none of the refused creates were written", func(t *testing.T) {
		for _, item := range listArticles(t, admin) {
			switch item.Title {
			case "不存在的头图", "视频当头图", "零头图":
				t.Fatalf("an article refused by cover validation was written anyway: %q", item.Title)
			}
		}
	})

	t.Run("a bad cover on update is a 400 and changes nothing", func(t *testing.T) {
		res := admin.call(http.MethodPut, "/api/admin/articles/"+strconv.FormatInt(article.ID, 10),
			map[string]any{"title": "偷换的标题", "cover_media_id": 77}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "PUT with a video as the cover")

		stored := getArticle(t, admin, article.ID)
		if stored.Title != "校验用文章" {
			t.Fatalf("a refused update changed the title to %q", stored.Title)
		}
		if stored.CoverMediaID != nil {
			t.Fatalf("a refused update set the cover to %v", stored.CoverMediaID)
		}
	})

	t.Run("updating an article that does not exist is a 404", func(t *testing.T) {
		res := admin.call(http.MethodPut, "/api/admin/articles/999999",
			map[string]any{"title": "不存在"}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusNotFound, "PUT on a missing article")
	})
}

// TestPermanentDeleteTakesTheCoverWithIt 是这一组里唯一**不可逆**的用例：
// 文章永久删除后，它专属的头图必须也从媒体库里消失。
//
// 分几步断言，因为「不见了」有几种不同的坏法：
//  1. 软删（进回收站）时头图必须**还在** —— 回收站的意义就是「删错了能找回来」；
//  2. 恢复之后头图必须完好 —— 否则「恢复文章」成了一个会丢数据的操作；
//  3. 永久删除后自己的头图没了，且公开 URL 返回 404（页面上不留破图）；
//  4. 另一篇文章的头图**没有**被误删 —— 这条最容易被忽略。删除是按
//     「有没有人还用」判定的，判宽一格就会毁掉别人的图，而且是通过外键的
//     SET NULL 静默发生。
func TestPermanentDeleteTakesTheCoverWithIt(t *testing.T) {
	server, mediaDir := newCoverServerWithDir(t)
	admin := installFreshBlog(t, server)

	doomedCover := uploadMedia(t, admin, "doomed.png", pngBytes(t))
	keptCover := uploadMedia(t, admin, "kept.png", pngBytes(t))

	// 两次上传各落一个文件（存储名是随机的，不做内容去重），先确认它们真在磁盘上——
	// 否则下面「少了一个」的断言可能只是「从来没有过」。
	if before := mediaFiles(t, mediaDir); len(before) != 2 {
		t.Fatalf("after two uploads the media dir holds %d file(s) %v, want 2", len(before), before)
	}

	kept := createArticle(t, admin, map[string]any{
		"title": "留下的", "content": "", "tags": []string{}, "cover_media_id": keptCover,
	})
	doomed := createArticle(t, admin, map[string]any{
		"title": "要删的", "content": "", "tags": []string{}, "cover_media_id": doomedCover,
	})

	path := "/api/admin/articles/" + strconv.FormatInt(doomed.ID, 10)

	expectStatus(t, admin.call(http.MethodDelete, path, nil, admin.unsafe(nil)),
		http.StatusNoContent, "DELETE (soft) the article")
	expectStatus(t, admin.call(http.MethodGet, "/api/media/"+strconv.FormatInt(doomedCover, 10), nil, nil),
		http.StatusOK, "GET the cover while the article is in the trash")

	expectStatus(t, admin.call(http.MethodPost, path+"/restore", nil, admin.unsafe(nil)),
		http.StatusNoContent, "POST restore")
	if restored := getArticle(t, admin, doomed.ID); restored.CoverMediaID == nil || *restored.CoverMediaID != doomedCover {
		t.Fatalf("cover after restore = %v, want it intact at %d", restored.CoverMediaID, doomedCover)
	}

	expectStatus(t, admin.call(http.MethodDelete, path, nil, admin.unsafe(nil)),
		http.StatusNoContent, "DELETE (soft) again")
	expectStatus(t, admin.call(http.MethodDelete, path+"/permanent", nil, admin.unsafe(nil)),
		http.StatusNoContent, "DELETE (permanent)")

	expectStatus(t, admin.call(http.MethodGet, "/api/media/"+strconv.FormatInt(doomedCover, 10), nil, nil),
		http.StatusNotFound, "GET the orphaned cover after the permanent delete")

	// 磁盘上必须真的少一个文件。
	//
	// 只断言 GET 404 是不够的：记录先被删掉，Get 在仓储层就返回 ErrMediaNotFound，
	// 根本走不到 os.Open——于是把 MediaService.Delete 里的 os.Remove 整行删掉，
	// 这个用例照样全绿，而磁盘会随每次「永久删除」慢慢堆起孤儿文件。
	if after := mediaFiles(t, mediaDir); len(after) != 1 {
		t.Fatalf("after the permanent delete the media dir holds %d file(s) %v, want 1", len(after), after)
	}

	// 媒体列表里也不该再出现它——避免「接口 404 但列表还列着」这种半清理状态。
	list := admin.call(http.MethodGet, "/api/admin/media?page=1&page_size=12", nil, nil)
	expectStatus(t, list, http.StatusOK, "GET /api/admin/media")
	var media struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	decodeInto(t, list, &media)
	for _, item := range media.Items {
		if item.ID == doomedCover {
			t.Fatalf("media %d is still listed after its only article was deleted", doomedCover)
		}
	}

	// 别人的头图丝毫未动。这条同时钉住「删掉的是对的那一个文件」：留下的那个
	// 存储名必须还在磁盘上，否则 GET 会在 os.Open 处 404。
	expectStatus(t, admin.call(http.MethodGet, "/api/media/"+strconv.FormatInt(keptCover, 10), nil, nil),
		http.StatusOK, "GET the unrelated cover")
	if still := getArticle(t, admin, kept.ID); still.CoverMediaID == nil || *still.CoverMediaID != keptCover {
		t.Fatalf("an unrelated article lost its cover: %v", still.CoverMediaID)
	}
}

// TestPublishedAtOverHTTP 覆盖「修改发布时间」这条链在 HTTP 层的两个方向：
// 显式传入必须生效（并改变列表顺序），不传必须一动不动。
func TestPublishedAtOverHTTP(t *testing.T) {
	server := newCoverServer(t)
	admin := installFreshBlog(t, server)

	// 取值都远大于 1e9，确保它们压过下面另一篇 1.6e9 的文章，
	// 否则列表首位的断言会因为「还有一篇文章更晚」而失败。
	const (
		oldDate     = int64(1_600_000_000) // 2020-09-13
		earlyDate   = int64(1_610_000_000)
		lateDate    = int64(1_620_000_000)
		republished = int64(1_690_000_000)
	)

	t.Run("an explicit published_at is honoured on create", func(t *testing.T) {
		article := createArticle(t, admin, map[string]any{
			"title": "补录的旧文", "content": "", "tags": []string{},
			"published_at": oldDate,
		})
		if article.CreatedAt != oldDate {
			t.Fatalf("created_at = %d, want the requested %d", article.CreatedAt, oldDate)
		}
	})

	dotted := createArticle(t, admin, map[string]any{
		"title": "有日期的文章", "content": "", "tags": []string{},
		"published_at": oldDate,
	})

	t.Run("a title-only update keeps the publish date", func(t *testing.T) {
		res := admin.call(http.MethodPut, "/api/admin/articles/"+strconv.FormatInt(dotted.ID, 10),
			map[string]any{"title": "改了标题"}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusOK, "PUT with a title only")

		if stored := getArticle(t, admin, dotted.ID); stored.CreatedAt != oldDate {
			t.Fatalf("created_at = %d after a title-only update, want it untouched at %d — "+
				"otherwise editing an old post silently republishes it to the top of the list",
				stored.CreatedAt, oldDate)
		}
	})

	early := createArticle(t, admin, map[string]any{
		"title": "早的", "content": "", "tags": []string{}, "published_at": earlyDate,
	})
	createArticle(t, admin, map[string]any{
		"title": "晚的", "content": "", "tags": []string{}, "published_at": lateDate,
	})

	t.Run("editing published_at moves the article in the list", func(t *testing.T) {
		// 把「早的」改到最晚：它应当排到最前面。
		res := admin.call(http.MethodPut, "/api/admin/articles/"+strconv.FormatInt(early.ID, 10),
			map[string]any{"title": "早的", "published_at": republished}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusOK, "PUT with a new published_at")
		if stored := getArticle(t, admin, early.ID); stored.CreatedAt != republished {
			t.Fatalf("created_at = %d, want %d (the repository must actually write the column)",
				stored.CreatedAt, republished)
		}

		items := listArticles(t, admin)
		if len(items) < 2 {
			t.Fatalf("expected the two articles in the list, got %d", len(items))
		}
		if items[0].ID != early.ID {
			t.Fatalf("the list leads with id=%d (%q), want the republished article id=%d",
				items[0].ID, items[0].Title, early.ID)
		}
	})

	t.Run("a millisecond timestamp is refused on create", func(t *testing.T) {
		// 前端用 1e12 作分界判断秒/毫秒；一个毫秒值若被放行，前台会显示成公元 55835 年，
		// 而数据本身没坏——没有任何一步会报错。
		res := admin.call(http.MethodPost, "/api/admin/articles", map[string]any{
			"title": "毫秒时间戳", "content": "", "tags": []string{},
			"published_at": 1_700_000_000_000,
		}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "POST with a millisecond timestamp")
	})

	t.Run("a millisecond timestamp is refused on update", func(t *testing.T) {
		res := admin.call(http.MethodPut, "/api/admin/articles/"+strconv.FormatInt(early.ID, 10),
			map[string]any{"title": "早的", "published_at": 1_700_000_000_000}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "PUT with a millisecond timestamp")

		if stored := getArticle(t, admin, early.ID); stored.CreatedAt != republished {
			t.Fatalf("a refused update moved the publish date to %d", stored.CreatedAt)
		}
	})

	t.Run("a zero published_at is refused and nothing is written", func(t *testing.T) {
		res := admin.call(http.MethodPost, "/api/admin/articles", map[string]any{
			"title": "零时间戳", "content": "", "tags": []string{}, "published_at": 0,
		}, admin.unsafe(nil))
		expectStatus(t, res, http.StatusBadRequest, "POST with published_at = 0")

		for _, item := range listArticles(t, admin) {
			if item.Title == "零时间戳" {
				t.Fatal("an article with an invalid published_at was written anyway")
			}
		}
	})
}
