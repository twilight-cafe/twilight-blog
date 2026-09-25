package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"blog-engine/internal/database"
	"blog-engine/internal/model"
)

// 本文件覆盖 MomentService 的配图校验轴（R6-01 的守护测试）。
//
// 为什么这条轴值得单独写满：修复前 `MediaIDs` 是**一路不校验**地写进外键列的，
// 而两个后果都是静默的——
//
//  1. 引用一个不存在的 id 时，`INSERT OR IGNORE` 并不会吞掉外键违规（这一点容易
//     误判：OR IGNORE 只忽略 UNIQUE/PRIMARY KEY 冲突，FOREIGN KEY 照样抛）。
//     于是驱动抛出一句 "FOREIGN KEY constraint failed"，服务层原样上抛，
//     handler 因为没有可匹配的错误类型而回 **500**，且当时**全文件没有一行
//     Error 日志**——运维在 log.txt 里查不到任何线索。而「客户端给错了一个
//     media id」本应是 400。
//
//  2. 任何类型的媒体都能被挂成动态配图。前台 viewmodel 为每条配图生成
//     `<img src>`，于是读者看到的是一张破图。这一点与
//     `ArticleService.usableCoverID` 的「存在 + 是图片」口径不一致。
//
// 可达性不是理论上的：管理端 `MomentsAdmin.tsx` 的 mediaIds 取自媒体选择器的
// **本地状态**，而媒体是**硬删且没有回收站**——「选好图 → 那张图被删了 →
// 回来保存动态」是一条真实路径。
//
// 断言全部落在**副作用**上（错误类型 + 库里的行数 + 落库的关联），而不是只看
// 返回值：返回 nil 却写坏了库，是最糟的一类回归。

// momentTestRig 是 MomentService 的测试台：临时库 + 真实仓储 + 假媒体库。
//
// 复用 article_cover_service_test.go 里的 fakeMediaStore —— 它已经是这个包
// 里「一张内存里的小媒体库」的统一替身，并且会同步在真实的 media 表里建行，
// 所以下面那些「关联真的落库了吗」的断言才有意义（只往内存 map 里塞一张图，
// 外键照样会拒绝，测的就是一个不存在的世界）。
func newMomentTestRig(t *testing.T) (*MomentService, *model.SQLiteMomentRepository, *fakeMediaStore) {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "moments.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repo := model.NewSQLiteMomentRepository(db)
	store := newFakeMediaStore()
	store.db = db
	return NewMomentService(repo, store), repo, store
}

