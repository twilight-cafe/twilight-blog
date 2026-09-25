package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/database"
	"blog-engine/internal/session"
)

// TestAuthenticateSwallowsUnexpectedLookupErrors covers the branch where a
// broken database must not reject an otherwise valid request. Authentication is
// best-effort here; route middleware decides whether a session is required.
func TestAuthenticateSwallowsUnexpectedLookupErrors(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	store := session.NewStore(db)
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	e := echo.New()
	called := false
	e.Use(Authenticate(store))
	e.GET("/", func(c echo.Context) error {
		called = true
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: session.SessionCookieName, Value: "any-token"})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if !called {
		t.Fatal("an unexpected lookup error must not block the request")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}
