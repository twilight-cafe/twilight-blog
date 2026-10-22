package service

import (
	"context"
	"errors"
	"testing"

	"blog-engine/internal/model"
)

// commentStubRepo 是一个可逐个方法定制的评论仓储。
//
// 错误路径测试需要精确地控制「哪一步失败」——评论写入要经过作者读取、目标校验、
// 父评论校验、插入、回读五步，任何一步失败的表现都不同。统一的「全部失败」
// 替身做不到这一点。
type commentStubRepo struct {
	create       func(ctx context.Context, comment *model.Comment) error
	getByID      func(ctx context.Context, id int64) (*model.Comment, error)
	getForUser   func(ctx context.Context, id int64, currentUserID *int64) (*model.Comment, error)
	list         func(ctx context.Context, targetType string, targetID int64, currentUserID *int64, p model.Pagination) ([]model.Comment, int64, error)
	addLike      func(ctx context.Context, commentID, userID, now int64) error
	removeLike   func(ctx context.Context, commentID, userID int64) error
	softDelete   func(ctx context.Context, ids []int64, deletedAt int64) (int64, error)
	restore      func(ctx context.Context, id, updatedAt int64) error
	batchRestore func(ctx context.Context, ids []int64, updatedAt int64) (int64, error)
	hardDelete   func(ctx context.Context, ids []int64) (int64, error)
}

func (s *commentStubRepo) Create(ctx context.Context, comment *model.Comment) error {
	if s.create != nil {
		return s.create(ctx, comment)
	}
	return nil
}

func (s *commentStubRepo) GetByID(ctx context.Context, id int64) (*model.Comment, error) {
	if s.getByID != nil {
		return s.getByID(ctx, id)
	}
	return &model.Comment{ID: id}, nil
}

func (s *commentStubRepo) GetByIDForUser(ctx context.Context, id int64, currentUserID *int64) (*model.Comment, error) {
	if s.getForUser != nil {
		return s.getForUser(ctx, id, currentUserID)
	}
	return &model.Comment{ID: id}, nil
}

func (s *commentStubRepo) ListByTarget(ctx context.Context, targetType string, targetID int64, currentUserID *int64, p model.Pagination) ([]model.Comment, int64, error) {
	if s.list != nil {
		return s.list(ctx, targetType, targetID, currentUserID, p)
	}
	return nil, 0, nil
}

func (s *commentStubRepo) ListAll(context.Context, model.Pagination) ([]model.Comment, int64, error) {
	return nil, 0, nil
}

func (s *commentStubRepo) ListDeleted(context.Context, model.Pagination) ([]model.Comment, int64, error) {
	return nil, 0, nil
}

func (s *commentStubRepo) AddLike(ctx context.Context, commentID, userID, now int64) error {
	if s.addLike != nil {
		return s.addLike(ctx, commentID, userID, now)
	}
	return nil
}

func (s *commentStubRepo) RemoveLike(ctx context.Context, commentID, userID int64) error {
	if s.removeLike != nil {
		return s.removeLike(ctx, commentID, userID)
	}
	return nil
}

func (s *commentStubRepo) SoftDelete(ctx context.Context, ids []int64, deletedAt int64) (int64, error) {
	if s.softDelete != nil {
		return s.softDelete(ctx, ids, deletedAt)
	}
	return int64(len(ids)), nil
}

func (s *commentStubRepo) Restore(ctx context.Context, id, updatedAt int64) error {
	if s.restore != nil {
		return s.restore(ctx, id, updatedAt)
	}
	return nil
}

func (s *commentStubRepo) BatchRestore(ctx context.Context, ids []int64, updatedAt int64) (int64, error) {
	if s.batchRestore != nil {
		return s.batchRestore(ctx, ids, updatedAt)
	}
	return int64(len(ids)), nil
}

func (s *commentStubRepo) HardDelete(ctx context.Context, ids []int64) (int64, error) {
	if s.hardDelete != nil {
		return s.hardDelete(ctx, ids)
	}
	return int64(len(ids)), nil
}

func alwaysTarget(context.Context, int64) (bool, error) { return true, nil }

