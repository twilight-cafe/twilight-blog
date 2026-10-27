package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"blog-engine/internal/model"
)

// commentRepoStub 允许逐方法配置评论仓储结果。
type commentRepoStub struct {
	create       func(context.Context, *model.Comment) error
	getByID      func(context.Context, int64) (*model.Comment, error)
	listByTarget func(context.Context, string, int64, *int64, model.Pagination) ([]model.Comment, int64, error)
	listAll      func(context.Context, model.Pagination) ([]model.Comment, int64, error)
	listDeleted  func(context.Context, model.Pagination) ([]model.Comment, int64, error)
	addLike      func(context.Context, int64, int64, int64) error
	removeLike   func(context.Context, int64, int64) error
	softDelete   func(context.Context, []int64, int64) (int64, error)
	restore      func(context.Context, int64, int64) error
	batchRestore func(context.Context, []int64, int64) (int64, error)
	hardDelete   func(context.Context, []int64) (int64, error)
}

func (s *commentRepoStub) Create(ctx context.Context, c *model.Comment) error {
	if s.create == nil {
		return errRepoDown
	}
	return s.create(ctx, c)
}
func (s *commentRepoStub) GetByID(ctx context.Context, id int64) (*model.Comment, error) {
	if s.getByID == nil {
		return nil, errRepoDown
	}
	return s.getByID(ctx, id)
}
func (s *commentRepoStub) GetByIDForUser(ctx context.Context, id int64, uid *int64) (*model.Comment, error) {
	return s.GetByID(ctx, id)
}
func (s *commentRepoStub) ListByTarget(ctx context.Context, target string, id int64, uid *int64, p model.Pagination) ([]model.Comment, int64, error) {
	if s.listByTarget == nil {
		return nil, 0, errRepoDown
	}
	return s.listByTarget(ctx, target, id, uid, p)
}
func (s *commentRepoStub) ListAll(ctx context.Context, p model.Pagination) ([]model.Comment, int64, error) {
	if s.listAll == nil {
		return nil, 0, errRepoDown
	}
	return s.listAll(ctx, p)
}
func (s *commentRepoStub) ListDeleted(ctx context.Context, p model.Pagination) ([]model.Comment, int64, error) {
	if s.listDeleted == nil {
		return nil, 0, errRepoDown
	}
	return s.listDeleted(ctx, p)
}
func (s *commentRepoStub) AddLike(ctx context.Context, id, uid, at int64) error {
	if s.addLike == nil {
		return errRepoDown
	}
	return s.addLike(ctx, id, uid, at)
}
func (s *commentRepoStub) RemoveLike(ctx context.Context, id, uid int64) error {
	if s.removeLike == nil {
		return errRepoDown
	}
	return s.removeLike(ctx, id, uid)
}
func (s *commentRepoStub) SoftDelete(ctx context.Context, ids []int64, at int64) (int64, error) {
	if s.softDelete == nil {
		return 0, errRepoDown
	}
	return s.softDelete(ctx, ids, at)
}
func (s *commentRepoStub) Restore(ctx context.Context, id, at int64) error {
	if s.restore == nil {
		return errRepoDown
	}
	return s.restore(ctx, id, at)
}
func (s *commentRepoStub) BatchRestore(ctx context.Context, ids []int64, at int64) (int64, error) {
	if s.batchRestore == nil {
		return 0, errRepoDown
	}
	return s.batchRestore(ctx, ids, at)
}
func (s *commentRepoStub) HardDelete(ctx context.Context, ids []int64) (int64, error) {
	if s.hardDelete == nil {
		return 0, errRepoDown
	}
	return s.hardDelete(ctx, ids)
}

func commentTargetsStub() map[string]CommentTargetLookup {
	return map[string]CommentTargetLookup{
		"article": func(context.Context, int64) (bool, error) { return true, nil },
	}
}

func TestCommentServiceWriteErrorBranches(t *testing.T) {
	ctx := context.Background()
	svc := NewCommentService(brokenCommentRepo{}, brokenUserRepo{}, commentTargetsStub())

	if err := svc.SoftDelete(ctx, nil); err != nil {
		t.Fatalf("SoftDelete(nil) = %v", err)
	}
	if err := svc.SoftDelete(ctx, []int64{1}); !errors.Is(err, errRepoDown) {
		t.Fatalf("SoftDelete err = %v", err)
	}
	if err := svc.BatchRestore(ctx, nil); err != nil {
		t.Fatalf("BatchRestore(nil) = %v", err)
	}
	if err := svc.BatchRestore(ctx, []int64{1}); !errors.Is(err, errRepoDown) {
		t.Fatalf("BatchRestore err = %v", err)
	}
	if err := svc.HardDelete(ctx, nil); err != nil {
		t.Fatalf("HardDelete(nil) = %v", err)
	}
	if err := svc.HardDelete(ctx, []int64{1}); !errors.Is(err, errRepoDown) {
		t.Fatalf("HardDelete err = %v", err)
	}
	if err := svc.Restore(ctx, 1); !errors.Is(err, errRepoDown) {
		t.Fatalf("Restore err = %v", err)
	}
}

