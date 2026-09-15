// vitest.config.ts —— 测试专用配置
// 与 vite.config.ts 保持独立的别名清单：测试里 twilight-blog-api 仍指向源码，
// node:fs/promises 仍然指向浏览器替身（client.ts → twilight-blog-api 的动态
// import 在 jsdom 里不该解析到真实 Node API）。
import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      'twilight-blog-api': fileURLToPath(new URL('../twilight-blog-api/src/index.ts', import.meta.url)),
      'node:fs/promises': fileURLToPath(new URL('./src/shims/fs-promises.ts', import.meta.url)),
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html', 'json-summary', 'json'],
      all: true,
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['src/vite-env.d.ts', 'src/test/**'],
      thresholds: {
        lines: 100,
        functions: 100,
        statements: 100,
        branches: 100,
      },
    },
  },
})
