package viewmodel

import (
	"strings"
	"testing"

	"blog-engine/internal/model"
)

func TestNewEmailResponseAndList(t *testing.T) {
	source := model.Email{ID: 7, FromEmail: "f@x", ToEmail: "t@x", Subject: "s", Body: "b", SentAt: 10, CreatedAt: 11}
	got := NewEmailResponse(&source)
	if got.ID != 7 || got.FromEmail != "f@x" || got.ToEmail != "t@x" || got.Subject != "s" || got.Body != "b" || got.SentAt != 10 || got.CreatedAt != 11 {
		t.Fatalf("NewEmailResponse = %+v", got)
	}
	list := NewEmailResponseList([]model.Email{source})
	if len(list) != 1 || list[0].ID != 7 {
		t.Fatalf("NewEmailResponseList = %+v", list)
	}
	if empty := NewEmailResponseList(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("NewEmailResponseList(nil) = %#v, want empty non-nil", empty)
	}
}

func TestNewMomentResponses(t *testing.T) {
	url := "https://example.com/pic.png"
	moment := &model.Moment{
		ID:        1,
		Content:   "c",
		Media:     []model.Media{{ID: 2, StorageName: "pic.png", MimeType: "image/png"}},
		CreatedAt: 3,
		UpdatedAt: 4,
	}
	got := NewMomentResponse(moment)
	if got.ID != 1 || got.Content != "c" || len(got.MediaItems) != 1 || got.MediaItems[0].ID != 2 {
		t.Fatalf("NewMomentResponse = %+v (url=%s)", got, url)
	}
	list := NewMomentResponseList([]model.Moment{*moment})
	if len(list) != 1 || list[0].ID != 1 {
		t.Fatalf("NewMomentResponseList = %+v", list)
	}
	if empty := NewMomentResponseList(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("NewMomentResponseList(nil) = %#v, want empty non-nil", empty)
	}
}

func TestNewPageResponseList(t *testing.T) {
	pages := []model.Page{{ID: 1, Title: "t", Content: "c"}}
	list := NewPageResponseList(pages)
	if len(list) != 1 || list[0].ID != 1 || list[0].Title != "t" {
		t.Fatalf("NewPageResponseList = %+v", list)
	}
	if empty := NewPageResponseList(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("NewPageResponseList(nil) = %#v, want empty non-nil", empty)
	}
}

func TestNewCommentResponseList(t *testing.T) {
	comments := []model.Comment{{ID: 1, AuthorType: "admin", AdminName: "Admin"}}
	list := NewCommentResponseList(comments, false)
	if len(list) != 1 || list[0].ID != 1 {
		t.Fatalf("NewCommentResponseList = %+v", list)
	}
	if empty := NewCommentResponseList(nil, false); empty == nil || len(empty) != 0 {
		t.Fatalf("NewCommentResponseList(nil) = %#v, want empty non-nil", empty)
	}
}

func TestNewPaginatedResponseNilItems(t *testing.T) {
	response := NewPaginatedResponse[model.Media](nil, 0, 0, 0)
	if response.Items == nil || len(response.Items) != 0 {
		t.Fatalf("Items = %#v, want empty non-nil", response.Items)
	}
	if response.TotalPages != 0 {
		t.Fatalf("TotalPages = %d, want 0", response.TotalPages)
	}
	intResponse := NewPaginatedResponse([]int{1, 2, 3}, 10, 1, 3)
	if intResponse.TotalPages != 4 {
		t.Fatalf("TotalPages = %d, want 4", intResponse.TotalPages)
	}
}

