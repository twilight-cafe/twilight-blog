package service

import (
	"context"
	"errors"
	"testing"

	"blog-engine/internal/model"
)

func TestArticleUpdateOptionalFields(t *testing.T) {
	ctx := context.Background()
	svc, _, mediaStore, _ := newArticleTestRig(t)
	_ = mediaStore
	article, err := svc.Create(ctx, ArticleWrite{Title: "原文", Content: stringPointer("正文")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	titleEN := "English"
	contentEN := "Body"
	ai := true
	updated, err := svc.Update(ctx, article.ID, ArticleWrite{
		Title:       "新标题",
		TitleEN:     &titleEN,
		ContentEN:   &contentEN,
		AIGenerated: &ai,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.TitleEN != titleEN || updated.ContentEN != contentEN || !updated.AIGenerated {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestMomentServiceEmptySets(t *testing.T) {
	ctx := context.Background()
	svc := NewMomentService(brokenMomentRepo{}, nil)
	if err := svc.SoftDelete(ctx, nil); err != nil {
		t.Fatalf("SoftDelete(nil) = %v", err)
	}
	if err := svc.BatchRestore(ctx, nil); err != nil {
		t.Fatalf("BatchRestore(nil) = %v", err)
	}
	if err := svc.HardDelete(ctx, nil); err != nil {
		t.Fatalf("HardDelete(nil) = %v", err)
	}
}

type tagRepoStub struct {
	rename func(context.Context, int64, string, string) error
	get    func(context.Context, int64) (*model.Tag, error)
}

func (s *tagRepoStub) List(context.Context, model.Pagination) ([]model.Tag, int64, error) {
	return nil, 0, errRepoDown
}
func (s *tagRepoStub) GetByID(ctx context.Context, id int64) (*model.Tag, error) {
	if s.get == nil {
		return nil, errRepoDown
	}
	return s.get(ctx, id)
}
func (s *tagRepoStub) ListArticlesByTag(context.Context, int64, model.Pagination) ([]model.Article, int64, error) {
	return nil, 0, errRepoDown
}
func (s *tagRepoStub) CountByTag(context.Context, int64) (int64, error) { return 0, errRepoDown }
func (s *tagRepoStub) Rename(ctx context.Context, id int64, name, nameEN string) error {
	if s.rename == nil {
		return errRepoDown
	}
	return s.rename(ctx, id, name, nameEN)
}

func TestTagServiceRenameBranches(t *testing.T) {
	ctx := context.Background()
	svc := NewTagService(&tagRepoStub{})
	if _, err := svc.Rename(ctx, 1, "  ", ""); !errors.Is(err, model.ErrInvalidTagName) {
		t.Fatalf("err = %v, want ErrInvalidTagName", err)
	}
	if _, err := svc.Rename(ctx, 1, "Go", ""); !errors.Is(err, errRepoDown) {
		t.Fatalf("rename error = %v", err)
	}

	svc = NewTagService(&tagRepoStub{
		rename: func(context.Context, int64, string, string) error { return nil },
		get:    func(context.Context, int64) (*model.Tag, error) { return nil, errRepoDown },
	})
	if _, err := svc.Rename(ctx, 1, "Go", ""); !errors.Is(err, errRepoDown) {
		t.Fatalf("read-back error = %v", err)
	}

	svc = NewTagService(&tagRepoStub{
		rename: func(context.Context, int64, string, string) error { return nil },
		get:    func(context.Context, int64) (*model.Tag, error) { return &model.Tag{ID: 1, Name: "Go"}, nil },
	})
	tag, err := svc.Rename(ctx, 1, " Go ", " Go ")
	if err != nil || tag.Name != "Go" {
		t.Fatalf("Rename = (%+v, %v)", tag, err)
	}
}

func TestEmailServiceDeliverErrors(t *testing.T) {
	mailer := &fakeMailer{}
	svc := NewEmailService(mailer, brokenEmailRepo{}, "blog@example.com")
	if err := svc.Send("to@example.com", "subject", "body"); !errors.Is(err, errRepoDown) {
		t.Fatalf("count error = %v", err)
	}

	createFails := &emailRepoStub{
		count:  func(context.Context, int64) (int64, error) { return 0, nil },
		create: func(context.Context, *model.Email) error { return errRepoDown },
	}
	svc = NewEmailService(mailer, createFails, "blog@example.com")
	if err := svc.Send("to@example.com", "subject", "body"); !errors.Is(err, errRepoDown) {
		t.Fatalf("store error = %v", err)
	}
}

// emailRepoStub 只覆盖 deliver 用到的两个方法。
type emailRepoStub struct {
	count  func(context.Context, int64) (int64, error)
	create func(context.Context, *model.Email) error
}

func (s *emailRepoStub) Create(ctx context.Context, email *model.Email) error {
	return s.create(ctx, email)
}
func (s *emailRepoStub) CountSince(ctx context.Context, since int64) (int64, error) {
	return s.count(ctx, since)
}
func (s *emailRepoStub) List(context.Context, model.Pagination) ([]model.Email, int64, error) {
	return nil, 0, errRepoDown
}
func (s *emailRepoStub) GetByID(context.Context, int64) (*model.Email, error) {
	return nil, errRepoDown
}

func stringPointer(value string) *string { return &value }
