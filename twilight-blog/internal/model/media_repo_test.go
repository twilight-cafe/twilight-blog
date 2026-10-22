package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func newTestMediaRepo(t *testing.T) (*SQLiteMediaRepository, *sql.DB) {
	t.Helper()

	db := openTestDB(t)
	return NewSQLiteMediaRepository(db), db
}

// seedMedia 直接插一行，返回它的 id。
//
// 用裸 SQL 而不是 repo.Create：Create 是**被测对象**之一，用它来准备数据会让
// 「Create 写错了」的表现变成十几个用例一起失败，而真正的原因（列的顺序）藏在
// 最下面一层。写入路径本身另有专门的用例。
func seedMedia(t *testing.T, db *sql.DB, storageName, originalName, mediaType, mimeType string, size int64, converted bool, createdAt int64) int64 {
	t.Helper()

	flag := 0
	if converted {
		flag = 1
	}
	res, err := db.Exec(
		`INSERT INTO media (storage_name, original_name, media_type, mime_type, size, converted, created_at, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
		storageName, originalName, mediaType, mimeType, size, flag, createdAt,
	)
	if err != nil {
		t.Fatalf("seed media %q: %v", storageName, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("read back the seeded id: %v", err)
	}
	return id
}

// TestMediaCreateBackfillsTheIDAndRoundTripsTheFlag 覆盖写入路径。
//
// converted 在结构体里是 bool、在库里是 0/1，两边都要转一次；写反了不会报错，
// 只会让「这张图是不是转码来的」永远回答错，而目前没有任何界面显示它。
func TestMediaCreateBackfillsTheIDAndRoundTripsTheFlag(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	ctx := context.Background()

	for i, converted := range []bool{true, false} {
		media := &Media{
			StorageName:  fmt.Sprintf("stored-%d.webp", i),
			OriginalName: "holiday.png",
			MediaType:    MediaTypeImage,
			MimeType:     "image/webp",
			Size:         1234,
			Converted:    converted,
			CreatedAt:    99,
		}
		if err := repo.Create(ctx, media); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if media.ID == 0 {
			t.Fatal("Create() did not backfill the id")
		}

		var stored int
		if err := db.QueryRow(`SELECT converted FROM media WHERE id = ?`, media.ID).Scan(&stored); err != nil {
			t.Fatalf("read the column back: %v", err)
		}
		if stored != 0 && !converted {
			t.Errorf("converted = %d in the database, want 0", stored)
		}
		if stored != 1 && converted {
			t.Errorf("converted = %d in the database, want 1", stored)
		}

		got, err := repo.GetByID(ctx, media.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if got.Converted != converted {
			t.Errorf("GetByID().Converted = %v, want %v", got.Converted, converted)
		}
		if got.DeletedAt != nil {
			t.Errorf("GetByID().DeletedAt = %v, want nil", *got.DeletedAt)
		}
	}
}

// TestMediaGetByIDReportsAMissingRow 钉住 GetByID 把 sql.ErrNoRows 换成了自己的
// 哨兵错误——handler 靠它区分 404 与 500。
func TestMediaGetByIDReportsAMissingRow(t *testing.T) {
	repo, _ := newTestMediaRepo(t)

	if _, err := repo.GetByID(context.Background(), 4242); !errors.Is(err, ErrMediaNotFound) {
		t.Fatalf("GetByID() error = %v, want ErrMediaNotFound", err)
	}
}

// TestMediaListPagesAndOrdersNewestFirst 覆盖列表的排序、分页与总数。
//
// total 与当页必须来自同一个快照：它们由 readTx 保证，而这里断言的是它们彼此一致
// ——分页错了会表现成「翻到第二页少了一条」，很难从界面上看出来。
func TestMediaListPagesAndOrdersNewestFirst(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	ctx := context.Background()

	for i, name := range []string{"oldest", "middle", "newest"} {
		seedMedia(t, db, name+".png", name+".png", MediaTypeImage, "image/png", 10, false, int64(i+1))
	}

	page, total, err := repo.List(ctx, Pagination{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(page) != 2 {
		t.Fatalf("List() returned %d rows, want 2", len(page))
	}
	if page[0].StorageName != "newest.png" || page[1].StorageName != "middle.png" {
		t.Errorf("List() = %q, %q; want newest first", page[0].StorageName, page[1].StorageName)
	}

	second, total, err := repo.List(ctx, Pagination{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatalf("List() page 2 error = %v", err)
	}
	if total != 3 || len(second) != 1 || second[0].StorageName != "oldest.png" {
		t.Errorf("page 2 = %+v (total %d), want just oldest.png", second, total)
	}

	empty, total, err := repo.List(ctx, Pagination{Page: 9, PageSize: 2})
	if err != nil {
		t.Fatalf("List() past the end error = %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 even past the end", total)
	}
	// 空页必须是**非 nil** 的空切片：它会被直接序列化成 JSON，nil 是 null。
	if empty == nil {
		t.Error("List() returned a nil slice for an empty page")
	}
}

// TestMediaDeleteRemovesTheRowAndReportsAMissingOne 覆盖硬删。
//
// 删第二次必须报 ErrMediaNotFound 而不是静默成功：静默成功会让 handler 回一个
// 200，让调用方以为「删掉了一条」。
func TestMediaDeleteRemovesTheRowAndReportsAMissingOne(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	ctx := context.Background()
	id := seedMedia(t, db, "a.png", "a.png", MediaTypeImage, "image/png", 10, false, 1)

	if err := repo.Delete(ctx, id); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := repo.GetByID(ctx, id); !errors.Is(err, ErrMediaNotFound) {
		t.Fatalf("GetByID() after Delete() error = %v, want ErrMediaNotFound", err)
	}
	if err := repo.Delete(ctx, id); !errors.Is(err, ErrMediaNotFound) {
		t.Fatalf("the second Delete() error = %v, want ErrMediaNotFound", err)
	}
}

// TestMediaUpdateStorageRewritesOnlyTheFourColumns 是 upgrade-media 的仓储契约。
//
// 「就地改写」的全部价值在于**其余列一个都不动**：original_name 是用户上传时的
// 原始名（留档用），media_type 与 created_at 与本次转码无关。多改一列，症状是
// 列表里的排序或留档名悄悄变了；少改一列，症状是界面按旧文件名去找图。两者都
// 不会报错，所以只能在这里钉住。
func TestMediaUpdateStorageRewritesOnlyTheFourColumns(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	ctx := context.Background()
	id := seedMedia(t, db, "old.png", "我的头图.png", MediaTypeImage, "image/png", 400, false, 7)

	if err := repo.UpdateStorage(ctx, id, "new.webp", "image/webp", 123, true); err != nil {
		t.Fatalf("UpdateStorage() error = %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if got.ID != id {
		t.Errorf("id = %d, want %d — the whole point is that the id does not move", got.ID, id)
	}
	if got.StorageName != "new.webp" || got.MimeType != "image/webp" || got.Size != 123 || !got.Converted {
		t.Errorf("the four columns were not rewritten: %+v", *got)
	}
	if got.OriginalName != "我的头图.png" {
		t.Errorf("original name = %q; it is the archival record and must not change", got.OriginalName)
	}
	if got.MediaType != MediaTypeImage {
		t.Errorf("media type = %q, want %q", got.MediaType, MediaTypeImage)
	}
	if got.CreatedAt != 7 {
		t.Errorf("created at = %d, want 7", got.CreatedAt)
	}
}

// TestMediaUpdateStorageIgnoresSoftDeletedRows 钉住那条 deleted_at IS NULL。
//
// 软删目前没有任何写入路径会设置，但列与查询都在。如果 UPDATE 漏掉这个条件，
// 升级命令会把一张「已被移除」的图片重新登记成 WebP 并使它复活——看起来就像
// 一条本该消失的图自己回来了。
func TestMediaUpdateStorageIgnoresSoftDeletedRows(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	ctx := context.Background()
	id := seedMedia(t, db, "gone.png", "gone.png", MediaTypeImage, "image/png", 10, false, 1)
	if _, err := db.Exec(`UPDATE media SET deleted_at = 123 WHERE id = ?`, id); err != nil {
		t.Fatalf("soft delete the row: %v", err)
	}

	if err := repo.UpdateStorage(ctx, id, "new.webp", "image/webp", 5, true); !errors.Is(err, ErrMediaNotFound) {
		t.Fatalf("UpdateStorage() on a soft-deleted row error = %v, want ErrMediaNotFound", err)
	}
}

// TestMediaUpdateStorageReportsAMissingRow 覆盖「找不到那一行」。
func TestMediaUpdateStorageReportsAMissingRow(t *testing.T) {
	repo, _ := newTestMediaRepo(t)

	err := repo.UpdateStorage(context.Background(), 999, "new.webp", "image/webp", 1, true)
	if !errors.Is(err, ErrMediaNotFound) {
		t.Fatalf("UpdateStorage() error = %v, want ErrMediaNotFound", err)
	}
}

// TestMediaListImagesForWebPUpgradeSelectsImagesInIDOrder 覆盖升级命令的选行逻辑。
//
// 三个点：只挑图片（音频视频不该被碰）、跳过软删、按 id 升序（中断后的重跑要能
// 与日志对照）。分页被刻意排除——升级会改 mime_type，于是下一页的边界会滑动。
func TestMediaListImagesForWebPUpgradeSelectsImagesInIDOrder(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	ctx := context.Background()

	first := seedMedia(t, db, "a.png", "a.png", MediaTypeImage, "image/png", 10, false, 30)
	removed := seedMedia(t, db, "gone.png", "gone.png", MediaTypeImage, "image/png", 10, false, 20)
	seedMedia(t, db, "song.mp3", "song.mp3", "audio", "audio/mpeg", 10, false, 10)
	seedMedia(t, db, "clip.mp4", "clip.mp4", "video", "video/mp4", 10, false, 5)
	second := seedMedia(t, db, "b.webp", "b.webp", MediaTypeImage, "image/webp", 10, true, 40)
	if _, err := db.Exec(`UPDATE media SET deleted_at = 123 WHERE id = ?`, removed); err != nil {
		t.Fatalf("soft delete the row: %v", err)
	}

	images, err := repo.ListImagesForWebPUpgrade(ctx)
	if err != nil {
		t.Fatalf("ListImagesForWebPUpgrade() error = %v", err)
	}
	if len(images) != 2 {
		t.Fatalf("got %d rows, want 2 (only the live images)", len(images))
	}
	if images[0].ID != first || images[1].ID != second {
		t.Errorf("ids = %d, %d; want %d, %d in ascending order", images[0].ID, images[1].ID, first, second)
	}
	for _, image := range images {
		if image.MediaType != MediaTypeImage {
			t.Errorf("row %d has media type %q; the upgrade only handles images", image.ID, image.MediaType)
		}
	}

	// 已经是 WebP 的那张也要列出来：要不要跳过是命令的判断（--all 会覆盖它），
	// 仓储层把它过滤掉的话，--all 就没有东西可以重编码了。
	if images[1].MimeType != "image/webp" || !images[1].Converted {
		t.Errorf("the already-WebP row came back as %+v", images[1])
	}
}

// TestMediaListImagesForWebPUpgradeReturnsAnEmptySlice 钉住空库的形状。
func TestMediaListImagesForWebPUpgradeReturnsAnEmptySlice(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	seedMedia(t, db, "song.mp3", "song.mp3", "audio", "audio/mpeg", 10, false, 1)

	images, err := repo.ListImagesForWebPUpgrade(context.Background())
	if err != nil {
		t.Fatalf("ListImagesForWebPUpgrade() error = %v", err)
	}
	if images == nil {
		t.Error("got a nil slice; it must be a non-nil empty one so callers need no special case")
	}
	if len(images) != 0 {
		t.Errorf("got %d rows, want 0", len(images))
	}
}

// TestMediaRepositorySurfacesQueryFailures 覆盖四处「查询本身失败」。
//
// 构造方式是把 media 表整个删掉——这是最接近真实事故的形状（有人手工动过库、
// 或者迁移没跑完），而且它同时让四条路径都在同一个连接上失败，不必为每种失败
// 单独造一个假驱动。
func TestMediaRepositorySurfacesQueryFailures(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	ctx := context.Background()
	id := seedMedia(t, db, "a.png", "a.png", MediaTypeImage, "image/png", 10, false, 1)

	if _, err := db.Exec(`DROP TABLE media`); err != nil {
		t.Fatalf("drop the media table: %v", err)
	}

	if _, err := repo.GetByID(ctx, id); err == nil || errors.Is(err, ErrMediaNotFound) {
		t.Errorf("GetByID() error = %v, want a query failure rather than a not-found", err)
	}
	if _, _, err := repo.List(ctx, Pagination{Page: 1, PageSize: 10}); err == nil {
		t.Error("List() succeeded with no media table")
	}
	if _, err := repo.ListImagesForWebPUpgrade(ctx); err == nil {
		t.Error("ListImagesForWebPUpgrade() succeeded with no media table")
	}
	if err := repo.UpdateStorage(ctx, id, "new.webp", "image/webp", 1, true); err == nil {
		t.Error("UpdateStorage() succeeded with no media table")
	}
	if err := repo.Create(ctx, &Media{StorageName: "new.png", MediaType: MediaTypeImage, MimeType: "image/png"}); err == nil {
		t.Error("Create() succeeded with no media table")
	}
	if err := repo.Delete(ctx, id); err == nil {
		t.Error("Delete() succeeded with no media table")
	}
	if err := repo.Rename(ctx, id, "new name"); err == nil {
		t.Error("Rename() succeeded with no media table")
	}
}

// TestMediaRenameOnlyRewritesTheDisplayName 钉住「改名只动 original_name」。
//
// 这是整个功能的承重墙：磁盘上的文件名（storage_name）、类型、大小、转码标记与
// 创建时间都必须原封不动，否则一次改名就会让引用方拿到一张「名字变了、图也变了」
// 或者库与磁盘不一致的记录。头图、微语配图、站长头像都按 media.id 引用，
// 所以只要 id 与其余列不变，那些引用一处都不用改。
func TestMediaRenameOnlyRewritesTheDisplayName(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	ctx := context.Background()
	id := seedMedia(t, db, "a1b2c3.webp", "old-name.png", MediaTypeImage, "image/webp", 1234, true, 7)

	if err := repo.Rename(ctx, id, "猫咪.png"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID() after the rename error = %v", err)
	}
	if got.OriginalName != "猫咪.png" {
		t.Fatalf("OriginalName = %q, want %q", got.OriginalName, "猫咪.png")
	}
	if got.StorageName != "a1b2c3.webp" {
		t.Fatalf("StorageName = %q after the rename, want the on-disk name untouched", got.StorageName)
	}
	if got.MimeType != "image/webp" || got.Size != 1234 || !got.Converted || got.CreatedAt != 7 || got.MediaType != MediaTypeImage {
		t.Fatalf("the rename touched columns it must not touch: %+v", got)
	}
}

// TestMediaRenameReportsAMissingRow 覆盖两种「没有可改的行」：id 不存在，
// 以及那一行已被软删（查询口径带 deleted_at IS NULL）。
//
// 两者都必须报 ErrMediaNotFound 而不是静默成功：静默成功会让调用方以为名字已经
// 改好，并把一个不存在的名字回给用户。
func TestMediaRenameReportsAMissingRow(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	ctx := context.Background()

	if err := repo.Rename(ctx, 4242, "随便.png"); !errors.Is(err, ErrMediaNotFound) {
		t.Fatalf("Rename() on an unknown id error = %v, want ErrMediaNotFound", err)
	}

	id := seedMedia(t, db, "gone.webp", "gone.png", MediaTypeImage, "image/webp", 10, false, 1)
	if _, err := db.Exec(`UPDATE media SET deleted_at = 1 WHERE id = ?`, id); err != nil {
		t.Fatalf("mark the row as deleted: %v", err)
	}
	if err := repo.Rename(ctx, id, "不该改到.png"); !errors.Is(err, ErrMediaNotFound) {
		t.Fatalf("Rename() on a soft-deleted row error = %v, want ErrMediaNotFound", err)
	}
}

// TestMediaSingleRowWritersSurfaceARowsAffectedFailure 覆盖「取影响行数失败」。
//
// 真实驱动不会在这里报错（所以渲染路径上这条分支永远走不到），但三个单行写入
// （Delete / UpdateStorage / Rename）都靠它区分「改到了」与「这一行不存在」：
// 取不到行数时若当成成功，界面会显示一个数据库里并不存在的状态，而调用方永远
// 不会知道。rowsAffectedFn 这层间接就是为它准备的（与 config 的
// writeFileAtomicFn 同一先例）。
func TestMediaSingleRowWritersSurfaceARowsAffectedFailure(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	ctx := context.Background()
	id := seedMedia(t, db, "a.png", "a.png", MediaTypeImage, "image/png", 10, false, 1)

	original := rowsAffectedFn
	rowsAffectedFn = func(sql.Result) (int64, error) {
		return 0, errors.New("the driver cannot report affected rows")
	}
	t.Cleanup(func() { rowsAffectedFn = original })

	// 三种失败都不是 ErrMediaNotFound：那是「这一行不存在」，而这里是「查不到」。
	if err := repo.Delete(ctx, id); err == nil || errors.Is(err, ErrMediaNotFound) {
		t.Errorf("Delete() error = %v, want the RowsAffected failure to surface", err)
	}
	if err := repo.UpdateStorage(ctx, id, "b.webp", "image/webp", 1, true); err == nil || errors.Is(err, ErrMediaNotFound) {
		t.Errorf("UpdateStorage() error = %v, want the RowsAffected failure to surface", err)
	}
	if err := repo.Rename(ctx, id, "新的名字.png"); err == nil || errors.Is(err, ErrMediaNotFound) {
		t.Errorf("Rename() error = %v, want the RowsAffected failure to surface", err)
	}
}

// TestMediaScanReportsAValueThatDoesNotFitTheColumn 覆盖扫描失败。
// SQLite 是动态类型的：列上写着 INTEGER，往里放一段文本照样存得下。于是扫描到
// int64 时失败——这正是「上一版写过别的形状、或者有人手工改过库」的症状，而它必须
// 报出来，不能被当成「没有这一行」。
func TestMediaScanReportsAValueThatDoesNotFitTheColumn(t *testing.T) {
	repo, db := newTestMediaRepo(t)
	seedMedia(t, db, "a.png", "a.png", MediaTypeImage, "image/png", 10, false, 1)
	if _, err := db.Exec(`UPDATE media SET size = 'not-a-number'`); err != nil {
		t.Fatalf("write a value that does not fit: %v", err)
	}

	if _, err := repo.ListImagesForWebPUpgrade(context.Background()); err == nil {
		t.Error("ListImagesForWebPUpgrade() scanned a text value into an int64 without complaining")
	}
	if _, _, err := repo.List(context.Background(), Pagination{Page: 1, PageSize: 10}); err == nil {
		t.Error("List() scanned a text value into an int64 without complaining")
	}
}
