package service

import (
	"context"
	"errors"
	"testing"

	"blog-engine/internal/model"
)

// 本文件逐个服务走一遍「仓储/依赖失败」的路径。
//
// 断言统一是「错误必须原样冒泡」：服务层在这些分支上只做两件事——记日志、
// 把错误原样返回。把它改成包装错误或吞掉，都会让上层失去「这一层到底哪一步
// 失败」的信息，而调用方（handler）正是靠 errors.Is 映射 HTTP 状态码的。

type brokenMediaStore struct{}

func (brokenMediaStore) Get(context.Context, int64) (*model.Media, error) { return nil, errRepoDown }
func (brokenMediaStore) Delete(context.Context, int64) error              { return errRepoDown }

type brokenArticleIndexer struct{}

func (brokenArticleIndexer) IndexArticle(context.Context, *model.Article) error { return errRepoDown }
func (brokenArticleIndexer) DeleteArticle(context.Context, int64) error         { return errRepoDown }
func (brokenArticleIndexer) DeleteArticles(context.Context, []int64) error      { return errRepoDown }

type brokenPageIndexer struct{}

func (brokenPageIndexer) IndexPage(context.Context, *model.Page) error { return errRepoDown }
func (brokenPageIndexer) DeletePage(context.Context, int64) error      { return errRepoDown }
func (brokenPageIndexer) DeletePages(context.Context, []int64) error   { return errRepoDown }

type brokenSender struct{}

func (brokenSender) Send(string, string, string) error                  { return errRepoDown }
func (brokenSender) SendVerification(a, b, c, d string) error           { return errRepoDown }
func (brokenSender) SendCommentNotification(a, b, c string) error       { return errRepoDown }
func (brokenSender) SendPasswordResetNotification(a, b, c string) error { return errRepoDown }

func requireRepoError(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: the repository error must surface", name)
	}
	if !errors.Is(err, errRepoDown) {
		t.Fatalf("%s: error = %v, want %v", name, err, errRepoDown)
	}
}

func TestPageServiceSurfacesRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	svc := NewPageService(brokenPageRepo{}, brokenPageIndexer{})

	_, err := svc.Create(ctx, "t", "", "c", "")
	requireRepoError(t, "Create", err)
	_, err = svc.Update(ctx, 1, "t", nil, nil, nil)
	requireRepoError(t, "Update/GetByID", err)
	requireRepoError(t, "SoftDelete", svc.SoftDelete(ctx, []int64{1}))
	requireRepoError(t, "Restore", svc.Restore(ctx, 1))
	requireRepoError(t, "BatchRestore", svc.BatchRestore(ctx, []int64{1}))
	requireRepoError(t, "HardDelete", svc.HardDelete(ctx, []int64{1}))
	_, _, err = svc.List(ctx, model.Pagination{})
	requireRepoError(t, "List", err)
	_, _, err = svc.ListDeleted(ctx, model.Pagination{})
	requireRepoError(t, "ListDeleted", err)
}

func TestMomentServiceSurfacesRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	svc := NewMomentService(brokenMomentRepo{}, brokenMediaStore{})

	_, err := svc.Create(ctx, "c", nil)
	requireRepoError(t, "Create", err)
	_, err = svc.Update(ctx, 1, "c", nil)
	requireRepoError(t, "Update", err)
	requireRepoError(t, "SoftDelete", svc.SoftDelete(ctx, []int64{1}))
	requireRepoError(t, "Restore", svc.Restore(ctx, 1))
	requireRepoError(t, "BatchRestore", svc.BatchRestore(ctx, []int64{1}))
	requireRepoError(t, "HardDelete", svc.HardDelete(ctx, []int64{1}))
	_, _, err = svc.List(ctx, model.Pagination{})
	requireRepoError(t, "List", err)
	_, _, err = svc.ListDeleted(ctx, model.Pagination{})
	requireRepoError(t, "ListDeleted", err)
}

func TestTagServiceSurfacesRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	svc := NewTagService(brokenTagRepo{})

	_, _, err := svc.List(ctx, model.Pagination{})
	requireRepoError(t, "List", err)
	_, _, err = svc.ListArticles(ctx, 1, model.Pagination{})
	requireRepoError(t, "ListArticles", err)
	_, err = svc.Count(ctx, 1)
	requireRepoError(t, "Count", err)
	_, err = svc.Rename(ctx, 1, "Go", "Go EN")
	requireRepoError(t, "Rename", err)
}

func TestAdminAuthSurfacesRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	svc := NewAdminAuth(brokenAdminRepo{}, brokenMediaStore{})

	_, err := svc.Get(ctx)
	requireRepoError(t, "Get", err)

	// Identity / DisplayName / AvatarMediaID 读不到管理员行时都要安全退化：
	// 前两者返回错误/空串，后者返回 (0, false) 而不是把错误抛给公开接口。
	_, _, err = svc.Identity(ctx)
	requireRepoError(t, "Identity", err)
	if name := svc.DisplayName(ctx); name != "" {
		t.Fatalf("DisplayName = %q, want an empty string", name)
	}
	if id, ok := svc.AvatarMediaID(ctx); id != 0 || ok {
		t.Fatalf("AvatarMediaID = (%d, %v)", id, ok)
	}
}

func TestArticleServiceSurfacesRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	svc := NewArticleService(brokenArticleRepo{}, brokenArticleIndexer{}, brokenMediaStore{})

	_, err := svc.Create(ctx, ArticleWrite{Title: "t"})
	requireRepoError(t, "Create", err)
	_, err = svc.Update(ctx, 1, ArticleWrite{Title: "t"})
	requireRepoError(t, "Update", err)
	requireRepoError(t, "SoftDelete", svc.SoftDelete(ctx, []int64{1}))
	requireRepoError(t, "Restore", svc.Restore(ctx, 1))
	requireRepoError(t, "BatchRestore", svc.BatchRestore(ctx, []int64{1}))
	requireRepoError(t, "HardDelete", svc.HardDelete(ctx, []int64{1}))
	_, _, err = svc.List(ctx, model.Pagination{})
	requireRepoError(t, "List", err)
	_, _, err = svc.ListDeleted(ctx, model.Pagination{})
	requireRepoError(t, "ListDeleted", err)
}

func TestCommentServiceSurfacesRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	targets := map[string]CommentTargetLookup{
		"article": func(context.Context, int64) (bool, error) { return true, nil },
	}
	svc := NewCommentService(brokenCommentRepo{}, brokenUserRepo{}, targets)

	_, err := svc.CreateAsUser(ctx, 7, "article", 1, nil, "c")
	requireRepoError(t, "CreateAsUser/author", err)

	// 目标校验通过、作者读取成功之后，仓储的失败才轮得到 Create/GetByID。
	svc = NewCommentService(brokenCommentRepo{}, stubUserRepo{nickname: "访客"}, targets)
	_, err = svc.CreateAsUser(ctx, 7, "article", 1, nil, "c")
	requireRepoError(t, "CreateAsUser/repo", err)
	_, err = svc.CreateAsAdmin(ctx, "Admin", "article", 1, nil, "c")
	requireRepoError(t, "CreateAsAdmin/repo", err)

	_, _, err = svc.ListByTarget(ctx, "article", 1, nil, model.Pagination{})
	requireRepoError(t, "ListByTarget", err)
	_, _, err = svc.ListAll(ctx, model.Pagination{})
	requireRepoError(t, "ListAll", err)
	_, _, err = svc.ListDeleted(ctx, model.Pagination{})
	requireRepoError(t, "ListDeleted", err)
	_, err = svc.Like(ctx, 1, 2)
	requireRepoError(t, "Like", err)
	_, err = svc.Unlike(ctx, 1, 2)
	requireRepoError(t, "Unlike", err)
	requireRepoError(t, "SoftDelete", svc.SoftDelete(ctx, []int64{1}))
	requireRepoError(t, "Restore", svc.Restore(ctx, 1))
	requireRepoError(t, "BatchRestore", svc.BatchRestore(ctx, []int64{1}))
	requireRepoError(t, "HardDelete", svc.HardDelete(ctx, []int64{1}))

	// 目标查询本身失败：必须与「目标不存在」区分开，不能伪装成 404。
	failing := NewCommentService(brokenCommentRepo{}, stubUserRepo{nickname: "访客"}, map[string]CommentTargetLookup{
		"article": func(context.Context, int64) (bool, error) { return false, errRepoDown },
	})
	_, _, err = failing.ListByTarget(ctx, "article", 1, nil, model.Pagination{})
	requireRepoError(t, "target lookup", err)
}

// stubUserRepo 是一个「能返回用户、其余都失败」的账号仓储。
type stubUserRepo struct {
	nickname string
}

func (s stubUserRepo) FindByUsername(context.Context, string) (*model.User, error) {
	return &model.User{Nickname: s.nickname}, nil
}
func (s stubUserRepo) FindByEmail(context.Context, string) (*model.User, error) {
	return &model.User{Nickname: s.nickname}, nil
}
func (s stubUserRepo) GetByID(context.Context, int64) (*model.User, error) {
	return &model.User{Nickname: s.nickname}, nil
}
func (s stubUserRepo) Create(context.Context, *model.User) error                  { return errRepoDown }
func (s stubUserRepo) UpdateNickname(context.Context, int64, string, int64) error { return errRepoDown }
func (s stubUserRepo) UpdateUsername(context.Context, int64, string, int64) error { return errRepoDown }
func (s stubUserRepo) UpdatePassword(context.Context, int64, string, int64) error {
	return errRepoDown
}
func (s stubUserRepo) UpdateProfile(context.Context, int64, string, string, string, int64) error {
	return errRepoDown
}
func (s stubUserRepo) List(context.Context, model.Pagination) ([]model.User, int64, error) {
	return nil, 0, errRepoDown
}
