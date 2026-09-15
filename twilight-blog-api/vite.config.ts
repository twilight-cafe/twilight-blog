// 把 twilight-blog-api 打包成可对外发布的库：ESM（index.js）与 CJS（index.cjs）
// 双产物，类型声明由 tsc -p tsconfig.build.json 另外生成。
//
// 有一点容易误解：本仓库里的 twilight-web **并不消费这份产物**——它通过 vite
// alias 直引 src/index.ts（见 twilight-web/vite.config.ts）。所以这个构建只在
// 对外发布时（prepublishOnly）才有意义，改动这里不会影响博客自身的构建结果。
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';

export default defineConfig({
  build: {
    lib: {
      entry: fileURLToPath(new URL('./src/index.ts', import.meta.url)),
      name: 'TwilightBlogApi',
      formats: ['es', 'cjs'],
      fileName: (format) => (format === 'es' ? 'index.js' : 'index.cjs'),
    },
    rollupOptions: {
      // node 内置模块标为外部依赖、不打包进产物，由消费方负责解析。
      // 浏览器端的消费方需要自行提供替身——本仓库里的对应物是
      // twilight-web/src/shims/fs-promises.ts，通过 alias 挂上去。
      external: ['node:fs/promises', 'node:path', 'node:buffer'],
    },
  },
});
