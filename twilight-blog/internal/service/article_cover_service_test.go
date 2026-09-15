package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"blog-engine/internal/database"
	"blog-engine/internal/model"
)

// 本文件覆盖 ArticleService 的头图与发布时间两条新轴，以及此前完全没被覆盖的
// 回收站路径（SoftDelete / Restore / BatchRestore / HardDelete）。
//
// 为什么这些路径值得单独写：它们是**不可逆**的。头图在永久删除时被抹掉，
// 而回收站的意义正是「删错了能找回来」——如果 HardDelete 的判据写宽了一格
// （比如把还被别的文章引用的图也删了，或者对活文章也动了手），损坏是不可撤销的，
// 而且不会有任何测试之外的信号提示。所以下面的断言都落在**媒体库的删除记录**
// 与**库里的行**上，而不是返回值。

// fakeMediaStore 是本文件里 ArticleMediaStore 的实现：一张内存里的小媒体库，
// 记录每次删除请求。真实实现（*MediaService）会往磁盘写文件，用它做测试会
// 让「不该删的图被删了」这种断言变成「文件还在不在」，既慢又绕。
//
// 它同时持有真实的 *sql.DB，因为内存里的这条记录**不足以**骗过数据库：
// articles.cover_media_id 上有真实外键，只往 items 里塞一张图就去插文章，
// 会撞 FOREIGN KEY constraint failed。假件必须连环境一起模拟，否则测试测的是
// 一个不存在的世界（这一点是被真实报错教会的，见 add 的说明）。
type fakeMediaStore struct {
	items     map[int64]*model.Media
	db        *sql.DB
	getErr    error
	deleteErr error
	deleted   []int64
}

func newFakeMediaStore() *fakeMediaStore {
	return &fakeMediaStore{items: map[int64]*model.Media{}}
}

// add 放一条媒体进假库，并在真实的 media 表里建出对应行。
func (f *fakeMediaStore) add(t *testing.T, id int64, mediaType string) {
	t.Helper()

	storageName := "store-" + itoa(id) + ".bin"
	f.items[id] = &model.Media{
		ID:          id,
		StorageName: storageName,
		MediaType:   mediaType,
		MimeType:    mediaType + "/x",
	}
	if f.db == nil {
		return
	}
	if _, err := f.db.Exec(`
INSERT INTO media (id, storage_name, original_name, media_type, mime_type, size, created_at)
VALUES (?, ?, ?, ?, ?, 1, 1)
`, id, storageName, "original.bin", mediaType, mediaType+"/x"); err != nil {
		t.Fatalf("seed media row %d: %v", id, err)
	}
}

func (f *fakeMediaStore) Get(_ context.Context, id int64) (*model.Media, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	media, ok := f.items[id]
	if !ok {
		return nil, model.ErrMediaNotFound
	}
	return media, nil
}

func (f *fakeMediaStore) Delete(_ context.Context, id int64) error {
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

// recordingIndexer 记录搜索索引的调用，并可让指定方法失败，用来覆盖
// 「尽力而为、失败只记日志」的各个分支。
type recordingIndexer struct {
	indexed     []*model.Article
	deleted     []int64
	bulkDeleted [][]int64
	indexErr    error
	deleteErr   error
	bulkErr     error
}

func (r *recordingIndexer) IndexArticle(_ context.Context, article *model.Article) error {
	r.indexed = append(r.indexed, article)
	return r.indexErr
}

func (r *recordingIndexer) DeleteArticle(_ context.Context, id int64) error {
	r.deleted = append(r.deleted, id)
	return r.deleteErr
}

func (r *recordingIndexer) DeleteArticles(_ context.Context, ids []int64) error {
	r.bulkDeleted = append(r.bulkDeleted, ids)
	return r.bulkErr
}

// failingArticleRepo 把一个真实仓储包起来，只把需要注入失败的方法替换掉。
// 其余方法照常走真实 SQL，测试因此仍然依赖真实的约束（例如 HardDelete 的
// deleted_at IS NOT NULL 前置条件）。
type failingArticleRepo struct {
	model.ArticleRepository
	createErr       error
	updateErr       error
	softDeleteErr   error
	restoreErr      error
	batchRestoreErr error
	hardDeleteErr   error
	coverQueryErr   error
	getByIDErr      error
}

func (r failingArticleRepo) Create(ctx context.Context, article *model.Article) error {
	if r.createErr != nil {
		return r.createErr
	}
	return r.ArticleRepository.Create(ctx, article)
}

func (r failingArticleRepo) Update(ctx context.Context, article *model.Article) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	return r.ArticleRepository.Update(ctx, article)
}

func (r failingArticleRepo) SoftDelete(ctx context.Context, ids []int64, deletedAt int64) (int64, error) {
	if r.softDeleteErr != nil {
		return 0, r.softDeleteErr
	}
	return r.ArticleRepository.SoftDelete(ctx, ids, deletedAt)
}

func (r failingArticleRepo) Restore(ctx context.Context, id int64, updatedAt int64) error {
	if r.restoreErr != nil {
		return r.restoreErr
	}
	return r.ArticleRepository.Restore(ctx, id, updatedAt)
}

