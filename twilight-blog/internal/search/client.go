// Package search 封装与 Meilisearch 的交互：索引的读写（client.go）、
// 内嵌二进制的托管（manager.go）与下载校验（download.go）。
//
// 可用性的边界要分清楚，因为「搜索是可选能力」这句话只对了一半。
// 以下三处会**终止整个进程**（见 main.go 与 manager.go）：
//   - 拿不到 Meilisearch 二进制（本地找不到且下载失败）→ 启动失败，进程退出；
//   - 二进制启动后 60 秒内没有就绪 → 启动失败，进程退出；
//   - 运行中的子进程意外退出 → monitor 打 Fatal，进程退出。
//
// 真正允许降级的只有索引本身：
//   - router 首次全量同步索引失败 → 只记 Error 并转后台指数退避重试，不阻断启动；
//   - service 层写入时的增量索引失败 → 只记日志，不回滚数据库。
//
// 代价是索引可能落后于库。恢复手段是保存任意一篇文章（会重索引该文档）
// 或重启进程（触发一次全量 Sync）。所以本包返回的错误**不等于**「博客坏了」，
// 但也不意味着可以忽略：它要么表示进程即将退出，要么表示搜索结果已经不准了。
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"blog-engine/internal/logging"
	"blog-engine/internal/model"
)

// IndexName 是唯一的索引 UID：文章与页面共用一个索引，靠文档的 kind 字段区分。
const IndexName = "blog_content"

// Client 是与 Meilisearch HTTP API 通信的薄封装。
//
// 它把「等待异步任务完成」这件麻烦事收在内部：Meilisearch 的所有写操作都是
// 异步的，只返回 taskUid，真正的成败必须再查一次任务状态才知道。
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient 构造客户端。baseURL 末尾的斜杠会被去掉，
// 因为后面所有路径拼接都自带前导斜杠。
// 30 秒超时是单次 HTTP 请求的上限，不含 waitTask 的轮询（那有独立的 30 秒总预算）。
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// EnsureIndex 幂等地建索引并应用可搜索字段设置。
//
// 索引已存在时 Meilisearch 返回 index_already_exists，这里按成功路径放行，
// 所以启动过程中反复调用是安全的。
func (c *Client) EnsureIndex(ctx context.Context) error {
	logging.Debugf("[search] ensuring index %s exists", IndexName)
	body := map[string]any{
		"uid":        IndexName,
		"primaryKey": "doc_id",
	}
	err := c.do(ctx, http.MethodPost, "/indexes", body, nil)
	if err != nil && !strings.Contains(err.Error(), "index_already_exists") {
		return err
	}

	return c.configureIndex(ctx)
}

// configureIndex 只把 title 与 content 设为可搜索字段。
//
// 这点值得留意：其余字段（tags、kind、created_at、id）仍随文档存储、
// 也会被搜到结果里返回，但不参与全文匹配。否则搜索「2024」会意外命中
// 时间戳，「article」会命中所有文章——把结构化字段排除在匹配之外才对。
func (c *Client) configureIndex(ctx context.Context) error {
	settings := map[string]any{
		"searchableAttributes": []string{"title", "content"},
	}
	var task taskEnvelope
	if err := c.do(ctx, http.MethodPatch, "/indexes/"+IndexName+"/settings", settings, &task); err != nil {
		return err
	}
	return c.waitTask(ctx, task.TaskUID)
}

// ResetIndex 删除并重建索引。删除是尽力而为的：首次启动时索引本就不存在，
// index_not_found 不算失败。
//
// 注意这是**全量重建**：删除与重新灌数据之间索引是空的，这段时间搜索会返回 0 条。
// 博客本身照常可用，只是搜索短暂失灵——调用方需要能接受这个窗口。
func (c *Client) ResetIndex(ctx context.Context) error {
	logging.Debugf("[search] resetting index %s", IndexName)
	err := c.performTask(ctx, http.MethodDelete, "/indexes/"+IndexName, nil)
	if err != nil && !strings.Contains(err.Error(), "index_not_found") {
		return err
	}
	return c.EnsureIndex(ctx)
}

