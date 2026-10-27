package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/model"
)

// 本文件补齐 UserService 的错误分支。正常路径由 user_service_test.go 用真实
// SQLite 覆盖；这里用「每个方法可单独失败」的替身，把「仓储在第 N 步失败时
// 服务层做什么」逐条钉住。

type userRepoStub struct {
	findUsername   func(context.Context, string) (*model.User, error)
	findEmail      func(context.Context, string) (*model.User, error)
	getByID        func(context.Context, int64) (*model.User, error)
	create         func(context.Context, *model.User) error
	updateNickname func(context.Context, int64, string, int64) error
	updateUsername func(context.Context, int64, string, int64) error
	updatePassword func(context.Context, int64, string, int64) error
	updateProfile  func(context.Context, int64, string, string, string, int64) error
	list           func(context.Context, model.Pagination) ([]model.User, int64, error)
}

func (s *userRepoStub) FindByUsername(ctx context.Context, username string) (*model.User, error) {
	if s.findUsername == nil {
		return nil, errRepoDown
	}
	return s.findUsername(ctx, username)
}

func (s *userRepoStub) FindByEmail(ctx context.Context, email string) (*model.User, error) {
	if s.findEmail == nil {
		return nil, errRepoDown
	}
	return s.findEmail(ctx, email)
}

func (s *userRepoStub) GetByID(ctx context.Context, id int64) (*model.User, error) {
	if s.getByID == nil {
		return nil, errRepoDown
	}
	return s.getByID(ctx, id)
}

func (s *userRepoStub) Create(ctx context.Context, user *model.User) error {
	if s.create == nil {
		return errRepoDown
	}
	return s.create(ctx, user)
}

func (s *userRepoStub) UpdateNickname(ctx context.Context, id int64, nickname string, at int64) error {
	if s.updateNickname == nil {
		return errRepoDown
	}
	return s.updateNickname(ctx, id, nickname, at)
}

func (s *userRepoStub) UpdateUsername(ctx context.Context, id int64, username string, at int64) error {
	if s.updateUsername == nil {
		return errRepoDown
	}
	return s.updateUsername(ctx, id, username, at)
}

func (s *userRepoStub) UpdatePassword(ctx context.Context, id int64, hash string, at int64) error {
	if s.updatePassword == nil {
		return errRepoDown
	}
	return s.updatePassword(ctx, id, hash, at)
}

func (s *userRepoStub) UpdateProfile(ctx context.Context, id int64, username, nickname, email string, at int64) error {
	if s.updateProfile == nil {
		return errRepoDown
	}
	return s.updateProfile(ctx, id, username, nickname, email, at)
}

func (s *userRepoStub) List(ctx context.Context, p model.Pagination) ([]model.User, int64, error) {
	if s.list == nil {
		return nil, 0, errRepoDown
	}
	return s.list(ctx, p)
}

type verificationRepoStub struct {
	create         func(context.Context, *model.EmailVerificationCode) error
	findLatest     func(context.Context, string, int64) (*model.EmailVerificationCode, error)
	claim          func(context.Context, int64, int64) error
	invalidate     func(context.Context, string, int64) error
	countCreated   func(context.Context, string, int64) (int64, error)
	lastCreatedAt  func(context.Context, string) (int64, bool, error)
	reserveAttempt func(context.Context, int64, int) (bool, error)
}

func (s *verificationRepoStub) Create(ctx context.Context, code *model.EmailVerificationCode) error {
	if s.create == nil {
		return errRepoDown
	}
	return s.create(ctx, code)
}

func (s *verificationRepoStub) FindLatestActiveByEmail(ctx context.Context, email string, now int64) (*model.EmailVerificationCode, error) {
	if s.findLatest == nil {
		return nil, errRepoDown
	}
	return s.findLatest(ctx, email, now)
}

func (s *verificationRepoStub) ClaimCode(ctx context.Context, id int64, consumedAt int64) error {
	if s.claim == nil {
		return errRepoDown
	}
	return s.claim(ctx, id, consumedAt)
}

