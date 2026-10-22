package search

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blog-engine/internal/model"
)

// stubArticleRepo 只实现 List；其余方法返回零值。
// Sync 只读 List，所以零值是安全默认——真正被调用的方法由字段注入。
type stubArticleRepo struct {
	list func(context.Context, model.Pagination) ([]model.Article, int64, error)
}

func (s stubArticleRepo) Create(context.Context, *model.Article) error { return nil }
func (s stubArticleRepo) Update(context.Context, *model.Article) error { return nil }
func (s stubArticleRepo) SoftDelete(context.Context, []int64, int64) (int64, error) {
	return 0, nil
}
func (s stubArticleRepo) Restore(context.Context, int64, int64) error { return nil }
func (s stubArticleRepo) BatchRestore(context.Context, []int64, int64) (int64, error) {
	return 0, nil
}
func (s stubArticleRepo) HardDelete(context.Context, []int64) (int64, error) { return 0, nil }
func (s stubArticleRepo) CoverMediaIDsToDelete(context.Context, []int64) ([]int64, error) {
	return nil, nil
}
func (s stubArticleRepo) GetByID(context.Context, int64) (*model.Article, error) {
	return nil, model.ErrNotFound
}
func (s stubArticleRepo) ListDeleted(context.Context, model.Pagination) ([]model.Article, int64, error) {
	return nil, 0, nil
}
func (s stubArticleRepo) List(ctx context.Context, p model.Pagination) ([]model.Article, int64, error) {
	if s.list == nil {
		return nil, 0, nil
	}
	return s.list(ctx, p)
}

type stubPageRepo struct {
	list func(context.Context, model.Pagination) ([]model.Page, int64, error)
}

func (s stubPageRepo) Create(context.Context, *model.Page) error { return nil }
func (s stubPageRepo) Update(context.Context, *model.Page) error { return nil }
func (s stubPageRepo) SoftDelete(context.Context, []int64, int64) (int64, error) {
	return 0, nil
}
func (s stubPageRepo) Restore(context.Context, int64, int64) error { return nil }
func (s stubPageRepo) BatchRestore(context.Context, []int64, int64) (int64, error) {
	return 0, nil
}
func (s stubPageRepo) HardDelete(context.Context, []int64) (int64, error) { return 0, nil }
func (s stubPageRepo) GetByID(context.Context, int64) (*model.Page, error) {
	return nil, model.ErrPageNotFound
}
func (s stubPageRepo) ListDeleted(context.Context, model.Pagination) ([]model.Page, int64, error) {
	return nil, 0, nil
}
func (s stubPageRepo) List(ctx context.Context, p model.Pagination) ([]model.Page, int64, error) {
	if s.list == nil {
		return nil, 0, nil
	}
	return s.list(ctx, p)
}

// searchServer 是一个按路径分派的 Meilisearch 替身，failPath 命中的请求返回 500。
type searchServer struct {
	failPath string
	docCalls int
	tasks    string
}

func (s *searchServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.failPath != "" && r.URL.Path == s.failPath {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(r.URL.Path, "/tasks/") {
		status := s.tasks
		if status == "" {
			status = "succeeded"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"uid": 1, "status": status})
		return
	}
	if r.URL.Path == "/indexes/"+IndexName+"/documents" {
		s.docCalls++
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"taskUid": 1})
}

func newSearchClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewClient(server.URL, "")
}

func TestEnsureIndexAndConfigureIndexErrors(t *testing.T) {
	failing := newSearchClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	if err := failing.EnsureIndex(context.Background()); err == nil {
		t.Fatal("EnsureIndex must surface a create-index failure")
	}

	settingsOnly := newSearchClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/indexes/"+IndexName+"/settings" {
			http.Error(w, "settings", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"taskUid": 1})
	}))
	if err := settingsOnly.EnsureIndex(context.Background()); err == nil {
		t.Fatal("EnsureIndex must surface a settings failure")
	}
}

func TestResetIndexSurfacesRealFailures(t *testing.T) {
	client := newSearchClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "delete failed", http.StatusInternalServerError)
	}))
	if err := client.ResetIndex(context.Background()); err == nil {
		t.Fatal("ResetIndex must surface a delete failure that is not index_not_found")
	}
}

func TestSyncRepositoryAndBatchErrors(t *testing.T) {
	ok := newSearchClient(t, &searchServer{})

	t.Run("article list failure", func(t *testing.T) {
		repo := stubArticleRepo{list: func(context.Context, model.Pagination) ([]model.Article, int64, error) {
			return nil, 0, errors.New("db down")
		}}
		if err := ok.Sync(context.Background(), repo, stubPageRepo{}); err == nil {
			t.Fatal("article list failure must surface")
		}
	})

	t.Run("article batch failure", func(t *testing.T) {
		bad := newSearchClient(t, &searchServer{failPath: "/indexes/" + IndexName + "/documents"})
		repo := stubArticleRepo{list: func(context.Context, model.Pagination) ([]model.Article, int64, error) {
			return []model.Article{{ID: 1, Title: "a"}}, 1, nil
		}}
		if err := bad.Sync(context.Background(), repo, stubPageRepo{}); err == nil {
			t.Fatal("article batch failure must surface")
		}
	})

	t.Run("page list failure", func(t *testing.T) {
		repo := stubPageRepo{list: func(context.Context, model.Pagination) ([]model.Page, int64, error) {
			return nil, 0, errors.New("db down")
		}}
		if err := ok.Sync(context.Background(), stubArticleRepo{}, repo); err == nil {
			t.Fatal("page list failure must surface")
		}
	})

	t.Run("page batch failure", func(t *testing.T) {
		bad := newSearchClient(t, &searchServer{failPath: "/indexes/" + IndexName + "/documents"})
		repo := stubPageRepo{list: func(context.Context, model.Pagination) ([]model.Page, int64, error) {
			return []model.Page{{ID: 1, Title: "p"}}, 1, nil
		}}
		if err := bad.Sync(context.Background(), stubArticleRepo{}, repo); err == nil {
			t.Fatal("page batch failure must surface")
		}
	})
}

