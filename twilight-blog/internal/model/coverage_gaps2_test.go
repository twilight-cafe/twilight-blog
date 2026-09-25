package model

import (
	"context"
	"errors"
	"testing"
)

func TestArticleRepositoryRecycleBinLifecycle(t *testing.T) {
	repo, mediaRepo, _, _ := newTestArticleRepos(t)
	ctx := context.Background()

	cover := addMedia(t, mediaRepo, "article-lifecycle-cover.png", 100)
	active := &Article{Title: "active", Content: "c", CreatedAt: 100, UpdatedAt: 100, Tags: []string{"Go"}}
	first := &Article{Title: "first", Content: "c", CreatedAt: 200, UpdatedAt: 200, CoverMediaID: &cover, Tags: []string{"Go"}}
	second := &Article{Title: "second", Content: "c", CreatedAt: 300, UpdatedAt: 300, Tags: []string{"Go"}}
	for _, article := range []*Article{active, first, second} {
		if err := repo.Create(ctx, article); err != nil {
			t.Fatalf("Create(%q): %v", article.Title, err)
		}
	}

	if _, err := repo.SoftDelete(ctx, []int64{first.ID, second.ID}, 400); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := repo.SoftDelete(ctx, []int64{first.ID}, 500); err != nil {
		t.Fatalf("SoftDelete(again): %v", err)
	}

	deleted, total, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if total != 2 || len(deleted) != 2 {
		t.Fatalf("ListDeleted = (%v, %d), want 2 rows", deleted, total)
	}
	if deleted[0].ID != second.ID || deleted[1].ID != first.ID {
		t.Fatalf("ListDeleted order = [%d %d], want [%d %d]", deleted[0].ID, deleted[1].ID, second.ID, first.ID)
	}
	if deleted[1].CoverMediaID == nil || *deleted[1].CoverMediaID != cover {
		t.Fatalf("deleted article lost its cover: %v", deleted[1].CoverMediaID)
	}
	if !equalStrings(deleted[0].Tags, []string{"Go"}) {
		t.Fatalf("deleted article tags = %v, want [Go]", deleted[0].Tags)
	}

	if err := repo.Restore(ctx, 999, 600); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Restore(missing) error = %v, want ErrNotFound", err)
	}
	if err := repo.Restore(ctx, active.ID, 600); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Restore(active) error = %v, want ErrNotFound", err)
	}
	if err := repo.Restore(ctx, first.ID, 600); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if n, err := repo.BatchRestore(ctx, nil, 700); err != nil || n != 0 {
		t.Fatalf("BatchRestore(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := repo.BatchRestore(ctx, []int64{first.ID, second.ID}, 700); err != nil || n != 1 {
		t.Fatalf("BatchRestore = (%d, %v), want (1, nil)", n, err)
	}

	if n, err := repo.HardDelete(ctx, nil); err != nil || n != 0 {
		t.Fatalf("HardDelete(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := repo.HardDelete(ctx, []int64{active.ID}); err != nil || n != 0 {
		t.Fatalf("HardDelete(active) = (%d, %v), want (0, nil)", n, err)
	}
	if _, err := repo.SoftDelete(ctx, []int64{second.ID}, 800); err != nil {
		t.Fatalf("SoftDelete before HardDelete: %v", err)
	}
	if n, err := repo.HardDelete(ctx, []int64{second.ID}); err != nil || n != 1 {
		t.Fatalf("HardDelete(deleted) = (%d, %v), want (1, nil)", n, err)
	}
	if err := repo.Restore(ctx, second.ID, 900); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Restore(hard-deleted) error = %v, want ErrNotFound", err)
	}
}

