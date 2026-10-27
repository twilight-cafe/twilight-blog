package handler

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"blog-engine/internal/config"
)

// The site update endpoint applies several optional fields one by one. The
// viewmodel admits only in-range values, so config setters cannot fail for a
// real request; these tests replace a setter to prove that a failure is mapped
// to 400 *and* that everything applied before it is rolled back. Without the
// rollback a refused request would still change what the public API reports
// until the next successful save.
//
// Each case first changes every earlier field successfully, so the rollback
// body for that field has something to undo.

func overrideSetter(t *testing.T, target *func(*config.Config, int) error, value func(*config.Config, int) error) {
	t.Helper()
	original := *target
	*target = value
	t.Cleanup(func() { *target = original })
}

func overrideNotifySetter(t *testing.T, value func(*config.Config, string) error) {
	t.Helper()
	original := setCommentNotifyToFn
	setCommentNotifyToFn = value
	t.Cleanup(func() { setCommentNotifyToFn = original })
}

// allFieldsBody changes every optional field at once. Values are inside the
// documented ranges so the viewmodel accepts them.
const allFieldsBody = `{"description":"新描述","description_en":"new description",` +
	`"code_block_max_lines":20,` +
	`"home_page_size":6,"border_radius":8,"post_banner_height":300,` +
	`"moments_enabled":true,"comment_notify_to":"owner@example.com"}`

func newSiteUpdateFixture(t *testing.T) (*SiteHandler, *config.Config, string) {
	t.Helper()
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	cfg.SetSiteDescription("原描述")
	cfg.SetSiteDescriptionEN("original description")
	if err := cfg.SetCodeBlockMaxLines(10); err != nil {
		t.Fatalf("seed code block lines: %v", err)
	}
	if err := cfg.SetHomePageSize(5); err != nil {
		t.Fatalf("seed home page size: %v", err)
	}
	if err := cfg.SetBorderRadius(4); err != nil {
		t.Fatalf("seed border radius: %v", err)
	}
	if err := cfg.SetPostBannerHeight(400); err != nil {
		t.Fatalf("seed banner height: %v", err)
	}
	cfg.SetMomentsEnabled(false)
	if err := cfg.SetCommentNotifyTo("old@example.com"); err != nil {
		t.Fatalf("seed notify address: %v", err)
	}
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	return NewSiteHandler(cfg, settingsPath, nil), cfg, settingsPath
}

func assertSettingsUnchanged(t *testing.T, cfg *config.Config) {
	t.Helper()
	if got := cfg.SiteDescriptionValue(); got != "原描述" {
		t.Fatalf("description = %q, want the original value after a refused update", got)
	}
	if got := cfg.SiteDescriptionENValue(); got != "original description" {
		t.Fatalf("description_en = %q, want the original value after a refused update", got)
	}
	if got := cfg.CodeBlockMaxLinesValue(); got != 10 {
		t.Fatalf("code_block_max_lines = %d", got)
	}
	if got := cfg.HomePageSizeValue(); got != 5 {
		t.Fatalf("home_page_size = %d", got)
	}
	if got := cfg.BorderRadiusValue(); got != 4 {
		t.Fatalf("border_radius = %d", got)
	}
	if got := cfg.PostBannerHeightValue(); got != 400 {
		t.Fatalf("post_banner_height = %d", got)
	}
	if cfg.MomentsEnabledValue() {
		t.Fatal("moments_enabled = true, want the original false")
	}
	if got := cfg.CommentNotifyToValue(); got != "old@example.com" {
		t.Fatalf("comment_notify_to = %q", got)
	}
}

func TestSiteUpdateRollsBackWhenASetterFails(t *testing.T) {
	failing := errors.New("setter refused the value")

	t.Run("code block lines", func(t *testing.T) {
		h, cfg, _ := newSiteUpdateFixture(t)
		overrideSetter(t, &setCodeBlockMaxLinesFn, func(*config.Config, int) error { return failing })

		c, _ := authContext(http.MethodPut, "/api/admin/site", allFieldsBody, nil)
		expectHTTPStatus(t, h.Update(c), http.StatusBadRequest)
		assertSettingsUnchanged(t, cfg)
	})

	t.Run("home page size", func(t *testing.T) {
		h, cfg, _ := newSiteUpdateFixture(t)
		overrideSetter(t, &setHomePageSizeFn, func(*config.Config, int) error { return failing })

		c, _ := authContext(http.MethodPut, "/api/admin/site", allFieldsBody, nil)
		expectHTTPStatus(t, h.Update(c), http.StatusBadRequest)
		assertSettingsUnchanged(t, cfg)
	})

	t.Run("border radius", func(t *testing.T) {
		h, cfg, _ := newSiteUpdateFixture(t)
		overrideSetter(t, &setBorderRadiusFn, func(*config.Config, int) error { return failing })

		c, _ := authContext(http.MethodPut, "/api/admin/site", allFieldsBody, nil)
		expectHTTPStatus(t, h.Update(c), http.StatusBadRequest)
		assertSettingsUnchanged(t, cfg)
	})

	t.Run("post banner height", func(t *testing.T) {
		h, cfg, _ := newSiteUpdateFixture(t)
		overrideSetter(t, &setPostBannerHeightFn, func(*config.Config, int) error { return failing })

		c, _ := authContext(http.MethodPut, "/api/admin/site", allFieldsBody, nil)
		expectHTTPStatus(t, h.Update(c), http.StatusBadRequest)
		assertSettingsUnchanged(t, cfg)
	})

	t.Run("comment notify address", func(t *testing.T) {
		h, cfg, _ := newSiteUpdateFixture(t)
		overrideNotifySetter(t, func(*config.Config, string) error { return failing })

		c, _ := authContext(http.MethodPut, "/api/admin/site", allFieldsBody, nil)
		expectHTTPStatus(t, h.Update(c), http.StatusBadRequest)
		assertSettingsUnchanged(t, cfg)
	})
}

// TestSiteUpdateRollsBackWhenSavingFails uses a directory in place of
// settings.json, so the rename in Save fails after every field has already
// been applied in memory. This is the only path that has to undo the notify
// address, because a failing notify setter never applied it.
func TestSiteUpdateRollsBackWhenSavingFails(t *testing.T) {
	h, cfg, settingsPath := newSiteUpdateFixture(t)
	if err := os.Mkdir(settingsPath, 0o755); err != nil {
		t.Fatalf("replace settings.json with a directory: %v", err)
	}

	c, _ := authContext(http.MethodPut, "/api/admin/site", allFieldsBody, nil)
	expectHTTPStatus(t, h.Update(c), http.StatusInternalServerError)
	assertSettingsUnchanged(t, cfg)
}

// TestCurrentAvatarURLWithoutASource covers the nil-source arm. It is how a
// handler built without an avatar service (older wiring or a focused test)
// answers the public site endpoint: no avatar rather than a panic.
func TestCurrentAvatarURLWithoutASource(t *testing.T) {
	h, _, _ := newSiteUpdateFixture(t)
	c, _ := authContext(http.MethodGet, "/api/site", "", nil)
	if got := h.currentAvatarURL(c); got != "" {
		t.Fatalf("avatar URL = %q, want empty for a handler without an avatar source", got)
	}
}
