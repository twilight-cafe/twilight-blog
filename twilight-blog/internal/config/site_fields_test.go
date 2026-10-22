package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPostBannerHeightValidation 覆盖头图高度的三条规则：
// 负数非法、0 表示「未设置」、显式值必须落在区间内。
func TestPostBannerHeightValidation(t *testing.T) {
	if err := validatePostBannerHeight(-1); err == nil {
		t.Fatal("a negative height must be refused")
	}
	if err := validatePostBannerHeight(0); err != nil {
		t.Fatalf("0 means 'not set' and must be accepted: %v", err)
	}
	if err := validatePostBannerHeight(MinPostBannerHeight - 1); err == nil {
		t.Fatal("a height below the range must be refused")
	}
	if err := validatePostBannerHeight(MaxPostBannerHeight + 1); err == nil {
		t.Fatal("a height above the range must be refused")
	}
	for _, value := range []int{MinPostBannerHeight, DefaultPostBannerHeight, MaxPostBannerHeight} {
		if err := validatePostBannerHeight(value); err != nil {
			t.Fatalf("height %d must be accepted: %v", value, err)
		}
	}
}

// TestSetPostBannerHeight 覆盖设置接口的边界：0 不接受（它是「未设置」），
// 越界不��受，合法值写进内存。
func TestSetPostBannerHeight(t *testing.T) {
	cfg := &Config{}
	if err := cfg.SetPostBannerHeight(0); err == nil {
		t.Fatal("0 must be refused by the setter")
	}
	if err := cfg.SetPostBannerHeight(MaxPostBannerHeight + 1); err == nil {
		t.Fatal("an out-of-range height must be refused")
	}
	if err := cfg.SetPostBannerHeight(260); err != nil {
		t.Fatalf("a valid height must be accepted: %v", err)
	}
	if got := cfg.PostBannerHeightValue(); got != 260 {
		t.Fatalf("PostBannerHeightValue = %d, want 260", got)
	}
}

// TestMomentsEnabledDefaultsToOn 钉住「键缺失 = 启用」这条向后兼容规则：
// 老版本的 settings.json 里没有这个键，升级后不能静默关掉已发布的内容。
func TestMomentsEnabledDefaultsToOn(t *testing.T) {
	cfg := &Config{}
	if !cfg.MomentsEnabledValue() {
		t.Fatal("a missing key must mean enabled")
	}
	cfg.SetMomentsEnabled(false)
	if cfg.MomentsEnabledValue() {
		t.Fatal("an explicit false must disable the feature")
	}
	cfg.SetMomentsEnabled(true)
	if !cfg.MomentsEnabledValue() {
		t.Fatal("an explicit true must enable the feature")
	}
}

// TestCommentNotifyTo 覆盖通知地址的读写：空串 = 关闭；非空必须是纯地址。
func TestCommentNotifyTo(t *testing.T) {
	cfg := &Config{}
	if got := cfg.CommentNotifyToValue(); got != "" {
		t.Fatalf("a configuration without a mail section must report an empty address, got %q", got)
	}

	if err := cfg.SetCommentNotifyTo("not-an-address"); err == nil {
		t.Fatal("an invalid address must be refused")
	}
	if err := cfg.SetCommentNotifyTo("  owner@example.com  "); err != nil {
		t.Fatalf("a valid address must be accepted: %v", err)
	}
	if got := cfg.CommentNotifyToValue(); got != "owner@example.com" {
		t.Fatalf("CommentNotifyToValue = %q", got)
	}
	if cfg.Mail == nil {
		t.Fatal("the mail section must be created on demand")
	}
}

// TestValidateEmailAddress 钉住「纯地址」这条规则本身。
func TestValidateEmailAddress(t *testing.T) {
	if err := ValidateEmailAddress(""); err != nil {
		t.Fatalf("an empty address must be accepted: %v", err)
	}
	if err := ValidateEmailAddress("   "); err != nil {
		t.Fatalf("a blank address must be accepted: %v", err)
	}
	if err := ValidateEmailAddress("owner@example.com"); err != nil {
		t.Fatalf("a bare address must be accepted: %v", err)
	}
	if err := ValidateEmailAddress("Twilight <owner@example.com>"); err == nil {
		t.Fatal("an address with a display name must be refused")
	}
	if err := ValidateEmailAddress("owner@@example.com"); err == nil {
		t.Fatal("a malformed address must be refused")
	}
}

// TestLoadRejectsInvalidNewFields 覆盖配置文件里两个新字段的校验。
func TestLoadRejectsInvalidNewFields(t *testing.T) {
	write := func(t *testing.T, content map[string]any) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "settings.json")
		encoded, err := json.Marshal(content)
		if err != nil {
			t.Fatalf("marshal settings: %v", err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatalf("write settings: %v", err)
		}
		return path
	}

	t.Run("the banner height", func(t *testing.T) {
		path := write(t, map[string]any{"post_banner_height": 900})
		if _, err := Load(path); err == nil {
			t.Fatal("an out-of-range banner height must be refused")
		}
	})

	t.Run("the notification address", func(t *testing.T) {
		path := write(t, map[string]any{
			"mail": map[string]any{"comment_notify_to": "not-an-address"},
		})
		if _, err := Load(path); err == nil {
			t.Fatal("an invalid notification address must be refused")
		}
	})

	t.Run("a valid configuration", func(t *testing.T) {
		path := write(t, map[string]any{
			"post_banner_height": 260,
			"mail":               map[string]any{"comment_notify_to": "owner@example.com"},
		})
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.PostBannerHeightValue() != 260 {
			t.Fatalf("PostBannerHeightValue = %d", cfg.PostBannerHeightValue())
		}
		if cfg.CommentNotifyToValue() != "owner@example.com" {
			t.Fatalf("CommentNotifyToValue = %q", cfg.CommentNotifyToValue())
		}
	})
}
