package view_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/config"
)

/* -------------------------------------------------------------------------- */
/* 搜索开关：外壳标记 + 路由消失                                               */
/* -------------------------------------------------------------------------- */

// TestIndexShellAnnouncesSearchFlag 钉住「服务端把开关下发给外壳」这一半契约：
// 外壳是 SPA 在首渲染前唯一可靠的开关来源，它标记的 searchEnabled 必须跟随配置，
// 属性缺失（老 settings.json 升级上来）视为启用。
//
// 通道是 <html data-search-enabled="...">，**不是内联脚本**——CSP 的
// script-src 不含 'unsafe-inline'/hash/nonce，内联脚本会被浏览器拒绝执行。
// 所以这里比对的是属性字面量；而「外壳里不许出现可执行的内联脚本」由
// TestIndexShellHasNoInlineScript 单独钉住。
//
// 反证方式：把 template/index.html 里的 {{.SearchEnabled}} 改回硬编码 true，
// 或把 index.go 的 newIndexData 参数拆掉，"search_enabled=false 下发 false" 会失败。
// shellAnnounces 归一化空白后再匹配，避免与模板的排版策略耦合。
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
		shellAnnounces(t, resp.body, `data-search-enabled="true"`)
	})

	t.Run("search_enabled=false announces false", func(t *testing.T) {
		server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
			off := false
			cfg.SearchEnabled = &off
		})
		anon := newAPI(t, server.URL)

		resp := anon.call(http.MethodGet, "/", nil, nil)
		expectStatus(t, resp, http.StatusOK, "GET / renders the shell")
		shellAnnounces(t, resp.body, `data-search-enabled="false"`)
	})
}

// TestIndexShellHasNoInlineScript 钉住「外壳里不得出现可执行的内联脚本」。
//
// 为什么必须单独钉一条：CSP 是 `script-src 'self'`（middleware/security.go），
// 任何内联脚本都会被浏览器**拒绝执行**——而「被拒绝」在服务端完全看不见：
// 响应照发、状态码 200、测试照绿。这里曾经用
// `<script>window.__SITE__ = { searchEnabled: ... }</script>` 下发搜索开关，
// 于是它上线后从未生效过：搜索关闭时前端读不到那个 false，走「缺省=全开」
// 兜底渲染出搜索入口，点进去 404。当时的断言只检查「HTML 文本里有这个
// 字符串」，而那句话对「浏览器肯不肯执行」没有任何约束。
//
// 判据只看**通道**，与脚本内容对不对无关。
//
// 反证方式：把 template/index.html 里任意一行改成 <script>…</script>。
func TestIndexShellHasNoInlineScript(t *testing.T) {
	server, _, _ := newTestServer(t)
	anon := newAPI(t, server.URL)

	resp := anon.call(http.MethodGet, "/", nil, nil)
	expectStatus(t, resp, http.StatusOK, "GET / renders the shell")

	if tags := executableInlineScripts(resp.body); len(tags) != 0 {
		t.Fatalf("the shell ships %d executable inline <script> element(s); the site CSP "+
			"(`script-src 'self'`, no unsafe-inline/hash/nonce) makes the browser refuse "+
			"them silently: %v\nbody:\n%s", len(tags), tags, truncate(resp.body))
	}
}

// executableInlineScripts 返回 body 里所有会被浏览器执行的**内联** <script> 标签。
//
// 两个例外不算：带 src 的（那是外部文件，走 script-src 'self' 的常规加载），
// 以及 type 是数据块的（浏览器不执行它们，CSP 也不约束）。大小写与换行都不敏感
// ——模板里标签与它的属性可能分属不同行。
func executableInlineScripts(body []byte) []string {
	tagRe := regexp.MustCompile(`(?is)<script\b([^>]*)>`)
	attrRe := regexp.MustCompile(`(?i)\b([a-zA-Z-]+)\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)

	var found []string
	for _, tag := range tagRe.FindAllStringSubmatch(string(body), -1) {
		attrs := make(map[string]string)
		for _, attr := range attrRe.FindAllStringSubmatch(tag[1], -1) {
			attrs[strings.ToLower(attr[1])] = strings.Trim(attr[2], `"'`)
		}
		if _, ok := attrs["src"]; ok {
			continue
		}
		if isDataBlockScriptType(attrs["type"]) {
			continue
		}
		found = append(found, strings.Join(strings.Fields(tag[0]), " "))
	}
	return found
}