func TestCommentServiceValidatesInput(t *testing.T) {
	ctx := context.Background()
	repo := &commentStubRepo{}
	svc := NewCommentService(repo, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{
		"article": alwaysTarget,
	})

	if _, err := svc.CreateAsUser(ctx, 1, "unknown", 1, nil, "c"); !errors.Is(err, model.ErrInvalidCommentTarget) {
		t.Fatalf("unknown target type error = %v", err)
	}
	if _, err := svc.CreateAsUser(ctx, 1, "article", 0, nil, "c"); !errors.Is(err, model.ErrInvalidCommentTarget) {
		t.Fatalf("non-positive target id error = %v", err)
	}
	if _, err := svc.CreateAsUser(ctx, 1, "article", 1, nil, "  "); err == nil {
		t.Fatal("empty content must be refused")
	}
	long := make([]byte, model.MaxCommentContentLength+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := svc.CreateAsUser(ctx, 1, "article", 1, nil, string(long)); err == nil {
		t.Fatal("an over-long comment must be refused")
	}

	// 昵称为空时回退匿名名，而不是拒绝这条评论。
	// 断言落在**写进仓储的那条评论**上：回读出来的对象由仓储决定，
	// 替身返回的是空评论，看不出署名规则。
	var written *model.Comment
	capturing := &commentStubRepo{create: func(_ context.Context, comment *model.Comment) error {
		written = comment
		return nil
	}}
	anonymous := NewCommentService(capturing, stubUserRepo{}, map[string]CommentTargetLookup{"article": alwaysTarget})
	if _, err := anonymous.CreateAsUser(ctx, 1, "article", 1, nil, "c"); err != nil {
		t.Fatalf("CreateAsUser: %v", err)
	}
	if written == nil || written.Nickname != model.AnonymousDisplayName {
		t.Fatalf("nickname = %+v", written)
	}

	// 站长评论不必有昵称。
	if _, err := svc.CreateAsAdmin(ctx, "  ", "article", 1, nil, "c"); err != nil {
		t.Fatalf("CreateAsAdmin: %v", err)
	}
}

func TestCommentServiceValidatesParent(t *testing.T) {
	ctx := context.Background()

	missing := &commentStubRepo{getByID: func(context.Context, int64) (*model.Comment, error) {
		return nil, model.ErrCommentNotFound
	}}
	svc := NewCommentService(missing, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{"article": alwaysTarget})
	parent := int64(9)
	if _, err := svc.CreateAsUser(ctx, 1, "article", 1, &parent, "c"); !errors.Is(err, model.ErrCommentNotFound) {
		t.Fatalf("missing parent error = %v", err)
	}

	broken := &commentStubRepo{getByID: func(context.Context, int64) (*model.Comment, error) {
		return nil, errRepoDown
	}}
	svc = NewCommentService(broken, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{"article": alwaysTarget})
	if _, err := svc.CreateAsUser(ctx, 1, "article", 1, &parent, "c"); !errors.Is(err, errRepoDown) {
		t.Fatalf("parent lookup failure error = %v", err)
	}

	other := &commentStubRepo{getByID: func(context.Context, int64) (*model.Comment, error) {
		return &model.Comment{ID: 9, TargetType: "moment", TargetID: 2}, nil
	}}
	svc = NewCommentService(other, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{"article": alwaysTarget})
	if _, err := svc.CreateAsUser(ctx, 1, "article", 1, &parent, "c"); !errors.Is(err, model.ErrInvalidCommentTarget) {
		t.Fatalf("cross-target reply error = %v", err)
	}
}

func TestCommentServiceFailsWhenTheTargetLookupIsMissing(t *testing.T) {
	ctx := context.Background()
	svc := NewCommentService(&commentStubRepo{}, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{})
	if _, err := svc.CreateAsUser(ctx, 1, "article", 1, nil, "c"); err == nil {
		t.Fatal("an unwired target lookup must refuse the write")
	}
}

func TestCommentServiceCreateAndReadFailures(t *testing.T) {
	ctx := context.Background()

	createFails := &commentStubRepo{create: func(context.Context, *model.Comment) error { return errRepoDown }}
	svc := NewCommentService(createFails, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{"article": alwaysTarget})
	if _, err := svc.CreateAsUser(ctx, 1, "article", 1, nil, "c"); !errors.Is(err, errRepoDown) {
		t.Fatalf("create failure error = %v", err)
	}

	reloadFails := &commentStubRepo{getByID: func(context.Context, int64) (*model.Comment, error) {
		return nil, errRepoDown
	}}
	svc = NewCommentService(reloadFails, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{"article": alwaysTarget})
	if _, err := svc.CreateAsUser(ctx, 1, "article", 1, nil, "c"); !errors.Is(err, errRepoDown) {
		t.Fatalf("reload failure error = %v", err)
	}
}

func TestCommentServiceListValidation(t *testing.T) {
	ctx := context.Background()
	svc := NewCommentService(&commentStubRepo{}, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{"article": alwaysTarget})

	if _, _, err := svc.ListByTarget(ctx, "unknown", 1, nil, model.Pagination{}); !errors.Is(err, model.ErrInvalidCommentTarget) {
		t.Fatalf("unknown target error = %v", err)
	}
	if _, _, err := svc.ListByTarget(ctx, "article", 0, nil, model.Pagination{}); !errors.Is(err, model.ErrInvalidCommentTarget) {
		t.Fatalf("non-positive target error = %v", err)
	}
}

func TestCommentServiceLikeAndUnlike(t *testing.T) {
	ctx := context.Background()

	missing := &commentStubRepo{getByID: func(context.Context, int64) (*model.Comment, error) {
		return nil, model.ErrCommentNotFound
	}}
	svc := NewCommentService(missing, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{"article": alwaysTarget})
	if _, err := svc.Like(ctx, 1, 2); !errors.Is(err, model.ErrCommentNotFound) {
		t.Fatalf("like on a missing comment error = %v", err)
	}
	if _, err := svc.Unlike(ctx, 1, 2); !errors.Is(err, model.ErrCommentNotFound) {
		t.Fatalf("unlike on a missing comment error = %v", err)
	}

	likeFails := &commentStubRepo{addLike: func(context.Context, int64, int64, int64) error { return errRepoDown }}
	svc = NewCommentService(likeFails, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{"article": alwaysTarget})
	if _, err := svc.Like(ctx, 1, 2); !errors.Is(err, errRepoDown) {
		t.Fatalf("like failure error = %v", err)
	}

	reloadFails := &commentStubRepo{getForUser: func(context.Context, int64, *int64) (*model.Comment, error) {
		return nil, errRepoDown
	}}
	svc = NewCommentService(reloadFails, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{"article": alwaysTarget})
	if _, err := svc.Like(ctx, 1, 2); !errors.Is(err, errRepoDown) {
		t.Fatalf("like reload failure error = %v", err)
	}
	if _, err := svc.Unlike(ctx, 1, 2); !errors.Is(err, errRepoDown) {
		t.Fatalf("unlike reload failure error = %v", err)
	}

	unlikeFails := &commentStubRepo{removeLike: func(context.Context, int64, int64) error { return errRepoDown }}
	svc = NewCommentService(unlikeFails, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{"article": alwaysTarget})
	if _, err := svc.Unlike(ctx, 1, 2); !errors.Is(err, errRepoDown) {
		t.Fatalf("unlike failure error = %v", err)
	}
}

func TestOrderCommentTree(t *testing.T) {
	parent := int64(1)
	other := int64(2)

	ordered := orderCommentTree([]model.Comment{
		{ID: 3, LikeCount: 1, CreatedAt: 30},
		{ID: 1, LikeCount: 5, CreatedAt: 10},
		{ID: 2, LikeCount: 5, CreatedAt: 20},
		{ID: 4, ParentID: &parent, CreatedAt: 40},
		{ID: 5, ParentID: &other, CreatedAt: 50},
	})
	ids := make([]int64, 0, len(ordered))
	for _, comment := range ordered {
		ids = append(ids, comment.ID)
	}
	want := []int64{1, 4, 2, 5, 3}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v, want %v", ids, want)
		}
	}

	// 父评论不在本页（被软删或被分页切走）时，这条回复被提升为根评论，
	// 而不是被悄悄丢掉。
	orphanParent := int64(99)
	promoted := orderCommentTree([]model.Comment{{ID: 6, ParentID: &orphanParent}})
	if len(promoted) != 1 || promoted[0].ID != 6 {
		t.Fatalf("promoted = %+v", promoted)
	}

	// 环：visited 集合保证不会无限递归。
	cycleA := int64(1)
	cycleB := int64(2)
	cyclic := orderCommentTree([]model.Comment{
		{ID: 1, ParentID: &cycleB},
		{ID: 2, ParentID: &cycleA},
	})
	// 环上没有根评论，输出为空即可；关键是它必须**终止**而不是无限递归。
	if len(cyclic) != 0 {
		t.Fatalf("cyclic = %+v", cyclic)
	}

	if got := orderCommentTree(nil); len(got) != 0 {
		t.Fatalf("empty input = %+v", got)
	}
}

func TestNewCommentTargetLookups(t *testing.T) {
	ctx := context.Background()
	lookups := NewCommentTargetLookups(brokenArticleRepo{}, brokenMomentRepo{}, brokenPageRepo{})

	if _, err := lookups["article"](ctx, 1); !errors.Is(err, errRepoDown) {
		t.Fatalf("article lookup error = %v", err)
	}
	if _, err := lookups["moment"](ctx, 1); !errors.Is(err, errRepoDown) {
		t.Fatalf("moment lookup error = %v", err)
	}
	if _, err := lookups["page"](ctx, 1); !errors.Is(err, errRepoDown) {
		t.Fatalf("page lookup error = %v", err)
	}
}
