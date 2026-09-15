import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'
import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'

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
 * 开发服务器需要一份 index.html，而工程里已经没有（见上）。这里读**同一份**
 * Go 模板，把三个占位符换成开发期的取值，再交给 Vite 的 HTML 管线
 * （transformIndexHtml 负责注入 /@vite/client 与 HMR 运行时）。
 *
 * 开发期不必填「正确的」描述：那段文案存在后端 settings.json 里，dev server
 * 够不到它。SPA 启动后 lib/site.ts 会照常把它改成真实值。
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
    alias: {
      'twilight-blog-api': fileURLToPath(new URL('../twilight-blog-api/src/index.ts', import.meta.url)),
      // media.ts reads a file only when a caller passes a filesystem path. That
      // import is deferred and never reached in the browser, but the specifier
      // still has to resolve for the bundler.
      'node:fs/promises': fileURLToPath(new URL('./src/shims/fs-promises.ts', import.meta.url)),
    },
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
    },
  },
})
