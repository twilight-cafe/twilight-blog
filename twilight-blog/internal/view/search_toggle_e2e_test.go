package view_test

import (
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/config"
)

/* -------------------------------------------------------------------------- */
/* 搜索开关：外壳注入 + 路由消失                                               */
/* -------------------------------------------------------------------------- */

// TestIndexShellAnnouncesSearchFlag 钉住「服务端注入参数到 index.html」这一半契约：
// 外壳是 SPA 在首渲染前唯一可靠的开关来源，它注入的 searchEnabled 必须跟随配置，
// 键缺失（老 settings.json 升级上来）视为启用。
//
// 反证方式：把 template/index.html 里的 {{.SearchEnabled}} 改回硬编码 true，
// 或把 index.go 的 newIndexData 参数拆掉，"search_enabled=false 注入 false" 会失败。
// shellAnnounces 归一化空白后再匹配。html/template 在 <script> 的 JS 上下文里
// 输出布尔值时会在两侧插入防御性空格（`searchEnabled:  true`），
// 直接匹配字面量会跟它的排版策略耦合。
func shellAnnounces(t *testing.T, body []byte, want string) {
	t.Helper()
	compact := strings.Join(strings.Fields(string(body)), " ")
	if !strings.Contains(compact, want) {
		t.Fatalf("the shell does not announce %q; body:\n%s", want, truncate(body))
	}
}

func TestIndexShellAnnouncesSearchFlag(t *testing.T) {
	t.Run("missing key defaults to enabled", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		anon := newAPI(t, server.URL)

		resp := anon.call(http.MethodGet, "/", nil, nil)
		expectStatus(t, resp, http.StatusOK, "GET / renders the shell")
		shellAnnounces(t, resp.body, "searchEnabled: true")
	})

	t.Run("search_enabled=false injects false", func(t *testing.T) {
		server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
			off := false
			cfg.SearchEnabled = &off
		})
		anon := newAPI(t, server.URL)

		resp := anon.call(http.MethodGet, "/", nil, nil)
		expectStatus(t, resp, http.StatusOK, "GET / renders the shell")
		shellAnnounces(t, resp.body, "searchEnabled: false")
	})
}

// TestSearchRouteVanishesWhenDisabled 钉住「后端也不开放搜索相关 API」：
// 关闭后 /api/search 必须是 404（路由不注册），而不是 503 或 500——
// 关掉的端点应该让探测者以为它从来不存在。
//
// 说明：e2e 挂载本身就是 nil search client（离线前提），所以这条测试证明的是
// 「client 为 nil 时路由不注册」；而 nil client 恰好就是 main.go 在
// search_enabled=false 时传给 Register 的值——同一分支，两种到达方式。
func TestSearchRouteVanishesWhenDisabled(t *testing.T) {
	server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		off := false
		cfg.SearchEnabled = &off
	})
	anon := newAPI(t, server.URL)

	expectStatus(t,
		anon.call(http.MethodGet, "/api/search?q=anything", nil, nil),
		http.StatusNotFound, "GET /api/search while search is disabled")
}

/* -------------------------------------------------------------------------- */
/* 站点设置三态：省略=保持现值 / 数字=更新 / 越界=400                          */
/* -------------------------------------------------------------------------- */

// TestSiteSettingsTriStateOverHTTP 钉住 PUT /api/admin/site 的三态语义与
// GET /api/site 的公开响应形状（三个键都恒输出）。
//
// 「省略=保持现值」是兼容性承重项：SDK 与旧调用方只发 description，
// 服务端若把省略解释成 0，前台代码块限高会在保存描述的瞬间被清掉。
func TestSiteSettingsTriStateOverHTTP(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash admin password: %v", err)
	}
	server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.PasswordHash = string(hash)
	})

	admin := newAPI(t, server.URL)
	expectStatus(t,
		admin.call(http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": adminPassword}, nil),
		http.StatusOK, "POST /api/auth/login")
	anon := newAPI(t, server.URL)

	// 只发 description（不带 code_block_max_lines 键）→ 行数保持现值。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/admin/site", map[string]any{"description": "只改描述"}, map[string]string{"X-CSRF-Token": admin.csrf()}),
		http.StatusOK, "PUT /api/admin/site without a line count")

	var site map[string]any
	decodeInto(t, anon.call(http.MethodGet, "/api/site", nil, nil), &site)
	if site["description"] != "只改描述" {
		t.Fatalf("description = %v, want 只改描述", site["description"])
	}
	if site["code_block_max_lines"] != float64(20) {
		t.Fatalf("code_block_max_lines = %v after a description-only save, want the current 20", site["code_block_max_lines"])
	}
	if _, ok := site["avatar_url"]; !ok {
		t.Fatal("GET /api/site no longer emits avatar_url: the snapshot shape regressed")
	}

	// 显式数字 → 更新，公开读端点立即反映。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/admin/site", map[string]any{"description": "只改描述", "code_block_max_lines": 35}, map[string]string{"X-CSRF-Token": admin.csrf()}),
		http.StatusOK, "PUT /api/admin/site with lines=35")

	decodeInto(t, anon.call(http.MethodGet, "/api/site", nil, nil), &site)
	if site["code_block_max_lines"] != float64(35) {
		t.Fatalf("code_block_max_lines = %v after an explicit save, want 35", site["code_block_max_lines"])
	}

	// 越界 → 400：拒绝而不是夹取。
	for _, bad := range []int{0, 2, 501} {
		expectStatus(t,
			admin.call(http.MethodPut, "/api/admin/site", map[string]any{"description": "只改描述", "code_block_max_lines": bad}, map[string]string{"X-CSRF-Token": admin.csrf()}),
			http.StatusBadRequest, "PUT /api/admin/site with an out-of-range line count")
	}
}