func (r failingArticleRepo) BatchRestore(ctx context.Context, ids []int64, updatedAt int64) (int64, error) {
	if r.batchRestoreErr != nil {
		return 0, r.batchRestoreErr
	}
	return r.ArticleRepository.BatchRestore(ctx, ids, updatedAt)
}

func (r failingArticleRepo) HardDelete(ctx context.Context, ids []int64) (int64, error) {
	if r.hardDeleteErr != nil {
		return 0, r.hardDeleteErr
	}
	return r.ArticleRepository.HardDelete(ctx, ids)
}

func (r failingArticleRepo) CoverMediaIDsToDelete(ctx context.Context, ids []int64) ([]int64, error) {
	if r.coverQueryErr != nil {
		return nil, r.coverQueryErr
	}
	return r.ArticleRepository.CoverMediaIDsToDelete(ctx, ids)
}

func (r failingArticleRepo) GetByID(ctx context.Context, id int64) (*model.Article, error) {
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	return r.ArticleRepository.GetByID(ctx, id)
}

// newArticleTestRig 起一个临时库 + 真实仓储，并挂上可注入的索引器与媒体库。
func newArticleTestRig(t *testing.T) (*ArticleService, *model.SQLiteArticleRepository, *fakeMediaStore, *recordingIndexer) {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "articles.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repo := model.NewSQLiteArticleRepository(db)
	store := newFakeMediaStore()
	store.db = db
	indexer := &recordingIndexer{}
	return NewArticleService(repo, indexer, store), repo, store, indexer
}

// newTestArticleRepo 起一个临时库并返回真实仓储（供需要注入失败的用例复用）。
func newTestArticleRepo(t *testing.T) *model.SQLiteArticleRepository {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "inject.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return model.NewSQLiteArticleRepository(db)
}

// strptr / intptr 让「显式提供」在测试里读得出来。
func strptr(s string) *string { return &s }
func intptr(v int64) *int64   { return &v }

func itoa(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}

// ---------------------------------------------------------------------------
// Create：发布时间
// ---------------------------------------------------------------------------

