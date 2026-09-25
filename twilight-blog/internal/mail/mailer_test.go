package mail

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestNewAndSendConfigurationErrors(t *testing.T) {
	m, err := New(Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := m.Send("to@example.com", "s", "b"); !errors.Is(err, ErrMailNotConfigured) {
		t.Fatalf("Send() with no from error = %v, want ErrMailNotConfigured", err)
	}

	m, err = New(Config{From: "from@example.com"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := m.Send("to@example.com", "s", "b"); !errors.Is(err, ErrResendNotConfigured) {
		t.Fatalf("Send() with no api key error = %v, want ErrResendNotConfigured", err)
	}

	// 显式空 client 也要走 fail closed，而不是 nil 解引用。
	m.client = nil
	if err := m.Send("to@example.com", "s", "b"); !errors.Is(err, ErrResendNotConfigured) {
		t.Fatalf("Send() with nil client error = %v, want ErrResendNotConfigured", err)
	}
}

func TestSendSuccessAndFailure(t *testing.T) {
	var gotFrom, gotTo, gotSubject, gotText string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/emails" {
			t.Errorf("request = %s %s, want POST /emails", r.Method, r.URL.Path)
		}
		var body struct {
			From    string   `json:"from"`
			To      []string `json:"to"`
			Subject string   `json:"subject"`
			Text    string   `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(body.To) > 0 {
			gotTo = body.To[0]
		}
		gotFrom, gotSubject, gotText = body.From, body.Subject, body.Text
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "mail-1"})
	}))
	defer server.Close()

	m, err := New(Config{From: "from@example.com", APIKey: "secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	base, _ := url.Parse(server.URL)
	m.client.BaseURL = base

	if err := m.Send("to@example.com", "hello", "body"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotFrom != "from@example.com" || gotTo != "to@example.com" || gotSubject != "hello" || gotText != "body" {
		t.Fatalf("sent payload = (%q, %q, %q, %q)", gotFrom, gotTo, gotSubject, gotText)
	}

	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer failServer.Close()
	base, _ = url.Parse(failServer.URL)
	m.client.BaseURL = base
	if err := m.Send("to@example.com", "hello", "body"); err == nil {
		t.Fatal("a failing Resend response must return an error")
	}
}

func TestRedactEmail(t *testing.T) {
	if got := redactEmail("alice@example.com"); got != "a***@example.com" {
		// The exact format belongs to logging.RedactEmail; this only proves the
		// mail package delegates instead of printing the address verbatim.
		if got == "alice@example.com" {
			t.Fatalf("redactEmail() = %q, did not redact", got)
		}
	}
}