// Sync 重建索引，并把所有**未删除**的文章与页面灌进去。
//
// 用的是 List 而非 ListDeleted，所以回收站里的内容不会被索引——
// 删掉的文章不会继续出现在搜索结果里。
//
// 分页拉取、每批 100 条、边取边写；终止条件是「已取满 total」或「本页为空」，
// 后者用来兜住内容在同步过程中被删除导致 total 偏大的情况。
//
// 中途失败会留下一个只灌了一半的索引，因此调用方必须能接受
// 「搜索结果暂时不完整」，router 正是这样处理的（降级 + 后台重试）。
func (c *Client) Sync(ctx context.Context, articleRepo model.ArticleRepository, pageRepo model.PageRepository) error {
	logging.Debugf("[search] starting index sync")
	if err := c.ResetIndex(ctx); err != nil {
		return err
	}

	const batchSize = 100
	page := 1
	articleCount := 0
	for {
		articles, total, err := articleRepo.List(ctx, model.Pagination{Page: page, PageSize: batchSize})
		if err != nil {
			return fmt.Errorf("list articles for search sync: %w", err)
		}
		docs := make([]map[string]any, 0, len(articles))
		for i := range articles {
			docs = append(docs, articleDoc(&articles[i]))
		}
		if len(docs) > 0 {
			if err := c.addDocuments(ctx, docs); err != nil {
				return fmt.Errorf("index articles batch: %w", err)
			}
			articleCount += len(docs)
		}
		if int64(page*batchSize) >= total || len(articles) == 0 {
			break
		}
		page++
	}
	logging.Debugf("[search] indexed %d articles", articleCount)

	page = 1
	pageCount := 0
	for {
		pages, total, err := pageRepo.List(ctx, model.Pagination{Page: page, PageSize: batchSize})
		if err != nil {
			return fmt.Errorf("list pages for search sync: %w", err)
		}
		docs := make([]map[string]any, 0, len(pages))
		for i := range pages {
			docs = append(docs, pageDoc(&pages[i]))
		}
		if len(docs) > 0 {
			if err := c.addDocuments(ctx, docs); err != nil {
				return fmt.Errorf("index pages batch: %w", err)
			}
			pageCount += len(docs)
		}
		if int64(page*batchSize) >= total || len(pages) == 0 {
			break
		}
		page++
	}
	logging.Debugf("[search] indexed %d pages", pageCount)

	logging.Debugf("[search] index sync complete")
	return nil
}

// IndexArticle 与 IndexPage 实现 service 层依赖的接口，负责单个文档的增量索引。
// 两者都会同步等到 Meilisearch 任务结束才返回，所以失败能被调用方立即看到
// （service 层据此决定是否记日志告警）。
func (c *Client) IndexArticle(ctx context.Context, article *model.Article) error {
	return c.addDocuments(ctx, []map[string]any{articleDoc(article)})
}

func (c *Client) IndexPage(ctx context.Context, page *model.Page) error {
	return c.addDocuments(ctx, []map[string]any{pageDoc(page)})
}

func (c *Client) DeleteArticle(ctx context.Context, id int64) error {
	return c.deleteDoc(ctx, docID("article", id))
}

// DeleteArticles 在一次请求里删除多篇文章文档。
//
// 为什么必须有批量版本：调用方（批量永久删除）原先是对每个 id 调一次
// DeleteArticle，而每次都要提交任务并**同步等到任务结束**——实测单 id 约 104ms，
// 成本只取决于请求里 id 的个数（不存在的 id 照样外呼）。改成 delete-batch 之后，
// 无论多少个 id 都只提交一个任务、只等一次。
func (c *Client) DeleteArticles(ctx context.Context, ids []int64) error {
	return c.deleteDocs(ctx, "article", ids)
}

// DeletePages 是 DeleteArticles 的页面版本。
func (c *Client) DeletePages(ctx context.Context, ids []int64) error {
	return c.deleteDocs(ctx, "page", ids)
}

