package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blog-engine/internal/model"
)

// TestNormalizeMediaName 把展示名的规则钉成一张表。
//
// 这张表就是「用户能改成什么」的全部契约，其中三条最容易在改动中被破坏：
//   - **扩展名跟随当前展示名**：用户写下的后缀一律丢掉换成现有的那一个。
//     没有这条，一次改名就会顺手把后缀也换掉（sunset.png → 猫咪.webp），
//     而展示名里的后缀本来就是「上传时那个名字」的留档，不该由改名来改写；
//   - **控制字符与路径分隔符拒绝**：不是为了防路径穿越（展示名从不参与路径拼接），
//     而是换行/制表符会把日志与界面撑断、分隔符会让人误以为能当路径用；
//   - **长度按字符数**：一个中文名 3 字节/字，按字节判会让中文用户莫名超限。
func TestNormalizeMediaName(t *testing.T) {
	long196 := strings.Repeat("字", 196)

	cases := []struct {
		name    string
		raw     string
		current string
		want    string
		wantErr bool
	}{
		{name: "普通改名", raw: "猫咪", current: "old.png", want: "猫咪.png"},
		{name: "自带同名扩展名", raw: "猫咪.png", current: "old.png", want: "猫咪.png"},
		{name: "自带别的扩展名会被换成记录的", raw: "猫咪.jpg", current: "old.webp", want: "猫咪.webp"},
		{name: "只剥最后一层扩展名", raw: "a.b.png", current: "old.webp", want: "a.b.webp"},
		{name: "首尾空白被裁掉", raw: "  猫咪  ", current: "old.png", want: "猫咪.png"},
		{name: "同名幂等", raw: "old.png", current: "old.png", want: "old.png"},
		{name: "记录本身没有扩展名", raw: "猫咪.png", current: "README", want: "猫咪"},
		{name: "恰好 200 字符（含扩展名）", raw: long196 + ".png", current: "old.png", want: long196 + ".png"},
		{name: "空串", raw: "", current: "old.png", wantErr: true},
		{name: "纯空白", raw: "   ", current: "old.png", wantErr: true},
		{name: "换行", raw: "猫\n咪", current: "old.png", wantErr: true},
		{name: "制表符", raw: "猫\t咪", current: "old.png", wantErr: true},
		{name: "DEL", raw: "猫\x7f咪", current: "old.png", wantErr: true},
		{name: "斜杠", raw: "a/b.png", current: "old.png", wantErr: true},
		{name: "反斜杠", raw: `a\b.png`, current: "old.png", wantErr: true},
		{name: "只剩扩展名", raw: ".png", current: "old.png", wantErr: true},
		{name: "单点", raw: ".", current: "old.png", wantErr: true},
		{name: "双点", raw: "..", current: "old.png", wantErr: true},
		{name: "三点（剥掉扩展名后是双点）", raw: "...", current: "old.png", wantErr: true},
		{name: "201 字符", raw: strings.Repeat("字", 197) + ".png", current: "old.png", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeMediaName(tc.raw, tc.current)
			if tc.wantErr {
				if !errors.Is(err, model.ErrInvalidMediaName) {
					t.Fatalf("normalizeMediaName(%q, %q) error = %v, want ErrInvalidMediaName", tc.raw, tc.current, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeMediaName(%q, %q) error = %v", tc.raw, tc.current, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeMediaName(%q, %q) = %q, want %q", tc.raw, tc.current, got, tc.want)
			}
		})
	}
}

