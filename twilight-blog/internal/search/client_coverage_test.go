package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"blog-engine/internal/database"
	"blog-engine/internal/model"
)

// fakeMeili 是一个足够覆盖 client.go 全部请求形状的本地 Meilisearch 替身。
// 所有写请求返回 taskUid，/tasks/:id 统一成功；需要构造错误时由字段控制。
type fakeMeili struct {
	t         *testing.T
	taskSeq   atomic.Int64
	taskState string // 空 = succeeded
	indexMode string // "exists" 让 POST /indexes 返回 index_already_exists
	resetMode string // "missing" 让 DELETE /indexes 返回 index_not_found
	searchHit map[string]any
	lastAuth  atomic.Value
	requests  atomic.Int64
}

func (f *fakeMeili) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	f.lastAuth.Store(r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path

	if path == "/indexes" && r.Method == http.MethodPost {
		if f.indexMode == "exists" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"index_already_exists"}`)
			return
		}
		writeTask(w, f.taskSeq.Add(1))
		return
	}
	if path == "/indexes/"+IndexName+"/settings" && r.Method == http.MethodPatch {
		writeTask(w, f.taskSeq.Add(1))
		return
	}
	if path == "/indexes/"+IndexName && r.Method == http.MethodDelete {
		if f.resetMode == "missing" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"index_not_found"}`)
			return
		}
		writeTask(w, f.taskSeq.Add(1))
		return
	}
	if strings.HasPrefix(path, "/tasks/") {
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/tasks/"), 10, 64)
		status := f.taskState
		if status == "" {
			status = "succeeded"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"uid": id, "status": status})
		return
	}
	if path == "/indexes/"+IndexName+"/documents" && r.Method == http.MethodPost {
		writeTask(w, f.taskSeq.Add(1))
		return
	}
	if path == "/indexes/"+IndexName+"/documents/delete-batch" && r.Method == http.MethodPost {
		writeTask(w, f.taskSeq.Add(1))
		return
	}
	if strings.HasPrefix(path, "/indexes/"+IndexName+"/documents/") && r.Method == http.MethodDelete {
		writeTask(w, f.taskSeq.Add(1))
		return
	}
	if path == "/indexes/"+IndexName+"/search" && r.Method == http.MethodPost {
		hit := f.searchHit
		if hit == nil {
			hit = map[string]any{
				"kind": "article", "id": float64(1), "title": "t", "content": "c",
				"created_at": float64(10), "updated_at": float64(20),
				"tags": []any{"Go"}, "cover_media_id": float64(9),
				"_formatted": map[string]any{"title": "<em>t</em>"},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": []any{hit}, "totalHits": 1})
		return
	}
	http.NotFound(w, r)
}

func writeTask(w http.ResponseWriter, uid int64) {
	_ = json.NewEncoder(w).Encode(map[string]any{"taskUid": uid})
}

func fmtSscan(path, prefix string, id *int64) (int, error) {
	return fmtSscanImpl(strings.TrimPrefix(path, prefix), id)
}

func fmtSscanImpl(value string, id *int64) (int, error) {
	var parsed int64
	for _, r := range value {
		if r < '0' || r > '9' {
			break
		}
		parsed = parsed*10 + int64(r-'0')
	}
	*id = parsed
	return 0, nil
}

func newSearchTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestClientEnsureResetAndSync(t *testing.T) {
	fake := &fakeMeili{t: t}
	server := httptest.NewServer(fake)
	defer server.Close()
	client := NewClient(server.URL+"/", "secret")

	ctx := context.Background()
	if err := client.EnsureIndex(ctx); err != nil {
		t.Fatalf("EnsureIndex: %v", err)
	}
	if got := fake.lastAuth.Load(); got != "Bearer secret" {
		t.Fatalf("Authorization = %v", got)
	}
	fake.indexMode = "exists"
	if err := client.EnsureIndex(ctx); err != nil {
		t.Fatalf("EnsureIndex already exists: %v", err)
	}

	fake.resetMode = "missing"
	if err := client.ResetIndex(ctx); err != nil {
		t.Fatalf("ResetIndex missing: %v", err)
	}

	db := newSearchTestDB(t)
	articleRepo := model.NewSQLiteArticleRepository(db)
	pageRepo := model.NewSQLitePageRepository(db)
	if err := articleRepo.Create(ctx, &model.Article{Title: "a", Content: "b", CreatedAt: 1, UpdatedAt: 1, Tags: []string{"Go"}}); err != nil {
		t.Fatalf("create article: %v", err)
	}
	if err := pageRepo.Create(ctx, &model.Page{Title: "p", Content: "c", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("create page: %v", err)
	}
	fake.indexMode = ""
	fake.resetMode = ""
	if err := client.Sync(ctx, articleRepo, pageRepo); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	article := &model.Article{ID: 5, Title: "t", Content: "c", Tags: nil}
	if err := client.IndexArticle(ctx, article); err != nil {
		t.Fatalf("IndexArticle: %v", err)
	}
	if err := client.IndexPage(ctx, &model.Page{ID: 6, Title: "p"}); err != nil {
		t.Fatalf("IndexPage: %v", err)
	}
	if err := client.DeleteArticle(ctx, 5); err != nil {
		t.Fatalf("DeleteArticle: %v", err)
	}
	if err := client.DeletePage(ctx, 6); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}
	if err := client.DeleteArticles(ctx, []int64{1, 2}); err != nil {
		t.Fatalf("DeleteArticles: %v", err)
	}
	if err := client.DeletePages(ctx, []int64{3}); err != nil {
		t.Fatalf("DeletePages: %v", err)
	}
}

func TestClientSearchAndDefaults(t *testing.T) {
	fake := &fakeMeili{t: t}
	server := httptest.NewServer(fake)
	defer server.Close()
	client := NewClient(server.URL, "")
	results, total, err := client.Search(context.Background(), "q", 0, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 1 || len(results) != 1 {
		t.Fatalf("Search = (%v, %d)", results, total)
	}
	if results[0].Tags == nil || len(*results[0].Tags) != 1 || results[0].CoverMediaID == nil {
		t.Fatalf("Search result = %+v", results[0])
	}
	if results[0].Highlight == nil {
		t.Fatal("highlight should be propagated")
	}
}

func TestClientErrorsAndWaitTask(t *testing.T) {
	// 非 2xx。
	failure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failure.Close()
	client := NewClient(failure.URL, "")
	if err := client.do(context.Background(), http.MethodGet, "/x", nil, nil); err == nil {
		t.Fatal("non-2xx should fail")
	}

	// 响应 JSON 无法解码。
	badJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "{")
	}))
	defer badJSON.Close()
	var out map[string]any
	if err := NewClient(badJSON.URL, "").do(context.Background(), http.MethodGet, "/x", nil, &out); err == nil {
		t.Fatal("invalid JSON should fail")
	}

	// 请求构造失败（非法 URL）。
	badURL := NewClient("http://[::1", "")
	if err := badURL.do(context.Background(), http.MethodGet, "/x", nil, nil); err == nil {
		t.Fatal("invalid base URL should fail")
	}

	// body 无法 JSON 序列化。
	if err := client.do(context.Background(), http.MethodPost, "/x", make(chan int), nil); err == nil {
		t.Fatal("unmarshalable body should fail")
	}

	// waitTask 0 直接返回。
	if err := client.waitTask(context.Background(), 0); err != nil {
		t.Fatalf("waitTask(0) = %v", err)
	}

	// task failed / canceled。
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uid": 1, "status": "failed", "error": map[string]any{"message": "nope"},
		})
	}))
	defer failed.Close()
	if err := NewClient(failed.URL, "").waitTask(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("failed task error = %v", err)
	}

	canceled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"uid": 1, "status": "canceled"})
	}))
	defer canceled.Close()
	if err := NewClient(canceled.URL, "").waitTask(context.Background(), 1); err == nil {
		t.Fatal("canceled task should fail")
	}

	// ctx 取消：需要先返回一个非终态，再在 sleep 分支取消。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"uid": 1, "status": "enqueued"})
	}))
	defer slow.Close()
	if err := NewClient(slow.URL, "").waitTask(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waitTask error = %v, want context.Canceled", err)
	}
}

func TestClientDeleteMissingIsIgnored(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"document_not_found"}`, http.StatusNotFound)
	}))
	defer server.Close()
	client := NewClient(server.URL, "")
	if err := client.DeleteArticle(context.Background(), 1); err != nil {
		t.Fatalf("missing document delete should be ignored: %v", err)
	}
	if err := client.DeleteArticles(context.Background(), []int64{1}); err != nil {
		t.Fatalf("missing batch delete should be ignored: %v", err)
	}
}