func TestValidationMissingBranches(t *testing.T) {
	if err := (&LoginRequest{Email: "user@example.com", Password: "p"}).Validate(); err != nil {
		// Identifier must fall back to Email when Username is empty.
		t.Fatalf("LoginRequest with email identifier Validate = %v", err)
	}
	if err := (&LoginRequest{}).Validate(); err == nil {
		t.Fatal("LoginRequest without identifier should fail")
	}

	if err := (&EmailCodeRequest{Email: "  "}).Validate(); err == nil {
		t.Fatal("EmailCodeRequest with empty email should fail")
	}

	longNickname := strings.Repeat("界", model.MaxCommentNicknameLength+1)
	if err := (&RegisterRequest{Nickname: longNickname, Username: "u", Password: "p", Email: "e", Code: "c"}).Validate(); err == nil {
		t.Fatal("RegisterRequest with an overlong nickname should fail")
	}
	if err := (&RegisterRequest{Nickname: "n", Username: "", Password: "p", Email: "e", Code: "c"}).Validate(); err == nil {
		t.Fatal("RegisterRequest without username should fail")
	}
	if err := (&RegisterRequest{Nickname: "n", Username: "u", Password: "", Email: "e", Code: "c"}).Validate(); err == nil {
		t.Fatal("RegisterRequest without password should fail")
	}
	if err := (&RegisterRequest{Nickname: "n", Username: "u", Password: "p"}).Validate(); err == nil {
		t.Fatal("RegisterRequest without email/code should fail")
	}

	if err := (&ResetPasswordRequest{Email: "", Code: "c", Password: "p"}).Validate(); err == nil {
		t.Fatal("ResetPasswordRequest without email should fail")
	}
	if err := (&ResetPasswordRequest{Email: "e", Code: "c", Password: ""}).Validate(); err == nil {
		t.Fatal("ResetPasswordRequest without password should fail")
	}
	if err := (&ChangePasswordRequest{CurrentPassword: "a", NewPassword: ""}).Validate(); err == nil {
		t.Fatal("ChangePasswordRequest without new password should fail")
	}

	admin := &AdminUpdateUserRequest{}
	if err := admin.Validate(); err == nil {
		t.Fatal("AdminUpdateUserRequest without username should fail")
	}
	admin = &AdminUpdateUserRequest{Username: "u"}
	if err := admin.Validate(); err == nil {
		t.Fatal("AdminUpdateUserRequest without nickname should fail")
	}
	admin = &AdminUpdateUserRequest{Username: "u", Nickname: longNickname}
	if err := admin.Validate(); err == nil {
		t.Fatal("AdminUpdateUserRequest with overlong nickname should fail")
	}
	admin = &AdminUpdateUserRequest{Username: "u", Nickname: "n"}
	if err := admin.Validate(); err == nil {
		t.Fatal("AdminUpdateUserRequest without email should fail")
	}
}

func TestRenameTagRequestValidate(t *testing.T) {
	if err := (&RenameTagRequest{}).Validate(); err == nil {
		t.Fatal("RenameTagRequest with empty name should fail")
	}
	if err := (&RenameTagRequest{Name: "  Go  "}).Validate(); err != nil {
		t.Fatalf("RenameTagRequest Validate = %v", err)
	}
}

func TestIdentifierPrefersUsername(t *testing.T) {
	if got := (&LoginRequest{Username: "  alice  ", Email: "a@b.co"}).Identifier(); got != "alice" {
		t.Fatalf("Identifier = %q, want alice", got)
	}
	if got := (&LoginRequest{Username: "  ", Email: " a@b.co "}).Identifier(); got != "a@b.co" {
		t.Fatalf("Identifier fallback = %q, want a@b.co", got)
	}
}

func TestOptionalSettersAndResponses(t *testing.T) {
	name := "站长"
	username := "root"
	admin := &AdminProfileRequest{Name: name, Username: &username}
	if !admin.SetUsername() || admin.SetAvatarMediaID() {
		t.Fatalf("SetUsername/SetAvatarMediaID = %v/%v", admin.SetUsername(), admin.SetAvatarMediaID())
	}
	admin.AvatarMediaID = OptionalID{Set: true}
	if !admin.SetAvatarMediaID() {
		t.Fatal("SetAvatarMediaID should report true after Set")
	}

	user := &model.User{ID: 1, Username: "alice", Nickname: "Alice", Email: "a@b.co", PasswordHash: "h", CreatedAt: 2, UpdatedAt: 3}
	response := NewUserResponse(user)
	if response.ID != 1 || response.Username != "alice" || !response.HasPassword {
		t.Fatalf("NewUserResponse = %+v", response)
	}
	if list := NewUserResponseList([]model.User{*user}); len(list) != 1 || list[0].ID != 1 {
		t.Fatalf("NewUserResponseList = %+v", list)
	}
	if me := NewAdminMeResponse("站长", "root"); me.Role != "admin" || me.Name != "站长" || me.Username != "root" {
		t.Fatalf("NewAdminMeResponse = %+v", me)
	}
	if me := NewUserMeResponse(user); me.Role != "user" || me.User == nil || me.User.ID != 1 {
		t.Fatalf("NewUserMeResponse = %+v", me)
	}
}