func (s *verificationRepoStub) InvalidateActiveForEmail(ctx context.Context, email string, consumedAt int64) error {
	if s.invalidate == nil {
		return errRepoDown
	}
	return s.invalidate(ctx, email, consumedAt)
}

func (s *verificationRepoStub) CountCreatedSince(ctx context.Context, email string, since int64) (int64, error) {
	if s.countCreated == nil {
		return 0, errRepoDown
	}
	return s.countCreated(ctx, email, since)
}

func (s *verificationRepoStub) LastCreatedAt(ctx context.Context, email string) (int64, bool, error) {
	if s.lastCreatedAt == nil {
		return 0, false, errRepoDown
	}
	return s.lastCreatedAt(ctx, email)
}

func (s *verificationRepoStub) ReserveAttempt(ctx context.Context, id int64, limit int) (bool, error) {
	if s.reserveAttempt == nil {
		return false, errRepoDown
	}
	return s.reserveAttempt(ctx, id, limit)
}

func hashForTest(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return string(hash)
}

// TestRequestEmailCodeErrorPaths 逐项覆盖签发链路上的仓储/发信失败。
func TestRequestEmailCodeErrorPaths(t *testing.T) {
	ctx := context.Background()
	sender := &brokenSender{}

	t.Run("count fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			countCreated: func(context.Context, string, int64) (int64, error) { return 0, errRepoDown },
		}, sender)
		if err := svc.RequestEmailCode(ctx, "a@example.com"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("last created at fails (fail closed)", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			countCreated:  func(context.Context, string, int64) (int64, error) { return 0, nil },
			lastCreatedAt: func(context.Context, string) (int64, bool, error) { return 0, false, errRepoDown },
		}, sender)
		if err := svc.RequestEmailCode(ctx, "a@example.com"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("invalidate fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			countCreated:  func(context.Context, string, int64) (int64, error) { return 0, nil },
			lastCreatedAt: func(context.Context, string) (int64, bool, error) { return 0, false, nil },
			invalidate:    func(context.Context, string, int64) error { return errRepoDown },
		}, sender)
		if err := svc.RequestEmailCode(ctx, "a@example.com"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("store fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			countCreated:  func(context.Context, string, int64) (int64, error) { return 0, nil },
			lastCreatedAt: func(context.Context, string) (int64, bool, error) { return 0, false, nil },
			invalidate:    func(context.Context, string, int64) error { return nil },
			create:        func(context.Context, *model.EmailVerificationCode) error { return errRepoDown },
		}, sender)
		if err := svc.RequestEmailCode(ctx, "a@example.com"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("send fails and invalidation also fails", func(t *testing.T) {
		calls := 0
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			countCreated:  func(context.Context, string, int64) (int64, error) { return 0, nil },
			lastCreatedAt: func(context.Context, string) (int64, bool, error) { return 0, false, nil },
			invalidate: func(context.Context, string, int64) error {
				calls++
				if calls >= 2 {
					return errRepoDown
				}
				return nil
			},
			create: func(context.Context, *model.EmailVerificationCode) error { return nil },
		}, sender)
		if err := svc.RequestEmailCode(ctx, "a@example.com"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("code generation fails", func(t *testing.T) {
		original := randInt
		randInt = func(io.Reader, *big.Int) (*big.Int, error) { return nil, errors.New("no entropy") }
		t.Cleanup(func() { randInt = original })

		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			countCreated:  func(context.Context, string, int64) (int64, error) { return 0, nil },
			lastCreatedAt: func(context.Context, string) (int64, bool, error) { return 0, false, nil },
		}, sender)
		if err := svc.RequestEmailCode(ctx, "a@example.com"); err == nil {
			t.Fatal("a failing entropy source must surface")
		}
	})
}

