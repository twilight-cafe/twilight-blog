package viewmodel

import (
	"strings"
	"testing"
)

// TestAdminProfileRequestValidatesTheLoginNameShape is the regression test for the
// administrator login name being the one identity field with no server-side shape
// check: it accepted anything non-empty, so a direct API call could store a name
// with a zero-width space or a Cyrillic look-alike while the sign-in form's copy
// of the rule was bypassed. The visitor flow has always rejected those.
func TestAdminProfileRequestValidatesTheLoginNameShape(t *testing.T) {
	accepted := []string{"twilight", "admin", "a1_b-2", "  twilight  "}
	for _, username := range accepted {
		request := AdminProfileRequest{Name: "Twilight", Username: &username}
		if err := request.Validate(); err != nil {
			t.Fatalf("Validate() rejected %q: %v", username, err)
		}
		if *request.Username != strings.TrimSpace(username) {
			t.Fatalf("Validate() stored %q for input %q, want it normalised", *request.Username, username)
		}
	}

	rejected := []string{
		"",
		"   ",
		"ab",
		strings.Repeat("a", 25),
		"1abc",
		"_abc",
		"abc def",
		"аdmin",       // Cyrillic а
		"admin\u200b", // zero-width space
	}
	for _, username := range rejected {
		request := AdminProfileRequest{Name: "Twilight", Username: &username}
		if err := request.Validate(); err == nil {
			t.Fatalf("Validate() accepted the login name %q", username)
		}
	}
}

// TestAdminProfileRequestAlwaysNeedsADisplayName pins the other required field, so
// a future change to the login-name branch cannot accidentally relax it.
func TestAdminProfileRequestAlwaysNeedsADisplayName(t *testing.T) {
	if err := (&AdminProfileRequest{Name: "   "}).Validate(); err == nil {
		t.Fatal("Validate() accepted an empty display name")
	}
	if err := (&AdminProfileRequest{Name: strings.Repeat("a", 25)}).Validate(); err == nil {
		t.Fatal("Validate() accepted an over-long display name")
	}
	// Omitting the login name entirely is the "leave it alone" signal and must
	// keep working: it is how the console changes only what the reader sees.
	if err := (&AdminProfileRequest{Name: "Twilight"}).Validate(); err != nil {
		t.Fatalf("Validate() rejected a display-name-only update: %v", err)
	}
}
