package service

import (
	"context"
	"errors"
	"testing"

	"blog-engine/internal/model"
)

// adminStubRepo 是一个可逐方法定制的管理员仓储。
type adminStubRepo struct {
	get            func(context.Context) (*model.User, error)
	create         func(context.Context, *model.User) error
	setPassword    func(context.Context, int64, string, int64) error
	updateIdentity func(context.Context, int64, string, string, int64) error
	updateAvatar   func(context.Context, int64, *int64, int64) error
}

func (s *adminStubRepo) Create(ctx context.Context, admin *model.User) error {
	if s.create != nil {
		return s.create(ctx, admin)
	}
	return nil
}

func (s *adminStubRepo) Get(ctx context.Context) (*model.User, error) {
	if s.get != nil {
		return s.get(ctx)
	}
	return &model.User{ID: 1, Username: "admin", Nickname: "站长"}, nil
}

func (s *adminStubRepo) Exists(context.Context) (bool, error) { return true, nil }

func (s *adminStubRepo) SetPassword(ctx context.Context, id int64, hash string, updatedAt int64) error {
	if s.setPassword != nil {
		return s.setPassword(ctx, id, hash, updatedAt)
	}
	return nil
}

func (s *adminStubRepo) UpdateIdentity(ctx context.Context, id int64, username, nickname string, updatedAt int64) error {
	if s.updateIdentity != nil {
		return s.updateIdentity(ctx, id, username, nickname, updatedAt)
	}
	return nil
}

func (s *adminStubRepo) UpdateAvatar(ctx context.Context, id int64, mediaID *int64, updatedAt int64) error {
	if s.updateAvatar != nil {
		return s.updateAvatar(ctx, id, mediaID, updatedAt)
	}
	return nil
}

type stubMediaStore struct {
	media *model.Media
	err   error
}

func (s stubMediaStore) Get(context.Context, int64) (*model.Media, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.media, nil
}

func TestAdminAuthIdentityFallbacks(t *testing.T) {
	ctx := context.Background()

	// 昵称为空（历史数据）时回退到登录名，而不是显示一个空名字。
	repo := &adminStubRepo{get: func(context.Context) (*model.User, error) {
		return &model.User{ID: 1, Username: "admin"}, nil
	}}
	svc := NewAdminAuth(repo, stubMediaStore{})
	username, nickname, err := svc.Identity(ctx)
	if err != nil || username != "admin" || nickname != "admin" {
		t.Fatalf("Identity = (%q, %q, %v)", username, nickname, err)
	}

	failing := &adminStubRepo{get: func(context.Context) (*model.User, error) { return nil, errRepoDown }}
	svc = NewAdminAuth(failing, stubMediaStore{})
	if _, _, err := svc.Identity(ctx); !errors.Is(err, errRepoDown) {
		t.Fatalf("Identity error = %v", err)
	}
}

func TestAdminAuthAvatarHandling(t *testing.T) {
	ctx := context.Background()
	avatarID := int64(9)

	repo := &adminStubRepo{get: func(context.Context) (*model.User, error) {
		return &model.User{ID: 1, Username: "admin", Nickname: "站长", AvatarMediaID: &avatarID}, nil
	}}
	svc := NewAdminAuth(repo, stubMediaStore{media: &model.Media{ID: avatarID, MediaType: model.MediaTypeImage}})

	if id, ok := svc.AvatarMediaID(ctx); id != avatarID || !ok {
		t.Fatalf("AvatarMediaID = (%d, %v)", id, ok)
	}
	if err := svc.ValidateAvatar(ctx, &avatarID); err != nil {
		t.Fatalf("ValidateAvatar: %v", err)
	}
	if err := svc.ValidateAvatar(ctx, nil); err != nil {
		t.Fatalf("removing the avatar must be allowed: %v", err)
	}

	zero := int64(0)
	if err := svc.ValidateAvatar(ctx, &zero); err == nil {
		t.Fatal("a non-positive avatar id must be refused")
	}

	// 媒体不存在 / 不是图片：都是校验错误，而不是 500。
	notFound := NewAdminAuth(repo, stubMediaStore{err: model.ErrMediaNotFound})
	if err := notFound.ValidateAvatar(ctx, &avatarID); err == nil {
		t.Fatal("a missing media must be refused")
	}
	notImage := NewAdminAuth(repo, stubMediaStore{media: &model.Media{ID: avatarID, MediaType: "video"}})
	if err := notImage.ValidateAvatar(ctx, &avatarID); err == nil {
		t.Fatal("a non-image media must be refused")
	}
	broken := NewAdminAuth(repo, stubMediaStore{err: errRepoDown})
	if err := broken.ValidateAvatar(ctx, &avatarID); !errors.Is(err, errRepoDown) {
		t.Fatalf("a media lookup failure must surface: %v", err)
	}

	// 没有媒体依赖的组装（CLI 那种）也要给出校验错误，而不是 panic。
	noMedia := NewAdminAuth(repo, nil)
	if err := noMedia.ValidateAvatar(ctx, &avatarID); err == nil {
		t.Fatal("a configuration without media must refuse an avatar")
	}
}

