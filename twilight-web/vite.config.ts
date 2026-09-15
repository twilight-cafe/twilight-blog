import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
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
  },
})