func TestEmailVerificationRepositoryLifecycle(t *testing.T) {
	db := openTestDB(t)
	repo := NewSQLiteEmailVerificationRepository(db)
	ctx := context.Background()

	first := &EmailVerificationCode{Email: "User@Example.com", CodeHash: "h1", ExpiresAt: 200, CreatedAt: 100}
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if first.ID == 0 {
		t.Fatal("Create did not backfill the id")
	}
	second := &EmailVerificationCode{Email: "user@example.com", CodeHash: "h2", ExpiresAt: 300, CreatedAt: 200}
	if err := repo.Create(ctx, second); err != nil {
		t.Fatalf("Create second: %v", err)
	}

	if got, err := repo.CountCreatedSince(ctx, "USER@example.com", 150); err != nil || got != 1 {
		t.Fatalf("CountCreatedSince = (%d, %v), want (1, nil)", got, err)
	}
	last, ok, err := repo.LastCreatedAt(ctx, "USER@example.com")
	if err != nil || !ok || last != 200 {
		t.Fatalf("LastCreatedAt = (%d, %v, %v), want (200, true, nil)", last, ok, err)
	}

	active, err := repo.FindLatestActiveByEmail(ctx, "user@example.com", 150)
	if err != nil {
		t.Fatalf("FindLatestActiveByEmail: %v", err)
	}
	if active.ID != second.ID {
		t.Fatalf("active code = %d, want newest id %d", active.ID, second.ID)
	}
	if _, err := repo.FindLatestActiveByEmail(ctx, "user@example.com", 301); !errors.Is(err, ErrVerificationCodeNotFound) {
		t.Fatalf("FindLatestActiveByEmail(expired) error = %v, want ErrVerificationCodeNotFound", err)
	}
	if _, _, err := repo.LastCreatedAt(ctx, "never@example.com"); err != nil {
		t.Fatalf("LastCreatedAt(missing): %v", err)
	}
	if last, ok, err := repo.LastCreatedAt(ctx, "never@example.com"); err != nil || ok || last != 0 {
		t.Fatalf("LastCreatedAt(missing) = (%d, %v, %v), want (0, false, nil)", last, ok, err)
	}

	if ok, err := repo.ReserveAttempt(ctx, second.ID, 0); err != nil || ok {
		t.Fatalf("ReserveAttempt(limit 0) = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := repo.ReserveAttempt(ctx, second.ID, 2); err != nil || !ok {
		t.Fatalf("ReserveAttempt first = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := repo.ReserveAttempt(ctx, second.ID, 2); err != nil || !ok {
		t.Fatalf("ReserveAttempt second = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := repo.ReserveAttempt(ctx, second.ID, 2); err != nil || ok {
		t.Fatalf("ReserveAttempt exhausted = (%v, %v), want (false, nil)", ok, err)
	}

	// InvalidateActiveForEmail 按大小写不敏感统一作废，然后新码可以重新签。
	if err := repo.InvalidateActiveForEmail(ctx, "USER@example.com", 250); err != nil {
		t.Fatalf("InvalidateActiveForEmail: %v", err)
	}
	if _, err := repo.FindLatestActiveByEmail(ctx, "user@example.com", 250); !errors.Is(err, ErrVerificationCodeNotFound) {
		t.Fatalf("FindLatestActiveByEmail after invalidate error = %v, want ErrVerificationCodeNotFound", err)
	}

	if err := repo.ClaimCode(ctx, second.ID, 260); !errors.Is(err, ErrVerificationCodeConsumed) {
		t.Fatalf("ClaimCode(invalidated) error = %v, want ErrVerificationCodeConsumed", err)
	}
	third := &EmailVerificationCode{Email: "user@example.com", CodeHash: "h3", ExpiresAt: 500, CreatedAt: 300}
	if err := repo.Create(ctx, third); err != nil {
		t.Fatalf("Create third: %v", err)
	}
	if err := repo.ClaimCode(ctx, third.ID, 310); err != nil {
		t.Fatalf("ClaimCode: %v", err)
	}
	if err := repo.ClaimCode(ctx, third.ID, 320); !errors.Is(err, ErrVerificationCodeConsumed) {
		t.Fatalf("ClaimCode(again) error = %v, want ErrVerificationCodeConsumed", err)
	}
	if _, err := repo.FindLatestActiveByEmail(ctx, "user@example.com", 320); !errors.Is(err, ErrVerificationCodeNotFound) {
		t.Fatalf("FindLatestActiveByEmail(consumed) error = %v, want ErrVerificationCodeNotFound", err)
	}
}

func TestUserRepositoryUpdates(t *testing.T) {
	db := openTestDB(t)
	repo := NewSQLiteUserRepository(db)
	ctx := context.Background()

	alice := &User{Email: "alice@example.com", Username: "alice", Nickname: "Alice", PasswordHash: "h", CreatedAt: 1, UpdatedAt: 1}
	if err := repo.Create(ctx, alice); err != nil {
		t.Fatalf("Create(alice): %v", err)
	}
	bob := &User{Email: "bob@example.com", Username: "bob", Nickname: "Bob", PasswordHash: "h", CreatedAt: 2, UpdatedAt: 2}
	if err := repo.Create(ctx, bob); err != nil {
		t.Fatalf("Create(bob): %v", err)
	}

	if err := repo.UpdateNickname(ctx, alice.ID, "Alice2", 10); err != nil {
		t.Fatalf("UpdateNickname: %v", err)
	}
	if err := repo.UpdateNickname(ctx, 999, "x", 10); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("UpdateNickname(missing) error = %v, want ErrUserNotFound", err)
	}
	if err := repo.UpdateUsername(ctx, alice.ID, "alice-new", 11); err != nil {
		t.Fatalf("UpdateUsername: %v", err)
	}
	if err := repo.UpdateUsername(ctx, alice.ID, "BOB", 12); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("UpdateUsername(duplicate) error = %v, want ErrUsernameTaken", err)
	}
	if err := repo.UpdateUsername(ctx, 999, "x", 12); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("UpdateUsername(missing) error = %v, want ErrUserNotFound", err)
	}
	if err := repo.UpdatePassword(ctx, alice.ID, "new-hash", 13); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	if err := repo.UpdatePassword(ctx, 999, "x", 13); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("UpdatePassword(missing) error = %v, want ErrUserNotFound", err)
	}

	if err := repo.UpdateProfile(ctx, alice.ID, "alice-new", "Alice New", "alice-new@example.com", 14); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if err := repo.UpdateProfile(ctx, alice.ID, "bob", "Alice New", "alice-new@example.com", 15); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("UpdateProfile(username duplicate) error = %v, want ErrUsernameTaken", err)
	}
	if err := repo.UpdateProfile(ctx, alice.ID, "alice-new", "Alice New", "bob@example.com", 16); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("UpdateProfile(email duplicate) error = %v, want ErrEmailTaken", err)
	}
	if err := repo.UpdateProfile(ctx, 999, "x", "X", "x@example.com", 17); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("UpdateProfile(missing) error = %v, want ErrUserNotFound", err)
	}

	adminRepo := NewSQLiteAdminRepository(db)
	admin := seedAdmin(t, adminRepo, "admin", "站长")
	if err := repo.UpdateProfile(ctx, admin.ID, "hacker", "Hacker", "hacker@example.com", 18); !errors.Is(err, ErrNotAVisitorAccount) {
		t.Fatalf("UpdateProfile(admin) error = %v, want ErrNotAVisitorAccount", err)
	}
}

