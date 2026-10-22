package viewmodel

import (
	"testing"
)

// TestSiteSettingsRequestValidateCoversTheNewFields 覆盖站点设置请求的校验分支：
// 头图高度的两端越界，以及评论通知地址的三种结局（空 = 关闭、合法、非法）。
func TestSiteSettingsRequestValidateCoversTheNewFields(t *testing.T) {
	height := func(value int) *int { return &value }
	address := func(value string) *string { return &value }

	t.Run("the banner height is inside the range", func(t *testing.T) {
		for _, value := range []int{MinPostBannerHeight, 400, MaxPostBannerHeight} {
			request := SiteSettingsRequest{Description: "站点", PostBannerHeight: height(value)}
			if err := request.Validate(); err != nil {
				t.Fatalf("height %d must be accepted: %v", value, err)
			}
		}
	})

	t.Run("the banner height is out of range", func(t *testing.T) {
		for _, value := range []int{MinPostBannerHeight - 1, 0, MaxPostBannerHeight + 1} {
			request := SiteSettingsRequest{Description: "站点", PostBannerHeight: height(value)}
			if err := request.Validate(); err == nil {
				t.Fatalf("height %d must be refused", value)
			}
		}
	})

	t.Run("an empty notification address disables the feature", func(t *testing.T) {
		request := SiteSettingsRequest{Description: "站点", CommentNotifyTo: address("   ")}
		if err := request.Validate(); err != nil {
			t.Fatalf("an empty address must be accepted: %v", err)
		}
		if request.CommentNotifyTo == nil || *request.CommentNotifyTo != "" {
			t.Fatalf("the address must be normalized to an empty string, got %v", request.CommentNotifyTo)
		}
	})

	t.Run("a valid address is trimmed and kept", func(t *testing.T) {
		request := SiteSettingsRequest{Description: "站点", CommentNotifyTo: address("  owner@example.com  ")}
		if err := request.Validate(); err != nil {
			t.Fatalf("a valid address must be accepted: %v", err)
		}
		if request.CommentNotifyTo == nil || *request.CommentNotifyTo != "owner@example.com" {
			t.Fatalf("the address must be trimmed, got %v", request.CommentNotifyTo)
		}
	})

	t.Run("an invalid address is refused", func(t *testing.T) {
		request := SiteSettingsRequest{Description: "站点", CommentNotifyTo: address("not-an-address")}
		if err := request.Validate(); err == nil {
			t.Fatal("an invalid address must be refused")
		}
	})
}

// TestValidateNotifyAddress 直接钉住地址规则本身：
// 「纯地址」是硬要求——带显示名的地址在邮件头里合法，但会让「保存了什么」
// 与「发到哪」看起来不一致。
func TestValidateNotifyAddress(t *testing.T) {
	if err := ValidateNotifyAddress(""); err != nil {
		t.Fatalf("an empty address must be accepted: %v", err)
	}
	if err := ValidateNotifyAddress("   "); err != nil {
		t.Fatalf("a blank address must be accepted: %v", err)
	}
	if err := ValidateNotifyAddress("owner@example.com"); err != nil {
		t.Fatalf("a bare address must be accepted: %v", err)
	}
	if err := ValidateNotifyAddress("Twilight <owner@example.com>"); err == nil {
		t.Fatal("an address with a display name must be refused")
	}
	if err := ValidateNotifyAddress("owner@example.com, other@example.com"); err == nil {
		t.Fatal("more than one address must be refused")
	}
	if err := ValidateNotifyAddress("owner@@example.com"); err == nil {
		t.Fatal("a malformed address must be refused")
	}
}