// deleteDocs 用 Meilisearch 的 delete-batch 接口一次删掉一批文档。
//
// 文档不存在时不报错：批量删除里混进一个从未被索引过的 id 是正常情况，
// 结果与「删掉了」对调用方没有区别。
func (c *Client) deleteDocs(ctx context.Context, kind string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	docIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		docIDs = append(docIDs, docID(kind, id))
	}
	logging.Debugf("[search] deleting %d %s document(s) in one batch", len(docIDs), kind)

	err := c.performTask(ctx, http.MethodPost, "/indexes/"+IndexName+"/documents/delete-batch", docIDs)
	if err != nil && (strings.Contains(err.Error(), "document_not_found") || strings.Contains(err.Error(), "not found")) {
		logging.Debugf("[search] batch delete found no matching documents kind=%s", kind)
		return nil
	}
	return err
}

func (c *Client) DeletePage(ctx context.Context, id int64) error {
	return c.deleteDoc(ctx, docID("page", id))
}

func (c *Client) addDocuments(ctx context.Context, docs []map[string]any) error {
	if len(docs) == 0 {
		return nil
	}
	return c.performTask(ctx, http.MethodPost, "/indexes/"+IndexName+"/documents", docs)
}

// deleteDoc 删除索引中的单个文档。
// 文档本就不存在时不报错：那通常意味着「删一个从未被索引过的对象」，
// 对调用方来说结果一样，不该升级成错误。
func (c *Client) deleteDoc(ctx context.Context, id string) error {
	logging.Debugf("[search] deleting document id=%s", id)
	err := c.performTask(ctx, http.MethodDelete, "/indexes/"+IndexName+"/documents/"+id, nil)
	if err != nil && (strings.Contains(err.Error(), "document_not_found") || strings.Contains(err.Error(), "not found")) {
		logging.Debugf("[search] delete document not found id=%s", id)
		return nil
	}
	return err
}

// articleDoc 把文章映射成索引文档。
//
// doc_id 带 "article_" 前缀而不是裸 ID：文章与页面共用一个索引，
// 两者的自增 ID 一定会撞车，靠前缀才能保证文档 ID 唯一。
// tags 为 nil 时规范成空数组——Meilisearch 不接受 null，
// 缺字段与空数组在这里都表示「没有标签」。
//
// cover_media_id 只在有头图时才写进去（没有就整个省略）。Meilisearch 的
// POST /documents 是「有则整体替换」，所以把字段删掉再索引一次，
// 索引进里的旧值也会随之消失，不会留下过期的头图 id。
//
// 为什么要进索引：twilight-blog-api 的 ArticleSearchResult 继承 Article，
// 而 Article 把 cover_media_id 声明为必填（`number | null`）。搜索结果若不带
// 这个字段，类型声明就与实际载荷不符——同类的坑在 tags 上踩过一次（那次是
// 直接白屏）。搜索页目前并不渲染头图，但契约不是「今天谁在用」决定的。
func articleDoc(article *model.Article) map[string]any {
	tags := article.Tags
	if tags == nil {
		tags = []string{}
	}
	doc := map[string]any{
		"doc_id":     docID("article", article.ID),
		"kind":       "article",
		"id":         article.ID,
		"title":      article.Title,
		"content":    article.Content,
		"created_at": article.CreatedAt,
		"updated_at": article.UpdatedAt,
		"tags":       tags,
	}
	if article.CoverMediaID != nil {
		doc["cover_media_id"] = *article.CoverMediaID
	}
	return doc
}

// pageDoc 把页面映射成索引文档，形状与 articleDoc 对齐，
// 差别是没有 tags（页面本来就没有标签）。
func pageDoc(page *model.Page) map[string]any {
	return map[string]any{
		"doc_id":     docID("page", page.ID),
		"kind":       "page",
		"id":         page.ID,
		"title":      page.Title,
		"content":    page.Content,
		"created_at": page.CreatedAt,
		"updated_at": page.UpdatedAt,
	}
}

// docID 生成跨类型唯一的文档 ID；kind 取 "article" 或 "page"。
func docID(kind string, id int64) string {
	return kind + "_" + strconv.FormatInt(id, 10)
}

type taskEnvelope struct {
	TaskUID int64 `json:"taskUid"`
}

