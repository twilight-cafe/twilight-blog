package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"blog-engine/internal/model"
)

type fakeMailer struct {
	sendErr  error
	calls    int
	lastBody string
}

func (f *fakeMailer) Send(to, subject, body string) error {
	f.calls++
	f.lastBody = body
	return f.sendErr
}

type fakeEmailRepo struct {
	created    []model.Email
	countSince int64
	countErr   error
}

func (f *fakeEmailRepo) Create(_ context.Context, email *model.Email) error {
	f.created = append(f.created, *email)
	return nil
}

func (f *fakeEmailRepo) CountSince(_ context.Context, _ int64) (int64, error) {
	return f.countSince, f.countErr
}

func (f *fakeEmailRepo) List(_ context.Context, _ model.Pagination) ([]model.Email, int64, error) {
	return f.created, int64(len(f.created)), nil
}

func (f *fakeEmailRepo) GetByID(_ context.Context, id int64) (*model.Email, error) {
	for i := range f.created {
		if f.created[i].ID == id {
			email := f.created[i]
			return &email, nil
		}
	}
	return nil, model.ErrEmailNotFound
}

func TestEmailServiceStoresOnlySuccessfulSend(t *testing.T) {
	mailer := &fakeMailer{}
	repo := &fakeEmailRepo{}
	svc := NewEmailService(mailer, repo, "blog@example.com")

	if err := svc.Send("user@example.com", "Subject", "Body"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("stored emails = %d, want 1", len(repo.created))
	}
	got := repo.created[0]
	if got.ToEmail != "user@example.com" || got.FromEmail != "blog@example.com" || got.Subject != "Subject" || got.Body != "Body" {
		t.Fatalf("stored email = %+v", got)
	}

	mailer.sendErr = errors.New("delivery failed")
	if err := svc.Send("other@example.com", "Subject", "Body"); err == nil {
		t.Fatal("Send() error = nil, want delivery error")
	}
	if len(repo.created) != 1 {
		t.Fatalf("stored emails after failed send = %d, want 1", len(repo.created))
	}
}

func TestEmailServiceRejectsWhenDailyLimitReached(t *testing.T) {
	mailer := &fakeMailer{}
	repo := &fakeEmailRepo{countSince: DailyEmailLimit}
	svc := NewEmailService(mailer, repo, "blog@example.com")

	err := svc.Send("user@example.com", "Subject", "Body")
	if !errors.Is(err, ErrDailyEmailLimitReached) {
		t.Fatalf("Send() error = %v, want ErrDailyEmailLimitReached", err)
	}
	if mailer.calls != 0 {
		t.Fatalf("mailer.Send() calls = %d, want 0", mailer.calls)
	}
	if len(repo.created) != 0 {
		t.Fatalf("stored emails = %d, want 0", len(repo.created))
	}
}

// TestSendVerificationRedactsTheCodeFromTheLog is the regression test for the
// verification code being readable back out of the email log: the recipient must
// still receive it, but the stored record must not contain it. Anyone able to call
// GET /api/admin/emails/:id (or to read a database copy) would otherwise be able
// to reset an arbitrary account's password.
func TestSendVerificationRedactsTheCodeFromTheLog(t *testing.T) {
	mailer := &fakeMailer{}
	repo := &fakeEmailRepo{}
	svc := NewEmailService(mailer, repo, "blog@example.com")

	const code = "483920"
	body := "Your verification code is " + code
	if err := svc.SendVerification("user@example.com", "Your verification code", body, code); err != nil {
		t.Fatalf("SendVerification() error = %v", err)
	}

	if mailer.lastBody != body {
		t.Fatalf("mailed body = %q, want the code to reach the recipient", mailer.lastBody)
	}
	if len(repo.created) != 1 {
		t.Fatalf("stored emails = %d, want 1", len(repo.created))
	}
	stored := repo.created[0].Body
	if strings.Contains(stored, code) {
		t.Fatalf("stored body %q still contains the verification code", stored)
	}
	if !strings.Contains(stored, "[redacted]") {
		t.Fatalf("stored body %q is not marked as redacted", stored)
	}
}

// TestRedactSecretRefusesToStoreAnUnmatchedBody covers the fail-closed path: if the
// caller hands over a secret that is not in the body, storing the body verbatim
// would risk persisting some unknown secret. The record becomes useless for
// auditing, which is the right trade — the email log exists to prove delivery.
func TestRedactSecretRefusesToStoreAnUnmatchedBody(t *testing.T) {
	got := redactSecret("Your verification code is 111111", "999999")
	if strings.Contains(got, "111111") {
		t.Fatalf("redactSecret() = %q, want no verbatim body when the secret does not match", got)
	}

	// An empty secret means "there is nothing to redact", not "redact everything".
	if plain := redactSecret("plain body", ""); plain != "plain body" {
		t.Fatalf("redactSecret(body, \"\") = %q, want the body unchanged", plain)
	}
}
