package view

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
)

func TestParseIndexTemplateReportsMissingEmbeddedTemplate(t *testing.T) {
	original := indexTemplateFS
	indexTemplateFS = embed.FS{}
	t.Cleanup(func() { indexTemplateFS = original })

	if _, err := parseIndexTemplate(); err == nil {
		t.Fatal("parseIndexTemplate must report a missing embedded template")
	}
}

func TestRenderIndexReportsTemplateExecutionError(t *testing.T) {
	tmpl := template.Must(template.New("bad").Funcs(template.FuncMap{
		"fail": func() (string, error) { return "", errors.New("render failed") },
	}).Parse(`{{fail}}`))

	if _, err := renderIndex(tmpl, indexData{}); err == nil {
		t.Fatal("renderIndex must report a template execution error")
	}
}

func TestResolveIndexAssetsBranches(t *testing.T) {
	t.Run("missing manifest", func(t *testing.T) {
		if _, _, err := resolveIndexAssets(fstest.MapFS{}); err == nil {
			t.Fatal("resolveIndexAssets must report a missing manifest")
		}
	})

	t.Run("invalid manifest json", func(t *testing.T) {
		fsys := fstest.MapFS{".vite/manifest.json": &fstest.MapFile{Data: []byte("{")}}
		if _, _, err := resolveIndexAssets(fsys); err == nil {
			t.Fatal("resolveIndexAssets must report invalid JSON")
		}
	})

	t.Run("empty entry file", func(t *testing.T) {
		body, _ := json.Marshal(map[string]viteManifestEntry{
			"a.js": {File: "", IsEntry: true},
		})
		fsys := fstest.MapFS{".vite/manifest.json": &fstest.MapFile{Data: body}}
		if _, _, err := resolveIndexAssets(fsys); err == nil {
			t.Fatal("resolveIndexAssets must reject an entry with an empty file")
		}
	})

	t.Run("empty css is skipped", func(t *testing.T) {
		body, _ := json.Marshal(map[string]viteManifestEntry{
			"a.js": {File: "assets/a.js", IsEntry: true, CSS: []string{"", "assets/a.css"}},
		})
		fsys := fstest.MapFS{".vite/manifest.json": &fstest.MapFile{Data: body}}
		styles, scripts, err := resolveIndexAssets(fsys)
		if err != nil {
			t.Fatalf("resolveIndexAssets error = %v", err)
		}
		if len(styles) != 1 || styles[0] != "/assets/a.css" || len(scripts) != 1 || scripts[0] != "/assets/a.js" {
			t.Fatalf("resolveIndexAssets = (%v, %v)", styles, scripts)
		}
	})

	t.Run("no entry", func(t *testing.T) {
		body, _ := json.Marshal(map[string]viteManifestEntry{
			"chunk.js": {File: "assets/chunk.js", IsEntry: false},
		})
		fsys := fstest.MapFS{".vite/manifest.json": &fstest.MapFile{Data: body}}
		if _, _, err := resolveIndexAssets(fsys); err == nil {
			t.Fatal("resolveIndexAssets must reject a manifest without an entry chunk")
		}
	})
}

func TestNewIndexDataFallsBackForInvalidValues(t *testing.T) {
	data := newIndexData("", false, 0, 9999, nil, nil)
	if data.HomePageSize == 0 {
		t.Fatal("newIndexData must replace a non-positive home page size")
	}
	if data.BorderRadius == 9999 {
		t.Fatal("newIndexData must replace an out-of-range border radius")
	}
}

func TestFileExistsAndHiddenSegmentHelpers(t *testing.T) {
	fsys := fstest.MapFS{
		"assets/app.js": &fstest.MapFile{Data: []byte("x")},
		"dir":           &fstest.MapFile{Mode: fs.ModeDir},
	}
	if !fileExists(fsys, "assets/app.js") {
		t.Fatal("fileExists must report an existing regular file")
	}
	if fileExists(fsys, "dir") {
		t.Fatal("fileExists must not report a directory")
	}
	if fileExists(fsys, "missing") {
		t.Fatal("fileExists must not report a missing file")
	}

	if hasHiddenSegment("/") || hasHiddenSegment("/assets/app.js") || hasHiddenSegment("/.") {
		t.Fatal("hasHiddenSegment must allow ordinary paths")
	}
	if !hasHiddenSegment("/.vite/manifest.json") || !hasHiddenSegment("/assets/.env") {
		t.Fatal("hasHiddenSegment must reject hidden path segments")
	}
}