type taskStatus struct {
	UID    int64  `json:"uid"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Type    string `json:"type"`
	} `json:"error"`
}

// performTask 提交写请求并等待其对应的异步任务结束。
// 不等待就无法判断「到底写成功了没有」——Meilisearch 的写接口只回一个 taskUid。
func (c *Client) performTask(ctx context.Context, method, path string, body any) error {
	var envelope taskEnvelope
	if err := c.do(ctx, method, path, body, &envelope); err != nil {
		return err
	}
	return c.waitTask(ctx, envelope.TaskUID)
}

// waitTask 轮询任务直到 succeeded / failed / canceled，或超过 30 秒总超时。
//
// 轮询间隔 100ms，并尊重 ctx 取消：上层请求断开后没必要继续空转。
// taskUID 为 0 视为「服务端未返回任务」，直接放行——某些幂等操作
// （例如删除一个不存在的文档）不会产生任务。
func (c *Client) waitTask(ctx context.Context, taskUID int64) error {
	if taskUID == 0 {
		return nil
	}
	logging.Debugf("[search] waiting for meilisearch task %d", taskUID)
	const timeout = 30 * time.Second
	deadline := time.Now().Add(timeout)
	for {
		var task taskStatus
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/tasks/%d", taskUID), nil, &task); err != nil {
			return err
		}
		switch task.Status {
		case "succeeded":
			logging.Debugf("[search] meilisearch task %d succeeded", taskUID)
			return nil
		case "failed", "canceled":
			message := task.Status
			if task.Error != nil && task.Error.Message != "" {
				message = task.Error.Message
			}
			return fmt.Errorf("meilisearch task %d %s: %s", taskUID, task.Status, message)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("meilisearch task %d timed out after %s (status %s)", taskUID, timeout, task.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type searchRequest struct {
	Query                 string   `json:"q"`
	Offset                int      `json:"offset"`
	Limit                 int      `json:"limit"`
	AttributesToHighlight []string `json:"attributesToHighlight,omitempty"`
}

type searchResponse struct {
	Hits               []map[string]any `json:"hits"`
	TotalHits          int64            `json:"totalHits"`
	EstimatedTotalHits int64            `json:"estimatedTotalHits"`
}

// Result 是返回给前端的单条搜索命中。
//
// Highlight 直接透传 Meilisearch 的 _formatted 字段（高亮片段用 <em> 包裹）。
// 注意其中含 HTML 标签，前端渲染时必须转义或走受控的白名单，
// 不能直接当 HTML 插入。
type Result struct {
	Type      string `json:"type"`
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`

	// Tags 只对文章命中存在：页面本来就没有标签（见 pageDoc），因此保持 nil、
	// 由 omitempty 把整个字段省掉。
	//
	// 这里**必须**用指针而不是切片。用 []string 时，一篇没打标签的文章同样
	// 是空切片，会被 omitempty 一并省掉，前端就再也分不清「页面没有 tags」
	// 与「文章有 tags 但为空」——而 ArticleSearchResult 的契约把 tags 列为
	// 必填且前端直接读 `result.tags.length`，缺字段会抛 TypeError 把整个搜索页
	// 打空。指针让「文章」这一侧始终输出数组（空也是 []）。
	Tags *[]string `json:"tags,omitempty"`

	// CoverMediaID 是文章头图的媒体 id，没有头图时是 null。
	//
	// 与 Tags 相反，它**不带** omitempty：ArticleSearchResult 继承 Article，
	// 而 Article 把 cover_media_id 声明为必填（`number | null`），所以文章命中
	// 必须恒输出这个字段，没头图时输出 null 而不是省略。页面命中也会带上它
	// （同为 null），这是可接受的：PageSearchResult 不声明该字段，多一个 null
	// 不会让任何调用方出错，而为一个类型分支增加一个字段形状的代价更大。
	CoverMediaID *int64 `json:"cover_media_id"`

	Highlight map[string]any `json:"highlight,omitempty"`
}