func TestCommentServiceCreateAdminGetBackError(t *testing.T) {
	ctx := context.Background()
	repo := &commentRepoStub{
		create: func(_ context.Context, c *model.Comment) error {
			c.ID = 5
			return nil
		},
		getByID: func(context.Context, int64) (*model.Comment, error) { return nil, errRepoDown },
	}
	svc := NewCommentService(repo, brokenUserRepo{}, commentTargetsStub())
	_, err := svc.CreateAsAdmin(ctx, "站长", "article", 1, nil, "内容")
	if !errors.Is(err, errRepoDown) {
		t.Fatalf("err = %v", err)
	}
}

// TestCreateAsAdminChecksTheParent pins that an administrator's reply also has
// to point at a real comment on the same target. The handler relies on this to
// answer 400 for a bad parent_id.
func TestCreateAsAdminChecksTheParent(t *testing.T) {
	repo := &commentRepoStub{getByID: func(context.Context, int64) (*model.Comment, error) {
		return nil, model.ErrCommentNotFound
	}}
	svc := NewCommentService(repo, brokenUserRepo{}, commentTargetsStub())
	_, err := svc.CreateAsAdmin(context.Background(), "站长", "article", 1, int64Ptr(9), "回复")
	if !errors.Is(err, model.ErrCommentNotFound) {
		t.Fatalf("err = %v, want ErrCommentNotFound", err)
	}
}