func TestRegisterWebReportsSubResolveAndParseErrors(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		original := subFS
		subFS = func(fs.FS, string) (fs.FS, error) { return nil, errors.New("sub failed") }
		t.Cleanup(func() { subFS = original })

		if err := registerWeb(echo.New(), &config.Config{}); err == nil {
			t.Fatal("registerWeb must report an embedded FS Sub error")
		}
	})

	t.Run("resolve", func(t *testing.T) {
		original := resolveIndexAssetsFn
		resolveIndexAssetsFn = func(fs.FS) ([]string, []string, error) {
			return nil, nil, errors.New("resolve failed")
		}
		t.Cleanup(func() { resolveIndexAssetsFn = original })

		if err := registerWeb(echo.New(), &config.Config{}); err == nil {
			t.Fatal("registerWeb must report an asset resolution error")
		}
	})

	t.Run("parse", func(t *testing.T) {
		original := parseIndexTemplateFn
		parseIndexTemplateFn = func() (*template.Template, error) { return nil, errors.New("parse failed") }
		t.Cleanup(func() { parseIndexTemplateFn = original })

		if err := registerWeb(echo.New(), &config.Config{}); err == nil {
			t.Fatal("registerWeb must report a template parse error")
		}
	})
}

func TestRegisterWebServesAssetsAndHiddenPaths(t *testing.T) {
	e := echo.New()
	e.HideBanner = true
	if err := registerWeb(e, &config.Config{}); err != nil {
		t.Fatalf("registerWeb() error = %v", err)
	}

	// Hidden path segment -> 404 rather than serving .vite/manifest.json.
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.vite/manifest.json", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("hidden path status = %d, want 404", rec.Code)
	}

	// API path must not fall back to the SPA shell.
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown API path status = %d, want 404", rec.Code)
	}

	// A real embedded asset must be served by the file branch.
	webFS, err := fs.Sub(webAssets, "web")
	if err != nil {
		t.Fatalf("sub embedded web assets: %v", err)
	}
	raw, err := fs.ReadFile(webFS, viteManifestPath)
	if err != nil {
		t.Fatalf("read embedded manifest: %v", err)
	}
	var manifest map[string]viteManifestEntry
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}
	var file string
	for _, entry := range manifest {
		if entry.IsEntry {
			file = entry.File
			break
		}
	}
	if file == "" {
		t.Fatal("embedded manifest has no entry file")
	}

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+strings.TrimLeft(file, "/"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("static asset status = %d, want 200", rec.Code)
	}
}

func TestRegisterWebRouteRendersIndex(t *testing.T) {
	e := echo.New()
	e.HideBanner = true
	if err := registerWeb(e, &config.Config{}); err != nil {
		t.Fatalf("registerWeb() error = %v", err)
	}
	for _, target := range []string{"/", "/some/spa/route"} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("index status for %s = %d, want 200", target, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<html") {
			t.Fatalf("index body for %s does not look like HTML: %q", target, rec.Body.String()[:min(80, rec.Body.Len())])
		}
	}
}

func TestRegisterWebReportsTemplateRenderError(t *testing.T) {
	original := parseIndexTemplateFn
	parseIndexTemplateFn = func() (*template.Template, error) {
		return template.Must(template.New("bad").Funcs(template.FuncMap{
			"fail": func() (string, error) { return "", errors.New("render failed") },
		}).Parse(`{{fail}}`)), nil
	}
	t.Cleanup(func() { parseIndexTemplateFn = original })

	e := echo.New()
	e.HideBanner = true
	if err := registerWeb(e, &config.Config{}); err != nil {
		t.Fatalf("registerWeb() error = %v", err)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("template render failure status = %d, want non-200", rec.Code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type statErrFS struct{}

func (statErrFS) Open(string) (fs.File, error) { return statErrFile{}, nil }

type statErrFile struct{}

func (statErrFile) Stat() (fs.FileInfo, error) { return nil, errors.New("stat failed") }
func (statErrFile) Read([]byte) (int, error)   { return 0, errors.New("read failed") }
func (statErrFile) Close() error               { return nil }

func TestFileExistsReportsStatErrors(t *testing.T) {
	if fileExists(statErrFS{}, "x") {
		t.Fatal("fileExists must treat a Stat error as a missing file")
	}
}
