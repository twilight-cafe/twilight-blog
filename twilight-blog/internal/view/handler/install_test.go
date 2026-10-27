package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"blog-engine/internal/config"
	"blog-engine/internal/model"
	"blog-engine/internal/service"
)

// installStoreStub fills the two methods the install endpoints use and leaves
// the rest of AdminCredentialsStore unimplemented (embedding the interface
// makes an accidental call panic instead of silently returning a zero value).
type installStoreStub struct {
	service.AdminCredentialsStore
	exists func(context.Context) (bool, error)
	create func(context.Context, string, string, string) (*model.User, error)
}

func (s *installStoreStub) Exists(ctx context.Context) (bool, error) { return s.exists(ctx) }

func (s *installStoreStub) CreateAdmin(ctx context.Context, username, nickname, password string) (*model.User, error) {
	return s.create(ctx, username, nickname, password)
}

// TestInstallStatusReportsStorageFailure covers the read used by the SPA to
// decide between the wizard and the login page. A broken database must not be
// answered with "not installed" — that would invite a second installation.
func TestInstallStatusReportsStorageFailure(t *testing.T) {
	h := NewInstallHandler(&installStoreStub{
		exists: func(context.Context) (bool, error) { return false, errors.New("db down") },
	}, &config.Config{})

	c, _ := authContext(http.MethodGet, "/install_status", "", nil)
	expectHTTPStatus(t, h.Status(c), http.StatusInternalServerError)
}

// TestInstallRejectsBadInput covers the two 400 paths before any store call:
// a body that is not JSON, and a password that fails the shared strength rule.
func TestInstallRejectsBadInput(t *testing.T) {
	h := NewInstallHandler(&installStoreStub{
		exists: func(context.Context) (bool, error) {
			t.Fatal("the store must not be consulted for an invalid request")
			return false, nil
		},
	}, &config.Config{})

	c, _ := authContext(http.MethodPost, "/install", "{", nil)
	expectHTTPStatus(t, h.Install(c), http.StatusBadRequest)

	c, _ = authContext(http.MethodPost, "/install", `{"password":"short"}`, nil)
	expectHTTPStatus(t, h.Install(c), http.StatusBadRequest)
}

// TestInstallReportsExistenceFailure covers the second Exists call, which is
// separate from Status: install must know whether it is allowed to proceed.
func TestInstallReportsExistenceFailure(t *testing.T) {
	h := NewInstallHandler(&installStoreStub{
		exists: func(context.Context) (bool, error) { return false, errors.New("db down") },
	}, &config.Config{})

	c, _ := authContext(http.MethodPost, "/install", `{"password":"Password1"}`, nil)
	expectHTTPStatus(t, h.Install(c), http.StatusInternalServerError)
}

// TestInstallRefusesWhenAlreadyInstalled pins the explicit 409. It is not the
// concurrency guard (the unique index is) but the readable answer for a repeat
// request.
func TestInstallRefusesWhenAlreadyInstalled(t *testing.T) {
	h := NewInstallHandler(&installStoreStub{
		exists: func(context.Context) (bool, error) { return true, nil },
	}, &config.Config{})

	c, _ := authContext(http.MethodPost, "/install", `{"password":"Password1"}`, nil)
	expectHTTPStatus(t, h.Install(c), http.StatusConflict)
}

// TestInstallMapsCreateAdminFailures covers the three ways the insert can fail:
// another request won the race, the service disagreed about the password, or
// the database refused the write.
func TestInstallMapsCreateAdminFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"race", model.ErrAdminAlreadyExists, http.StatusConflict},
		{"validation", &service.ValidationError{Message: "weak password"}, http.StatusBadRequest},
		{"storage", errors.New("disk full"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewInstallHandler(&installStoreStub{
				exists: func(context.Context) (bool, error) { return false, nil },
				create: func(context.Context, string, string, string) (*model.User, error) {
					return nil, tc.err
				},
			}, &config.Config{})

			c, _ := authContext(http.MethodPost, "/install", `{"password":"Password1"}`, nil)
			expectHTTPStatus(t, h.Install(c), tc.want)
		})
	}
}

// TestInstallSucceeds creates the administrator and reports the site as
// installed. It also checks that the default sign-in name is used, because the
// request body deliberately carries only a password.
func TestInstallSucceeds(t *testing.T) {
	var gotUsername, gotNickname string
	h := NewInstallHandler(&installStoreStub{
		exists: func(context.Context) (bool, error) { return false, nil },
		create: func(_ context.Context, username, nickname, _ string) (*model.User, error) {
			gotUsername, gotNickname = username, nickname
			return &model.User{ID: 1, Username: username, Nickname: nickname, IsAdmin: true}, nil
		},
	}, &config.Config{})

	c, rec := authContext(http.MethodPost, "/install", `{"password":"Password1"}`, nil)
	if err := h.Install(c); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if gotUsername != config.DefaultAdminName || gotNickname != config.DefaultAdminName {
		t.Fatalf("created %q/%q, want the default %q", gotUsername, gotNickname, config.DefaultAdminName)
	}
	if !strings.Contains(rec.Body.String(), `"installed":true`) {
		t.Fatalf("response = %s", rec.Body.String())
	}
}
