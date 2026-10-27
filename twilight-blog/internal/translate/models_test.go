package translate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResolveModelsEndpoint(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/v1":                      "https://api.example.com/v1/models",
		"https://api.example.com/v1/":                     "https://api.example.com/v1/models",
		"https://api.example.com/v1/chat/completions":     "https://api.example.com/v1/models",
		"https://api.example.com/v1/models":               "https://api.example.com/v1/models",
		"https://api.example.com/v1/chat/completions?a=b": "https://api.example.com/v1/models?a=b",
	}
	for input, want := range cases {
		got, err := resolveModelsEndpoint(input)
		if err != nil {
			t.Fatalf("resolveModelsEndpoint(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("resolveModelsEndpoint(%q) = %q, want %q", input, got, want)
		}
	}

	for _, input := range []string{"ftp://x", "https://", "://bad"} {
		if _, err := resolveModelsEndpoint(input); err == nil {
			t.Fatalf("resolveModelsEndpoint(%q) must fail", input)
		}
	}
}

func TestListModelsAcceptsCommonShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{name: "data objects", body: `{"data":[{"id":"gpt-4o"},{"id":"gpt-4o"},{"id":" "},{"name":"local"}]}`, want: []string{"gpt-4o", "local"}},
		{name: "models strings", body: `{"models":["a","b"]}`, want: []string{"a", "b"}},
		{name: "top level array", body: `["x","y"]`, want: []string{"x", "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var auth string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" {
					t.Errorf("path = %s", r.URL.Path)
				}
				auth = r.Header.Get("Authorization")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			models, err := ListModels(context.Background(), Config{
				URL: server.URL + "/v1", APIKey: "k", Timeout: time.Second,
			})
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			if strings.Join(models, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("models = %v, want %v", models, tc.want)
			}
			if auth != "Bearer k" {
				t.Fatalf("authorization = %q", auth)
			}
		})
	}
}

// TestListModelsWithoutKey 覆盖本地服务的常见形态：不需要密钥。
func TestListModelsWithoutKey(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"data":[{"id":"llama"}]}`)
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), Config{URL: server.URL})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0] != "llama" {
		t.Fatalf("models = %v", models)
	}
	if auth != "" {
		t.Fatalf("authorization must be absent without a key: %q", auth)
	}
}

func TestListModelsEmptyListIsValid(t *testing.T) {
	for _, body := range []string{`{"data":[]}`, `[]`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()

			models, err := ListModels(context.Background(), Config{URL: server.URL})
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			if len(models) != 0 {
				t.Fatalf("models = %v", models)
			}
		})
	}
}

// TestDecodeModelsRejectsNonArrayShapes 覆盖数组里出现非对象/非字符串的情况。
func TestDecodeModelsRejectsNonArrayShapes(t *testing.T) {
	if _, err := decodeModels([]byte(`{"data":{"id":"x"}}`)); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput", err)
	}
	if got := decodeModelList([]byte(`{"id":"x"}`)); got != nil {
		t.Fatalf("decodeModelList = %v, want nil", got)
	}
	if got := decodeModelList(nil); got != nil {
		t.Fatalf("decodeModelList(nil) = %v, want nil", got)
	}
}

func TestListModelsErrors(t *testing.T) {
	t.Run("missing url", func(t *testing.T) {
		if _, err := ListModels(context.Background(), Config{}); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("err = %v, want ErrNotConfigured", err)
		}
	})

	t.Run("invalid url", func(t *testing.T) {
		if _, err := ListModels(context.Background(), Config{URL: "ftp://x"}); err == nil {
			t.Fatal("invalid scheme must fail")
		}
	})

	t.Run("upstream status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()
		_, err := ListModels(context.Background(), Config{URL: server.URL})
		var upstream *UpstreamError
		if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusUnauthorized {
			t.Fatalf("err = %v, want 401 UpstreamError", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		override(t, &newHTTPClient, func(time.Duration) *http.Client {
			return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, context.DeadlineExceeded
			})}
		})
		if _, err := ListModels(context.Background(), Config{URL: "https://x"}); !errors.Is(err, ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
	})

	t.Run("network", func(t *testing.T) {
		override(t, &newHTTPClient, func(time.Duration) *http.Client {
			return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial failed")
			})}
		})
		if _, err := ListModels(context.Background(), Config{URL: "https://x"}); err == nil {
			t.Fatal("network failure must surface")
		}
	})

	t.Run("read", func(t *testing.T) {
		override(t, &newHTTPClient, func(time.Duration) *http.Client {
			return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: failingReader{}}, nil
			})}
		})
		if _, err := ListModels(context.Background(), Config{URL: "https://x"}); err == nil {
			t.Fatal("read failure must surface")
		}
	})

	t.Run("unexpected body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"error":"nope"}`)
		}))
		defer server.Close()
		if _, err := ListModels(context.Background(), Config{URL: server.URL}); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("err = %v, want ErrInvalidOutput", err)
		}
	})

	t.Run("request construction", func(t *testing.T) {
		override(t, &newRequest, func(context.Context, string, string, io.Reader) (*http.Request, error) {
			return nil, errors.New("bad request")
		})
		if _, err := ListModels(context.Background(), Config{URL: "https://x"}); err == nil {
			t.Fatal("request construction failure must surface")
		}
	})
}