func TestAdminAuthUpdateAvatar(t *testing.T) {
	ctx := context.Background()
	avatarID := int64(9)

	svc := NewAdminAuth(&adminStubRepo{}, stubMediaStore{media: &model.Media{ID: avatarID, MediaType: model.MediaTypeImage}})
	updated, err := svc.UpdateAvatar(ctx, &avatarID)
	if err != nil || updated.AvatarMediaID == nil || *updated.AvatarMediaID != avatarID {
		t.Fatalf("UpdateAvatar = (%+v, %v)", updated, err)
	}

	cleared, err := svc.UpdateAvatar(ctx, nil)
	if err != nil || cleared.AvatarMediaID != nil {
		t.Fatalf("clearing the avatar = (%+v, %v)", cleared, err)
	}

	failing := NewAdminAuth(&adminStubRepo{get: func(context.Context) (*model.User, error) {
		return nil, errRepoDown
	}}, stubMediaStore{media: &model.Media{ID: avatarID, MediaType: model.MediaTypeImage}})
	if _, err := failing.UpdateAvatar(ctx, &avatarID); !errors.Is(err, errRepoDown) {
		t.Fatalf("UpdateAvatar error = %v", err)
	}

	writeFails := NewAdminAuth(&adminStubRepo{updateAvatar: func(context.Context, int64, *int64, int64) error {
		return errRepoDown
	}}, stubMediaStore{media: &model.Media{ID: avatarID, MediaType: model.MediaTypeImage}})
	if _, err := writeFails.UpdateAvatar(ctx, &avatarID); !errors.Is(err, errRepoDown) {
		t.Fatalf("UpdateAvatar write failure = %v", err)
	}

	invalid := NewAdminAuth(&adminStubRepo{}, stubMediaStore{media: &model.Media{ID: avatarID, MediaType: "video"}})
	if _, err := invalid.UpdateAvatar(ctx, &avatarID); err == nil {
		t.Fatal("a non-image avatar must be refused before any write")
	}
}

func TestAdminAuthVerify(t *testing.T) {
	ctx := context.Background()

	// 未安装（没有管理员行）时返回 false，而不是错误。
	missing := NewAdminAuth(&adminStubRepo{get: func(context.Context) (*model.User, error) {
		return nil, model.ErrUserNotFound
	}}, stubMediaStore{})
	if missing.Verify(ctx, "whatever") {
		t.Fatal("an uninstalled site must not verify any password")
	}

	broken := NewAdminAuth(&adminStubRepo{get: func(context.Context) (*model.User, error) {
		return nil, errRepoDown
	}}, stubMediaStore{})
	if broken.Verify(ctx, "whatever") {
		t.Fatal("a repository failure must not verify any password")
	}

	// 管理员行存在但没有密码哈希（历史数据）时同样不通过。
	noHash := NewAdminAuth(&adminStubRepo{get: func(context.Context) (*model.User, error) {
		return &model.User{ID: 1, Username: "admin"}, nil
	}}, stubMediaStore{})
	if noHash.Verify(ctx, "whatever") {
		t.Fatal("an account without a password must not verify")
	}
}

func TestAdminAuthExistsAndGet(t *testing.T) {
	ctx := context.Background()
	svc := NewAdminAuth(&adminStubRepo{}, stubMediaStore{})

	exists, err := svc.Exists(ctx)
	if err != nil || !exists {
		t.Fatalf("Exists = (%v, %v)", exists, err)
	}
	if _, err := svc.Get(ctx); err != nil {
		t.Fatalf("Get: %v", err)
	}
}
