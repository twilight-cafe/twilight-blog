package model

import (
	"context"
	"database/sql"
	"testing"
)

// newTestMomentRepo 返回动态仓储、媒体仓储，以及底层连接
// （关联表没有仓储级读取入口，数行数要直接走 SQL）。
func newTestMomentRepo(t *testing.T) (*SQLiteMomentRepository, *SQLiteMediaRepository, *sql.DB) {
	t.Helper()

	db := openTestDB(t)
	return NewSQLiteMomentRepository(db), NewSQLiteMediaRepository(db), db
}

func addMedia(t *testing.T, repo *SQLiteMediaRepository, name string, createdAt int64) int64 {
	t.Helper()

	media := &Media{
		StorageName:  name,
		OriginalName: name,
		MediaType:    "image",
		MimeType:     "image/png",
		Size:         1024,
		CreatedAt:    createdAt,
	}
	if err := repo.Create(context.Background(), media); err != nil {
		t.Fatalf("create media %q: %v", name, err)
	}
	return media.ID
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()

	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestHardDeleteLeavesActiveMomentUntouched 是「HardDelete 顺手删关联行」这个
// 缺陷的回归测试。
//
// 旧实现在 DELETE FROM moments 之前先无条件执行了 DELETE FROM moment_media。
// 对一条**未软删**的动态，第二句因为 deleted_at IS NOT NULL 的条件匹配不到行、
// 所以动不了主体；但第一句没有这个条件，于是配图关联被清空了——一次「什么都没
// 删掉」的调用却造成了损坏。交回给外键级联后这种不对称就消失了。
func TestHardDeleteLeavesActiveMomentUntouched(t *testing.T) {
	repo, mediaRepo, db := newTestMomentRepo(t)
	ctx := context.Background()

	mediaID := addMedia(t, mediaRepo, "a.png", 100)
	moment := &Moment{Content: "hello", MediaIDs: []int64{mediaID}, CreatedAt: 100, UpdatedAt: 100}
	if err := repo.Create(ctx, moment); err != nil {
		t.Fatalf("create moment: %v", err)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM moment_media WHERE moment_id = ?`, moment.ID); got != 1 {
		t.Fatalf("moment_media rows = %d before HardDelete, want 1", got)
	}

	n, err := repo.HardDelete(ctx, []int64{moment.ID})
	if err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	if n != 0 {
		t.Fatalf("HardDelete removed %d rows for an active moment, want 0", n)
	}

	// 动态本身必须还在。
	fetched, err := repo.GetByID(ctx, moment.ID)
	if err != nil {
		t.Fatalf("active moment disappeared after HardDelete: %v", err)
	}
	if !equalIDs(fetched.MediaIDs, []int64{mediaID}) {
		t.Fatalf("media ids = %v after HardDelete on an active moment, want [%d]", fetched.MediaIDs, mediaID)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM moment_media WHERE moment_id = ?`, moment.ID); got != 1 {
		t.Fatalf("moment_media rows = %d after HardDelete on an active moment, want 1", got)
	}
}

// TestHardDeleteOfDeletedMomentCascadesMediaLinks 从另一侧守住同一个改动：
// 一旦动态真的位于回收站，关联行必须被清干净——清理现在完全依赖外键级联，
// 若 pragma 失效或 schema 被改坏，这里会立刻暴露。
func TestHardDeleteOfDeletedMomentCascadesMediaLinks(t *testing.T) {
	repo, mediaRepo, db := newTestMomentRepo(t)
	ctx := context.Background()

	mediaID := addMedia(t, mediaRepo, "b.png", 100)
	moment := &Moment{Content: "bye", MediaIDs: []int64{mediaID}, CreatedAt: 100, UpdatedAt: 100}
	if err := repo.Create(ctx, moment); err != nil {
		t.Fatalf("create moment: %v", err)
	}

	if _, err := repo.SoftDelete(ctx, []int64{moment.ID}, 200); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	n, err := repo.HardDelete(ctx, []int64{moment.ID})
	if err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	if n != 1 {
		t.Fatalf("HardDelete removed %d rows, want 1", n)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM moment_media WHERE moment_id = ?`, moment.ID); got != 0 {
		t.Fatalf("moment_media rows = %d after cascade, want 0", got)
	}
	// 级联删的是关联行，不是媒体本身：被删动态用过的图片仍应留在媒体库里。
	if _, err := mediaRepo.GetByID(ctx, mediaID); err != nil {
		t.Fatalf("media row was removed along with the moment: %v", err)
	}
}

// TestListGroupsMediaPerMoment 覆盖批量装配改成一次 IN 查询后的分组正确性：
// 每条动态只能拿到自己的配图，顺序按关联建立时间；没有配图的动态拿到的是
// **非 nil 空切片**而不是 nil（Media / MediaIDs 会直接序列化成 JSON，
// nil 会渲染成 null）。
func TestListGroupsMediaPerMoment(t *testing.T) {
	repo, mediaRepo, db := newTestMomentRepo(t)
	ctx := context.Background()
	_ = db

	first := addMedia(t, mediaRepo, "first.png", 100)
	second := addMedia(t, mediaRepo, "second.png", 100)
	only := addMedia(t, mediaRepo, "only.png", 100)

	oldest := &Moment{Content: "two images", MediaIDs: []int64{first, second}, CreatedAt: 100, UpdatedAt: 100}
	middle := &Moment{Content: "one image", MediaIDs: []int64{only}, CreatedAt: 200, UpdatedAt: 200}
	newest := &Moment{Content: "no image", CreatedAt: 300, UpdatedAt: 300}
	for _, moment := range []*Moment{oldest, middle, newest} {
		if err := repo.Create(ctx, moment); err != nil {
			t.Fatalf("create moment %q: %v", moment.Content, err)
		}
	}

	moments, total, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(moments) != 3 {
		t.Fatalf("got %d moments, want 3", len(moments))
	}

	// List 按 created_at 倒序，所以顺序是 newest、middle、oldest。
	if !equalIDs(moments[0].MediaIDs, []int64{}) {
		t.Fatalf("newest media ids = %v, want []", moments[0].MediaIDs)
	}
	if moments[0].Media == nil || moments[0].MediaIDs == nil {
		t.Fatal("moment without images got nil Media/MediaIDs, want empty slices (nil marshals to JSON null)")
	}
	if !equalIDs(moments[1].MediaIDs, []int64{only}) {
		t.Fatalf("middle media ids = %v, want [%d]", moments[1].MediaIDs, only)
	}
	if !equalIDs(moments[2].MediaIDs, []int64{first, second}) {
		t.Fatalf("oldest media ids = %v, want [%d %d]", moments[2].MediaIDs, first, second)
	}

	// 元数据也要装配到位，且是与 id 一一对应的那些。
	if len(moments[2].Media) != 2 || moments[2].Media[0].StorageName != "first.png" {
		t.Fatalf("oldest media metadata = %+v, want first.png then second.png", moments[2].Media)
	}
}
