package service

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/model"
)

// adminRepoStub 让每个仓储方法都能单独配置结果。
type adminRepoStub struct {
	create         func(context.Context, *model.User) error
	get            func(context.Context) (*model.User, error)
	exists         func(context.Context) (bool, error)
	setPassword    func(context.Context, int64, string, int64) error
	updateIdentity func(context.Context, int64, string, string, int64) error
	updateAvatar   func(context.Context, int64, *int64, int64) error
}

func (s *adminRepoStub) Create(ctx context.Context, user *model.User) error {
	if s.create == nil {
		return errRepoDown
	}
	return s.create(ctx, user)
}
func (s *adminRepoStub) Get(ctx context.Context) (*model.User, error) {
	if s.get == nil {
		return nil, errRepoDown
	}
	return s.get(ctx)
}
func (s *adminRepoStub) Exists(ctx context.Context) (bool, error) {
	if s.exists == nil {
		return false, errRepoDown
	}
	return s.exists(ctx)
}
func (s *adminRepoStub) SetPassword(ctx context.Context, id int64, hash string, at int64) error {
	if s.setPassword == nil {
		return errRepoDown
	}
	return s.setPassword(ctx, id, hash, at)
}
func (s *adminRepoStub) UpdateIdentity(ctx context.Context, id int64, username, nickname string, at int64) error {
	if s.updateIdentity == nil {
		return errRepoDown
	}
	return s.updateIdentity(ctx, id, username, nickname, at)
}
func (s *adminRepoStub) UpdateAvatar(ctx context.Context, id int64, mediaID *int64, at int64) error {
	if s.updateAvatar == nil {
		return errRepoDown
	}
	return s.updateAvatar(ctx, id, mediaID, at)
}

type avatarStoreStub struct {
	get func(context.Context, int64) (*model.Media, error)
}

func (s avatarStoreStub) Get(ctx context.Context, id int64) (*model.Media, error) {
	return s.get(ctx, id)
}

func TestAdminAuthIdentityAndDisplayName(t *testing.T) {
	t.Run("identity falls back to username", func(t *testing.T) {
		auth := NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) {
			return &model.User{Username: "admin"}, nil
		}}, nil)
		username, nickname, err := auth.Identity(context.Background())
		if err != nil || username != "admin" || nickname != "admin" {
			t.Fatalf("Identity = (%q, %q, %v)", username, nickname, err)
		}
	})

	t.Run("identity surfaces lookup errors", func(t *testing.T) {
		auth := NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) {
			return nil, errRepoDown
		}}, nil)
		if _, _, err := auth.Identity(context.Background()); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("display name", func(t *testing.T) {
		auth := NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) {
			return &model.User{Username: "admin", Nickname: "站长"}, nil
		}}, nil)
		if got := auth.DisplayName(context.Background()); got != "站长" {
			t.Fatalf("DisplayName = %q", got)
		}
	})

	t.Run("display name falls back to empty", func(t *testing.T) {
		auth := NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) {
			return nil, errRepoDown
		}}, nil)
		if got := auth.DisplayName(context.Background()); got != "" {
			t.Fatalf("DisplayName = %q", got)
		}
	})
}

