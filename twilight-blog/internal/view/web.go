// Package view 负责把 HTTP 请求接到内部实现上，并内嵌前端产物。
//
// 分工：router.go 组装依赖并划分路由（公开 / 需登录 / 管理员），
// web.go 提供静态资源与 SPA 回退，handler/ 下是具体端点实现。
package view

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/labstack/echo/v4"
)

//go:embed all:web
var webAssets embed.FS

// registerWeb 把内嵌的前端产物挂到根路径上。
//
// 资源的来源是 internal/view/web 目录，它由根目录的 build.py 从
// twilight-web/dist 拷贝而来，**属于生成物而非源码**——改了前端要重跑
// build.py 才能在这里生效。index.html 在启动时读一次并常驻内存，
// 所以替换它同样需要重新构建二进制。
func registerWeb(e *echo.Echo) error {
	webFS, err := fs.Sub(webAssets, "web")
	if err != nil {
		return fmt.Errorf("extract embedded web assets: %w", err)
	}

	index, err := fs.ReadFile(webFS, "index.html")
	if err != nil {
		return fmt.Errorf("read embedded index.html: %w", err)
	}

	serveIndex := func(c echo.Context) error {
		return c.HTMLBlob(http.StatusOK, index)
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