// TestNormalizeNicknameDropsControlCharacters covers the two distinct removals:
// newlines/tabs fold to a space, while other control characters disappear
// entirely. The second arm is what stops a nickname from injecting terminal
// escapes into logs and the admin list.
func TestNormalizeNicknameDropsControlCharacters(t *testing.T) {
	cases := map[string]string{
		"a\nb":                  "a b",
		"a\r\tb":                "a b",
		"zero\x00byte":          "zerobyte",
		"del\x7fchar":           "delchar",
		"  lots   of   space  ": "lots of space",
	}
	for input, want := range cases {
		if got := normalizeNickname(input); got != want {
			t.Fatalf("normalizeNickname(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidateCommentInputBranches(t *testing.T) {
	cases := []struct {
		name     string
		target   string
		id       int64
		content  string
		nickname string
		required bool
		bad      bool
	}{
		{name: "invalid target", target: "bogus", id: 1, content: "c", bad: true},
		{name: "invalid id", target: "article", id: 0, content: "c", bad: true},
		{name: "empty content", target: "article", id: 1, bad: true},
		{name: "long content", target: "article", id: 1, content: strings.Repeat("文", model.MaxCommentContentLength+1), bad: true},
		{name: "nickname required", target: "article", id: 1, content: "c", required: true, bad: true},
		{name: "nickname too long", target: "article", id: 1, content: "c",
			nickname: strings.Repeat("名", model.MaxCommentNicknameLength+1), required: true, bad: true},
		{name: "valid", target: "article", id: 1, content: "c", nickname: "访客", required: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCommentInput(tc.target, tc.id, tc.content, tc.nickname, tc.required)
			if tc.bad && err == nil {
				t.Fatal("validation must fail")
			}
			if !tc.bad && err != nil {
				t.Fatalf("validation failed: %v", err)
			}
		})
	}
}

func TestValidateParentBranches(t *testing.T) {
	ctx := context.Background()
	comment := &model.Comment{ID: 2, TargetType: "article", TargetID: 1}
	cases := []struct {
		name   string
		repo   *commentRepoStub
		parent *int64
		bad    bool
	}{
		{name: "root", repo: &commentRepoStub{}, parent: nil},
		{name: "missing", repo: &commentRepoStub{getByID: func(context.Context, int64) (*model.Comment, error) {
			return nil, model.ErrCommentNotFound
		}}, parent: int64Ptr(9), bad: true},
		{name: "generic", repo: &commentRepoStub{getByID: func(context.Context, int64) (*model.Comment, error) {
			return nil, errRepoDown
		}}, parent: int64Ptr(9), bad: true},
		{name: "different target", repo: &commentRepoStub{getByID: func(context.Context, int64) (*model.Comment, error) {
			return &model.Comment{ID: 2, TargetType: "page", TargetID: 1}, nil
		}}, parent: int64Ptr(2), bad: true},
		{name: "match", repo: &commentRepoStub{getByID: func(context.Context, int64) (*model.Comment, error) {
			return comment, nil
		}}, parent: int64Ptr(2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewCommentService(tc.repo, brokenUserRepo{}, commentTargetsStub())
			err := svc.validateParent(ctx, tc.parent, "article", 1)
			if tc.bad && err == nil {
				t.Fatal("validation must fail")
			}
			if !tc.bad && err != nil {
				t.Fatalf("validation failed: %v", err)
			}
		})
	}
}

func TestOrderCommentTreeBranches(t *testing.T) {
	// 两个根评论点赞与时间都相同，只能靠 ID 决定顺序。
	tree := []model.Comment{
		{ID: 2, LikeCount: 1, CreatedAt: 1},
		{ID: 1, LikeCount: 1, CreatedAt: 1},
	}
	ordered := orderCommentTree(tree)
	if ordered[0].ID != 1 || ordered[1].ID != 2 {
		t.Fatalf("root tie order = %v", []int64{ordered[0].ID, ordered[1].ID})
	}

	// 子评论同时间时按 ID 排序；不同时间时按时间排序。
	parent := model.Comment{ID: 10, LikeCount: 5, CreatedAt: 1}
	tree = []model.Comment{
		parent,
		{ID: 12, ParentID: int64Ptr(10), CreatedAt: 2},
		{ID: 11, ParentID: int64Ptr(10), CreatedAt: 2},
	}
	ordered = orderCommentTree(tree)
	if ordered[1].ID != 11 || ordered[2].ID != 12 {
		t.Fatalf("child tie order = %v", []int64{ordered[1].ID, ordered[2].ID})
	}

	// 时间不同时按时间升序，与 ID 无关：先到先展示。
	tree = []model.Comment{
		parent,
		{ID: 13, ParentID: int64Ptr(10), CreatedAt: 3},
		{ID: 12, ParentID: int64Ptr(10), CreatedAt: 2},
	}
	ordered = orderCommentTree(tree)
	if ordered[1].ID != 12 || ordered[2].ID != 13 {
		t.Fatalf("child time order = %v", []int64{ordered[1].ID, ordered[2].ID})
	}

	// 父评论缺失：回复提升为根，不消失。
	missingParent := []model.Comment{{ID: 20, ParentID: int64Ptr(999)}}
	if got := orderCommentTree(missingParent); len(got) != 1 || got[0].ID != 20 {
		t.Fatalf("orphan = %v", got)
	}

	// 重复 ID 构成的环：根 30 的子节点又是一个 ID=30 的行，
	// visited 必须阻止 appendTree 再次展开它。
	cycle := []model.Comment{
		{ID: 30},
		{ID: 30, ParentID: int64Ptr(30)},
	}
	if got := orderCommentTree(cycle); len(got) != 1 {
		t.Fatalf("cycle result length = %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// 页面服务
// ---------------------------------------------------------------------------

type pageRepoStub struct {
	create       func(context.Context, *model.Page) error
	update       func(context.Context, *model.Page) error
	softDelete   func(context.Context, []int64, int64) (int64, error)
	restore      func(context.Context, int64, int64) error
	batchRestore func(context.Context, []int64, int64) (int64, error)
	hardDelete   func(context.Context, []int64) (int64, error)
	getByID      func(context.Context, int64) (*model.Page, error)
	list         func(context.Context, model.Pagination) ([]model.Page, int64, error)
	listDeleted  func(context.Context, model.Pagination) ([]model.Page, int64, error)
}

func (s *pageRepoStub) Create(ctx context.Context, p *model.Page) error {
	if s.create == nil {
		return errRepoDown
	}
	return s.create(ctx, p)
}
func (s *pageRepoStub) Update(ctx context.Context, p *model.Page) error {
	if s.update == nil {
		return errRepoDown
	}
	return s.update(ctx, p)
}
func (s *pageRepoStub) SoftDelete(ctx context.Context, ids []int64, at int64) (int64, error) {
	if s.softDelete == nil {
		return 0, errRepoDown
	}
	return s.softDelete(ctx, ids, at)
}
func (s *pageRepoStub) Restore(ctx context.Context, id, at int64) error {
	if s.restore == nil {
		return errRepoDown
	}
	return s.restore(ctx, id, at)
}
func (s *pageRepoStub) BatchRestore(ctx context.Context, ids []int64, at int64) (int64, error) {
	if s.batchRestore == nil {
		return 0, errRepoDown
	}
	return s.batchRestore(ctx, ids, at)
}
func (s *pageRepoStub) HardDelete(ctx context.Context, ids []int64) (int64, error) {
	if s.hardDelete == nil {
		return 0, errRepoDown
	}
	return s.hardDelete(ctx, ids)
}
func (s *pageRepoStub) GetByID(ctx context.Context, id int64) (*model.Page, error) {
	if s.getByID == nil {
		return nil, errRepoDown
	}
	return s.getByID(ctx, id)
}
func (s *pageRepoStub) List(ctx context.Context, p model.Pagination) ([]model.Page, int64, error) {
	if s.list == nil {
		return nil, 0, errRepoDown
	}
	return s.list(ctx, p)
}
func (s *pageRepoStub) ListDeleted(ctx context.Context, p model.Pagination) ([]model.Page, int64, error) {
	if s.listDeleted == nil {
		return nil, 0, errRepoDown
	}
	return s.listDeleted(ctx, p)
}

type failingPageIndexer struct{}

func (failingPageIndexer) IndexPage(context.Context, *model.Page) error { return errRepoDown }
func (failingPageIndexer) DeletePage(context.Context, int64) error      { return errRepoDown }
func (failingPageIndexer) DeletePages(context.Context, []int64) error   { return errRepoDown }

func TestPageServiceIndexErrorBranches(t *testing.T) {
	ctx := context.Background()

	svc := NewPageService(&pageRepoStub{
		create:     func(_ context.Context, p *model.Page) error { p.ID = 1; return nil },
		softDelete: func(context.Context, []int64, int64) (int64, error) { return 1, nil },
		hardDelete: func(context.Context, []int64) (int64, error) { return 1, nil },
		list:       func(context.Context, model.Pagination) ([]model.Page, int64, error) { return nil, 0, nil },
	}, failingPageIndexer{})
	// 索引失败只记日志，不影响返回值。
	if _, err := svc.Create(ctx, "t", "", "c", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.SoftDelete(ctx, []int64{1}); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if err := svc.HardDelete(ctx, []int64{1}); err != nil {
		t.Fatalf("HardDelete: %v", err)
	}

	// search == nil 的部署：索引步骤直接跳过。
	noIndex := NewPageService(&pageRepoStub{}, nil)
	if _, err := noIndex.Create(ctx, "t", "", "c", ""); !errors.Is(err, errRepoDown) {
		t.Fatalf("err = %v", err)
	}

	// 空集合直接返回。
	if err := noIndex.SoftDelete(ctx, nil); err != nil {
		t.Fatalf("SoftDelete(nil) = %v", err)
	}
	if err := noIndex.BatchRestore(ctx, nil); err != nil {
		t.Fatalf("BatchRestore(nil) = %v", err)
	}
	if err := noIndex.HardDelete(ctx, nil); err != nil {
		t.Fatalf("HardDelete(nil) = %v", err)
	}
}

func TestPageServiceReindexErrorsAreLogged(t *testing.T) {
	ctx := context.Background()
	svc := NewPageService(&pageRepoStub{
		restore: func(context.Context, int64, int64) error { return nil },
		batchRestore: func(context.Context, []int64, int64) (int64, error) {
			return 1, nil
		},
		getByID: func(context.Context, int64) (*model.Page, error) { return nil, errRepoDown },
	}, failingPageIndexer{})
	if err := svc.Restore(ctx, 1); err != nil {
		t.Fatalf("Restore must not fail on a reindex error: %v", err)
	}
	if err := svc.BatchRestore(ctx, []int64{1}); err != nil {
		t.Fatalf("BatchRestore must not fail on a reindex error: %v", err)
	}
}

func TestPageServiceUpdateAndListErrors(t *testing.T) {
	ctx := context.Background()
	svc := NewPageService(&pageRepoStub{
		getByID: func(context.Context, int64) (*model.Page, error) { return &model.Page{ID: 1}, nil },
		update:  func(context.Context, *model.Page) error { return errRepoDown },
	}, nil)
	if _, err := svc.Update(ctx, 1, "t", nil, nil, nil); !errors.Is(err, errRepoDown) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := svc.List(ctx, model.Pagination{}); !errors.Is(err, errRepoDown) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := svc.ListDeleted(ctx, model.Pagination{}); !errors.Is(err, errRepoDown) {
		t.Fatalf("err = %v", err)
	}
	if _, err := svc.Get(ctx, 1); err != nil {
		t.Fatalf("Get: %v", err)
	}
}