// isDataBlockScriptType 报告 <script type> 是否是「浏览器不执行」的数据块。
// 这类元素不会被当作脚本执行，因此也不受 script-src 约束。
func isDataBlockScriptType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "application/json", "application/ld+json", "text/template", "text/x-template":
		return true
	}
	return false
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

// TestIndexShellAnnouncesHomePageSize 钉住「首页每页篇数下发给外壳」这一半契约。
//
// 它走的是与搜索开关**同一条**属性通道，而不是 GET /api/site：首页列表在挂载后
// 立刻就要发请求，等 /api/site 回来才知道 pageSize 意味着要么多发一次请求、
// 要么先按错的页数渲染再重排。属性在入口 chunk 之前就绪，第一次请求就是对的。
//
// 反证方式：把 index.go 的 newIndexData 参数拆掉（外壳退回没有这个属性），
// 或把 web.go 的调用改回只传 searchEnabled —— 前两个子用例都会失败。
func TestIndexShellAnnouncesHomePageSize(t *testing.T) {
	t.Run("default announces the default size", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		anon := newAPI(t, server.URL)

		resp := anon.call(http.MethodGet, "/", nil, nil)
		expectStatus(t, resp, http.StatusOK, "GET / renders the shell")
		shellAnnounces(t, resp.body, `data-home-page-size="12"`)
	})

	t.Run("configured size is announced", func(t *testing.T) {
		server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
			cfg.HomePageSize = 5
		})
		anon := newAPI(t, server.URL)

		resp := anon.call(http.MethodGet, "/", nil, nil)
		expectStatus(t, resp, http.StatusOK, "GET / renders the shell")
		shellAnnounces(t, resp.body, `data-home-page-size="5"`)
	})

	// 配置对象不完整时（0）不能把 0 发到页面上：前端会照它发一个
	// page_size=0 的列表请求，而列表端点的 `page_size >= 1` 会直接 400。
	t.Run("zero falls back instead of announcing an invalid size", func(t *testing.T) {
		server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
			cfg.HomePageSize = 0
		})
		anon := newAPI(t, server.URL)

		resp := anon.call(http.MethodGet, "/", nil, nil)
		expectStatus(t, resp, http.StatusOK, "GET / renders the shell")
		shellAnnounces(t, resp.body, `data-home-page-size="12"`)
	})
}

// TestSiteSettingsHomePageSizeOverHTTP 钉住 home_page_size 的三态语义与
// 公开响应形状，边界与行数同构。
//
// 「省略=保持现值」是兼容性承重项：只发 description 的旧客户端不该把首页篇数
// 打回默认值。上界 12 是**硬约束**（等于列表端点的 maxPageSize），越界必须 400
// ——放行就等于让首页的第一次请求必然失败。
func TestSiteSettingsHomePageSizeOverHTTP(t *testing.T) {
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

	var site map[string]any
	// 公开读端点恒输出 home_page_size（新字段，前端设置页靠它回显）。
	decodeInto(t, anon.call(http.MethodGet, "/api/site", nil, nil), &site)
	if site["home_page_size"] != float64(12) {
		t.Fatalf("home_page_size = %v in the public snapshot, want the default 12", site["home_page_size"])
	}

	// 只发 description（不带 home_page_size 键）→ 篇数保持现值。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/admin/site", map[string]any{"description": "只改描述"}, map[string]string{"X-CSRF-Token": admin.csrf()}),
		http.StatusOK, "PUT /api/admin/site without a page size")
	decodeInto(t, anon.call(http.MethodGet, "/api/site", nil, nil), &site)
	if site["home_page_size"] != float64(12) {
		t.Fatalf("home_page_size = %v after a description-only save, want the current 12", site["home_page_size"])
	}

	// 显式数字 → 更新，公开读端点立即反映，外壳也跟着变。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/admin/site", map[string]any{"description": "只改描述", "home_page_size": 7}, map[string]string{"X-CSRF-Token": admin.csrf()}),
		http.StatusOK, "PUT /api/admin/site with page size 7")
	decodeInto(t, anon.call(http.MethodGet, "/api/site", nil, nil), &site)
	if site["home_page_size"] != float64(7) {
		t.Fatalf("home_page_size = %v after an explicit save, want 7", site["home_page_size"])
	}
	shellAnnounces(t, anon.call(http.MethodGet, "/", nil, nil).body, `data-home-page-size="7"`)

	// 改篇数不能把行数带跑（两个字段各自独立）。
	if site["code_block_max_lines"] != float64(20) {
		t.Fatalf("code_block_max_lines = %v after changing the page size, want 20", site["code_block_max_lines"])
	}

	// 越界 → 400：拒绝而不是夹取。上界之外（13+）尤其重要，见上面说明。
	for _, bad := range []int{-1, 0, 13, 100} {
		expectStatus(t,
			admin.call(http.MethodPut, "/api/admin/site", map[string]any{"description": "只改描述", "home_page_size": bad}, map[string]string{"X-CSRF-Token": admin.csrf()}),
			http.StatusBadRequest, "PUT /api/admin/site with an out-of-range page size")
	}

	// 越界请求被拒后，值必须保持在上一次成功保存的 7（不是被夹成 12 或 0）。
	decodeInto(t, anon.call(http.MethodGet, "/api/site", nil, nil), &site)
	if site["home_page_size"] != float64(7) {
		t.Fatalf("home_page_size = %v after rejected saves, want the last good value 7", site["home_page_size"])
	}
}

