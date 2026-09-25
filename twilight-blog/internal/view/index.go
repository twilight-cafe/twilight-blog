package view

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"sort"

	"blog-engine/internal/viewmodel"
)

// 公开站点的 HTML 外壳由本包渲染，而不是由前端工程产出。理由见
// template/index.html 的开头：`<meta name="description">` 与 `<title>` 是站点级
// 文案，只有后端知道它们的当前值，放在前端工程里就只能在 SPA 启动后才被纠正。
//
// 分工：
//   - template/index.html 是外壳本身（带三个占位符）；
//   - index.go 负责解析它、从资源清单里问出入口 chunk，并在每次请求时按当前
//     站点描述渲染。
//
// 每次请求都渲染而不是启动时渲染一次：站点描述是**可以在运行期改**的
// （PUT /api/admin/site），启动时渲染会把描述冻在二进制启动的那一刻，
// 于是「后台改完描述，刷新页面看到旧的」——正是这次改动要消灭的那种 bug。
// 模板很小，渲染成本可以忽略。

//go:embed template/index.html
var indexTemplateFS embed.FS

// indexTemplatePath 是外壳模板在内嵌文件系统里的路径。
const indexTemplatePath = "template/index.html"

// viteManifestPath 是 Vite 的资源清单（twilight-web/vite.config.ts 里
// build.manifest = true）在嵌入目录里的位置。带点号的目录不会对外提供
// （见 web.go 的 hidden-segment 判断），它只是构建元数据。
const viteManifestPath = ".vite/manifest.json"

// indexTitleBrand 是标签页标题的品牌前缀。首页标题的完整形状是
// 「Twilight · <站点描述>」，与前端 useHomeDocumentTitle 一致——两处不一致的话，
// 页面加载完成的一瞬间标题会跳一下。
const indexTitleBrand = "Twilight"

// viteManifestEntry 是资源清单里我们真正用得上的那几个字段。
//
// 刻意只声明这几个：manifest 里还有 src / name / imports 等一长串字段，
// 全量建模只会让人以为服务端依赖它们。真正被依赖的只有「入口是谁」与
// 「入口带哪些 CSS」。
type viteManifestEntry struct {
	File string `json:"file"`
	// CSS 是入口 chunk 依赖的样式表。Vite 会把它们从 JS 里抽出来，
	// 并在 manifest 上登记——这就是「Go 不必知道 index.html 长什么样」
	// 却仍然能拼出正确 <link> 的原因。
	CSS []string `json:"css"`
	// IsEntry 标记构建入口。本应用只有一个入口（src/main.tsx）。
	IsEntry bool `json:"isEntry"`
}

// indexData 是外壳模板的渲染数据。
type indexData struct {
	// Description 是站点描述，已套用「未设置时的内置默认值」。
	Description string
	// Title 是 <title> 的完整内容（含品牌前缀）。
	Title string
	// SearchEnabled 决定外壳下发的站点功能开关：SPA 在首渲染前读它决定
	// 是否挂载搜索入口。它作为 <html data-search-enabled="..."> 输出，
	// 而不是内联脚本——CSP 是 `script-src 'self'` 且不含
	// 'unsafe-inline'/hash/nonce，内联脚本会被浏览器**拒绝执行**。
	// 完整缘由见 template/index.html 的头部说明。
	SearchEnabled bool
	// HomePageSize 是首页每页文章数，作为 <html data-home-page-size="..."> 下发。
	//
	// 它必须与 SearchEnabled 走同一条属性通道而不是 GET /api/site：
	// 首页列表在挂载后**立刻**就要发请求，若等 getSite 回来才知道 pageSize，
	// 首次请求只能用一个写死的兜底值——要么多发一次请求，要么先按错的页数
	// 渲染再重排。属性在入口 chunk 之前就绪，第一次请求就是对的。
	// 属性值是数字（见前端 flags.ts 的读取方式：dataset 永远是 string）。
	HomePageSize int
	// BorderRadius 是站点统一圆角（px），作为 <html data-border-radius="..."> 下发。
	//
	// 走属性通道的理由与 HomePageSize 相同：它是首屏就要定下来的排版参数。
	// 若等 GET /api/site 回来再改 --radius，首屏会先按默认圆角画一遍、
	// 拿到响应后再跳变成设置值——一次可见的「圆角闪动」。
	//
	// 属性值是数字（dataset 永远是 string，见前端 flags.ts 的读取方式）。
	BorderRadius int
	// Stylesheets / Scripts 是站内绝对路径（以 / 开头）。
	Stylesheets []string
	Scripts     []string
}