func TestSyncPaginatesUntilTotalsAreReached(t *testing.T) {
	server := &searchServer{}
	client := newSearchClient(t, server)

	articlePages := map[int][]model.Article{
		1: make([]model.Article, 100),
		2: {{ID: 101, Title: "last"}},
	}
	pagePages := map[int][]model.Page{
		1: make([]model.Page, 100),
		2: {{ID: 101, Title: "last"}},
	}
	articles := stubArticleRepo{list: func(_ context.Context, p model.Pagination) ([]model.Article, int64, error) {
		return articlePages[p.Page], 101, nil
	}}
	pages := stubPageRepo{list: func(_ context.Context, p model.Pagination) ([]model.Page, int64, error) {
		return pagePages[p.Page], 101, nil
	}}

	if err := client.Sync(context.Background(), articles, pages); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// 两批文章 + 两批页面。
	if server.docCalls != 4 {
		t.Fatalf("document batches = %d, want 4", server.docCalls)
	}
}

func TestDeleteAndAddDocumentGuards(t *testing.T) {
	ctx := context.Background()
	client := newSearchClient(t, &searchServer{})

	if err := client.DeleteArticles(ctx, nil); err != nil {
		t.Fatalf("DeleteArticles(nil): %v", err)
	}
	if err := client.addDocuments(ctx, nil); err != nil {
		t.Fatalf("addDocuments(nil): %v", err)
	}

	notFound := newSearchClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"document_not_found"}`, http.StatusNotFound)
	}))
	if err := notFound.DeleteArticle(ctx, 1); err != nil {
		t.Fatalf("missing document must be tolerated: %v", err)
	}
	plainNotFound := newSearchClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}))
	if err := plainNotFound.DeleteArticles(ctx, []int64{1}); err != nil {
		t.Fatalf("missing batch must be tolerated: %v", err)
	}

	failing := newSearchClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	if err := failing.DeleteArticle(ctx, 1); err == nil {
		t.Fatal("other delete failures must surface")
	}
	if err := failing.DeleteArticles(ctx, []int64{1}); err == nil {
		t.Fatal("other batch delete failures must surface")
	}
}

func TestWaitTaskPollingEdges(t *testing.T) {
	ctx := context.Background()
	enqueued := newSearchClient(t, &searchServer{tasks: "enqueued"})

	restore := swap(&taskPollTimeout, 5*time.Millisecond)
	restoreInterval := swap(&taskPollInterval, time.Millisecond)
	if err := enqueued.waitTask(ctx, 7); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("waitTask timeout error = %v", err)
	}
	restoreInterval()
	restore()

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	restoreCancel := swap(&taskPollInterval, time.Hour)
	if err := enqueued.waitTask(canceled, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitTask canceled error = %v", err)
	}
	restoreCancel()

	failedNoMessage := newSearchClient(t, &searchServer{tasks: "failed"})
	if err := failedNoMessage.waitTask(ctx, 7); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("failed task without a message must still explain itself: %v", err)
	}
}

func TestSearchErrorAndEstimatedTotal(t *testing.T) {
	failing := newSearchClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	if _, _, err := failing.Search(context.Background(), "q", 1, 12); err == nil {
		t.Fatal("search failure must surface")
	}

	estimated := newSearchClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": []any{}, "estimatedTotalHits": 42})
	}))
	_, total, err := estimated.Search(context.Background(), "q", 1, 12)
	if err != nil || total != 42 {
		t.Fatalf("estimated total = (%d, %v)", total, err)
	}
}

func TestDoReportsBodyReadFailure(t *testing.T) {
	// 声明 100 字节、只写 5 字节就断开：客户端在 io.ReadAll 上拿到 unexpected EOF。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nhello"))
	}()
	client := NewClient("http://"+listener.Addr().String(), "")
	if err := client.do(context.Background(), http.MethodGet, "/x", nil, nil); err == nil {
		t.Fatal("a truncated body must surface")
	}
}

func TestArticleAndPageDocs(t *testing.T) {
	cover := int64(9)
	doc := articleDoc(&model.Article{ID: 1, Title: "t", Tags: nil, TagsEN: nil, CoverMediaID: &cover})
	if doc["cover_media_id"] != int64(9) {
		t.Fatalf("cover_media_id = %v", doc["cover_media_id"])
	}
	if tags, ok := doc["tags"].([]string); !ok || len(tags) != 0 {
		t.Fatalf("nil tags must become an empty slice: %#v", doc["tags"])
	}
	if tags, ok := doc["tags_en"].([]string); !ok || len(tags) != 0 {
		t.Fatalf("nil tags_en must become an empty slice: %#v", doc["tags_en"])
	}
	withoutCover := articleDoc(&model.Article{ID: 2})
	if _, ok := withoutCover["cover_media_id"]; ok {
		t.Fatal("a post without a cover must not carry cover_media_id")
	}
	page := pageDoc(&model.Page{ID: 3, Title: "p"})
	if page["doc_id"] != "page_3" {
		t.Fatalf("page doc_id = %v", page["doc_id"])
	}
}

var _ = io.Discard