// TestCreateWithoutPublishedAtUsesNow 钉住「不传发布时间」= 用服务器当前时间。
// 若哪天有人把 nil 当成 0，文章会以 1970 年发布并沉到列表最底——一次静默的排序错乱。
func TestCreateWithoutPublishedAtUsesNow(t *testing.T) {
	svc, repo, _, indexer := newArticleTestRig(t)
	ctx := context.Background()

	before := time.Now().Unix()
	article, err := svc.Create(ctx, ArticleWrite{Title: "默认时间", Content: strptr("正文")})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	after := time.Now().Unix()

	if article.CreatedAt < before || article.CreatedAt > after {
		t.Fatalf("CreatedAt = %d, want within [%d, %d]", article.CreatedAt, before, after)
	}
	if article.CoverMediaID != nil {
		t.Fatalf("CoverMediaID = %v, want nil when no cover was requested", *article.CoverMediaID)
	}

	// 库里必须与返回值一致，而不是只有内存里的那一个。
	stored, err := repo.GetByID(ctx, article.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.CreatedAt != article.CreatedAt {
		t.Fatalf("stored CreatedAt = %d, want %d", stored.CreatedAt, article.CreatedAt)
	}
	if len(indexer.indexed) != 1 {
		t.Fatalf("created article must be indexed once, got %d", len(indexer.indexed))
	}
}

// TestCreateHonoursAnExplicitPublishedAt 是「补录旧文 / 校正日期」这个需求的本体：
// 客户端给的发布时间必须原样落进 created_at（列表按它排序），而不是被「现在」覆盖。
func TestCreateHonoursAnExplicitPublishedAt(t *testing.T) {
	svc, repo, _, _ := newArticleTestRig(t)
	ctx := context.Background()

	const publishedAt = 1_600_000_000 // 2020-09-13，远早于「现在」
	article, err := svc.Create(ctx, ArticleWrite{
		Title:       "补录的旧文",
		Content:     strptr("正文"),
		PublishedAt: intptr(publishedAt),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if article.CreatedAt != publishedAt {
		t.Fatalf("CreatedAt = %d, want the requested %d", article.CreatedAt, publishedAt)
	}
	if article.UpdatedAt < publishedAt {
		t.Fatalf("UpdatedAt = %d, want it to stay at «now» rather than following the publish date", article.UpdatedAt)
	}

	stored, err := repo.GetByID(ctx, article.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.CreatedAt != publishedAt {
		t.Fatalf("stored CreatedAt = %d, want %d", stored.CreatedAt, publishedAt)
	}
}

// ---------------------------------------------------------------------------
// Create / Update：头图校验
// ---------------------------------------------------------------------------

// TestCreateAcceptsAnImageCover 钉住正向路径，并确认写进去的是**媒体 id**
// 而不是别的什么（比如文件名）。
func TestCreateAcceptsAnImageCover(t *testing.T) {
	svc, repo, store, _ := newArticleTestRig(t)
	ctx := context.Background()
	store.add(t, 5, model.MediaTypeImage)

	article, err := svc.Create(ctx, ArticleWrite{Title: "带头图", CoverMediaID: intptr(5)})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if article.CoverMediaID == nil || *article.CoverMediaID != 5 {
		t.Fatalf("CoverMediaID = %v, want 5", article.CoverMediaID)
	}

	stored, err := repo.GetByID(ctx, article.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.CoverMediaID == nil || *stored.CoverMediaID != 5 {
		t.Fatalf("stored CoverMediaID = %v, want 5", stored.CoverMediaID)
	}
}

// TestCreateRejectsAnUnusableCover 是这一组里最重要的一条：三种「客户端给错了」
// 都必须在**写入之前**变成 *ValidationError（→ 400），并且库里不能留下半篇文章。
//
// 断言分两层：
//   - 错误类型是 *ValidationError（不是 500，更不是静默通过）；
//   - 库里的文章数为 0 —— 校验必须发生在 INSERT 之前。
//
// 反证方式：把 Create 里的 usableCoverID 调用挪到 repo.Create 之后 →
// countArticles 会得到 1，本条失败。
func TestCreateRejectsAnUnusableCover(t *testing.T) {
	const (
		videoID   = int64(6)
		missingID = int64(999)
	)
	cases := []struct {
		name  string
		cover *int64
		seed  func(t *testing.T, s *fakeMediaStore)
	}{
		{name: "non-positive id", cover: intptr(0)},
		{name: "negative id", cover: intptr(-3)},
		{name: "media does not exist", cover: intptr(missingID)},
		{name: "media is not an image", cover: intptr(videoID), seed: func(t *testing.T, s *fakeMediaStore) {
			s.add(t, videoID, "video")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, store, indexer := newArticleTestRig(t)
			ctx := context.Background()
			if tc.seed != nil {
				tc.seed(t, store)
			}

			_, err := svc.Create(ctx, ArticleWrite{Title: "坏头图", CoverMediaID: tc.cover})
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("Create() error = %v, want *ValidationError", err)
			}
			if count := countArticles(t, svc); count != 0 {
				t.Fatalf("%d article(s) were written despite an invalid cover", count)
			}
			if len(indexer.indexed) != 0 {
				t.Fatal("an article that failed validation must not be indexed")
			}
		})
	}
}

// TestCreateSurfacesAnUnexpectedMediaError 区分「客户端给错」与「媒体库坏了」：
// 后者不是 *ValidationError，必须原样上抛（handler 会据此答 500），
// 而不是被折叠成 400 让调用方以为自己传错了 id。
func TestCreateSurfacesAnUnexpectedMediaError(t *testing.T) {
	svc, _, store, _ := newArticleTestRig(t)
	ctx := context.Background()

	boom := errors.New("media storage unavailable")
	store.add(t, 5, model.MediaTypeImage)
	store.getErr = boom

	_, err := svc.Create(ctx, ArticleWrite{Title: "库里坏了", CoverMediaID: intptr(5)})
	if !errors.Is(err, boom) {
		t.Fatalf("Create() error = %v, want the underlying storage error", err)
	}
	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		t.Fatal("an infrastructure failure must not be reported as a client error")
	}
}

// TestCreateSwallowsAnIndexingFailure 钉住「尽力而为」：索引失败不能把已经
// 落库的文章操作变成失败，否则调用方会重试并写出第二篇。
func TestCreateSwallowsAnIndexingFailure(t *testing.T) {
	svc, _, _, indexer := newArticleTestRig(t)
	ctx := context.Background()
	indexer.indexErr = errors.New("meilisearch down")

	if _, err := svc.Create(ctx, ArticleWrite{Title: "索引坏了也要成功"}); err != nil {
		t.Fatalf("Create() error = %v, want nil (indexing is best-effort)", err)
	}
	if count := countArticles(t, svc); count != 1 {
		t.Fatalf("the article must still be persisted, got %d row(s)", count)
	}
}

// TestCreateSurfacesARepositoryFailure 确认数据库失败会原样上抛：
// 落库没成功却返回「创建成功」是比任何 500 都糟的结果。
func TestCreateSurfacesARepositoryFailure(t *testing.T) {
	boom := errors.New("disk full")
	svc := NewArticleService(failingArticleRepo{
		ArticleRepository: newTestArticleRepo(t),
		createErr:         boom,
	}, &recordingIndexer{}, nil)

	if _, err := svc.Create(context.Background(), ArticleWrite{Title: "写不进去"}); !errors.Is(err, boom) {
		t.Fatalf("Create() error = %v, want the repository error", err)
	}
}

// TestUpdateSurfacesARepositoryFailure 同上，但走 Update 路径。
func TestUpdateSurfacesARepositoryFailure(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("disk full")
	base := newTestArticleRepo(t)
	article := mustCreateArticle(t, base, "改不进去")

	svc := NewArticleService(failingArticleRepo{
		ArticleRepository: base,
		updateErr:         boom,
	}, &recordingIndexer{}, nil)

	if _, err := svc.Update(ctx, article.ID, ArticleWrite{Title: "新标题"}); !errors.Is(err, boom) {
		t.Fatalf("Update() error = %v, want the repository error", err)
	}
}

// ---------------------------------------------------------------------------
// Update：发布时间与头图的三态
// ---------------------------------------------------------------------------

// TestUpdatePublishedAtChangesTheSortPosition 是「修改发布时间」的核心契约。
// 只断言存储值还不够——这个功能的意义在于改变**排序**，所以额外验证
// List 的第一条随发布时间变化，这才证明它真的影响了展示顺序。
//
// 这条测试抓出过一个真实缺陷：仓储层的 UPDATE 语句没有写 created_at，
// 于是响应里是新日期、库里还是旧日期，功能整个静默失效。
// 反证方式：把 SQLiteArticleRepository.Update 的 `created_at = ?,` 去掉 → 本条失败。
func TestUpdatePublishedAtChangesTheSortPosition(t *testing.T) {
	svc, repo, _, _ := newArticleTestRig(t)
	ctx := context.Background()

	older, err := svc.Create(ctx, ArticleWrite{Title: "早的", PublishedAt: intptr(1_000)})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	newer, err := svc.Create(ctx, ArticleWrite{Title: "晚的", PublishedAt: intptr(2_000)})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	page, _, err := svc.List(ctx, model.Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page) != 2 || page[0].ID != newer.ID {
		t.Fatalf("before the edit, the newest article must lead the list, got %v", titles(page))
	}

	// 把「早的」改到 3000：它应当顶到最前面。
	if _, err := svc.Update(ctx, older.ID, ArticleWrite{Title: older.Title, PublishedAt: intptr(3_000)}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	page, _, err = svc.List(ctx, model.Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page) != 2 || page[0].ID != older.ID {
		t.Fatalf("after republishing, the edited article must lead the list, got %v", titles(page))
	}

	stored, err := repo.GetByID(ctx, older.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.CreatedAt != 3_000 {
		t.Fatalf("stored CreatedAt = %d, want 3000", stored.CreatedAt)
	}

	// 不传发布时间时它必须一动不动 —— 一次只改标题的保存不能顺手把日期刷成「现在」，
	// 那会把一篇旧文突然顶到首页最前面。
	if _, err := svc.Update(ctx, older.ID, ArticleWrite{Title: "改个标题"}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	stored, err = repo.GetByID(ctx, older.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.CreatedAt != 3_000 {
		t.Fatalf("CreatedAt = %d after a title-only update, want it untouched at 3000", stored.CreatedAt)
	}
}

// TestUpdateCoverIsTriState 钉住头图的三态。三态里的任何一态写错都是数据问题：
// 塌成两态会让「换图」变成「删图」，或者让「只改标题」静默摘掉头图。
func TestUpdateCoverIsTriState(t *testing.T) {
	t.Run("omitted leaves the cover alone", func(t *testing.T) {
		svc, repo, store, _ := newArticleTestRig(t)
		ctx := context.Background()
		store.add(t, 5, model.MediaTypeImage)

		article, err := svc.Create(ctx, ArticleWrite{Title: "原文", CoverMediaID: intptr(5)})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		// CoverSet 为 false，即客户端根本没提这个字段。
		if _, err := svc.Update(ctx, article.ID, ArticleWrite{Title: "新标题"}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		stored, err := repo.GetByID(ctx, article.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if stored.CoverMediaID == nil || *stored.CoverMediaID != 5 {
			t.Fatalf("CoverMediaID = %v, want it untouched at 5", stored.CoverMediaID)
		}
		if len(store.deleted) != 0 {
			t.Fatalf("a title-only update deleted media %v", store.deleted)
		}
	})

	t.Run("explicit null clears the cover without deleting the media", func(t *testing.T) {
		svc, repo, store, _ := newArticleTestRig(t)
		ctx := context.Background()
		store.add(t, 5, model.MediaTypeImage)

		article, err := svc.Create(ctx, ArticleWrite{Title: "原文", CoverMediaID: intptr(5)})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		if _, err := svc.Update(ctx, article.ID, ArticleWrite{Title: "原文", CoverSet: true, CoverMediaID: nil}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		stored, err := repo.GetByID(ctx, article.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if stored.CoverMediaID != nil {
			t.Fatalf("CoverMediaID = %v, want nil after an explicit clear", *stored.CoverMediaID)
		}
		// 摘掉指针 ≠ 删媒体。用户很可能只是换图，旧图该留在库里。
		if len(store.deleted) != 0 {
			t.Fatalf("clearing the cover deleted the media %v; it should stay in the library", store.deleted)
		}
	})

	t.Run("a provided id replaces the cover", func(t *testing.T) {
		svc, repo, store, _ := newArticleTestRig(t)
		ctx := context.Background()
		store.add(t, 5, model.MediaTypeImage)
		store.add(t, 6, model.MediaTypeImage)

		article, err := svc.Create(ctx, ArticleWrite{Title: "原文", CoverMediaID: intptr(5)})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		if _, err := svc.Update(ctx, article.ID, ArticleWrite{Title: "原文", CoverSet: true, CoverMediaID: intptr(6)}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		stored, err := repo.GetByID(ctx, article.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if stored.CoverMediaID == nil || *stored.CoverMediaID != 6 {
			t.Fatalf("CoverMediaID = %v, want 6", stored.CoverMediaID)
		}
		// 换图同样不删旧图：它可能还被别处引用，而且用户可能反悔。
		if len(store.deleted) != 0 {
			t.Fatalf("replacing the cover deleted media %v", store.deleted)
		}
	})
}

// TestUpdateWithAnInvalidCoverChangesNothing 钉住「校验排在赋值之前」。
// 这一条最容易被无意破坏：把 usableCoverID 挪到赋值之后，标题与正文仍会被写进去，
// 一次本该被拒绝的请求却改掉了半篇文章——而且返回的还是 400，调用方以为没生效。
func TestUpdateWithAnInvalidCoverChangesNothing(t *testing.T) {
	svc, repo, store, _ := newArticleTestRig(t)
	ctx := context.Background()
	store.add(t, 5, model.MediaTypeImage)

	article, err := svc.Create(ctx, ArticleWrite{Title: "原标题", Content: strptr("原正文"), CoverMediaID: intptr(5)})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	_, err = svc.Update(ctx, article.ID, ArticleWrite{
		Title:        "偷换的标题",
		Content:      strptr("偷换的正文"),
		CoverSet:     true,
		CoverMediaID: intptr(999), // 不存在
	})
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("Update() error = %v, want *ValidationError", err)
	}

	stored, err := repo.GetByID(ctx, article.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.Title != "原标题" || stored.Content != "原正文" {
		t.Fatalf("a rejected update modified the article: title=%q content=%q", stored.Title, stored.Content)
	}
	if stored.CoverMediaID == nil || *stored.CoverMediaID != 5 {
		t.Fatalf("CoverMediaID = %v, want it untouched at 5", stored.CoverMediaID)
	}
}

// TestUpdateTagsStayTriState 顺带覆盖 tags 的「省略 / 显式空」两种语义，
// 与头图同一类问题：塌成两态会让「只改标题」清空标签。
func TestUpdateTagsStayTriState(t *testing.T) {
	svc, repo, _, _ := newArticleTestRig(t)
	ctx := context.Background()

	article, err := svc.Create(ctx, ArticleWrite{Title: "带标签", Tags: &[]string{"Go", "go", "  ", "SQLite"}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// normalizeTags 去空白、按小写去重并保留首次出现的写法。
	if len(article.Tags) != 2 || article.Tags[0] != "Go" || article.Tags[1] != "SQLite" {
		t.Fatalf("Create() tags = %v, want [Go SQLite]", article.Tags)
	}

	if _, err := svc.Update(ctx, article.ID, ArticleWrite{Title: "带标签"}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	stored, err := repo.GetByID(ctx, article.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if len(stored.Tags) != 2 {
		t.Fatalf("tags = %v, want them untouched", stored.Tags)
	}

	empty := []string{}
	if _, err := svc.Update(ctx, article.ID, ArticleWrite{Title: "带标签", Tags: &empty}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	stored, err = repo.GetByID(ctx, article.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if len(stored.Tags) != 0 {
		t.Fatalf("tags = %v, want them cleared by an explicit empty array", stored.Tags)
	}
}

// TestUpdateOnAMissingArticleIsNotFound 确认 404 与 400 的边界：
// 文章不存在（或已进回收站）不是客户端的格式错误，必须是 model.ErrNotFound。
func TestUpdateOnAMissingArticleIsNotFound(t *testing.T) {
	svc, _, _, _ := newArticleTestRig(t)
	ctx := context.Background()

	_, err := svc.Update(ctx, 4242, ArticleWrite{Title: "不存在"})
	if !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("Update() error = %v, want model.ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// 回收站：SoftDelete / Restore / BatchRestore
// ---------------------------------------------------------------------------

func TestSoftDelete(t *testing.T) {
	svc, _, _, indexer := newArticleTestRig(t)
	ctx := context.Background()

	article, err := svc.Create(ctx, ArticleWrite{Title: "待删"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// 空 ids 是空操作，不应产生任何索引调用。
	if err := svc.SoftDelete(ctx, nil); err != nil {
		t.Fatalf("SoftDelete(nil) error = %v", err)
	}
	indexer.bulkDeleted = nil

	if err := svc.SoftDelete(ctx, []int64{article.ID}); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}
	if count := countArticles(t, svc); count != 0 {
		t.Fatalf("soft-deleted article is still visible (%d row(s))", count)
	}
	if len(indexer.bulkDeleted) != 1 {
		t.Fatalf("soft delete must de-index in one batch call, got %v", indexer.bulkDeleted)
	}
}

// TestSoftDeleteSwallowsAnIndexFailure 与 Create 同一条契约：搜索索引是外部依赖，
// 它挂掉不能把已经生效的软删变成失败——否则调用方会以为没删掉，而重试时
// 那些行已经在回收站里了。
func TestSoftDeleteSwallowsAnIndexFailure(t *testing.T) {
	svc, _, _, indexer := newArticleTestRig(t)
	ctx := context.Background()

	article, err := svc.Create(ctx, ArticleWrite{Title: "索引挂了也要删掉"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	indexer.bulkErr = errors.New("meilisearch down")

	if err := svc.SoftDelete(ctx, []int64{article.ID}); err != nil {
		t.Fatalf("SoftDelete() error = %v, want nil (de-indexing is best-effort)", err)
	}
	if count := countArticles(t, svc); count != 0 {
		t.Fatalf("the article must still be soft-deleted, got %d visible row(s)", count)
	}
}

func TestSoftDeleteSurfacesARepositoryFailure(t *testing.T) {
	ctx := context.Background()
	svc := NewArticleService(failingArticleRepo{
		ArticleRepository: newTestArticleRepo(t),
		softDeleteErr:     errors.New("disk full"),
	}, nil, nil)

	if err := svc.SoftDelete(ctx, []int64{1}); err == nil {
		t.Fatal("a failing SoftDelete must be reported to the caller, not swallowed")
	}
}

// TestSoftDeleteWithoutASearchBackend 覆盖「未启用搜索」的部署：search 为 nil
// 表示这个实例没接 Meilisearch，删除不能因此失败，也不能试图去碰一个不存在的索引。
func TestSoftDeleteWithoutASearchBackend(t *testing.T) {
	repo := newTestArticleRepo(t)
	svc := NewArticleService(repo, nil, nil) // 未启用搜索
	ctx := context.Background()

	article := mustCreateArticle(t, repo, "没有搜索引擎")
	if err := svc.SoftDelete(ctx, []int64{article.ID}); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}

	if _, total, err := svc.ListDeleted(ctx, model.Pagination{Page: 1, PageSize: 10}); err != nil {
		t.Fatalf("ListDeleted() error = %v", err)
	} else if total != 1 {
		t.Fatalf("the article must be in the trash, got total %d", total)
	}
}

func TestRestore(t *testing.T) {
	svc, repo, _, indexer := newArticleTestRig(t)
	ctx := context.Background()

	article, err := svc.Create(ctx, ArticleWrite{Title: "找回我"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := svc.SoftDelete(ctx, []int64{article.ID}); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}
	indexer.indexed = nil

	if err := svc.Restore(ctx, article.ID); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if count := countArticles(t, svc); count != 1 {
		t.Fatalf("restored article is not visible (%d row(s))", count)
	}
	if _, err := repo.GetByID(ctx, article.ID); err != nil {
		t.Fatalf("GetByID() after restore error = %v", err)
	}
	// 恢复必须重新进索引，否则文章回来了却搜不到。
	if len(indexer.indexed) != 1 {
		t.Fatalf("Restore() must re-index, got %d call(s)", len(indexer.indexed))
	}

	// 再恢复一次（此时它不在回收站里）应当报 ErrNotFound。
	if err := svc.Restore(ctx, article.ID); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("Restore() on a live article error = %v, want model.ErrNotFound", err)
	}
}

// TestRestoreStillSucceedsWhenReindexingCannotReadTheRow 钉住「先改库、再索引」
// 的容错方向：行已经恢复成功，只是读不回来时，方法必须返回 nil
// （否则调用方会以为恢复失败，而重试会撞上 ErrNotFound）。
func TestRestoreStillSucceedsWhenReindexingCannotReadTheRow(t *testing.T) {
	ctx := context.Background()
	base := newTestArticleRepo(t)
	article := mustCreateArticle(t, base, "恢复后读不回来")
	if _, err := base.SoftDelete(ctx, []int64{article.ID}, 1); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}

	svc := NewArticleService(failingArticleRepo{
		ArticleRepository: base,
		getByIDErr:        errors.New("transient read failure"),
	}, &recordingIndexer{}, nil)

	if err := svc.Restore(ctx, article.ID); err != nil {
		t.Fatalf("Restore() error = %v, want nil — the restore itself already succeeded", err)
	}
}

func TestRestoreSurfacesARepositoryFailure(t *testing.T) {
	ctx := context.Background()
	svc := NewArticleService(failingArticleRepo{
		ArticleRepository: newTestArticleRepo(t),
		restoreErr:        errors.New("locked"),
	}, nil, nil)

	if err := svc.Restore(ctx, 1); err == nil {
		t.Fatal("a failing Restore must be reported")
	}
}

func TestBatchRestore(t *testing.T) {
	svc, _, _, indexer := newArticleTestRig(t)
	ctx := context.Background()

	first, err := svc.Create(ctx, ArticleWrite{Title: "第一篇"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	second, err := svc.Create(ctx, ArticleWrite{Title: "第二篇"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := svc.SoftDelete(ctx, []int64{first.ID, second.ID}); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}
	indexer.indexed = nil

	if err := svc.BatchRestore(ctx, nil); err != nil {
		t.Fatalf("BatchRestore(nil) error = %v", err)
	}
	if err := svc.BatchRestore(ctx, []int64{first.ID, second.ID}); err != nil {
		t.Fatalf("BatchRestore() error = %v", err)
	}
	if count := countArticles(t, svc); count != 2 {
		t.Fatalf("expected both articles back, got %d", count)
	}
	if len(indexer.indexed) != 2 {
		t.Fatalf("BatchRestore must re-index every restored article, got %d", len(indexer.indexed))
	}
}

// TestBatchRestoreKeepsGoingWhenOneReindexFails 覆盖「逐条重新索引、单条失败只记日志」
// 的分支：一条读不回来不能影响其它条已经被恢复的事实。
func TestBatchRestoreKeepsGoingWhenOneReindexFails(t *testing.T) {
	ctx := context.Background()
	base := newTestArticleRepo(t)
	article := mustCreateArticle(t, base, "批量恢复")
	if _, err := base.SoftDelete(ctx, []int64{article.ID}, 1); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}

	svc := NewArticleService(failingArticleRepo{
		ArticleRepository: base,
		getByIDErr:        errors.New("transient read failure"),
	}, &recordingIndexer{}, nil)

	if err := svc.BatchRestore(ctx, []int64{article.ID}); err != nil {
		t.Fatalf("BatchRestore() error = %v, want nil", err)
	}
}

func TestBatchRestoreSurfacesARepositoryFailure(t *testing.T) {
	ctx := context.Background()
	svc := NewArticleService(failingArticleRepo{
		ArticleRepository: newTestArticleRepo(t),
		batchRestoreErr:   errors.New("locked"),
	}, nil, nil)

	if err := svc.BatchRestore(ctx, []int64{1}); err == nil {
		t.Fatal("a failing BatchRestore must be reported")
	}
}

// ---------------------------------------------------------------------------
// 永久删除：头图跟着走
// ---------------------------------------------------------------------------

// TestHardDeleteRemovesTheOrphanedCover 是本组的核心：文章被永久删除后，
// 它专属的头图（没有任何其它引用者）也应当从媒体库里消失——否则磁盘上
// 会积累一批再也无法通过界面触达的孤儿文件。
func TestHardDeleteRemovesTheOrphanedCover(t *testing.T) {
	svc, repo, store, _ := newArticleTestRig(t)
	ctx := context.Background()
	store.add(t, 5, model.MediaTypeImage)
	store.add(t, 7, model.MediaTypeImage) // 另一篇的头图，必须幸存

	kept, err := svc.Create(ctx, ArticleWrite{Title: "留着", CoverMediaID: intptr(7)})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	doomed, err := svc.Create(ctx, ArticleWrite{Title: "要删", CoverMediaID: intptr(5)})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := svc.SoftDelete(ctx, []int64{doomed.ID}); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}
	if err := svc.HardDelete(ctx, []int64{doomed.ID}); err != nil {
		t.Fatalf("HardDelete() error = %v", err)
	}

	if len(store.deleted) != 1 || store.deleted[0] != 5 {
		t.Fatalf("deleted media = %v, want exactly [5]", store.deleted)
	}
	if _, err := repo.GetByID(ctx, kept.ID); err != nil {
		t.Fatalf("the unrelated article must survive: %v", err)
	}
}

// TestHardDeleteKeepsACoverStillUsedByAnotherArticle 是「头图是引用、不是所有权」
// 这条设计的直接断言。同一张图被两篇文章指向时删掉其中一篇，
// 另一篇的图必须还在——外键的 SET NULL 会把那篇文章的头图静默清成 NULL，
// 页面上的图就消失了，而没有任何一步报错。
func TestHardDeleteKeepsACoverStillUsedByAnotherArticle(t *testing.T) {
	svc, repo, store, _ := newArticleTestRig(t)
	ctx := context.Background()
	store.add(t, 5, model.MediaTypeImage)

	first, err := svc.Create(ctx, ArticleWrite{Title: "共用图 A", CoverMediaID: intptr(5)})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	second, err := svc.Create(ctx, ArticleWrite{Title: "共用图 B", CoverMediaID: intptr(5)})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := svc.SoftDelete(ctx, []int64{first.ID}); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}
	if err := svc.HardDelete(ctx, []int64{first.ID}); err != nil {
		t.Fatalf("HardDelete() error = %v", err)
	}

	if len(store.deleted) != 0 {
		t.Fatalf("media %v was deleted while another article still references it", store.deleted)
	}
	survivor, err := repo.GetByID(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if survivor.CoverMediaID == nil || *survivor.CoverMediaID != 5 {
		t.Fatalf("the surviving article lost its cover: %v (the media must not be deleted)", survivor.CoverMediaID)
	}
}

// TestHardDeleteIgnoresLiveArticles 钉住「永久删除必须先经过回收站」。
// 对一篇活动文章调用它：文章不能消失，头图更不能被删——「什么都没删掉」的操作
// 却毁掉一张图，是这种 bug 里最隐蔽的一种。
func TestHardDeleteIgnoresLiveArticles(t *testing.T) {
	svc, repo, store, _ := newArticleTestRig(t)
	ctx := context.Background()
	store.add(t, 5, model.MediaTypeImage)

	article, err := svc.Create(ctx, ArticleWrite{Title: "活着的", CoverMediaID: intptr(5)})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err := svc.HardDelete(ctx, []int64{article.ID}); err != nil {
		t.Fatalf("HardDelete() error = %v", err)
	}

	if _, err := repo.GetByID(ctx, article.ID); err != nil {
		t.Fatalf("a live article was permanently deleted: %v", err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("media %v was deleted for an article that was not actually removed", store.deleted)
	}
}

func TestHardDeleteEmptyIDsIsANoOp(t *testing.T) {
	svc, _, store, indexer := newArticleTestRig(t)
	if err := svc.HardDelete(context.Background(), nil); err != nil {
		t.Fatalf("HardDelete(nil) error = %v", err)
	}
	if len(store.deleted) != 0 || len(indexer.bulkDeleted) != 0 {
		t.Fatal("an empty permanent delete must not touch media or the index")
	}
}

// TestHardDeleteAbortsWhenTheCoverQueryFails 钉住调用顺序的第一步：
// 问不出「哪些图会成为孤儿」就不该往下删。否则文章被删了、图却留在原地——
// 表面上看只是没清理干净，实际上调用方会认为整个操作失败并重试，
// 而那次重试面对的是一篇已经不存在、也问不出头图的文章。
func TestHardDeleteAbortsWhenTheCoverQueryFails(t *testing.T) {
	ctx := context.Background()
	base := newTestArticleRepo(t)
	article := mustCreateArticle(t, base, "查询失败")

	svc := NewArticleService(failingArticleRepo{
		ArticleRepository: base,
		coverQueryErr:     errors.New("query blew up"),
	}, nil, nil)

	if err := svc.HardDelete(ctx, []int64{article.ID}); err == nil {
		t.Fatal("HardDelete() must abort when the cover lookup fails")
	}
	// 文章必须还在（它本来是活文章，这里主要确认流程没有继续往下走）。
	if _, err := base.GetByID(ctx, article.ID); err != nil {
		t.Fatalf("the article must not be touched when the cover lookup fails: %v", err)
	}
}

func TestHardDeleteSurfacesARepositoryFailure(t *testing.T) {
	ctx := context.Background()
	svc := NewArticleService(failingArticleRepo{
		ArticleRepository: newTestArticleRepo(t),
		hardDeleteErr:     errors.New("locked"),
	}, nil, nil)

	if err := svc.HardDelete(ctx, []int64{1}); err == nil {
		t.Fatal("a failing HardDelete must be reported")
	}
}

// TestHardDeleteSwallowsAMediaRemovalFailure 钉住收尾策略：文章已经永久删除，
// 此时残留一个文件只能记日志——把失败上抛会让接口报错，调用方重试时
// 面对的是一个已不存在的 id，只会得到更多困惑。
func TestHardDeleteSwallowsAMediaRemovalFailure(t *testing.T) {
	svc, repo, store, _ := newArticleTestRig(t)
	ctx := context.Background()
	store.add(t, 5, model.MediaTypeImage)
	store.deleteErr = errors.New("permission denied")

	article, err := svc.Create(ctx, ArticleWrite{Title: "收尾失败", CoverMediaID: intptr(5)})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := svc.SoftDelete(ctx, []int64{article.ID}); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}

	if err := svc.HardDelete(ctx, []int64{article.ID}); err != nil {
		t.Fatalf("HardDelete() error = %v, want nil — the article is already gone", err)
	}
	if _, err := repo.GetByID(ctx, article.ID); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("the article must be gone, got %v", err)
	}
	if len(store.deleted) != 1 {
		t.Fatalf("the cleanup must still be attempted, got %v", store.deleted)
	}
}

// ---------------------------------------------------------------------------
// 读取路径
// ---------------------------------------------------------------------------

func TestArticleServiceReadPaths(t *testing.T) {
	svc, _, _, _ := newArticleTestRig(t)
	ctx := context.Background()

	article, err := svc.Create(ctx, ArticleWrite{Title: "可读的"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := svc.Get(ctx, article.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Title != "可读的" {
		t.Fatalf("Get() title = %q", got.Title)
	}

	list, total, err := svc.List(ctx, model.Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("List() = %d rows / total %d, want 1/1", len(list), total)
	}

	if err := svc.SoftDelete(ctx, []int64{article.ID}); err != nil {
		t.Fatalf("SoftDelete() error = %v", err)
	}

	deleted, total, err := svc.ListDeleted(ctx, model.Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListDeleted() error = %v", err)
	}
	if total != 1 || len(deleted) != 1 {
		t.Fatalf("ListDeleted() = %d rows / total %d, want 1/1", len(deleted), total)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func mustCreateArticle(t *testing.T, repo *model.SQLiteArticleRepository, title string) *model.Article {
	t.Helper()
	article := &model.Article{Title: title, CreatedAt: 1, UpdatedAt: 1}
	if err := repo.Create(context.Background(), article); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	return article
}

// countArticles 数一数**未删除**的文章有几篇，用来断言「校验失败时库里什么都没变」。
func countArticles(t *testing.T, svc *ArticleService) int {
	t.Helper()
	_, total, err := svc.List(context.Background(), model.Pagination{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	return int(total)
}

func titles(articles []model.Article) []string {
	out := make([]string, 0, len(articles))
	for _, a := range articles {
		out = append(out, a.Title)
	}
	return out
}