// TestRegisterErrorPaths 覆盖注册各步骤的失败：查邮箱、用户名预检、
// 就地升级账号的三次写库、以及新建账号的写库。
func TestRegisterErrorPaths(t *testing.T) {
	ctx := context.Background()
	validCode := func() *verificationRepoStub {
		return &verificationRepoStub{
			findLatest: func(context.Context, string, int64) (*model.EmailVerificationCode, error) {
				return &model.EmailVerificationCode{ID: 1, CodeHash: hashVerificationCode("12345678")}, nil
			},
			reserveAttempt: func(context.Context, int64, int) (bool, error) { return true, nil },
			claim:          func(context.Context, int64, int64) error { return nil },
		}
	}
	legacy := &model.User{ID: 7, Email: "legacy@example.com", Nickname: "旧访客"}

	newService := func(users *userRepoStub, codes *verificationRepoStub) *UserService {
		return NewUserService(users, codes, &captureSender{})
	}

	t.Run("find by email fails", func(t *testing.T) {
		users := &userRepoStub{findEmail: func(context.Context, string) (*model.User, error) { return nil, errRepoDown }}
		_, err := newService(users, validCode()).Register(ctx, "a@example.com", "12345678", "alice", "Alice", "Password1")
		if !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("username precheck fails for a legacy account", func(t *testing.T) {
		users := &userRepoStub{
			findEmail:    func(context.Context, string) (*model.User, error) { return legacy, nil },
			findUsername: func(context.Context, string) (*model.User, error) { return nil, errRepoDown },
		}
		_, err := newService(users, validCode()).Register(ctx, "legacy@example.com", "12345678", "alice", "Alice", "Password1")
		if !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("upgrading the password fails", func(t *testing.T) {
		users := &userRepoStub{
			findEmail:      func(context.Context, string) (*model.User, error) { return legacy, nil },
			findUsername:   func(context.Context, string) (*model.User, error) { return nil, model.ErrUserNotFound },
			updatePassword: func(context.Context, int64, string, int64) error { return errRepoDown },
		}
		_, err := newService(users, validCode()).Register(ctx, "legacy@example.com", "12345678", "alice", "Alice", "Password1")
		if !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("upgrading the username maps a conflict", func(t *testing.T) {
		users := &userRepoStub{
			findEmail:      func(context.Context, string) (*model.User, error) { return legacy, nil },
			findUsername:   func(context.Context, string) (*model.User, error) { return nil, model.ErrUserNotFound },
			updatePassword: func(context.Context, int64, string, int64) error { return nil },
			updateUsername: func(context.Context, int64, string, int64) error {
				return fmt.Errorf("%w", model.ErrUsernameTaken)
			},
		}
		_, err := newService(users, validCode()).Register(ctx, "legacy@example.com", "12345678", "alice", "Alice", "Password1")
		if !errors.Is(err, ErrUsernameTaken) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("upgrading the username fails generically", func(t *testing.T) {
		users := &userRepoStub{
			findEmail:      func(context.Context, string) (*model.User, error) { return legacy, nil },
			findUsername:   func(context.Context, string) (*model.User, error) { return nil, model.ErrUserNotFound },
			updatePassword: func(context.Context, int64, string, int64) error { return nil },
			updateUsername: func(context.Context, int64, string, int64) error { return errRepoDown },
		}
		_, err := newService(users, validCode()).Register(ctx, "legacy@example.com", "12345678", "alice", "Alice", "Password1")
		if !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("upgrading the nickname fails", func(t *testing.T) {
		users := &userRepoStub{
			findEmail:      func(context.Context, string) (*model.User, error) { return legacy, nil },
			findUsername:   func(context.Context, string) (*model.User, error) { return nil, model.ErrUserNotFound },
			updatePassword: func(context.Context, int64, string, int64) error { return nil },
			updateUsername: func(context.Context, int64, string, int64) error { return nil },
			updateNickname: func(context.Context, int64, string, int64) error { return errRepoDown },
		}
		_, err := newService(users, validCode()).Register(ctx, "legacy@example.com", "12345678", "alice", "Alice", "Password1")
		if !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("creating a new account maps a conflict", func(t *testing.T) {
		users := &userRepoStub{
			findEmail:    func(context.Context, string) (*model.User, error) { return nil, model.ErrUserNotFound },
			findUsername: func(context.Context, string) (*model.User, error) { return nil, model.ErrUserNotFound },
			create:       func(context.Context, *model.User) error { return fmt.Errorf("%w", model.ErrUsernameTaken) },
		}
		_, err := newService(users, validCode()).Register(ctx, "new@example.com", "12345678", "alice", "Alice", "Password1")
		if !errors.Is(err, ErrUsernameTaken) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("creating a new account fails generically", func(t *testing.T) {
		users := &userRepoStub{
			findEmail:    func(context.Context, string) (*model.User, error) { return nil, model.ErrUserNotFound },
			findUsername: func(context.Context, string) (*model.User, error) { return nil, model.ErrUserNotFound },
			create:       func(context.Context, *model.User) error { return errRepoDown },
		}
		_, err := newService(users, validCode()).Register(ctx, "new@example.com", "12345678", "alice", "Alice", "Password1")
		if !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestLoginAndResetErrorPaths 覆盖登录与重置密码里剩余的仓储失败分支。
func TestLoginAndResetErrorPaths(t *testing.T) {
	ctx := context.Background()
	user := &model.User{ID: 3, Username: "alice", Email: "a@example.com", PasswordHash: hashForTest(t, "Password1")}

	t.Run("login lookup fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			findUsername: func(context.Context, string) (*model.User, error) { return nil, errRepoDown },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.LoginWithUsername(ctx, "alice", "Password1"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("email fallback fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			findUsername: func(context.Context, string) (*model.User, error) { return nil, model.ErrUserNotFound },
			findEmail:    func(context.Context, string) (*model.User, error) { return nil, errRepoDown },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.LoginWithUsername(ctx, "a@example.com", "Password1"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("account without a password", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			findUsername: func(context.Context, string) (*model.User, error) {
				return &model.User{ID: 4, Username: "legacy"}, nil
			},
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.LoginWithUsername(ctx, "legacy", "Password1"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			findUsername: func(context.Context, string) (*model.User, error) { return user, nil },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.LoginWithUsername(ctx, "alice", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v", err)
		}
	})

	validCode := func() *verificationRepoStub {
		return &verificationRepoStub{
			findLatest: func(context.Context, string, int64) (*model.EmailVerificationCode, error) {
				return &model.EmailVerificationCode{ID: 1, CodeHash: hashVerificationCode("12345678")}, nil
			},
			reserveAttempt: func(context.Context, int64, int) (bool, error) { return true, nil },
			claim:          func(context.Context, int64, int64) error { return nil },
		}
	}

	t.Run("reset: account lookup fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			findEmail: func(context.Context, string) (*model.User, error) { return nil, errRepoDown },
		}, validCode(), &captureSender{})
		if _, err := svc.ResetPassword(ctx, "a@example.com", "12345678", "Password1"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("reset: administrator is refused", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			findEmail: func(context.Context, string) (*model.User, error) {
				return &model.User{ID: 1, IsAdmin: true}, nil
			},
			getByID: func(context.Context, int64) (*model.User, error) {
				return &model.User{ID: 1, IsAdmin: true}, nil
			},
		}, validCode(), &captureSender{})
		if _, err := svc.ResetPassword(ctx, "a@example.com", "12345678", "Password1"); !errors.Is(err, ErrAccountNotFound) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("reset: update password fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			findEmail: func(context.Context, string) (*model.User, error) {
				return &model.User{ID: 2, Email: "a@example.com"}, nil
			},
			updatePassword: func(context.Context, int64, string, int64) error { return errRepoDown },
		}, validCode(), &captureSender{})
		if _, err := svc.ResetPassword(ctx, "a@example.com", "12345678", "Password1"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestPasswordAndProfileErrorPaths 覆盖改密、自助资料与管理员改资料。
func TestPasswordAndProfileErrorPaths(t *testing.T) {
	ctx := context.Background()
	hashed := hashForTest(t, "Password1")

	t.Run("change password: lookup fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			getByID: func(context.Context, int64) (*model.User, error) { return nil, errRepoDown },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.ChangePassword(ctx, 1, "Password1", "Password2"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("change password: account has none", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			getByID: func(context.Context, int64) (*model.User, error) { return &model.User{ID: 1}, nil },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.ChangePassword(ctx, 1, "Password1", "Password2"); err == nil {
			t.Fatal("an account without a password must be refused")
		}
	})

	t.Run("change password: wrong current", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			getByID: func(context.Context, int64) (*model.User, error) {
				return &model.User{ID: 1, PasswordHash: hashed}, nil
			},
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.ChangePassword(ctx, 1, "wrong", "Password2"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("change password: update fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			getByID: func(context.Context, int64) (*model.User, error) {
				return &model.User{ID: 1, PasswordHash: hashed}, nil
			},
			updatePassword: func(context.Context, int64, string, int64) error { return errRepoDown },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.ChangePassword(ctx, 1, "Password1", "Password2"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("nickname: write fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			updateNickname: func(context.Context, int64, string, int64) error { return errRepoDown },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.UpdateOwnNickname(ctx, 1, "Alice"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("nickname: re-read fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			updateNickname: func(context.Context, int64, string, int64) error { return nil },
			getByID:        func(context.Context, int64) (*model.User, error) { return nil, errRepoDown },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.UpdateOwnNickname(ctx, 1, "Alice"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("username: precheck fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			findUsername: func(context.Context, string) (*model.User, error) { return nil, errRepoDown },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.UpdateOwnUsername(ctx, 1, "alice2"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("username: taken by someone else", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			findUsername: func(context.Context, string) (*model.User, error) {
				return &model.User{ID: 2, Username: "alice2"}, nil
			},
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.UpdateOwnUsername(ctx, 1, "alice2"); !errors.Is(err, ErrUsernameTaken) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("username: write conflict", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			findUsername:   func(context.Context, string) (*model.User, error) { return nil, model.ErrUserNotFound },
			updateUsername: func(context.Context, int64, string, int64) error { return model.ErrUsernameTaken },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.UpdateOwnUsername(ctx, 1, "alice2"); !errors.Is(err, ErrUsernameTaken) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("username: re-read fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			findUsername:   func(context.Context, string) (*model.User, error) { return nil, model.ErrUserNotFound },
			updateUsername: func(context.Context, int64, string, int64) error { return nil },
			getByID:        func(context.Context, int64) (*model.User, error) { return nil, errRepoDown },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.UpdateOwnUsername(ctx, 1, "alice2"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("admin update: username conflict", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			updateProfile: func(context.Context, int64, string, string, string, int64) error {
				return model.ErrUsernameTaken
			},
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.AdminUpdateUser(ctx, 2, "alice", "Alice", "a@example.com"); !errors.Is(err, ErrUsernameTaken) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("admin update: email conflict", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			updateProfile: func(context.Context, int64, string, string, string, int64) error {
				return model.ErrEmailTaken
			},
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.AdminUpdateUser(ctx, 2, "alice", "Alice", "a@example.com"); !errors.Is(err, ErrEmailAlreadyRegistered) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("admin update: generic failure", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			updateProfile: func(context.Context, int64, string, string, string, int64) error {
				return errRepoDown
			},
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.AdminUpdateUser(ctx, 2, "alice", "Alice", "a@example.com"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("admin update: re-read fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{
			updateProfile: func(context.Context, int64, string, string, string, int64) error { return nil },
			getByID:       func(context.Context, int64) (*model.User, error) { return nil, errRepoDown },
		}, &verificationRepoStub{}, &captureSender{})
		if _, err := svc.AdminUpdateUser(ctx, 2, "alice", "Alice", "a@example.com"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestConsumeCodeErrorPaths 覆盖验证码校验链路的每一种失败。
func TestConsumeCodeErrorPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("empty code", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{}, &captureSender{})
		if err := svc.consumeCode(ctx, "a@example.com", "  "); err == nil {
			t.Fatal("an empty code must be refused")
		}
	})

	t.Run("no active code", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			findLatest: func(context.Context, string, int64) (*model.EmailVerificationCode, error) {
				return nil, model.ErrVerificationCodeNotFound
			},
		}, &captureSender{})
		if err := svc.consumeCode(ctx, "a@example.com", "12345678"); !errors.Is(err, ErrInvalidVerificationCode) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("repository failure", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			findLatest: func(context.Context, string, int64) (*model.EmailVerificationCode, error) {
				return nil, errRepoDown
			},
		}, &captureSender{})
		if err := svc.consumeCode(ctx, "a@example.com", "12345678"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("reserve fails", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			findLatest: func(context.Context, string, int64) (*model.EmailVerificationCode, error) {
				return &model.EmailVerificationCode{ID: 1}, nil
			},
			reserveAttempt: func(context.Context, int64, int) (bool, error) { return false, errRepoDown },
		}, &captureSender{})
		if err := svc.consumeCode(ctx, "a@example.com", "12345678"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("attempts exhausted", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			findLatest: func(context.Context, string, int64) (*model.EmailVerificationCode, error) {
				return &model.EmailVerificationCode{ID: 1}, nil
			},
			reserveAttempt: func(context.Context, int64, int) (bool, error) { return false, nil },
		}, &captureSender{})
		if err := svc.consumeCode(ctx, "a@example.com", "12345678"); !errors.Is(err, ErrInvalidVerificationCode) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("wrong code", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			findLatest: func(context.Context, string, int64) (*model.EmailVerificationCode, error) {
				return &model.EmailVerificationCode{ID: 1, CodeHash: hashVerificationCode("12345678")}, nil
			},
			reserveAttempt: func(context.Context, int64, int) (bool, error) { return true, nil },
		}, &captureSender{})
		if err := svc.consumeCode(ctx, "a@example.com", "00000000"); !errors.Is(err, ErrInvalidVerificationCode) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("claimed concurrently", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			findLatest: func(context.Context, string, int64) (*model.EmailVerificationCode, error) {
				return &model.EmailVerificationCode{ID: 1, CodeHash: hashVerificationCode("12345678")}, nil
			},
			reserveAttempt: func(context.Context, int64, int) (bool, error) { return true, nil },
			claim:          func(context.Context, int64, int64) error { return model.ErrVerificationCodeConsumed },
		}, &captureSender{})
		if err := svc.consumeCode(ctx, "a@example.com", "12345678"); !errors.Is(err, ErrInvalidVerificationCode) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("claim fails generically", func(t *testing.T) {
		svc := NewUserService(&userRepoStub{}, &verificationRepoStub{
			findLatest: func(context.Context, string, int64) (*model.EmailVerificationCode, error) {
				return &model.EmailVerificationCode{ID: 1, CodeHash: hashVerificationCode("12345678")}, nil
			},
			reserveAttempt: func(context.Context, int64, int) (bool, error) { return true, nil },
			claim:          func(context.Context, int64, int64) error { return errRepoDown },
		}, &captureSender{})
		if err := svc.consumeCode(ctx, "a@example.com", "12345678"); !errors.Is(err, errRepoDown) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestUserServiceValidationBranches 覆盖各入口在触达仓储之前就拒绝的输入。
func TestUserServiceValidationBranches(t *testing.T) {
	ctx := context.Background()
	svc := NewUserService(&userRepoStub{}, &verificationRepoStub{}, &captureSender{})

	t.Run("request code: invalid email", func(t *testing.T) {
		if err := svc.RequestEmailCode(ctx, "not-an-email"); err == nil {
			t.Fatal("an invalid address must be refused")
		}
	})

	t.Run("login: empty identifier or password", func(t *testing.T) {
		for _, in := range [][2]string{{"", "Password1"}, {"alice", "  "}} {
			if _, err := svc.LoginWithUsername(ctx, in[0], in[1]); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("LoginWithUsername(%q, %q) err = %v", in[0], in[1], err)
			}
		}
	})

	t.Run("reset: invalid email", func(t *testing.T) {
		if _, err := svc.ResetPassword(ctx, "", "12345678", "Password1"); err == nil {
			t.Fatal("an empty address must be refused")
		}
	})

	t.Run("reset: weak password", func(t *testing.T) {
		if _, err := svc.ResetPassword(ctx, "a@example.com", "12345678", "short"); err == nil {
			t.Fatal("a weak password must be refused")
		}
	})

	t.Run("reset: verification fails before the lookup", func(t *testing.T) {
		broken := NewUserService(&userRepoStub{}, &verificationRepoStub{
			findLatest: func(context.Context, string, int64) (*model.EmailVerificationCode, error) {
				return nil, model.ErrVerificationCodeNotFound
			},
		}, &captureSender{})
		if _, err := broken.ResetPassword(ctx, "a@example.com", "12345678", "Password1"); !errors.Is(err, ErrInvalidVerificationCode) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("username: invalid shape", func(t *testing.T) {
		if _, err := svc.UpdateOwnUsername(ctx, 1, "a"); err == nil {
			t.Fatal("an invalid username must be refused")
		}
	})

	t.Run("admin update: each field is validated", func(t *testing.T) {
		cases := [][3]string{
			{"a", "Alice", "a@example.com"},
			{"alice", "", "a@example.com"},
			{"alice", "Alice", "not-an-email"},
		}
		for _, fields := range cases {
			if _, err := svc.AdminUpdateUser(ctx, 2, fields[0], fields[1], fields[2]); err == nil {
				t.Fatalf("AdminUpdateUser(%q, %q, %q) must fail", fields[0], fields[1], fields[2])
			}
		}
	})

	t.Run("empty email normalization", func(t *testing.T) {
		if _, err := normalizeAndValidateEmail("  "); err == nil {
			t.Fatal("an empty address must be refused")
		}
	})
}

// TestValidationAndHashingHelpers 覆盖纯函数的错误/边界分支。
func TestValidationAndHashingHelpers(t *testing.T) {
	t.Run("nickname too long", func(t *testing.T) {
		_, err := normalizeAndValidateNickname(strings.Repeat("名", model.MaxCommentNicknameLength+1))
		if err == nil {
			t.Fatal("an oversized nickname must be refused")
		}
	})

	t.Run("mapUsernameError", func(t *testing.T) {
		if !errors.Is(mapUsernameError(model.ErrUsernameTaken), ErrUsernameTaken) {
			t.Fatal("a unique violation must map to ErrUsernameTaken")
		}
		if !errors.Is(mapUsernameError(errRepoDown), errRepoDown) {
			t.Fatal("unrelated errors must pass through")
		}
	})

	t.Run("hashPassword rejects oversized input", func(t *testing.T) {
		if _, err := hashPassword(strings.Repeat("x", 100)); err == nil {
			t.Fatal("bcrypt must reject input longer than 72 bytes")
		}
		if _, err := HashPasswordForStorage(strings.Repeat("x", 100)); err == nil {
			t.Fatal("the exported wrapper must surface the same error")
		}
	})

	t.Run("hashVerificationCode reports an unusable hash", func(t *testing.T) {
		if got := HashVerificationCodeForStorage(strings.Repeat("x", 100)); got != "" {
			t.Fatalf("an overlong code must hash to the empty sentinel, got %q", got)
		}
	})

	t.Run("verifyVerificationCode fails closed on empty hash", func(t *testing.T) {
		if verifyVerificationCode("", "12345678") {
			t.Fatal("an empty stored hash must never verify")
		}
	})

	t.Run("generateVerificationCode failure", func(t *testing.T) {
		original := randInt
		randInt = func(io.Reader, *big.Int) (*big.Int, error) { return nil, errors.New("no entropy") }
		t.Cleanup(func() { randInt = original })
		if _, err := generateVerificationCode(); err == nil {
			t.Fatal("a failing entropy source must surface")
		}
	})
}
