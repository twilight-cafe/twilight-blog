package viewmodel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"blog-engine/internal/model"
)

func userComment() *model.Comment {
	userID := int64(7)
	return &model.Comment{
		ID:         1,
		AuthorType: "user",
		UserID:     &userID,
		Nickname:   "夜行者",
		UserEmail:  "visitor@example.com",
		TargetType: "article",
		TargetID:   1,
		Content:    "hello",
		CreatedAt:  100,
		UpdatedAt:  100,
	}
}

// TestPublicCommentResponseHidesEmail is the regression test for visitor email
// addresses being returned to anonymous readers of GET /api/comments.
func TestPublicCommentResponseHidesEmail(t *testing.T) {
	public := NewCommentResponse(userComment(), false)

	if public.Author.Email != "" {
		t.Fatalf("public author email = %q, want empty", public.Author.Email)
	}
	if public.Author.Nickname != "夜行者" {
		t.Fatalf("public author nickname = %q, want 夜行者", public.Author.Nickname)
	}
	if public.Author.Name != "" {
		t.Fatalf("visitor author must not populate Name, got %q", public.Author.Name)
	}
}

func TestAdminCommentResponseIncludesEmail(t *testing.T) {
	admin := NewCommentResponse(userComment(), true)

	if admin.Author.Email != "visitor@example.com" {
		t.Fatalf("admin author email = %q, want visitor@example.com", admin.Author.Email)
	}
}

// TestEmptyNicknameFallsBackWithoutLeakingEmail covers rows created before
// nicknames existed: the previous code derived a display name from the email
// local part, which leaked half of a private address.
func TestEmptyNicknameFallsBackWithoutLeakingEmail(t *testing.T) {
	comment := userComment()
	comment.Nickname = ""

	response := NewCommentResponse(comment, false)

	if response.Author.Nickname != anonDisplayName {
		t.Fatalf("nickname = %q, want %q", response.Author.Nickname, anonDisplayName)
	}
	if strings.Contains(response.Author.Nickname, "visitor") {
		t.Fatalf("nickname %q leaks the email local part", response.Author.Nickname)
	}
}

func TestAdminAuthorUsesName(t *testing.T) {
	comment := &model.Comment{
		ID:         2,
		AuthorType: "admin",
		AdminName:  "Admin",
		TargetType: "article",
		TargetID:   1,
		Content:    "hi",
	}

	response := NewCommentResponse(comment, true)
	if response.Author.Name != "Admin" {
		t.Fatalf("admin author name = %q, want Admin", response.Author.Name)
	}
	if response.Author.Nickname != "" {
		t.Fatalf("admin author must not set Nickname, got %q", response.Author.Nickname)
	}
}

func TestCommentContentLengthIsEnforced(t *testing.T) {
	tooLong := &CreateCommentRequest{
		TargetType: "article",
		TargetID:   1,
		Content:    strings.Repeat("a", model.MaxCommentContentLength+1),
	}
	if err := tooLong.Validate(); err == nil {
		t.Fatal("over-long content was accepted")
	}

	atLimit := &CreateCommentRequest{
		TargetType: "article",
		TargetID:   1,
		Content:    strings.Repeat("a", model.MaxCommentContentLength),
	}
	if err := atLimit.Validate(); err != nil {
		t.Fatalf("content at the limit was rejected: %v", err)
	}
}

// TestCreateCommentRequestHasNoAuthorField 记录一条刻意设计的约束：
// **署名不可由请求指定**。这里用 JSON 解码而不是结构体字面量，
// 因为结构体字面量写不出「多传一个字段」这种请求——而真实客户端恰恰
// 可能带着多余的键来（老版本前端就一直在传 nickname）。
//
// 断言的是「解出来的请求体里没有地方放作者名」，因此只要有人日后
// 把 Nickname/Author 之类的字段加回请求结构，这条测试就会先炸。
func TestCreateCommentRequestHasNoAuthorField(t *testing.T) {
	var request CreateCommentRequest
	body := []byte(`{"target_type":"article","target_id":1,"content":"hi","nickname":"Mallory","author":"Mallory","username":"mallory"}`)
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	// 结构体里若还存在可承载作者名的字段，反射能看见它。
	value := reflect.ValueOf(request)
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		switch name {
		case "nickname", "author", "username", "name":
			t.Fatalf("CreateCommentRequest still exposes author field %q", typ.Field(i).Name)
		}
	}
}

// TestArticleContentIsNotLengthCapped records the deliberate policy: only
// visitor-authored content is bounded.
func TestArticleContentIsNotLengthCapped(t *testing.T) {
	request := &CreateArticleRequest{
		Title:   "title",
		Content: strings.Repeat("a", model.MaxCommentContentLength*4),
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("article content was capped unexpectedly: %v", err)
	}
}
