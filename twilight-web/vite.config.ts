import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'
import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'

// 开发期给数字型属性填的兜底值。**刻意写成字面量而不是 import src/lib/flags.ts**：
// 本文件属于 tsconfig.node.json（lib 只有 ES2023，没有 DOM），而 flags.ts 里
// 有 document.* —— 一旦 import 过来，tsc -b 会报 TS2584「Cannot find name
// 'document'」，把这道类型门整个弄坏。
//
// 代价是这两个数与 flags.ts 的 FALLBACK_HOME_PAGE_SIZE / FALLBACK_BORDER_RADIUS
// 各存一份。它们是**兜底值**，正常站点永远用不到，所以漂移的后果只是「开发环境
// 初始值与测试断言不一致」——由 shells 的守护测试兜着（data-* 属性的存在性与
// 取值形状在 shell-flags.test.ts 里断言）。改 flags.ts 那两个常量时同步改这里。
const DEV_FALLBACK_HOME_PAGE_SIZE = 12
const DEV_FALLBACK_BORDER_RADIUS = 4

// 公开站点的 HTML 外壳**不在本工程里**——它由 Go 后端持有并渲染
// （twilight-blog/internal/view/template/index.html），因为 `<meta name="description">`
// 与 `<title>` 要带上服务端才知道的站点描述，等 SPA 启动再纠正就已经晚了。
//
// 本工程因此只贡献 src/main.tsx 这一个构建入口，产物是带内容指纹的 JS/CSS，
// 以及一份资源清单（build.manifest）——Go 靠清单才知道入口文件叫什么名字。
const INDEX_SHELL = fileURLToPath(
  new URL('../twilight-blog/internal/view/template/index.html', import.meta.url),
)

/**
 * ink-design 的**源码**目录（已安装包内自带 `src/`，见其 package.json 的
 * `files: ["dist", "src"]`）。
 *
 * 为什么不能直接用包的入口：`dist/index.mjs` 是 Rollup 拍平的单文件产物，
 * 145 个导出只有一条 `export {}` 语句，且包 package.json **没有 sideEffects
 * 字段**——于是「引 1 个组件」与「引 145 个组件」的代价完全相同（实测：单独
 * `import { Button }` = 318.1 KB，裸 import 也是 318.1 KB）。应用只用到 18 个
 * 具名导出，其余全是白付。
 *
 * 改指源码后由 Vite/Rollup 逐模块打包，实测 328.6 → 36.8 KB。同理 `@/` 必须
 * 一并映射：InkUI 源码内部统一用 `@/X` 互相引用，而本工程自己一次都没用过
 * `@/`（已核对），所以这条别名不会误伤本工程的任何导入。
 *
 * 两个前提，改动前请重新确认（`ink-design-alias.test.ts` 会在崩掉时报警）：
 *   1. 每个组件的样式只覆盖它自己要用的类（P2-4 把样式表按组件拆成了
 *      src/styles/parts/*.css，各组件源码 import 自己那几段）。所以这里
 *      「丢掉未使用组件」现在是**应当**连带丢掉它的样式的——与改前缀时代的
 *      契约正好相反，那时样式全部来自 ink-design/styles.css，丢组件不该丢样式；
 *   2. 其裸依赖只有 react / react-dom（本工程都有），否则 alias 指过来会解析不到。
 */
const INK_SRC = fileURLToPath(new URL('./node_modules/ink-design/src', import.meta.url))

/**
 * 开发服务器需要一份 index.html，而工程里已经没有（见上）。这里读**同一份**
 * Go 模板，把占位符换成开发期的取值，再交给 Vite 的 HTML 管线
 * （transformIndexHtml 负责注入 /@vite/client 与 HMR 运行时）。
 *
 * 开发期不必填「正确的」描述：那段文案存在后端 settings.json 里，dev server
 * 够不到它。SPA 启动后 lib/site.ts 会照常把它改成真实值。
 *
 * **每个占位符都必须在这里处理掉。** 漏一个的后果不是报错，而是那个
 * `{{.Xxx}}` 字面量被原样发到页面上——前端读到的属性值就是那串花括号，
 * Number.parseInt 得到 NaN，于是静默回落到兜底值。表现是「站点的这项设置
 * 在开发环境里怎么改都没反应」，而构建、类型检查、单测全绿。
 *
 * 这条**踩过**：外壳新增 data-home-page-size / data-border-radius 之后，
 * 这里没同步，dev 下两个属性一直是 `{{...}}`。所以下面刻意用具名常量、
 * 与模板占位符一一对应，改动时对照 template/index.html 的注释清点一遍。
 *
 * 只在 serve 阶段生效，构建产物不受影响。
 */
