package model

import (
	"strings"
	"testing"
)

// 这组测试钉住「用户名长什么样」这条规则，因为它的三个调用方分属不同层
// （访客注册走 service、管理员登录名走 viewmodel、仓储唯一性走 SQL 的
// COLLATE NOCASE）。规则一旦在某处被复制并漂移，最先出问题的不是编译，
// 而是「有没有人能冒充别人」——同形异义字符就是靠这种漂移溜进来的。

func TestNormalizeAndValidateUsernameAcceptsTheDocumentedShape(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "nightwalker", "nightwalker"},
		{"with digits and separators", "a1_b-2", "a1_b-2"},
		{"minimum length", "abc", "abc"},
		{"maximum length", strings.Repeat("a", MaxUsernameLength), strings.Repeat("a", MaxUsernameLength)},
		{"surrounding whitespace is trimmed", "  alice  ", "alice"},
		// Case is preserved rather than folded: uniqueness and lookup use
		// COLLATE NOCASE, so "Alice" and "alice" are the same account but the
		// owner still sees the spelling they typed.
		{"case is preserved", "Alice", "Alice"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeAndValidateUsername(tc.input)
			if err != nil {
				t.Fatalf("NormalizeAndValidateUsername(%q) error = %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeAndValidateUsername(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNormalizeAndValidateUsernameRejectsTheDocumentedOutsiders(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"too short", "ab"},
		{"too long", strings.Repeat("a", MaxUsernameLength+1)},
		{"leading digit", "1abc"},
		{"leading underscore", "_abc"},
		{"leading hyphen", "-abc"},
		{"space inside", "abc def"},
		{"dot", "a.b"},
		{"at sign", "a@b"},
		// Cyrillic "а" (U+0430) renders like a Latin "a" in most interfaces.
		{"cyrillic look-alike", "аdmin"},
		// A zero-width space is invisible, so a name containing one looks exactly
		// like the name without it — and can lock its owner out of sign-in.
		{"zero width space", "admin\u200b"},
		{"newline", "abc\ndef"},
		{"emoji", "abc😀"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := NormalizeAndValidateUsername(tc.input); err == nil {
				t.Fatalf("NormalizeAndValidateUsername(%q) = %q, want an error", tc.input, got)
			}
		})
	}
}