// Search 执行一次查询，返回结果与总数。
//
// page / pageSize 在这里再兜一次默认值（默认 12，与 handler 的 maxPageSize 一致），
// 因为本方法也可能被非 HTTP 调用方使用；正常 HTTP 路径上已经由 parsePagination 校验过。
//
// total 取自 totalHits，缺失时退回 estimatedTotalHits——用 offset/limit 查询时
// Meilisearch 只给后者，而它名字里的 estimated 是认真的：**它是估算值**。
// 所以搜索页的总页数可能与实际略有偏差，前端不要把它当精确计数用
// （数据库侧的分页是精确 COUNT，两者性质不同）。
func (c *Client) Search(ctx context.Context, query string, page, pageSize int) ([]Result, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 12
	}
	logging.Debugf("[search] searching query=%q page=%d page_size=%d", query, page, pageSize)

	reqBody := searchRequest{
		Query:                 query,
		Offset:                (page - 1) * pageSize,
		Limit:                 pageSize,
		AttributesToHighlight: []string{"title", "content"},
	}

	var resp searchResponse
	if err := c.do(ctx, http.MethodPost, "/indexes/"+IndexName+"/search", reqBody, &resp); err != nil {
		return nil, 0, err
	}

	results := make([]Result, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		results = append(results, resultFromHit(hit))
	}

	total := resp.TotalHits
	if total == 0 && resp.EstimatedTotalHits > 0 {
		total = resp.EstimatedTotalHits
	}
	logging.Debugf("[search] search returned %d/%d results", len(results), total)
	return results, total, nil
}

// resultFromHit 把一条 Meilisearch 命中映射成返回给前端的 Result。
//
// 文章的 tags 一定被填成**非 nil** 数组，哪怕它是空的：契约要求文章的
// tags 字段始终存在（见 Result.Tags 的说明）。页面的 tags 保持 nil，
// 于是序列化时整个字段消失。
//
// cover_media_id 只从索引里取值：0（取不到）视为「没有头图」，因为媒体的自增
// id 从 1 开始，0 永远不是一张真实的图。这里不能像 tags 那样在 JSON 里区分
// 「字段缺失」与「值为 0」——对调用方来说两者都是同一件事：没有头图。
func resultFromHit(hit map[string]any) Result {
	kind := sourceString(hit, "kind")
	item := Result{
		Type:      kind,
		ID:        sourceInt64(hit, "id"),
		Title:     sourceString(hit, "title"),
		Content:   sourceString(hit, "content"),
		CreatedAt: sourceInt64(hit, "created_at"),
		UpdatedAt: sourceInt64(hit, "updated_at"),
	}

	if kind == "article" {
		tags := []string{}
		if list, ok := hit["tags"].([]any); ok {
			for _, tag := range list {
				if s, ok := tag.(string); ok {
					tags = append(tags, s)
				}
			}
		}
		item.Tags = &tags

		if coverID := sourceInt64(hit, "cover_media_id"); coverID > 0 {
			item.CoverMediaID = &coverID
		}
	}

	if formatted, ok := hit["_formatted"].(map[string]any); ok && len(formatted) > 0 {
		item.Highlight = formatted
	}
	return item
}

func sourceString(source map[string]any, key string) string {
	value, _ := source[key].(string)
	return value
}

// sourceInt64 从反序列化后的 JSON map 里取整数。
// 需要同时兼容 float64（encoding/json 的默认数字类型）与 json.Number，
// 因为同一份数据可能来自不同的解码路径。取不到时返回 0 而不是报错——
// 搜索结果的字段缺失不应该让整个查询失败。
func sourceInt64(source map[string]any, key string) int64 {
	switch v := source[key].(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case int64:
		return v
	}
	return 0
}

// do 是本包唯一的 HTTP 出口。
//
// 它统一做了四件事：按需加 Bearer 鉴权头与 Content-Type、把非 2xx 归一化成
// 带状态码和响应体的 error（这是唯一能看到 Meilisearch 具体报错的地方）、
// 把响应体读取上限压到 8MB 以防异常响应吃满内存、以及只在有内容时才反序列化。
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	logging.Debugf("[search] HTTP %s %s", method, path)
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("meilisearch %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	logging.Debugf("[search] HTTP %s %s -> %d", method, path, resp.StatusCode)
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode meilisearch response: %w", err)
		}
	}
	return nil
}