// TestIndexShellAnnouncesBorderRadius 钉住「圆角下发给外壳」这一半契约。
//
// 与首页每页篇数同一条属性通道（<html data-border-radius>），理由也相同：
// 圆角必须在首帧就定下来，等 /api/site 回来再改 --radius 会让首屏先按默认圆角
// 画一遍、拿到响应后再跳变。
//
// 反证方式：把 index.go 的 newIndexData 少传一个参数、或把 web.go 的调用改回
// 不带 BorderRadiusValue() —— 第一个子用例就会失败。
func TestIndexShellAnnouncesBorderRadius(t *testing.T) {
	t.Run("default announces the default radius", func(t *testing.T) {
		server, _, _ := newTestServer(t)
		anon := newAPI(t, server.URL)

		resp := anon.call(http.MethodGet, "/", nil, nil)
		expectStatus(t, resp, http.StatusOK, "GET / renders the shell")
		shellAnnounces(t, resp.body, `data-border-radius="4"`)
	})

	t.Run("configured radius is announced", func(t *testing.T) {
		server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
			radius := 12
			cfg.BorderRadius = &radius
		})
		anon := newAPI(t, server.URL)

		resp := anon.call(http.MethodGet, "/", nil, nil)
		expectStatus(t, resp, http.StatusOK, "GET / renders the shell")
		shellAnnounces(t, resp.body, `data-border-radius="12"`)
	})

	// 这一条是整个圆角功能**最容易写错的地方**，也是它与首页每页篇数的分水岭：
	// 圆角 0 是合法配置（直角），外壳必须原样发 0。
	//
	// 若 index.go 的兜底写成 `if borderRadius <= 0 { 用默认值 }`（从
	// homePageSize 的兜底抄过来就会这样，那儿的 0 确实表示「未设置」），
	// 站长的直角设置会被静默换成 4px —— 设置页显示 0、页面却是圆的，
	// 没有任何报错可查。所以这里必须断言字面量 `data-border-radius="0"`。
	t.Run("zero is announced as zero, not replaced by the default", func(t *testing.T) {
		server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
			radius := 0
			cfg.BorderRadius = &radius
		})
		anon := newAPI(t, server.URL)

		resp := anon.call(http.MethodGet, "/", nil, nil)
		expectStatus(t, resp, http.StatusOK, "GET / renders the shell")
		shellAnnounces(t, resp.body, `data-border-radius="0"`)
	})

	// 真正越界（配置对象被构造得不完整）才走兜底：把 -1 发到页面上，
	// 前端的区间校验会再把它兜回 4，于是「兜底」与「非法」互相抵消、
	// 这个设置项被彻底忽略而无人察觉。
	t.Run("an out-of-range radius falls back instead of being announced", func(t *testing.T) {
		server, _, _ := newTestServerWithConfig(t, func(cfg *config.Config) {
			radius := -1
			cfg.BorderRadius = &radius
		})
		anon := newAPI(t, server.URL)

		resp := anon.call(http.MethodGet, "/", nil, nil)
		expectStatus(t, resp, http.StatusOK, "GET / renders the shell")
		shellAnnounces(t, resp.body, `data-border-radius="4"`)
	})
}