// parseIndexTemplate 解析内嵌的外壳模板。
//
// 解析放在启动期（registerWeb 里），而不是每次请求：模板是编译进二进制的常量，
// 语法错误属于「构建出了坏产物」，该让进程起不来，而不是让每个页面都返回 500。
func parseIndexTemplate() (*template.Template, error) {
	tmpl, err := template.ParseFS(indexTemplateFS, indexTemplatePath)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", indexTemplatePath, err)
	}
	return tmpl, nil
}

// renderIndex 按 data 渲染外壳模板。
//
// 用 bytes.Buffer 而不是直接写 Response：模板执行到一半失败时，缓冲区里的
// 半截 HTML 不会被发出去——半个文档比一个明确的 500 难排查得多。
func renderIndex(tmpl *template.Template, data indexData) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render %s: %w", indexTemplatePath, err)
	}
	return buf.Bytes(), nil
}

// resolveIndexAssets 从 Vite 的资源清单里问出入口 chunk 与它依赖的样式表。
//
// **为什么需要清单**：产物文件名带内容指纹（assets/main-<hash>.js），只有构建
// 结束后才知道。外壳模板因此不能写死路径，而清单是构建期唯一可靠的答案
// ——过去这个答案藏在 index.html 里，那也正是「HTML 必须留在前端工程」的理由。
//
// 返回的路径统一加上前导 /，因为清单里的 file / css 是相对 base（默认 /）的。
//
// 按 key 排序后再遍历：map 的迭代顺序是随机的，而「有多个入口」属于构建配置
// 出错，排序后报错顺序稳定、输出顺序也稳定（否则每次重启的标签顺序都可能变）。
func resolveIndexAssets(webFS fs.FS) (stylesheets []string, scripts []string, err error) {
	raw, err := fs.ReadFile(webFS, viteManifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", viteManifestPath, err)
	}

	var manifest map[string]viteManifestEntry
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", viteManifestPath, err)
	}

	keys := make([]string, 0, len(manifest))
	for key := range manifest {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	stylesheets = make([]string, 0, 4)
	scripts = make([]string, 0, 1)
	for _, key := range keys {
		entry := manifest[key]
		if !entry.IsEntry {
			continue
		}
		if entry.File == "" {
			return nil, nil, fmt.Errorf("%s: entry %q declares no file", viteManifestPath, key)
		}
		scripts = append(scripts, "/"+entry.File)
		for _, css := range entry.CSS {
			if css == "" {
				continue
			}
			stylesheets = append(stylesheets, "/"+css)
		}
	}
	if len(scripts) == 0 {
		// 没有入口的清单只能来自「构建入口被改坏」，此时页面会是一个空壳：
		// 返回错误让启动失败，比发一个永远白屏的页面好。
		return nil, nil, fmt.Errorf("%s: no entry chunk found", viteManifestPath)
	}
	return stylesheets, scripts, nil
}

// newIndexData 组装外壳的渲染数据。
//
// 描述为空（站长没设置过）时用 viewmodel.DefaultSiteDescription，与前端
// lib/site.ts 的 FALLBACK_DESCRIPTION 是同一句话——两处不一致会让页面在
// SPA 启动的瞬间把描述换掉，正是这次要修的观感问题。
//
// homePageSize 的兜底（<=0 时用 viewmodel.DefaultHomePageSize）与服务端配置层
// 的 ApplyDefaults 是双保险：这里面对的是「配置对象被构造得不完整」这种
// 理论上不该发生的情况，宁可发一个能用的默认值，也不要下发
// data-home-page-size="0"——那会让首页请求一个 page_size=0 的列表。
//
// borderRadius 的兜底**不能照抄上面的 `<= 0`**：圆角的下界就是 0，
// 0 是合法的「直角」。这里只能对**越界**值兜底，否则站长设的直角
// 会在渲染外壳时被悄悄改成默认圆角（设置页显示 0、前台却是 4）。
func newIndexData(description string, searchEnabled bool, homePageSize, borderRadius int, stylesheets, scripts []string) indexData {
	description = viewmodel.SiteDescription(description)
	if homePageSize <= 0 {
		homePageSize = viewmodel.DefaultHomePageSize
	}
	if borderRadius < viewmodel.MinBorderRadius || borderRadius > viewmodel.MaxBorderRadius {
		borderRadius = viewmodel.DefaultBorderRadius
	}
	return indexData{
		Description:   description,
		Title:         indexTitleBrand + " · " + description,
		SearchEnabled: searchEnabled,
		HomePageSize:  homePageSize,
		BorderRadius:  borderRadius,
		Stylesheets:   stylesheets,
		Scripts:       scripts,
	}
}
