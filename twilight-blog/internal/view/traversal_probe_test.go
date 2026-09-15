package view_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestTraversalPathsNeverBypassAdminAuth 是第五轮渗透测试的守护测试。
//
// 背景：动态测试发现 echo v4.12 对含 `..` 的原始路径有一个反直觉但安全的
// 行为——路由器不解析点段，`/api/admin/../admin/users` 永远到不了真正的
// `/api/admin/users` 路由，而是落进 echo 自动注册的分组级兜底路由
// `echo_route_not_found /api/admin/*`。由于兜底路由属于 admin 分组，
// RequireAdmin 对它同样生效：未鉴权 401、已鉴权管理员 404。
//
// 这里钉死两条独立性质（任何一条被破坏，测试都会精确失败）：
//  1. **分组中间件覆盖未匹配路径**：未鉴权访问任何 `/api/admin/..` 形态的
//     原始路径必须 401——不是 404，更不是 200。反证方式：把 router.go 里
//     admin 分组的 RequireAdmin 拆掉 → 未鉴权请求拿到 404，测试立即失败。
//  2. **路由器不做点段规范化**：即使是合法管理员会话，`..` 变体也拿不到
//     用户列表（404 而非 200）。若未来升级的 echo 开始解析点段，
//     「管理员会话 + 路径变体」会拿到 200 + 用户列表，测试立即失败——
//     那意味着注入形态（例如 `/api/admin/../admin/users/1` 命中别的参数
//     路由）重新变得可能，需要重新做一轮授权面审查。
//
// 附带断言双斜杠与大小写变体要么被拒、要么落进 SPA 回退（text/html），
// 绝不允许以 application/json 的形态吐出管理数据。
func TestTraversalPathsNeverBypassAdminAuth(t *testing.T) {
	server, _, _ := newTestServer(t)
	admin := installFreshBlog(t, server)

	adminPrefixed := []string{
		"/api/admin/../admin/users",
		"/api/admin/../users",
		"/api/admin/../site",
		"/api/admin/../../api/admin/users",
	}
	for _, p := range adminPrefixed {
		anon := newAPI(t, server.URL).call(http.MethodGet, p+"?page=1&page_size=12", nil, nil)
		if anon.status != http.StatusUnauthorized {
			t.Fatalf("%s as anonymous: status = %d, want 401 (group middleware must cover unmatched admin paths); body: %s",
				p, anon.status, truncate(anon.body))
		}
		authed := admin.call(http.MethodGet, p+"?page=1&page_size=12", nil, nil)
		if authed.status == http.StatusOK && strings.Contains(string(authed.body), `"items"`) {
			t.Fatalf("%s as admin: path variant resolved to a real route and returned data: %s",
				p, truncate(authed.body))
		}
	}

	// 其余变体（编码点段、大小写、双斜杠、%2F）不落在 /api/admin 前缀下，
	// 只断言「绝不吐出管理数据」：要么被拒，要么落进 SPA 回退。
	otherVariants := []string{
		"/api/%2e%2e/api/admin/users",
		"/api%2Fadmin%2Fusers",
		"/api/ADMIN/users",
		"//api/admin/users",
	}
	for _, p := range otherVariants {
		for _, who := range []struct {
			name string
			call func(method, path string, body any, headers map[string]string) response
		}{
			{"anonymous", newAPI(t, server.URL).call},
			{"admin", admin.call},
		} {
			res := who.call(http.MethodGet, p+"?page=1&page_size=12", nil, nil)
			if res.status == http.StatusOK && strings.Contains(string(res.body), `"items"`) {
				ct := res.header.Get("Content-Type")
				t.Fatalf("%s as %s: status=200 content-type=%q body=%s",
					p, who.name, ct, truncate(res.body))
			}
		}
	}
}