func TestAdminAuthAvatar(t *testing.T) {
	ctx := context.Background()

	t.Run("avatar id", func(t *testing.T) {
		cases := []struct {
			name  string
			user  *model.User
			err   error
			id    int64
			valid bool
		}{
			{name: "lookup fails", err: errRepoDown},
			{name: "not installed", err: model.ErrUserNotFound},
			{name: "no avatar", user: &model.User{}},
			{name: "non positive avatar", user: &model.User{AvatarMediaID: int64Pointer(0)}},
			{name: "valid avatar", user: &model.User{AvatarMediaID: int64Pointer(7)}, id: 7, valid: true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				auth := NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) {
					if tc.err != nil {
						return nil, tc.err
					}
					return tc.user, nil
				}}, nil)
				id, valid := auth.AvatarMediaID(ctx)
				if id != tc.id || valid != tc.valid {
					t.Fatalf("AvatarMediaID = (%d, %t), want (%d, %t)", id, valid, tc.id, tc.valid)
				}
			})
		}
	})

	t.Run("validate avatar", func(t *testing.T) {
		cases := []struct {
			name  string
			media *avatarStoreStub
			id    *int64
			bad   bool
		}{
			{name: "nil", media: nil, id: nil},
			{name: "non positive", media: nil, id: int64Pointer(0), bad: true},
			{name: "store missing", media: &avatarStoreStub{get: func(context.Context, int64) (*model.Media, error) {
				return nil, model.ErrMediaNotFound
			}}, id: int64Pointer(1), bad: true},
			{name: "store fails", media: &avatarStoreStub{get: func(context.Context, int64) (*model.Media, error) {
				return nil, errRepoDown
			}}, id: int64Pointer(1), bad: true},
			{name: "not an image", media: &avatarStoreStub{get: func(context.Context, int64) (*model.Media, error) {
				return &model.Media{MediaType: "video"}, nil
			}}, id: int64Pointer(1), bad: true},
			{name: "valid", media: &avatarStoreStub{get: func(context.Context, int64) (*model.Media, error) {
				return &model.Media{MediaType: model.MediaTypeImage}, nil
			}}, id: int64Pointer(1)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var store AdminAvatarStore
				if tc.media != nil {
					store = tc.media
				}
				auth := NewAdminAuth(&adminRepoStub{}, store)
				err := auth.ValidateAvatar(ctx, tc.id)
				if tc.bad && err == nil {
					t.Fatal("validation must fail")
				}
				if !tc.bad && err != nil {
					t.Fatalf("validation failed: %v", err)
				}
			})
		}
	})

	t.Run("update avatar errors", func(t *testing.T) {
		auth := NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) {
			return &model.User{ID: 1}, nil
		}, updateAvatar: func(context.Context, int64, *int64, int64) error { return errRepoDown }}, avatarStoreStub{
			get: func(context.Context, int64) (*model.Media, error) {
				return &model.Media{MediaType: model.MediaTypeImage}, nil
			},
		})
		if _, err := auth.UpdateAvatar(ctx, int64Pointer(1)); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}

		auth = NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) {
			return nil, errRepoDown
		}}, avatarStoreStub{get: func(context.Context, int64) (*model.Media, error) {
			return &model.Media{MediaType: model.MediaTypeImage}, nil
		}})
		if _, err := auth.UpdateAvatar(ctx, int64Pointer(1)); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}

		auth = NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) {
			return &model.User{ID: 1}, nil
		}, updateAvatar: func(context.Context, int64, *int64, int64) error { return nil }}, nil)
		admin, err := auth.UpdateAvatar(ctx, nil)
		if err != nil || admin.AvatarMediaID != nil {
			t.Fatalf("UpdateAvatar(clear) = (%+v, %v)", admin, err)
		}
	})
}