// TestSiteSettingsBorderRadiusOverHTTP 钉住 border_radius 的三态语义与公开响应形状。
//
// 三态与前两项同构，但**0 的归属不同**，是本条测试的全部重点：
//
//   - 「省略」= 保持现值（兼容性承重项：只发 description 的旧客户端不该把
//     站长设的圆角打回默认值）；
//   - 「显式 0」= 真的改成直角，必须被接受并原样下发。
//
// 若把这两者混为一谈（Validate 里写 `radius == 0` 早退、或响应字段带 omitempty），
// 站长保存直角后会拿到 400，或者 GET /api/site 里缺 border_radius 这个键、
// 前端回退到默认的 4 —— 两种都表现为「设置页显示 0、页面却是圆的」。
func TestSiteSettingsBorderRadiusOverHTTP(t *testing.T) {
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

	var site map[string]any
	// 公开读端点恒输出 border_radius（前端设置页靠它回显；字段缺席会与
	// 「值就是 0」无法区分）。
	decodeInto(t, anon.call(http.MethodGet, "/api/site", nil, nil), &site)
	if got, ok := site["border_radius"]; !ok || got != float64(4) {
		t.Fatalf("border_radius = %v (present=%v) in the public snapshot, want the default 4", got, ok)
	}

	// 只发 description（不带 border_radius 键）→ 圆角保持现值。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/admin/site", map[string]any{"description": "只改描述"}, map[string]string{"X-CSRF-Token": admin.csrf()}),
		http.StatusOK, "PUT /api/admin/site without a radius")
	decodeInto(t, anon.call(http.MethodGet, "/api/site", nil, nil), &site)
	if site["border_radius"] != float64(4) {
		t.Fatalf("border_radius = %v after a description-only save, want the current 4", site["border_radius"])
	}

	// 显式非零 → 更新，公开读端点与外壳都立即反映。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/admin/site", map[string]any{"description": "只改描述", "border_radius": 16}, map[string]string{"X-CSRF-Token": admin.csrf()}),
		http.StatusOK, "PUT /api/admin/site with radius 16")
	decodeInto(t, anon.call(http.MethodGet, "/api/site", nil, nil), &site)
	if site["border_radius"] != float64(16) {
		t.Fatalf("border_radius = %v after an explicit save, want 16", site["border_radius"])
	}
	shellAnnounces(t, anon.call(http.MethodGet, "/", nil, nil).body, `data-border-radius="16"`)

	// 改圆角不能把别的字段带跑。
	if site["code_block_max_lines"] != float64(20) || site["home_page_size"] != float64(12) {
		t.Fatalf("saving a radius disturbed other settings: lines=%v size=%v",
			site["code_block_max_lines"], site["home_page_size"])
	}

	// ★ 显式 0（直角）必须被接受，并且真的落下去。
	expectStatus(t,
		admin.call(http.MethodPut, "/api/admin/site", map[string]any{"description": "只改描述", "border_radius": 0}, map[string]string{"X-CSRF-Token": admin.csrf()}),
		http.StatusOK, "PUT /api/admin/site with radius 0 (square)")
	decodeInto(t, anon.call(http.MethodGet, "/api/site", nil, nil), &site)
	if site["border_radius"] != float64(0) {
		t.Fatalf("border_radius = %v after saving 0, want 0: a square corner was silently replaced", site["border_radius"])
	}
	shellAnnounces(t, anon.call(http.MethodGet, "/", nil, nil).body, `data-border-radius="0"`)

	// 越界 → 400：拒绝而不是夹取（-1 与 25 都是审美之外的值）。
	for _, bad := range []int{-1, 25, 100} {
		expectStatus(t,
			admin.call(http.MethodPut, "/api/admin/site", map[string]any{"description": "只改描述", "border_radius": bad}, map[string]string{"X-CSRF-Token": admin.csrf()}),
			http.StatusBadRequest, "PUT /api/admin/site with an out-of-range radius")
	}

	// 越界请求被拒后，值必须保持在最后一次成功保存的 0（不是被夹成 4）。
	decodeInto(t, anon.call(http.MethodGet, "/api/site", nil, nil), &site)
	if site["border_radius"] != float64(0) {
		t.Fatalf("border_radius = %v after rejected saves, want the last good value 0", site["border_radius"])
	}
}
