package childenv

import (
	"slices"
	"strings"
	"testing"

	"blog-engine/internal/config"
)

// 这一组测试钉住「凭据不外传给第三方子进程」这条不变量。
//
// 起因（第三轮渗透测试 R3-14）：Meilisearch 那一路有过滤，媒体转码那一路没有，
// 于是 cwebp / ffmpeg 会原样继承 BLOG_MAIL_API_KEY。同一个不变量在两处各写一遍，
// 漏掉的那一处不会报错也不会失败编译——只有这样的测试能发现它。

func TestSanitizedDropsCredentialVariables(t *testing.T) {
	t.Setenv(config.MailAPIKeyEnvVar, "re_must_not_reach_the_child")

	env := Sanitized()

	for _, entry := range env {
		if strings.HasPrefix(entry, config.MailAPIKeyEnvVar+"=") {
			t.Fatalf("Sanitized() leaked the mail API key to the child: %q", entry)
		}
	}
}

func TestSanitizedKeepsUnrelatedVariables(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("BLOG_UNRELATED_PROBE", "keep-me")

	env := Sanitized()

	if !slices.Contains(env, "BLOG_UNRELATED_PROBE=keep-me") {
		t.Fatal("Sanitized() dropped an unrelated variable; the child would lose its environment")
	}
	found := false
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			found = true
		}
	}
	if !found {
		t.Fatal("Sanitized() dropped PATH; the child could not find its own helpers")
	}
}

func TestSanitizedDropsRequestedPrefixes(t *testing.T) {
	t.Setenv("MEILI_MASTER_KEY", "external-override")
	t.Setenv("MEILI_HTTP_ADDR", "127.0.0.1:1")
	t.Setenv("NOT_MEILI_MASTER_KEY", "keep-me")

	env := Sanitized("MEILI_")

	for _, entry := range env {
		if strings.HasPrefix(entry, "MEILI_") {
			t.Fatalf("Sanitized(\"MEILI_\") kept %q, which would override the explicit configuration", entry)
		}
	}
	if !slices.Contains(env, "NOT_MEILI_MASTER_KEY=keep-me") {
		t.Fatal("prefix filtering is not anchored at the start of the variable name")
	}
}

// TestCredentialDenylistIsNotEmpty guards against the list being emptied during a
// refactor: an empty denylist silently disables the protection everywhere.
func TestCredentialDenylistIsNotEmpty(t *testing.T) {
	if len(CredentialDenylist) == 0 {
		t.Fatal("CredentialDenylist is empty; child processes would inherit every secret")
	}
	if !slices.Contains(CredentialDenylist, config.MailAPIKeyEnvVar) {
		t.Fatalf("CredentialDenylist does not cover %s", config.MailAPIKeyEnvVar)
	}
}