func TestAdminAuthVerifyBranches(t *testing.T) {
	ctx := context.Background()
	hash, _ := bcrypt.GenerateFromPassword([]byte("Password1"), bcrypt.MinCost)

	cases := []struct {
		name string
		get  func(context.Context) (*model.User, error)
		want bool
	}{
		{name: "lookup fails", get: func(context.Context) (*model.User, error) { return nil, errRepoDown }},
		{name: "not installed", get: func(context.Context) (*model.User, error) { return nil, model.ErrUserNotFound }},
		{name: "no password", get: func(context.Context) (*model.User, error) { return &model.User{}, nil }},
		{name: "wrong", get: func(context.Context) (*model.User, error) {
			return &model.User{PasswordHash: string(hash)}, nil
		}},
		{name: "correct", get: func(context.Context) (*model.User, error) {
			return &model.User{PasswordHash: string(hash)}, nil
		}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := NewAdminAuth(&adminRepoStub{get: tc.get}, nil)
			password := "wrong"
			if tc.want {
				password = "Password1"
			}
			if got := auth.Verify(ctx, password); got != tc.want {
				t.Fatalf("Verify = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAdminAuthWritePaths(t *testing.T) {
	ctx := context.Background()

	t.Run("create admin validates input", func(t *testing.T) {
		auth := NewAdminAuth(&adminRepoStub{}, nil)
		if _, err := auth.CreateAdmin(ctx, "admin", "Admin", "short"); err == nil {
			t.Fatal("a weak password must be refused")
		}
		if _, err := auth.CreateAdmin(ctx, "a", "Admin", "Password1"); err == nil {
			t.Fatal("an invalid username must be refused")
		}
	})

	t.Run("create admin defaults and persists", func(t *testing.T) {
		var created *model.User
		auth := NewAdminAuth(&adminRepoStub{create: func(_ context.Context, user *model.User) error {
			created = user
			user.ID = 1
			return nil
		}}, nil)
		admin, err := auth.CreateAdmin(ctx, "  ", "  ", "Password1")
		if err != nil {
			t.Fatalf("CreateAdmin: %v", err)
		}
		if created == nil || created.Username != "admin" || created.Nickname != "admin" || admin.ID != 1 {
			t.Fatalf("created = %+v", created)
		}
	})

	t.Run("create admin surfaces storage errors", func(t *testing.T) {
		auth := NewAdminAuth(&adminRepoStub{create: func(context.Context, *model.User) error { return errRepoDown }}, nil)
		if _, err := auth.CreateAdmin(ctx, "admin", "Admin", "Password1"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("set password", func(t *testing.T) {
		auth := NewAdminAuth(&adminRepoStub{}, nil)
		if err := auth.SetPassword(ctx, "short"); err == nil {
			t.Fatal("a weak password must be refused")
		}
		auth = NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) { return nil, errRepoDown }}, nil)
		if err := auth.SetPassword(ctx, "Password1"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
		auth = NewAdminAuth(&adminRepoStub{
			get:         func(context.Context) (*model.User, error) { return &model.User{ID: 1}, nil },
			setPassword: func(context.Context, int64, string, int64) error { return errRepoDown },
		}, nil)
		if err := auth.SetPassword(ctx, "Password1"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
		auth = NewAdminAuth(&adminRepoStub{
			get:         func(context.Context) (*model.User, error) { return &model.User{ID: 1}, nil },
			setPassword: func(context.Context, int64, string, int64) error { return nil },
		}, nil)
		if err := auth.SetPassword(ctx, "Password1"); err != nil {
			t.Fatalf("SetPassword: %v", err)
		}
	})

	t.Run("update identity", func(t *testing.T) {
		auth := NewAdminAuth(&adminRepoStub{}, nil)
		if _, err := auth.UpdateIdentity(ctx, "", "Nick"); err == nil {
			t.Fatal("an empty login name must be refused")
		}
		if _, err := auth.UpdateIdentity(ctx, "admin", ""); err == nil {
			t.Fatal("an empty display name must be refused")
		}
		if _, err := auth.UpdateIdentity(ctx, "a", "Nick"); err == nil {
			t.Fatal("an invalid login name must be refused")
		}
		auth = NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) { return nil, errRepoDown }}, nil)
		if _, err := auth.UpdateIdentity(ctx, "admin", "Nick"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
		auth = NewAdminAuth(&adminRepoStub{
			get: func(context.Context) (*model.User, error) { return &model.User{ID: 1}, nil },
			updateIdentity: func(context.Context, int64, string, string, int64) error {
				return model.ErrUsernameTaken
			},
		}, nil)
		if _, err := auth.UpdateIdentity(ctx, "admin", "Nick"); !errors.Is(err, ErrUsernameTaken) {
			t.Fatalf("err = %v", err)
		}
		auth = NewAdminAuth(&adminRepoStub{
			get: func(context.Context) (*model.User, error) { return &model.User{ID: 1}, nil },
			updateIdentity: func(context.Context, int64, string, string, int64) error {
				return errRepoDown
			},
		}, nil)
		if _, err := auth.UpdateIdentity(ctx, "admin", "Nick"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
		auth = NewAdminAuth(&adminRepoStub{
			get:            func(context.Context) (*model.User, error) { return &model.User{ID: 1}, nil },
			updateIdentity: func(context.Context, int64, string, string, int64) error { return nil },
		}, nil)
		if _, err := auth.UpdateIdentity(ctx, "admin", "Nick"); err != nil {
			t.Fatalf("UpdateIdentity: %v", err)
		}
	})

	t.Run("change password", func(t *testing.T) {
		hash, _ := bcrypt.GenerateFromPassword([]byte("Password1"), bcrypt.MinCost)

		auth := NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) { return nil, errRepoDown }}, nil)
		if _, err := auth.ChangePassword(ctx, "Password1", "Password2"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}

		auth = NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) { return &model.User{}, nil }}, nil)
		if _, err := auth.ChangePassword(ctx, "Password1", "Password2"); err == nil {
			t.Fatal("an account without a password must be refused")
		}

		auth = NewAdminAuth(&adminRepoStub{get: func(context.Context) (*model.User, error) {
			return &model.User{PasswordHash: string(hash)}, nil
		}}, nil)
		if _, err := auth.ChangePassword(ctx, "wrong", "Password2"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v", err)
		}
		if _, err := auth.ChangePassword(ctx, "Password1", "short"); err == nil {
			t.Fatal("a weak new password must be refused")
		}

		auth = NewAdminAuth(&adminRepoStub{
			get:         func(context.Context) (*model.User, error) { return &model.User{ID: 1, PasswordHash: string(hash)}, nil },
			setPassword: func(context.Context, int64, string, int64) error { return errRepoDown },
		}, nil)
		if _, err := auth.ChangePassword(ctx, "Password1", "Password2"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}

		auth = NewAdminAuth(&adminRepoStub{
			get:         func(context.Context) (*model.User, error) { return &model.User{ID: 1, PasswordHash: string(hash)}, nil },
			setPassword: func(context.Context, int64, string, int64) error { return nil },
		}, nil)
		admin, err := auth.ChangePassword(ctx, "Password1", "Password2")
		if err != nil || admin.ID != 1 {
			t.Fatalf("ChangePassword = (%+v, %v)", admin, err)
		}
	})
}

func int64Pointer(value int64) *int64 { return &value }