function indexShellPlugin(): Plugin {
  return {
    name: 'twilight-index-shell',
    apply: 'serve',
    configureServer(server) {
      // 直接调用（而不是 return 一个函数）：这样中间件排在 Vite 内置中间件**之前**，
      // 才有机会接管 "/"。
      server.middlewares.use((req, res, next) => {
        const pathname = (req.url ?? '/').split('?')[0]
        if (pathname !== '/' && pathname !== '/index.html') {
          next()
          return
        }
        let shell: string
        try {
          shell = readFileSync(INDEX_SHELL, 'utf8')
        } catch (error) {
          next(error)
          return
        }
        const html = shell
          .replace(/\{\{range \.Stylesheets\}\}[\s\S]*?\{\{end\}\}/g, '')
          .replace(
            /\{\{range \.Scripts\}\}[\s\S]*?\{\{end\}\}/g,
            '<script type="module" src="/src/main.tsx"></script>\n',
          )
          .replace(/\{\{\.Description\}\}/g, '')
          .replace(/\{\{\.Title\}\}/g, 'Twilight')
          // 开发期功能开关全开：dev server 够不到 settings.json，而开发时
          // 总是需要看到全部功能（要调试搜索禁用态，改测试里的 mock 即可）。
          // 它落在 <html data-search-enabled="..."> 属性里——开关**不能**走内联
          // 脚本，站点的 `script-src 'self'` 会让浏览器直接拒绝执行
          // （详见 internal/view/template/index.html）。
          .replace(/\{\{\.SearchEnabled\}\}/g, 'true')
          // 这两个是数字型属性，走同一通道。填**兜底值**而不是留空：
          // 前端对「属性缺失」与「属性是垃圾」都回落到同一个兜底常量
          // （flags.ts 的 FALLBACK_HOME_PAGE_SIZE / FALLBACK_BORDER_RADIUS），
          // 但填上真实数字能让页面上的属性看起来与生产一致，便于对照排查。
          // 数值必须与那两个常量一致，否则开发环境看到的默认值与测试里断言的不同。
          .replace(/\{\{\.HomePageSize\}\}/g, String(DEV_FALLBACK_HOME_PAGE_SIZE))
          .replace(/\{\{\.BorderRadius\}\}/g, String(DEV_FALLBACK_BORDER_RADIUS))
        server
          .transformIndexHtml(pathname, html)
          .then((transformed) => {
            res.statusCode = 200
            res.setHeader('Content-Type', 'text/html; charset=utf-8')
            res.end(transformed)
          })
          .catch(next)
      })
    },
  }
}

export default defineConfig({
  plugins: [react(), indexShellPlugin()],
  resolve: {
    // 数组形态：正则别名必须与字符串别名共存，而对象键只能是精确字符串。
    alias: [
      { find: 'twilight-blog-api', replacement: fileURLToPath(new URL('../twilight-blog-api/src/index.ts', import.meta.url)) },
      // media.ts reads a file only when a caller passes a filesystem path. That
      // import is deferred and never reached in the browser, but the specifier
      // still has to resolve for the bundler.
      { find: 'node:fs/promises', replacement: fileURLToPath(new URL('./src/shims/fs-promises.ts', import.meta.url)) },
      // ink-design 只映射**精确匹配**的包名（理由见 INK_SRC 注释）。刻意不用前缀
      // 匹配：那会把 `ink-design/styles.css` 一起改写掉，而那个子路径必须继续走
      // 包 exports 映射（→ dist/inkui.css）——前缀匹配会让样式解析直接裂开。
      { find: /^ink-design$/, replacement: `${INK_SRC}/index.ts` },
      { find: /^@\//, replacement: `${INK_SRC}/` },
    ],
  },
  server: {
    "host": "127.0.0.1",
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/install': 'http://127.0.0.1:8080',
      '/install_status': 'http://127.0.0.1:8080',
      '/uploads': 'http://127.0.0.1:8080',
    },
  },
  build: {
    outDir: 'dist',
    // 资源清单：入口 chunk 的文件名带内容指纹，只有构建结束才知道，
    // 而引用它的那一行现在写在 Go 模板里。清单就是这个答案的载体。
    manifest: true,
    rollupOptions: {
      // 显式的 JS 入口：没有 index.html 可当入口了。
      input: fileURLToPath(new URL('./src/main.tsx', import.meta.url)),
      treeshake: {
        /**
         * 只对 ink-design **纯 JS 源码**声明「无副作用」，其余一律 true（Rollup 默认）。
         *
         * 为什么不能写 `return null`：运行时 Rollup 把 null 也当默认处理，但它的
         * 类型是 `(id: string) => boolean`，返回 null 过不了 tsc，让 tsc -b 这道门失效。
         *
         * 这一步是 tree-shaking 能生效的**必要条件**：别名把模块换成了源码，但
         * 包 package.json 里没有 `sideEffects` 字段，Rollup 因此保守地认为每个模块
         * 都可能有副作用，进而保留整张依赖图。声明无副作用之后，未使用的组件会被
         * 真正删掉（实测 156.2 → 36.8 KB）。
         *
         * CSS 必须**先放行**：P2-4 之后组件源码里有 `import '../styles/parts/xx.css'`，
         * 那正是「用到这个组件就带上这段样式」的表达。要是 .css 也被判成无副作用，
         * Rollup 会把它们连同未用组件一起摇掉——构建不报错、类型检查不报错、
         * 单测也不报错，只有页面上样式消失。所以 .css 一律返回 true。
         */
        moduleSideEffects(id) {
          if (id.includes('.css')) return true
          return !id.includes('/node_modules/ink-design/src/')
        },
      },
    },
  },
})