func TestAdminRepositoryRemainingWrites(t *testing.T) {
	adminRepo, userRepo, db := newTestAdminRepo(t)
	ctx := context.Background()
	admin := seedAdmin(t, adminRepo, "admin", "站长")

	if got, err := adminRepo.Get(ctx); err != nil || got.ID != admin.ID {
		t.Fatalf("Get = (%+v, %v), want admin", got, err)
	}
	if exists, err := adminRepo.Exists(ctx); err != nil || !exists {
		t.Fatalf("Exists = (%v, %v), want true", exists, err)
	}
	if err := adminRepo.SetPassword(ctx, admin.ID, "new-hash", 20); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if err := adminRepo.SetPassword(ctx, 999, "x", 20); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("SetPassword(missing) error = %v, want ErrUserNotFound", err)
	}
	if err := adminRepo.UpdateIdentity(ctx, admin.ID, "root", "站长2", 21); err != nil {
		t.Fatalf("UpdateIdentity: %v", err)
	}
	if err := adminRepo.UpdateIdentity(ctx, 999, "root", "x", 22); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("UpdateIdentity(missing) error = %v, want ErrUserNotFound", err)
	}

	mediaID := seedMedia(t, db, "avatar.png", "avatar.png", MediaTypeImage, "image/png", 1, false, 1)
	if err := adminRepo.UpdateAvatar(ctx, admin.ID, &mediaID, 23); err != nil {
		t.Fatalf("UpdateAvatar(set): %v", err)
	}
	got, err := userRepo.GetByID(ctx, admin.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.AvatarMediaID == nil || *got.AvatarMediaID != mediaID {
		t.Fatalf("AvatarMediaID = %v, want %d", got.AvatarMediaID, mediaID)
	}
	if err := adminRepo.UpdateAvatar(ctx, admin.ID, nil, 24); err != nil {
		t.Fatalf("UpdateAvatar(clear): %v", err)
	}
	got, err = userRepo.GetByID(ctx, admin.ID)
	if err != nil {
		t.Fatalf("GetByID after clear: %v", err)
	}
	if got.AvatarMediaID != nil {
		t.Fatalf("AvatarMediaID = %v after clear, want nil", got.AvatarMediaID)
	}
	if err := adminRepo.UpdateAvatar(ctx, 999, nil, 25); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("UpdateAvatar(missing) error = %v, want ErrUserNotFound", err)
	}
	if exists, err := adminRepo.Exists(ctx); err != nil || !exists {
		t.Fatalf("Exists after writes = (%v, %v), want true", exists, err)
	}
}

func TestCommentGetByIDForUser(t *testing.T) {
	repo := newTestCommentRepo(t)
	ctx := context.Background()
	seedLiker(t, repo, 1)

	id := addComment(t, repo, 1, nil, "hello", 100)
	if err := repo.AddLike(ctx, id, 1, 150); err != nil {
		t.Fatalf("AddLike: %v", err)
	}
	for _, tc := range []struct {
		name   string
		userID *int64
		want   bool
	}{
		{name: "anonymous", userID: nil, want: false},
		{name: "liker", userID: int64Ptr(1), want: true},
		{name: "non-liker", userID: int64Ptr(2), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.GetByIDForUser(ctx, id, tc.userID)
			if err != nil {
				t.Fatalf("GetByIDForUser: %v", err)
			}
			if got.LikedByMe != tc.want {
				t.Fatalf("LikedByMe = %v, want %v", got.LikedByMe, tc.want)
			}
		})
	}
	if _, err := repo.GetByIDForUser(ctx, 999, nil); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("GetByIDForUser(missing) error = %v, want ErrCommentNotFound", err)
	}
}

func int64Ptr(value int64) *int64 { return &value }
