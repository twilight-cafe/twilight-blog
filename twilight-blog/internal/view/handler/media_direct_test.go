package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/database"
	"blog-engine/internal/mediaconv"
	"blog-engine/internal/model"
	"blog-engine/internal/service"
)

// TestMediaGetReportsAnUnopenableFile covers the last storage branch of the
// public media endpoint: the record and its MIME are fine, but the path cannot
// be opened for a reason other than "not there". That is a corrupted library
// or a broken filesystem, so the answer is 500 rather than a quiet 404.
//
// The storage name uses a character Windows refuses in a path, which is the
// portable way to produce a non-ENOENT open error without touching chmod or
// permissions.
func TestMediaGetReportsAnUnopenableFile(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "media-direct.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := model.NewSQLiteMediaRepository(db)
	media := &model.Media{
		StorageName:  "bad?name.png",
		OriginalName: "bad.png",
		MediaType:    model.MediaTypeImage,
		MimeType:     "image/png",
		Size:         3,
		CreatedAt:    1,
	}
	if err := repo.Create(context.Background(), media); err != nil {
		t.Fatalf("seed media: %v", err)
	}

	h := NewMediaHandler(service.NewMediaService(repo, t.TempDir(), &mediaconv.Converter{}))
	req := httptest.NewRequest(http.MethodGet, "/api/media/"+strconv.FormatInt(media.ID, 10), nil)
	c := echo.New().NewContext(req, httptest.NewRecorder())
	c.SetParamNames("id")
	c.SetParamValues(strconv.FormatInt(media.ID, 10))

	expectHTTPStatus(t, h.Get(c), http.StatusInternalServerError)
}
