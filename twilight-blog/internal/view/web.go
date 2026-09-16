// Package view 负责把 HTTP 请求接到内部实现上，并内嵌前端产物。
//
// 分工：router.go 组装依赖并划分路由（公开 / 需登录 / 管理员），
// web.go 提供静态资源与 SPA 回退，index.go 渲染 HTML 外壳，
// handler/ 下是具体端点实现。
package view

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
)

//go:embed all:web
var webAssets embed.FS

// registerWeb 把内嵌的前端产物挂到根路径上。
//
// 资源的来源是 internal/view/web 目录，它由根目录的 build.py 从
// twilight-web/dist 拷贝而来，**属于生成物而非源码**——改了前端要重跑
// build.py 才能在这里生效。
//
// 与改动前最大的差别：**HTML 外壳不再来自内嵌产物**。twilight-web 里已经没有
// index.html（它挪到了 template/index.html，见 index.go），因此在嵌入目录里
// 也找不到它——外壳由本函数每次请求现渲染，好让 `<meta name="description">`
// 与 `<title>` 带上**当前**的站点描述。嵌入目录只提供带指纹的静态资源
// （JS/CSS/字体）与那份资源清单。
func registerWeb(e *echo.Echo, cfg *config.Config) error {
	webFS, err := fs.Sub(webAssets, "web")
	if err != nil {
		return fmt.Errorf("extract embedded web assets: %w", err)
	}

	// 入口 chunk 的文件名带内容指纹，只有 Vite 的清单知道。启动时解析一次：
	// 清单随二进制一起被嵌入，运行期不会变（换前端=换二进制）。
	stylesheets, scripts, err := resolveIndexAssets(webFS)
	if err != nil {
		return fmt.Errorf("resolve front-end entry assets: %w", err)
	}
	indexTmpl, err := parseIndexTemplate()
	if err != nil {
		return err
	}

	serveIndex := func(c echo.Context) error {
		// 每次都读一遍当前配置：站点描述与搜索开关都可以在运行期被改
		// （PUT /api/admin/site / settings.json），值班到启动时渲染的副本上
		// 就会得到「改完刷新还是旧的」。
		body, err := renderIndex(indexTmpl, newIndexData(cfg.SiteDescriptionValue(), cfg.SearchEnabledValue(), stylesheets, scripts))
		if err != nil {
			return err
		}
		return c.HTMLBlob(http.StatusOK, body)
	}

	serve := func(c echo.Context) error {
		requestPath := c.Request().URL.Path
		if requestPath == "" {
			requestPath = "/"
		}

		// 未知的 /api 路径维持 404，不要回退成 index.html：
		// 否则前端把接口路径写错时会拿到一坨 HTML 并当成 JSON 解析，
		// 排查成本远高于直接看到一个 404。
		if requestPath == "/api" || strings.HasPrefix(requestPath, "/api/") {
			return echo.ErrNotFound
		}

		// 点开头的路径段同样 404，理由与 /api 一致但多一层：内嵌目录里唯一的
		// 隐藏条目是 .vite/manifest.json，那是**构建元数据**（列出全部产物路径），
		// 只对服务端有意义。把它当普通静态文件回放，等于把「这里有个文件」
		// 也一并告诉了任何人。
		if hasHiddenSegment(requestPath) {
			return echo.ErrNotFound
		}

		// 路径先 Clean 再经 fs.ValidPath 校验，挡掉 ../ 之类的穿越尝试；
		// 任何不合法或查不到对应文件的路径都回退到 index.html，
		// 这正是 SPA history 路由能正常工作的原因。
		name := strings.TrimLeft(requestPath, "/")
		name = path.Clean(name)
		if name == "" || name == "." || name == "index.html" {
			return serveIndex(c)
		}
		if !fs.ValidPath(name) {
			return serveIndex(c)
		}
		if fileExists(webFS, name) {
			http.ServeFileFS(c.Response(), c.Request(), webFS, name)
			return nil
		}

		// SPA history 回退：所有未知的非 API 路径都返回 index.html，
		// 由前端路由自行解析。
		return serveIndex(c)
	}

	e.GET("/", serve)
	e.HEAD("/", serve)
	e.GET("/*", serve)
	e.HEAD("/*", serve)
	return nil
}

// hasHiddenSegment 报告路径里是否有以点开头的路径段（如 /.vite/manifest.json）。
//
// 长度为 1 的 "." 不算：它是 path.Clean 会消掉的普通相对段，
// 真正要挡的是隐藏**条目**（.vite、.git、.env 之类）。
func hasHiddenSegment(requestPath string) bool {
	for _, segment := range strings.Split(requestPath, "/") {
		if len(segment) > 1 && strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

// fileExists 判断内嵌文件系统里是否存在该文件（目录不算命中）。
// 用 Open + Stat 而不是 fs.Stat，是为了能在这里吞掉错误——
// 对调用方来说「打不开」与「不存在」都是「没有这个文件」。
func fileExists(fsys fs.FS, name string) bool {
	f, err := fsys.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return false
	}
	return !info.IsDir()
}