// TestRenameOnlyChangesTheDisplayName 是改名功能的端到端守护（服务层这一层）：
// 改完之后库里那一列变了，而磁盘文件名与文件内容一个字节都没动。
//
// 为什么字节也不能动：头图、微语配图、站长头像都按 media.id 取图，
// 而 GET /api/media/:id 回放的是磁盘文件。只要文件不变，改名前后的页面完全一致，
// 只是名字更好认——这正是「只改展示名」的全部承诺。
//
// 反证方式：让 Rename 顺手 os.Rename 磁盘文件（或改写 storage_name 列），
// 本用例的最后两段断言会失败。
func TestRenameOnlyChangesTheDisplayName(t *testing.T) {
	rig := newMediaTestRig(t, nil)
	body := []byte("not really a webp, but the bytes are what matter here")
	seeded := rig.seedImage(t, "a1b2c3d4.webp", body, "image/webp")

	updated, err := rig.svc.Rename(context.Background(), seeded.ID, "  猫咪.jpg  ")
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	// 返回的就是更新后的记录，前端拿它替换列表项即可。
	if updated.OriginalName != "猫咪.webp" {
		t.Fatalf("OriginalName in the response = %q, want %q", updated.OriginalName, "猫咪.webp")
	}
	if updated.StorageName != "a1b2c3d4.webp" {
		t.Fatalf("StorageName in the response = %q, want it untouched", updated.StorageName)
	}
	if updated.ID != seeded.ID || updated.MimeType != seeded.MimeType || updated.Size != seeded.Size {
		t.Fatalf("the rename changed more than the display name: %+v", updated)
	}

	stored, err := rig.repo.GetByID(context.Background(), seeded.ID)
	if err != nil {
		t.Fatalf("GetByID() after the rename error = %v", err)
	}
	if stored.OriginalName != "猫咪.webp" {
		t.Fatalf("stored OriginalName = %q, want the new display name", stored.OriginalName)
	}

	names := mediaFiles(t, rig.dir)
	if len(names) != 1 || names[0] != "a1b2c3d4.webp" {
		t.Fatalf("media directory = %v, want the original stored file name", names)
	}
	onDisk, err := os.ReadFile(filepath.Join(rig.dir, "a1b2c3d4.webp"))
	if err != nil {
		t.Fatalf("read the stored file: %v", err)
	}
	if string(onDisk) != string(body) {
		t.Fatalf("the stored file changed: got %q, want the original bytes", onDisk)
	}
}

// TestRenameRejectsAnInvalidNameWithoutTouchingTheRow 覆盖「名字不合法」这一支：
// 返回 ErrInvalidMediaName，并且库里那一行**一个字段都没被改到**。
func TestRenameRejectsAnInvalidNameWithoutTouchingTheRow(t *testing.T) {
	rig := newMediaTestRig(t, nil)
	seeded := rig.seedImage(t, "a1b2c3d4.webp", []byte("x"), "image/webp")

	bad := []string{"", "   ", "a/b", `a\b`, "..", strings.Repeat("字", 201)}
	for _, name := range bad {
		if _, err := rig.svc.Rename(context.Background(), seeded.ID, name); !errors.Is(err, model.ErrInvalidMediaName) {
			t.Fatalf("Rename(%q) error = %v, want ErrInvalidMediaName", name, err)
		}
	}

	stored, err := rig.repo.GetByID(context.Background(), seeded.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if stored.OriginalName != "a1b2c3d4.webp" {
		t.Fatalf("OriginalName = %q after rejected renames, want it untouched", stored.OriginalName)
	}
}

// TestRenameReportsAMissingMedia 覆盖未知 id：必须先报 ErrMediaNotFound，
// 而不是让一次 UPDATE 影响 0 行之后静默返回成功。
func TestRenameReportsAMissingMedia(t *testing.T) {
	rig := newMediaTestRig(t, nil)

	if _, err := rig.svc.Rename(context.Background(), 4242, "猫咪.png"); !errors.Is(err, model.ErrMediaNotFound) {
		t.Fatalf("Rename() on an unknown id error = %v, want ErrMediaNotFound", err)
	}
}

// TestRenameSurfacesARepositoryFailure 覆盖仓库改写失败那一支：错误必须冒泡，
// 不能吞掉——否则界面会显示新名字，而库里还是旧的。
func TestRenameSurfacesARepositoryFailure(t *testing.T) {
	rig := newMediaTestRig(t, nil)
	seeded := rig.seedImage(t, "a1b2c3d4.webp", []byte("x"), "image/webp")
	rig.svc.repo = failingRenameRepo{MediaRepository: rig.repo, err: errors.New("the database is gone")}

	if _, err := rig.svc.Rename(context.Background(), seeded.ID, "猫咪.png"); err == nil {
		t.Fatal("Rename() succeeded although the record could not be updated")
	}
}

// failingRenameRepo 只让 Rename 失败，其余方法照常走真实仓库（与 failingMediaRepo
// 同一写法：要验的是「这一处失败之后调用方做了什么」，别的方法在这里只是通道）。
type failingRenameRepo struct {
	model.MediaRepository
	err error
}

func (r failingRenameRepo) Rename(ctx context.Context, id int64, originalName string) error {
	if r.err != nil {
		return r.err
	}
	return r.MediaRepository.Rename(ctx, id, originalName)
}
