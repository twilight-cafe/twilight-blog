package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blog-engine/internal/backup"
	"blog-engine/internal/config"
)

// --- 设置读取与改写 ---------------------------------------------------------

func TestReadSettingsMap(t *testing.T) {
	dir := t.TempDir()

	t.Run("file missing is not an error", func(t *testing.T) {
		values, err := readSettingsMap(filepath.Join(dir, "missing.json"))
		if err != nil {
			t.Fatalf("readSettingsMap() error = %v", err)
		}
		if values != nil {
			t.Fatalf("readSettingsMap() = %v, want nil", values)
		}
	})

	t.Run("non-not-exist read error is reported", func(t *testing.T) {
		if _, err := readSettingsMap(dir); err == nil {
			t.Fatal("reading a directory should fail")
		}
	})

	t.Run("invalid json is reported", func(t *testing.T) {
		path := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSettingsMap(path); err == nil {
			t.Fatal("invalid JSON should fail")
		}
	})

	t.Run("BOM and null decode to an empty map", func(t *testing.T) {
		path := filepath.Join(dir, "null.json")
		if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, []byte("null")...), 0o600); err != nil {
			t.Fatal(err)
		}
		values, err := readSettingsMap(path)
		if err != nil {
			t.Fatalf("readSettingsMap() error = %v", err)
		}
		if values == nil || len(values) != 0 {
			t.Fatalf("readSettingsMap() = %#v, want an empty non-nil map", values)
		}
	})
}

func TestSetSettingsMediaDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"media_dir":"old","site_description":"keep me"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := setSettingsMediaDir(path, "new"); err != nil {
		t.Fatalf("setSettingsMediaDir() error = %v", err)
	}
	values, err := readSettingsMap(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := values["media_dir"]; got != "new" {
		t.Fatalf("media_dir = %v, want new", got)
	}
	if got := values["site_description"]; got != "keep me" {
		t.Fatalf("site_description = %v, want keep me", got)
	}

	t.Run("read error is reported", func(t *testing.T) {
		if err := setSettingsMediaDir(dir, "new"); err == nil {
			t.Fatal("a directory cannot be read as JSON")
		}
	})

	t.Run("write error is reported", func(t *testing.T) {
		readonly := filepath.Join(dir, "readonly.json")
		if err := os.WriteFile(readonly, []byte(`{}`), 0o400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(readonly, 0o400); err != nil {
			t.Fatal(err)
		}
		if os.Geteuid() == 0 {
			t.Skip("root can write a read-only file")
		}
		if err := setSettingsMediaDir(readonly, "new"); err == nil {
			t.Fatal("writing a read-only file should fail")
		}
	})
}

// --- R7-03 安全字段判定 ------------------------------------------------------

func TestSettingsWeakening(t *testing.T) {
	safeBase := map[string]any{
		"rate_limit":      map[string]any{"enabled": true},
		"cookie_secure":   config.CookieSecureAuto,
		"trusted_proxies": []any{"127.0.0.1/32", "::1/128"},
		"allowed_origins": []any{"https://example.com"},
		"webp_tool":       "/usr/bin/cwebp",
		"webm_tool":       "/usr/bin/ffmpeg",
	}
	with := func(overrides map[string]any) map[string]any {
		m := make(map[string]any, len(safeBase)+len(overrides))
		for k, v := range safeBase {
			m[k] = v
		}
		for k, v := range overrides {
			m[k] = v
		}
		return m
	}
	without := func(m map[string]any, keys ...string) map[string]any {
		copy := make(map[string]any, len(m))
		for k, v := range m {
			copy[k] = v
		}
		for _, key := range keys {
			delete(copy, key)
		}
		return copy
	}

	tests := []struct {
		name    string
		archive map[string]any
		local   map[string]any
		want    []string
	}{
		{
			name:    "identical safe configuration",
			archive: with(nil),
			local:   with(nil),
		},
		{
			name:    "rate limit disabled",
			archive: with(map[string]any{"rate_limit": map[string]any{"enabled": false}}),
			local:   with(nil),
			want:    []string{"rate_limit.enabled is false, which disables rate limiting"},
		},
		{
			name:    "rate quota widened",
			archive: with(map[string]any{"rate_limit": map[string]any{"enabled": true, "login_per_minute": float64(1000000000)}}),
			local:   with(nil),
			want:    []string{"rate_limit.login_per_minute is 1000000000, above this host's 3"},
		},
		{
			name:    "rate quota narrowed",
			archive: with(map[string]any{"rate_limit": map[string]any{"enabled": true, "login_per_minute": float64(1)}}),
			local:   with(nil),
		},
		{
			name:    "rate quota zero falls back to the default",
			archive: with(map[string]any{"rate_limit": map[string]any{"enabled": true, "login_per_minute": float64(0)}}),
			local:   with(nil),
		},
		{
			name:    "cookie secure never is rejected",
			archive: with(map[string]any{"cookie_secure": "never"}),
			local:   with(nil),
			want:    []string{`cookie_secure is "never", but only auto/always are accepted`},
		},
		{
			name:    "cookie secure garbage is rejected",
			archive: with(map[string]any{"cookie_secure": "sometimes"}),
			local:   with(nil),
			want:    []string{`cookie_secure is "sometimes", but only auto/always are accepted`},
		},
		{
			name:    "cookie secure auto is weaker than always",
			archive: with(map[string]any{"cookie_secure": config.CookieSecureAuto}),
			local:   with(map[string]any{"cookie_secure": config.CookieSecureAlways}),
			want:    []string{`cookie_secure is "auto", weaker than this host's "always"`},
		},
		{
			name:    "cookie secure always is stronger than auto",
			archive: with(map[string]any{"cookie_secure": config.CookieSecureAlways}),
			local:   with(nil),
		},
		{
			name:    "server name broadens",
			archive: with(map[string]any{"server_name": "0.0.0.0"}),
			local:   with(map[string]any{"server_name": "127.0.0.1"}),
			want:    []string{`server_name changes from "127.0.0.1" to "0.0.0.0", which may expose the service more broadly`},
		},
		{
			name:    "server name narrows",
			archive: with(map[string]any{"server_name": "127.0.0.1"}),
			local:   with(nil),
		},
		{
			name:    "server name changes to another specific address",
			archive: with(map[string]any{"server_name": "localhost"}),
			local:   with(map[string]any{"server_name": "127.0.0.1"}),
			want:    []string{`server_name changes from "127.0.0.1" to "localhost", which may expose the service more broadly`},
		},
		{
			name:    "max upload size widened",
			archive: with(map[string]any{"max_upload_size": float64(100)}),
			local:   with(nil),
			want:    []string{"max_upload_size is 100, above this host's 50"},
		},
		{
			name:    "max upload size narrowed",
			archive: with(map[string]any{"max_upload_size": float64(10)}),
			local:   with(nil),
		},
		{
			name:    "trusted proxies widened",
			archive: with(map[string]any{"trusted_proxies": []any{"127.0.0.1/32", "::1/128", "0.0.0.0/0"}}),
			local:   with(nil),
			want:    []string{`trusted_proxies adds "0.0.0.0/0", which this host does not trust`},
		},
		{
			name:    "trusted proxies narrowed",
			archive: with(map[string]any{"trusted_proxies": []any{"127.0.0.1/32"}}),
			local:   with(nil),
		},
		{
			name:    "missing archive trusted proxies means the loopback default",
			archive: without(with(nil), "trusted_proxies"),
			local:   with(map[string]any{"trusted_proxies": []any{}}),
			want: []string{
				`trusted_proxies adds "127.0.0.1/32", which this host does not trust`,
				`trusted_proxies adds "::1/128", which this host does not trust`,
			},
		},
		{
			name:    "allowed origins widened",
			archive: with(map[string]any{"allowed_origins": []any{"https://example.com", "https://evil.example"}}),
			local:   with(nil),
			want:    []string{`allowed_origins adds "https://evil.example", which this host does not trust`},
		},
		{
			name:    "allowed origins narrowed",
			archive: with(map[string]any{"allowed_origins": []any{}}),
			local:   with(nil),
		},
		{
			name:    "transcoding tool changes",
			archive: with(map[string]any{"webp_tool": "/tmp/evil"}),
			local:   with(nil),
			want:    []string{`webp_tool changes from "/usr/bin/cwebp" to "/tmp/evil"; only this host's configured tool is accepted`},
		},
		{
			name:    "transcoding tool removed",
			archive: without(with(nil), "webp_tool"),
			local:   with(nil),
			want:    []string{`webp_tool changes from "/usr/bin/cwebp" to ""; only this host's configured tool is accepted`},
		},
		{
			name: "wrong-typed nested and string values are ignored safely",
			archive: with(map[string]any{
				"rate_limit":      "off",
				"cookie_secure":   42,
				"trusted_proxies": "127.0.0.1",
				"allowed_origins": nil,
			}),
			local: with(nil),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := settingsWeakening(tc.archive, tc.local)
			if !equalStringSlices(got, tc.want) {
				t.Fatalf("settingsWeakening() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRestoreSettingsHelpers 把几个纯函数的边界单独钉住。它们都被 settingsWeakening
// 间接触达，但出错的后果各不相同：数值解析错会漏报额度放宽，监听地址判断错会漏报
// 全接口绑定，trusted_proxies 缺失语义错会把“默认信任回环”当成“什么都不信”。
func TestRestoreSettingsHelpers(t *testing.T) {
	t.Run("numberAt", func(t *testing.T) {
		if _, ok := numberAt(nil, "x"); ok {
			t.Fatal("nil map should not contain a number")
		}
		if n, ok := numberAt(map[string]any{"x": float64(7)}, "x"); !ok || n != 7 {
			t.Fatalf("float number = %d, %v; want 7, true", n, ok)
		}
		if _, ok := numberAt(map[string]any{"x": float64(7.5)}, "x"); ok {
			t.Fatal("non-integral float should be rejected")
		}
		if n, ok := numberAt(map[string]any{"x": 9}, "x"); !ok || n != 9 {
			t.Fatalf("int number = %d, %v; want 9, true", n, ok)
		}
		if _, ok := numberAt(map[string]any{"x": "9"}, "x"); ok {
			t.Fatal("string should not be accepted as a number")
		}
	})

	t.Run("effectiveTrustedProxies", func(t *testing.T) {
		if got := effectiveTrustedProxies(nil); !equalStringSlices(got, config.DefaultTrustedProxies) {
			t.Fatalf("nil map should fall back to the default, got %v", got)
		}
		if got := effectiveTrustedProxies(map[string]any{}); !equalStringSlices(got, config.DefaultTrustedProxies) {
			t.Fatalf("missing key should fall back to the default, got %v", got)
		}
		if got := effectiveTrustedProxies(map[string]any{"trusted_proxies": []any{}}); len(got) != 0 {
			t.Fatalf("explicit empty list should mean no trust, got %v", got)
		}
		if got := effectiveTrustedProxies(map[string]any{"trusted_proxies": []any{"10.0.0.1"}}); !equalStringSlices(got, []string{"10.0.0.1"}) {
			t.Fatalf("explicit list should be returned, got %v", got)
		}
	})

	t.Run("cookieSecureRank", func(t *testing.T) {
		if got := cookieSecureRank(config.CookieSecureAlways); got != 2 {
			t.Fatalf("always rank = %d, want 2", got)
		}
		for _, mode := range []string{config.CookieSecureAuto, ""} {
			if got := cookieSecureRank(mode); got != 1 {
				t.Fatalf("%q rank = %d, want 1", mode, got)
			}
		}
		if got := cookieSecureRank(config.CookieSecureNever); got != 0 {
			t.Fatalf("never rank = %d, want 0", got)
		}
		if got := cookieSecureRank("sometimes"); got != 0 {
			t.Fatalf("unknown rank = %d, want 0", got)
		}
	})

	t.Run("isBroadBindAddress", func(t *testing.T) {
		for _, value := range []string{"", "0.0.0.0", "::", "[::]"} {
			if !isBroadBindAddress(value) {
				t.Fatalf("%q should be considered broad", value)
			}
		}
		for _, value := range []string{"127.0.0.1", "localhost", "192.168.1.10"} {
			if isBroadBindAddress(value) {
				t.Fatalf("%q should not be considered broad", value)
			}
		}
	})
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestMapHelpers(t *testing.T) {
	if got := nestedMap(nil, "x"); got != nil {
		t.Fatalf("nestedMap(nil) = %v", got)
	}
	if got := nestedMap(map[string]any{"x": "not-a-map"}, "x"); got != nil {
		t.Fatalf("nestedMap(non-map) = %v", got)
	}
	if got := nestedMap(map[string]any{"x": map[string]any{"y": 1.0}}, "x"); got["y"] != 1.0 {
		t.Fatalf("nestedMap(valid) = %v", got)
	}

	if _, present := boolAt(nil, "x"); present {
		t.Fatal("boolAt(nil) should not report presence")
	}
	if _, present := boolAt(map[string]any{"x": "true"}, "x"); present {
		t.Fatal("boolAt(wrong type) should not report presence")
	}
	if value, present := boolAt(map[string]any{"x": true}, "x"); !present || !value {
		t.Fatalf("boolAt(valid) = %v, %v", value, present)
	}

	if got := stringAt(nil, "x"); got != "" {
		t.Fatalf("stringAt(nil) = %q", got)
	}
	if got := stringAt(map[string]any{"x": 1.0}, "x"); got != "" {
		t.Fatalf("stringAt(wrong type) = %q", got)
	}
	if got := stringAt(map[string]any{"x": "  hi  "}, "x"); got != "hi" {
		t.Fatalf("stringAt(valid) = %q", got)
	}

	if got := stringListAt(nil, "x"); got != nil {
		t.Fatalf("stringListAt(nil) = %v", got)
	}
	if got := stringListAt(map[string]any{"x": "not-a-list"}, "x"); got != nil {
		t.Fatalf("stringListAt(wrong type) = %v", got)
	}
	got := stringListAt(map[string]any{"x": []any{" a ", "", 3.0, "b"}}, "x")
	if !equalStringSlices(got, []string{"a", "b"}) {
		t.Fatalf("stringListAt() = %v", got)
	}
}

func TestValidateArchiveSettings(t *testing.T) {
	local := map[string]any{"trusted_proxies": []any{"127.0.0.1/32"}}
	safe := map[string]any{"trusted_proxies": []any{"127.0.0.1/32"}}
	weakening := map[string]any{"trusted_proxies": []any{"127.0.0.1/32", "0.0.0.0/0"}}

	if err := validateArchiveSettings(safe, local, false); err != nil {
		t.Fatalf("safe settings should be accepted: %v", err)
	}

	err := validateArchiveSettings(weakening, local, false)
	if err == nil {
		t.Fatal("weakening settings must be rejected by default")
	}
	if !strings.Contains(err.Error(), "would weaken") || !strings.Contains(err.Error(), "--accept-archive-settings") {
		t.Fatalf("error should explain the refusal and the override: %v", err)
	}

	out, _ := captureStreams(t, "")
	if err := validateArchiveSettings(weakening, local, true); err != nil {
		t.Fatalf("accept-archive-settings should allow it: %v", err)
	}
	if !strings.Contains(out.String(), "Warning: the archive weakens") {
		t.Fatalf("accepting should print a warning, got %q", out.String())
	}
}

// --- 媒体落点决定 -----------------------------------------------------------

func TestRestoreMediaDir(t *testing.T) {
	t.Run("explicit valid path wins", func(t *testing.T) {
		got, err := restoreMediaDir(restoreOptions{mediaDir: "photos"})
		if err != nil {
			t.Fatalf("restoreMediaDir() error = %v", err)
		}
		if got != "photos" {
			t.Fatalf("restoreMediaDir() = %q, want photos", got)
		}
	})

	t.Run("explicit invalid path is rejected", func(t *testing.T) {
		for _, value := range []string{"../x", "/tmp/x", "."} {
			if _, err := restoreMediaDir(restoreOptions{mediaDir: value}); err == nil {
				t.Fatalf("restoreMediaDir(%q) should be rejected", value)
			}
		}
	})

	t.Run("missing local settings falls back to the default", func(t *testing.T) {
		inTempWorkdir(t)
		got, err := restoreMediaDir(restoreOptions{})
		if err != nil {
			t.Fatalf("restoreMediaDir() error = %v", err)
		}
		if got != config.DefaultMediaDir {
			t.Fatalf("restoreMediaDir() = %q, want %q", got, config.DefaultMediaDir)
		}
	})

	t.Run("local settings value is used", func(t *testing.T) {
		inTempWorkdir(t)
		if err := os.WriteFile(settingsPath, []byte(`{"media_dir":"photos"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := restoreMediaDir(restoreOptions{})
		if err != nil {
			t.Fatalf("restoreMediaDir() error = %v", err)
		}
		if got != "photos" {
			t.Fatalf("restoreMediaDir() = %q, want photos", got)
		}
	})

	t.Run("invalid local settings is reported", func(t *testing.T) {
		inTempWorkdir(t)
		if err := os.WriteFile(settingsPath, []byte(`{"media_dir":"../escape"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := restoreMediaDir(restoreOptions{}); err == nil {
			t.Fatal("invalid local media_dir should be reported")
		}
	})
}

func TestStagedSettingsPath(t *testing.T) {
	staging := t.TempDir()

	t.Run("no settings part means no path", func(t *testing.T) {
		got, err := stagedSettingsPath(staging, backup.Manifest{})
		if err != nil {
			t.Fatalf("stagedSettingsPath() error = %v", err)
		}
		if got != "" {
			t.Fatalf("stagedSettingsPath() = %q, want empty", got)
		}
	})

	t.Run("valid part is joined to staging", func(t *testing.T) {
		manifest := backup.Manifest{Parts: []backup.Part{{Name: backup.PartSettings, Path: backup.DataPrefix + "settings.json"}}}
		got, err := stagedSettingsPath(staging, manifest)
		if err != nil {
			t.Fatalf("stagedSettingsPath() error = %v", err)
		}
		if want := filepath.Join(staging, "settings.json"); got != want {
			t.Fatalf("stagedSettingsPath() = %q, want %q", got, want)
		}
	})

	t.Run("invalid part path is reported", func(t *testing.T) {
		manifest := backup.Manifest{Parts: []backup.Part{{Name: backup.PartSettings, Path: "../settings.json"}}}
		if _, err := stagedSettingsPath(staging, manifest); err == nil {
			t.Fatal("a path outside data/ should be rejected")
		}
	})
}

func TestCheckRestoreOptions(t *testing.T) {
	withoutSettings := backup.Manifest{}
	withSettings := backup.Manifest{Parts: []backup.Part{{Name: backup.PartSettings, Path: backup.DataPrefix + "settings.json"}}}

	if err := checkRestoreOptions(withoutSettings, restoreOptions{}); err != nil {
		t.Fatalf("no --media-dir should always be accepted: %v", err)
	}
	if err := checkRestoreOptions(withSettings, restoreOptions{mediaDir: "photos"}); err != nil {
		t.Fatalf("--media-dir with an archived settings.json should be accepted: %v", err)
	}
	if err := checkRestoreOptions(withSettings, restoreOptions{mediaDir: "photos", keepSettings: true}); err == nil {
		t.Fatal("--media-dir with --keep-settings should be rejected")
	}
	if err := checkRestoreOptions(withoutSettings, restoreOptions{mediaDir: "photos"}); err == nil {
		t.Fatal("--media-dir without an archived settings.json should be rejected")
	}
}

// TestPerformRestoreRejectsDuplicateSettingsParts 是本轮 R8-01 的端到端守护。
//
// 构造一份「两个 settings part」的归档；performRestore 必须在 Open 阶段失败，
// 不能先校验第一份、再把第二份换到 settings.json。
func TestPerformRestoreRejectsDuplicateSettingsParts(t *testing.T) {
	inTempWorkdir(t)
	seedSite(t, map[string]string{"a.png": "png"})

	writeRestoreArchive(t, "evil.tar.gz", []backup.Entry{
		{Name: backup.PartSettings, Source: settingsPath, Target: backup.DataPrefix + "settings.json", Hash: true},
		{Name: backup.PartSettings, Source: settingsPath, Target: backup.DataPrefix + "settings2.json", Hash: true},
	})

	err := performRestore(restoreOptions{archive: "evil.tar.gz", yes: true})
	if err == nil {
		t.Fatal("duplicate settings parts must be rejected before restore starts")
	}
	if !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("error should explain that duplicate parts are refused: %v", err)
	}
	if leftovers := replacedLeftovers(t, "."); len(leftovers) != 0 {
		t.Fatalf("a rejected archive must not move any site data aside: %v", leftovers)
	}
}

// --- 归档构造与 R7-01 回归 --------------------------------------------------

type restoreArchivePart struct {
	name   string
	source string
	target string
}

func writeRestoreArchive(t *testing.T, path string, parts []backup.Entry) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if _, err := backup.Create(file, parts, backup.BuiltBy{OS: "test", Arch: "test"}, time.Unix(1, 0)); err != nil {
		file.Close()
		t.Fatalf("write archive: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
}

func archivedSettingsPart(t *testing.T, dir, body string) backup.Entry {
	t.Helper()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return backup.Entry{Name: backup.PartSettings, Source: path, Target: backup.DataPrefix + "settings.json", Hash: true}
}

func archivedMediaPart(t *testing.T, dir string, files map[string]string) backup.Entry {
	t.Helper()
	media := filepath.Join(dir, "media")
	seedTree(t, media, files)
	return backup.Entry{Name: backup.PartMedia, Source: media, Target: backup.DataPrefix + "media"}
}

func settingsBody(t *testing.T, extra map[string]any, mediaDir string) string {
	t.Helper()
	values := map[string]any{"media_dir": mediaDir}
	for key, value := range extra {
		values[key] = value
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestRestoreNeverUsesTheArchiveMediaDir 是 R7-01 的回归。
//
// 归档里的 media_dir 可能指向工作目录之外的任意位置；旧的实现把它当成 swapIn 的
// 目标，于是 restore 会先挪走/删除那个目录，再把归档内容放进去。这里分别覆盖
// 绝对路径与 `..` 相对路径，并断言工作目录之外没有任何 .replaced-* 痕迹。
func TestRestoreNeverUsesTheArchiveMediaDir(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, workdir string) (archiveMedia string, victim string)
	}{
		{
			name: "absolute path",
			setup: func(t *testing.T, workdir string) (string, string) {
				victim := filepath.Join(t.TempDir(), "victim")
				if err := os.MkdirAll(victim, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(victim, "secret.txt"), []byte("still here"), 0o600); err != nil {
					t.Fatal(err)
				}
				return victim, victim
			},
		},
		{
			name: "dot dot relative path",
			setup: func(t *testing.T, workdir string) (string, string) {
				parent := filepath.Dir(workdir)
				victim := filepath.Join(parent, "victim-"+filepath.Base(workdir))
				if err := os.MkdirAll(victim, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(victim, "secret.txt"), []byte("still here"), 0o600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join("..", filepath.Base(victim)), victim
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workdir := inTempWorkdir(t)
			seedSite(t, nil)
			archiveMedia, victim := tc.setup(t, workdir)

			archiveDir := t.TempDir()
			settings := archivedSettingsPart(t, archiveDir, settingsBody(t, nil, archiveMedia))
			media := archivedMediaPart(t, archiveDir, map[string]string{"evil.txt": "from-archive"})
			writeRestoreArchive(t, "evil.tar.gz", []backup.Entry{settings, media})

			captureStreams(t, "")
			err := performRestore(restoreOptions{archive: "evil.tar.gz", yes: true})
			if err != nil {
				t.Fatalf("restore with archive media_dir %q failed: %v", archiveMedia, err)
			}

			secret := filepath.Join(victim, "secret.txt")
			if body, err := os.ReadFile(secret); err != nil || string(body) != "still here" {
				t.Fatalf("outside directory was touched: body=%q err=%v", body, err)
			}
			// The local media directory is still the destination, and the
			// archive's media_dir was rewritten to match it.
			if body, err := os.ReadFile(filepath.Join("media", "evil.txt")); err != nil || string(body) != "from-archive" {
				t.Fatalf("media was not restored into the local media_dir: body=%q err=%v", body, err)
			}
			raw, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatal(err)
			}
			var restored map[string]any
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			if got := restored["media_dir"]; got != "media" {
				t.Fatalf("restored settings media_dir = %v, want the local value media", got)
			}
			// No .replaced-* side effect may appear next to the victim either.
			parent := filepath.Dir(victim)
			for _, name := range replacedLeftovers(t, parent) {
				if strings.HasPrefix(name, filepath.Base(victim)) {
					t.Fatalf("a .replaced-* leftover was created for the outside target: %s", name)
				}
			}
		})
	}
}

func TestRestoreMediaDirOption(t *testing.T) {
	t.Run("explicit media-dir is used and written into settings", func(t *testing.T) {
		inTempWorkdir(t)
		seedSite(t, nil)

		archiveDir := t.TempDir()
		settings := archivedSettingsPart(t, archiveDir, settingsBody(t, nil, "media"))
		media := archivedMediaPart(t, archiveDir, map[string]string{"pic.txt": "pic"})
		writeRestoreArchive(t, "site.tar.gz", []backup.Entry{settings, media})

		captureStreams(t, "")
		if err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true, mediaDir: "photos"}); err != nil {
			t.Fatalf("restore with --media-dir failed: %v", err)
		}
		if body, err := os.ReadFile(filepath.Join("photos", "pic.txt")); err != nil || string(body) != "pic" {
			t.Fatalf("media was not written to --media-dir: body=%q err=%v", body, err)
		}
		raw, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"media_dir": "photos"`) {
			t.Fatalf("restored settings should point at --media-dir: %s", raw)
		}
	})

	t.Run("invalid media-dir is refused without touching data", func(t *testing.T) {
		inTempWorkdir(t)
		seedSite(t, nil)
		if err := os.WriteFile(settingsPath, []byte(`{"media_dir":"media","site_description":"local"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		archiveDir := t.TempDir()
		settings := archivedSettingsPart(t, archiveDir, settingsBody(t, nil, "media"))
		writeRestoreArchive(t, "site.tar.gz", []backup.Entry{settings})

		captureStreams(t, "")
		err := performRestore(restoreOptions{archive: "site.tar.gz", yes: true, mediaDir: "../escape"})
		if err == nil {
			t.Fatal("an invalid --media-dir must be refused")
		}
		if !strings.Contains(err.Error(), "invalid --media-dir") {
			t.Fatalf("error should mention --media-dir: %v", err)
		}
		raw, readErr := os.ReadFile(settingsPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(raw), `"site_description":"local"`) {
			t.Fatalf("settings.json was changed by a refused restore: %s", raw)
		}
	})
}

func TestRestoreAcceptArchiveSettings(t *testing.T) {
	t.Run("weakening settings are refused by default", func(t *testing.T) {
		inTempWorkdir(t)
		seedSite(t, nil)
		archiveDir := t.TempDir()
		settings := archivedSettingsPart(t, archiveDir, settingsBody(t, map[string]any{"rate_limit": map[string]any{"enabled": false}}, "media"))
		writeRestoreArchive(t, "weak.tar.gz", []backup.Entry{settings})

		captureStreams(t, "")
		err := performRestore(restoreOptions{archive: "weak.tar.gz", yes: true})
		if err == nil {
			t.Fatal("weakening settings must be refused without --accept-archive-settings")
		}
		if !strings.Contains(err.Error(), "would weaken") {
			t.Fatalf("error should explain the refusal: %v", err)
		}
		raw, readErr := os.ReadFile(settingsPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(raw), "rate_limit") {
			t.Fatalf("settings.json should not have been replaced: %s", raw)
		}
	})

	t.Run("accept-archive-settings allows it and warns", func(t *testing.T) {
		inTempWorkdir(t)
		seedSite(t, nil)
		archiveDir := t.TempDir()
		settings := archivedSettingsPart(t, archiveDir, settingsBody(t, map[string]any{"rate_limit": map[string]any{"enabled": false}}, "media"))
		writeRestoreArchive(t, "weak.tar.gz", []backup.Entry{settings})

		out, _ := captureStreams(t, "")
		if err := performRestore(restoreOptions{archive: "weak.tar.gz", yes: true, acceptArchiveSettings: true}); err != nil {
			t.Fatalf("restore with --accept-archive-settings failed: %v", err)
		}
		if !strings.Contains(out.String(), "Warning: the archive weakens") {
			t.Fatalf("a warning should be printed, got %q", out.String())
		}
		raw, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"enabled":false`) {
			t.Fatalf("settings.json should have been restored: %s", raw)
		}
	})
}
