package session

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func TestRandomTokenFailuresPropagate(t *testing.T) {
	original := randRead
	t.Cleanup(func() { randRead = original })

	t.Run("first token", func(t *testing.T) {
		calls := 0
		randRead = func([]byte) (int, error) {
			calls++
			return 0, errors.New("entropy unavailable")
		}
		store, _ := newTestStore(t)
		if _, _, _, err := store.Create(context.Background(), RoleUser, nil, UserTTL); err == nil {
			t.Fatal("Create must fail when the first random token cannot be generated")
		}
		if calls != 1 {
			t.Fatalf("randRead calls = %d, want 1", calls)
		}
	})

	t.Run("second token", func(t *testing.T) {
		calls := 0
		randRead = func(b []byte) (int, error) {
			calls++
			if calls == 1 {
				for i := range b {
					b[i] = byte(i)
				}
				return len(b), nil
			}
			return 0, errors.New("entropy unavailable")
		}
		store, _ := newTestStore(t)
		if _, _, _, err := store.Create(context.Background(), RoleUser, nil, UserTTL); err == nil {
			t.Fatal("Create must fail when the second random token cannot be generated")
		}
		if calls != 2 {
			t.Fatalf("randRead calls = %d, want 2", calls)
		}
	})
}

func TestStoreMethodsReportClosedDatabaseErrors(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	if _, _, _, err := store.Create(ctx, RoleUser, nil, UserTTL); err == nil {
		t.Fatal("Create must report a closed database")
	}
	if _, err := store.Lookup(ctx, "token"); err == nil || errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrSessionExpired) {
		t.Fatalf("Lookup closed database error = %v, want an unexpected store error", err)
	}
	if store.VerifyCSRF(ctx, &Session{TokenHash: "hash"}, "csrf") {
		t.Fatal("VerifyCSRF must return false when the database is unavailable")
	}
	if err := store.Revoke(ctx, "token"); err == nil {
		t.Fatal("Revoke must report a closed database")
	}
	if err := store.RevokeUserSessions(ctx, 1); err == nil {
		t.Fatal("RevokeUserSessions must report a closed database")
	}
	if err := store.RevokeAll(ctx); err == nil {
		t.Fatal("RevokeAll must report a closed database")
	}
	if _, err := store.RevokeLegacySessions(ctx); err == nil {
		t.Fatal("RevokeLegacySessions must report a closed database")
	}
}

func TestLookupWarnsWhenExpiredDeleteFails(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()

	userID := createTestUser(t, db, "expired-delete@example.com")
	userIDCopy := userID
	rawToken := "expired-delete-token"
	if _, err := db.Exec(`
INSERT INTO sessions (token_hash, role, user_id, csrf_hash, expires_at, created_at)
VALUES (?, 'user', ?, ?, ?, ?)
`, HashToken(rawToken), userIDCopy, HashToken("csrf"), time.Now().Add(-time.Hour).Unix(), time.Now().Add(-2*time.Hour).Unix()); err != nil {
		t.Fatalf("insert expired session: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER block_session_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(FAIL, 'blocked'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, err := store.Lookup(ctx, rawToken); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("Lookup expired error = %v, want ErrSessionExpired", err)
	}
}

func TestLookupWarnsWhenMismatchedDeleteFails(t *testing.T) {
	store, db := newTestStore(t)
	ctx := context.Background()

	userID := createTestUser(t, db, "mismatch-delete@example.com")
	userIDCopy := userID
	rawToken := "mismatch-delete-token"
	if _, err := db.Exec(`
INSERT INTO sessions (token_hash, role, user_id, csrf_hash, expires_at, created_at)
VALUES (?, 'admin', ?, ?, ?, ?)
`, HashToken(rawToken), userIDCopy, HashToken("csrf"), time.Now().Add(time.Hour).Unix(), time.Now().Unix()); err != nil {
		t.Fatalf("insert mismatched session: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER block_session_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(FAIL, 'blocked'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, err := store.Lookup(ctx, rawToken); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Lookup mismatched error = %v, want ErrSessionNotFound", err)
	}
}

func TestVerifyCSRFSkipsLoggingForMissingSession(t *testing.T) {
	store, _ := newTestStore(t)
	if store.VerifyCSRF(context.Background(), &Session{TokenHash: "does-not-exist"}, "any") {
		t.Fatal("VerifyCSRF must fail for a missing session row")
	}
}

func TestRevokeEmptyTokenIsNoop(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Revoke(context.Background(), "   "); err != nil {
		t.Fatalf("Revoke(empty) error = %v, want nil", err)
	}
}

func TestRoleMatchesAccountRejectsUnknownRole(t *testing.T) {
	if roleMatchesAccount(Role("operator"), sql.NullInt64{Int64: 1, Valid: true}, 0) {
		t.Fatal("an unknown role must never match an account")
	}
}

func TestUserIDWithoutSessionIsNil(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	if got := UserID(c); got != nil {
		t.Fatalf("UserID without session = %v, want nil", got)
	}
}

func TestSessionCookieAndContextHelpers(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c := e.NewContext(req, rec)

	if got := TokenFromRequest(c); got != "" {
		t.Fatalf("TokenFromRequest without cookie = %q, want empty", got)
	}
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "  raw-token  "})
	if got := TokenFromRequest(c); got != "raw-token" {
		t.Fatalf("TokenFromRequest = %q, want raw-token", got)
	}
	req.Header.Set(CSRFHeaderName, "  csrf-token  ")
	if got := CSRFFromRequest(c); got != "csrf-token" {
		t.Fatalf("CSRFFromRequest = %q, want csrf-token", got)
	}
	if _, ok := From(c); ok {
		t.Fatal("From must report no session before Set")
	}
	if got := UserID(c); got != nil {
		t.Fatalf("UserID without session = %v, want nil", got)
	}
	if IsAdmin(c) {
		t.Fatal("IsAdmin without session must be false")
	}

	id := int64(42)
	sess := &Session{TokenHash: "hash", Role: RoleUser, UserID: &id}
	Set(c, sess)
	got, ok := From(c)
	if !ok || got != sess {
		t.Fatalf("From after Set = (%v, %v), want the stored session", got, ok)
	}
	if got := UserID(c); got == nil || *got != id {
		t.Fatalf("UserID after Set = %v, want %d", got, id)
	}
	if IsAdmin(c) {
		t.Fatal("a normal user session must not be reported as admin")
	}

	admin := &Session{Role: RoleAdmin}
	Set(c, admin)
	if !IsAdmin(c) {
		t.Fatal("an admin session must be reported as admin")
	}
}

func TestRevokeAllSucceedsOnOpenDatabase(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.RevokeAll(context.Background()); err != nil {
		t.Fatalf("RevokeAll() error = %v, want nil", err)
	}
}