func TestNewMediaAndTagResponseLists(t *testing.T) {
	media := []model.Media{{ID: 1, OriginalName: "a.png", MediaType: "image", MimeType: "image/png"}}
	mediaList := NewMediaResponseList(media)
	if len(mediaList) != 1 || mediaList[0].URL != "/api/media/1" {
		t.Fatalf("NewMediaResponseList = %+v", mediaList)
	}
	if empty := NewMediaResponseList(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("NewMediaResponseList(nil) = %#v", empty)
	}

	tag := model.Tag{ID: 1, Name: "Go", ArticleCount: 2}
	if response := NewTagResponse(&tag); response.ID != 1 || response.Name != "Go" || response.ArticleCount != 2 {
		t.Fatalf("NewTagResponse = %+v", response)
	}
	if list := NewTagResponseList([]model.Tag{tag}); len(list) != 1 || list[0].ID != 1 {
		t.Fatalf("NewTagResponseList = %+v", list)
	}
	if empty := NewTagResponseList(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("NewTagResponseList(nil) = %#v", empty)
	}
}

func TestRequestValidationSuccessBranches(t *testing.T) {
	if err := (&EmailCodeRequest{Email: "a@b.co"}).Validate(); err != nil {
		t.Fatalf("EmailCodeRequest valid = %v", err)
	}
	if err := (&CreateMomentRequest{Content: "  hi  "}).Validate(); err != nil {
		t.Fatalf("CreateMomentRequest valid = %v", err)
	}
	if err := (&UpdateMomentRequest{Content: "  hi  "}).Validate(); err != nil {
		t.Fatalf("UpdateMomentRequest valid = %v", err)
	}
	if err := (&CreatePageRequest{Title: "  About  "}).Validate(); err != nil {
		t.Fatalf("CreatePageRequest valid = %v", err)
	}
	content := "body"
	if err := (&UpdatePageRequest{Title: "About", Content: &content}).Validate(); err != nil {
		t.Fatalf("UpdatePageRequest valid = %v", err)
	}
	if err := (&AdminUpdateUserRequest{Username: "u", Nickname: "n", Email: "e@x.co"}).Validate(); err != nil {
		t.Fatalf("AdminUpdateUserRequest valid = %v", err)
	}
}

func TestSiteDescriptionHelpers(t *testing.T) {
	if got := SiteDescription("   "); got != DefaultSiteDescription {
		t.Fatalf("SiteDescription(blank) = %q, want default", got)
	}
	if got := SiteDescription("  hello  "); got != "  hello  " {
		t.Fatalf("SiteDescription(configured) = %q", got)
	}
	if got := normalizeSiteDescription("a\n\tb\x00c   d"); got != "a bc d" {
		t.Fatalf("normalizeSiteDescription = %q", got)
	}
}

func TestRemainingTinyBranches(t *testing.T) {
	response := NewCommentResponse(&model.Comment{AuthorType: "admin", AdminName: ""}, false)
	if response.Author.Name != "Admin" {
		t.Fatalf("empty admin name fallback = %q, want Admin", response.Author.Name)
	}
	if err := (&CreatePageRequest{}).Validate(); err == nil {
		t.Fatal("CreatePageRequest with empty title should fail")
	}
	if err := (&UpdatePageRequest{}).Validate(); err == nil {
		t.Fatal("UpdatePageRequest with empty title should fail")
	}
}