// countMoments 数一数**未删除**的动态有几条，用来断言「校验失败时库里什么都没变」。
func countMoments(t *testing.T, svc *MomentService) int {
	t.Helper()
	_, total, err := svc.List(context.Background(), model.Pagination{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	return int(total)
}

// ---------------------------------------------------------------------------
// Create：配图校验
// ---------------------------------------------------------------------------

// TestMomentCreateAcceptsImageAttachments 钉住正向路径，并确认写进去的是
// **媒体 id** 且顺序被保留（前台按关联建立时间排序展示配图）。
func TestMomentCreateAcceptsImageAttachments(t *testing.T) {
	svc, repo, store := newMomentTestRig(t)
	ctx := context.Background()
	store.add(t, 3, model.MediaTypeImage)
	store.add(t, 7, model.MediaTypeImage)

	moment, err := svc.Create(ctx, "两张图", []int64{3, 7})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if len(moment.Media) != 2 {
		t.Fatalf("Media = %d item(s), want 2", len(moment.Media))
	}
	if moment.Media[0].ID != 3 || moment.Media[1].ID != 7 {
		t.Fatalf("Media ids = [%d, %d], want [3, 7] — the caller's order must survive",
			moment.Media[0].ID, moment.Media[1].ID)
	}

	// 关联必须真的落在 moment_media 里，而不是只有返回值好看。
	stored, err := repo.GetByID(ctx, moment.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if len(stored.MediaIDs) != 2 || stored.MediaIDs[0] != 3 || stored.MediaIDs[1] != 7 {
		t.Fatalf("stored MediaIDs = %v, want [3 7]", stored.MediaIDs)
	}
}

// TestMomentCreateRejectsUnusableMedia 是本文件最重要的一条：三种「客户端给错了」
// 都必须在**写入之前**变成 *ValidationError（→ 400），并且库里不能留下半条动态。
//
// 断言分两层：
//   - 错误类型是 *ValidationError（不是 500，更不是静默通过）；
//   - 库里的动态数为 0 —— 校验必须发生在 INSERT 之前。
//
// 反证方式：把 Create 里的 validMomentMediaIDs 调用挪到 repo.Create 之后 →
// countMoments 会得到 1，本条失败；把校验整个删掉 → 前两组用例拿到的是驱动错误
// （errors.As 到 ValidationError 失败），第三组会**静默成功**，同样报警。
func TestMomentCreateRejectsUnusableMedia(t *testing.T) {
	const (
		videoID   = int64(6)
		audioID   = int64(8)
		missingID = int64(999)
	)
	cases := []struct {
		name string
		ids  []int64
		seed func(t *testing.T, s *fakeMediaStore)
	}{
		{name: "zero id", ids: []int64{0}},
		{name: "negative id", ids: []int64{-3}},
		{name: "media does not exist", ids: []int64{missingID}},
		{name: "media is a video", ids: []int64{videoID}, seed: func(t *testing.T, s *fakeMediaStore) {
			s.add(t, videoID, "video")
		}},
		{name: "media is audio", ids: []int64{audioID}, seed: func(t *testing.T, s *fakeMediaStore) {
			s.add(t, audioID, "audio")
		}},
		{name: "one good image among bad ids", ids: []int64{1, missingID}, seed: func(t *testing.T, s *fakeMediaStore) {
			s.add(t, 1, model.MediaTypeImage)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, store := newMomentTestRig(t)
			if tc.seed != nil {
				tc.seed(t, store)
			}

			_, err := svc.Create(context.Background(), "坏配图", tc.ids)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("Create(%v) error = %v, want *ValidationError", tc.ids, err)
			}
			if count := countMoments(t, svc); count != 0 {
				t.Fatalf("%d moment(s) were written despite an invalid media id %v", count, tc.ids)
			}
		})
	}
}

// countingMediaStore 包住 fakeMediaStore，记录每个 id 被问了几次。
//
// 为什么需要它：去重这件事**看不出来**。Create 落库后会重新读回动态，而
// `moment_media` 的主键是 (moment_id, media_id)，`INSERT OR IGNORE` 会把重复项
// 静默吞掉——所以无论服务层有没有去重，最终读回来的 Media/MediaIDs 都一样长。
// 第一版这条测试正是这么写的，结果把去重整个删掉它依然绿（用探针确认过），
// 等于没有测试。真正的可观测信号只有一个：**同一个 id 被问了几次**。
type countingMediaStore struct {
	*fakeMediaStore
	gets map[int64]int
}

func newCountingMediaStore(inner *fakeMediaStore) *countingMediaStore {
	return &countingMediaStore{fakeMediaStore: inner, gets: map[int64]int{}}
}

func (c *countingMediaStore) Get(ctx context.Context, id int64) (*model.Media, error) {
	c.gets[id]++
	return c.fakeMediaStore.Get(ctx, id)
}

// TestMomentCreateDeduplicatesMediaIDs 钉住去重。
//
// 断言落在**媒体库被问了几次**上：id 4 重复传三次，媒体库只应被问一次。
// 这既是性能上的意义（每次 Get 都可能是一次查询），更是语义上的——
// 「调用方传了什么」与「实际存了什么」必须一致，否则返回值在骗人。
//
// 反证方式：删掉 validMomentMediaIDs 里的 `seen` 判断 → gets[4] 变成 3，本条失败。
// （注意：不能靠断言最终配图条数来反证，`INSERT OR IGNORE` 会把重复项吞掉，
// 库里的结果与有没有去重无关——这一点是被第一版测试的假绿教会的。）
func TestMomentCreateDeduplicatesMediaIDs(t *testing.T) {
	svc, repo, store := newMomentTestRig(t)
	counting := newCountingMediaStore(store)
	svc.media = counting
	ctx := context.Background()
	store.add(t, 4, model.MediaTypeImage)

	moment, err := svc.Create(ctx, "同一张图三次", []int64{4, 4, 4})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if counting.gets[4] != 1 {
		t.Fatalf("the media store was asked for id 4 %d time(s), want exactly 1 — duplicates must be collapsed before lookup",
			counting.gets[4])
	}
	if len(moment.MediaIDs) != 1 || moment.MediaIDs[0] != 4 {
		t.Fatalf("MediaIDs = %v, want [4]", moment.MediaIDs)
	}

	stored, err := repo.GetByID(ctx, moment.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if len(stored.MediaIDs) != 1 {
		t.Fatalf("stored MediaIDs = %v, want a single link", stored.MediaIDs)
	}
}

// TestMomentCreateSurfacesUnexpectedMediaErrors 区分「客户端给错」与「媒体库坏了」：
// 后者不是 *ValidationError，必须原样上抛（handler 会据此答 500），而不是被折叠成
// 400 让调用方以为自己传错了 id。
func TestMomentCreateSurfacesUnexpectedMediaErrors(t *testing.T) {
	svc, _, store := newMomentTestRig(t)
	boom := errors.New("media storage unavailable")
	store.add(t, 5, model.MediaTypeImage)
	store.getErr = boom

	_, err := svc.Create(context.Background(), "库里坏了", []int64{5})
	if !errors.Is(err, boom) {
		t.Fatalf("Create() error = %v, want the underlying storage error", err)
	}
	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		t.Fatal("an infrastructure failure must not be reported as a client error")
	}
}

// TestMomentCreateWithoutMediaStoreSkipsValidation 钉住部署口径：
// media 为 nil（该部署没有媒体服务）时跳过校验，而不是 panic 或一律拒绝。
//
// 注意断言的是「这张图**仍然必须真实存在**」：跳过校验只意味着服务层不再问
// 媒体库，外键那道兜底照旧生效。所以这里必须先往真实的 media 表里种一行——
// 否则测出来的是外键，而不是「nil 依赖被跳过」这件事（这个区别是被第一次
// 跑失败教会的：用了一个不存在的 id，于是 FK 依然拒绝，用例红得毫无意义）。
func TestMomentCreateWithoutMediaStoreSkipsValidation(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "nomedia.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	seed := newFakeMediaStore()
	seed.db = db
	seed.add(t, 11, "video") // 故意是非图片：没有媒体服务时没人拦它

	svc := NewMomentService(model.NewSQLiteMomentRepository(db), nil)
	moment, err := svc.Create(context.Background(), "没有媒体服务", []int64{11})
	if err != nil {
		t.Fatalf("Create() error = %v, want nil when there is no media service", err)
	}
	if len(moment.MediaIDs) != 1 || moment.MediaIDs[0] != 11 {
		t.Fatalf("MediaIDs = %v, want [11]", moment.MediaIDs)
	}
}

// TestMomentCreateWithNoMedia 钉住空切片这条边界：不传配图不该触发任何查询
// （因此就算媒体库已经坏了也不该失败），也不该因为「空」被误判成非法。
func TestMomentCreateWithNoMedia(t *testing.T) {
	svc, _, store := newMomentTestRig(t)
	// 让 Get 必定失败：只要服务层为空配图走了一次媒体查询，这条就会红。
	store.getErr = errors.New("the media store must not be consulted when there are no ids")

	moment, err := svc.Create(context.Background(), "纯文字", nil)
	if err != nil {
		t.Fatalf("Create() error = %v, want nil for an empty media list", err)
	}
	if len(moment.Media) != 0 {
		t.Fatalf("Media = %v, want none", moment.Media)
	}
	if count := countMoments(t, svc); count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// Update：配图的三态（缺失 / 空 / 有值）
// ---------------------------------------------------------------------------

// TestMomentUpdateValidatesProvidedMedia 校验 Update 的配图路径。
//
// 两个断言要点：
//   - 非法配图必须在**写入之前**被拒，库里那条动态（正文、配图）一个字段都不动；
//   - 那条早退发生在 `moment.Content = content` 之后的**内存副本**上，所以库里
//     必须仍是最初的正文——这条最容易被写错成「先 UPDATE 再校验」。
func TestMomentUpdateValidatesProvidedMedia(t *testing.T) {
	svc, repo, store := newMomentTestRig(t)
	ctx := context.Background()
	store.add(t, 2, model.MediaTypeImage)
	store.add(t, 9, "video")

	created, err := svc.Create(ctx, "最初的正文", []int64{2})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	bad := []int64{9}
	_, err = svc.Update(ctx, created.ID, "改过的正文", &bad)
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("Update() error = %v, want *ValidationError", err)
	}

	stored, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.Content != "最初的正文" {
		t.Fatalf("content = %q, want the original — a rejected update must not be partially applied", stored.Content)
	}
	if len(stored.MediaIDs) != 1 || stored.MediaIDs[0] != 2 {
		t.Fatalf("stored MediaIDs = %v, want the untouched [2]", stored.MediaIDs)
	}
}

// TestMomentUpdateMissingMediaKeepsAttachments 钉住三态里最危险的一态：
// **省略 media_ids** 表示「保留原有配图」，不是「清空」。
//
// 反证方式：把 `if mediaIDs != nil` 去掉、无条件赋值 → 配图会消失，本条失败。
func TestMomentUpdateMissingMediaKeepsAttachments(t *testing.T) {
	svc, repo, store := newMomentTestRig(t)
	ctx := context.Background()
	store.add(t, 2, model.MediaTypeImage)

	created, err := svc.Create(ctx, "带图", []int64{2})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	updated, err := svc.Update(ctx, created.ID, "只改正文", nil)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Content != "只改正文" {
		t.Fatalf("content = %q, want 只改正文", updated.Content)
	}
	if len(updated.Media) != 1 || updated.Media[0].ID != 2 {
		t.Fatalf("Media = %v, want the original attachment to survive an omitted field", updated.Media)
	}

	stored, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if len(stored.MediaIDs) != 1 || stored.MediaIDs[0] != 2 {
		t.Fatalf("stored MediaIDs = %v, want the untouched [2]", stored.MediaIDs)
	}
}

// TestMomentUpdateEmptyMediaClearsAttachments 与上一态相对：**显式传空**是明确的
// 「摘掉全部配图」，必须生效。把这两条放在一起，才能证明指针的 nil 与非 nil
// 真的有区分度，而不是「反正都保留」。
func TestMomentUpdateEmptyMediaClearsAttachments(t *testing.T) {
	svc, repo, store := newMomentTestRig(t)
	ctx := context.Background()
	store.add(t, 2, model.MediaTypeImage)

	created, err := svc.Create(ctx, "带图", []int64{2})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	empty := []int64{}
	updated, err := svc.Update(ctx, created.ID, "摘掉图", &empty)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if len(updated.Media) != 0 {
		t.Fatalf("Media = %v, want none after an explicit empty list", updated.Media)
	}

	stored, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if len(stored.MediaIDs) != 0 {
		t.Fatalf("stored MediaIDs = %v, want none", stored.MediaIDs)
	}
}

// TestMomentUpdateReplacesAttachments 钉住正常替换：新图进来、旧图走掉。
func TestMomentUpdateReplacesAttachments(t *testing.T) {
	svc, repo, store := newMomentTestRig(t)
	ctx := context.Background()
	store.add(t, 2, model.MediaTypeImage)
	store.add(t, 3, model.MediaTypeImage)

	created, err := svc.Create(ctx, "换图", []int64{2})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	next := []int64{3}
	if _, err = svc.Update(ctx, created.ID, "换图", &next); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	stored, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if len(stored.MediaIDs) != 1 || stored.MediaIDs[0] != 3 {
		t.Fatalf("stored MediaIDs = %v, want [3]", stored.MediaIDs)
	}
}

// TestMomentUpdateReportsAMissingMoment 钉住「改一条不存在/已在回收站的动态」
// 仍然是 ErrMomentNotFound，而不是被配图校验抢先变成 400（那会让 handler
// 把 404 答成 400）。
func TestMomentUpdateReportsAMissingMoment(t *testing.T) {
	svc, _, store := newMomentTestRig(t)
	store.add(t, 2, model.MediaTypeImage)

	ids := []int64{2}
	_, err := svc.Update(context.Background(), 4242, "不存在", &ids)
	if !errors.Is(err, model.ErrMomentNotFound) {
		t.Fatalf("Update() error = %v, want ErrMomentNotFound", err)
	}
}

// TestMomentCreateSurfacesARepositoryFailure 确认数据库失败会原样上抛：
// 落库没成功却返回「创建成功」是比任何 500 都糟的结果。
func TestMomentCreateSurfacesARepositoryFailure(t *testing.T) {
	boom := errors.New("disk full")
	svc := NewMomentService(failingMomentRepo{
		MomentRepository: newTestMomentRepo(t),
		createErr:        boom,
	}, nil)

	if _, err := svc.Create(context.Background(), "写不进去", nil); !errors.Is(err, boom) {
		t.Fatalf("Create() error = %v, want the repository error", err)
	}
}

// TestMomentUpdateSurfacesARepositoryFailure 同上，但走 Update 路径。
func TestMomentUpdateSurfacesARepositoryFailure(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("disk full")
	base := newTestMomentRepo(t)

	seeded := &model.Moment{Content: "改不进去", CreatedAt: 1, UpdatedAt: 1}
	if err := base.Create(ctx, seeded); err != nil {
		t.Fatalf("seed moment: %v", err)
	}

	svc := NewMomentService(failingMomentRepo{
		MomentRepository: base,
		updateErr:        boom,
	}, nil)

	if _, err := svc.Update(ctx, seeded.ID, "新正文", nil); !errors.Is(err, boom) {
		t.Fatalf("Update() error = %v, want the repository error", err)
	}
}

// failingMomentRepo 把一个真实仓储包起来，只把需要注入失败的方法替换掉。
// 其余方法照常走真实 SQL，测试因此仍然依赖真实的约束（例如 Update 的
// deleted_at IS NULL 前置条件）。
type failingMomentRepo struct {
	model.MomentRepository
	createErr error
	updateErr error
}

func (r failingMomentRepo) Create(ctx context.Context, moment *model.Moment) error {
	if r.createErr != nil {
		return r.createErr
	}
	return r.MomentRepository.Create(ctx, moment)
}

func (r failingMomentRepo) Update(ctx context.Context, moment *model.Moment) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	return r.MomentRepository.Update(ctx, moment)
}

// newTestMomentRepo 起一个临时库并返回真实仓储（供需要注入失败的用例复用）。
func newTestMomentRepo(t *testing.T) *model.SQLiteMomentRepository {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "inject-moment.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return model.NewSQLiteMomentRepository(db)
}

// ---------------------------------------------------------------------------
// 仓储层：外键确实是「不校验」时的那道兜底
// ---------------------------------------------------------------------------

// TestMomentRepoForeignKeyRejectsUnknownMedia 钉住修复所依据的那个事实：
// `INSERT OR IGNORE` **不吞**外键违规。服务层之所以必须自己做校验，正是因为
// 这道兜底虽然挡得住「不存在」，但报出来的是一句没有字段名的驱动错误，
// 会被 handler 当成 500。
//
// 这条测试的存在意义是**防止有人误以为 OR IGNORE 能吞掉一切**，从而把服务层
// 的校验当成冗余删掉。
func TestMomentRepoForeignKeyRejectsUnknownMedia(t *testing.T) {
	repo := newTestMomentRepo(t)
	ctx := context.Background()

	moment := &model.Moment{Content: "引用不存在的图", MediaIDs: []int64{999}, CreatedAt: 1, UpdatedAt: 1}
	err := repo.Create(ctx, moment)
	if err == nil {
		t.Fatal("Create() accepted a media id that does not exist — OR IGNORE must not swallow a foreign-key violation")
	}

	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		t.Fatal("the repository must not manufacture a ValidationError; classifying the failure is the service's job")
	}
}
