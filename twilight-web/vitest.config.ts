// vitest.config.ts —— 测试专用配置
// 与 vite.config.ts 保持独立的别名清单：测试里 twilight-blog-api 仍指向源码，
// node:fs/promises 仍然指向浏览器替身（client.ts → twilight-blog-api 的动态
// import 在 jsdom 里不该解析到真实 Node API）。
//
// ink-design 的两条别名与 vite.config.ts **必须逐字一致**（`ink-design-alias.test.ts`
// 直接比对两份配置对象的 alias，漂移就会红）：构建走源码、测试走 dist 是最糟的
// 分叉——「测试绿、构建红」或反过来都会让人以为改对了。
import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

/** 与 vite.config.ts 的 INK_SRC 同一个目录；改一处必须改另一处。 */
const INK_SRC = fileURLToPath(new URL('./node_modules/ink-design/src', import.meta.url))

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: [
      { find: 'twilight-blog-api', replacement: fileURLToPath(new URL('../twilight-blog-api/src/index.ts', import.meta.url)) },
      { find: 'node:fs/promises', replacement: fileURLToPath(new URL('./src/shims/fs-promises.ts', import.meta.url)) },
      { find: /^ink-design$/, replacement: `${INK_SRC}/index.ts` },
      { find: /^@\//, replacement: `${INK_SRC}/` },
    ],
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    // 别名指进 node_modules 后，这些文件默认会被当成「外部依赖」交给 Node 直接
    // 加载——而 Node 不认识 .tsx。必须内联，让 Vite 的转译管线处理它们。
    server: { deps: { inline: [/ink-design/] } },
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html', 'json-summary', 'json'],
      // 不写 `all: true`：该选项在 Vitest 3 起已被移除（未知键会被忽略），
      // 但它不在 CoverageOptions 的类型里，留着会让 `tsc -b` 报错。
      // 「未被测试导入的文件也计入」现在由 include 独立保证——A/B 实测：
      // 去掉前后覆盖率表逐行完全一致（见 debug/cov-table-*.txt）。
      include: ['src/**/*.{ts,tsx}'],
      // src/types 只放纯类型声明（.d.ts），没有可执行语句；留着会让覆盖率表
      // 多一行 0/0 的噪音。0.1.4 之后这里已经空了（ink-design 的类型补丁随
      // 上游重建 dist 一起删除），规则留着是为了下次有声明文件时不必再想起来加。
      // src/lib/dialog.tsx 同理：它只是 `export { ... } from 'ink-design'` 这一行
      // 纯转发，v8 会给「零语句模块」记一条空记录并算成 0%，把整站阈值拖垮。
      // 它没有可覆盖的代码；文件头注明了「加逻辑就把它移出 exclude」。
      exclude: ['src/vite-env.d.ts', 'src/types/**', 'src/test/**', 'src/lib/dialog.tsx'],
      thresholds: {
        lines: 100,
        functions: 100,
        statements: 100,
        branches: 100,
      },
    },
  },
})
