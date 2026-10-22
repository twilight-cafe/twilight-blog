// @vitest-environment node
//
// 环境必须是 node 而不是全局默认的 jsdom：这个用例要 import 两份 vite 配置，
// 而 vite 会连带加载 esbuild，esbuild 在初始化时校验
// `new TextEncoder().encode('') instanceof Uint8Array`——jsdom 的 TextEncoder
// 与 Node 的 Uint8Array 不同源，该校验在 jsdom 里必然失败并直接终止整个测试文件。
// 本文件只读文件系统、不碰 DOM，用 node 环境是正解，不是绕路。
//
// ink-design-alias.test.ts —— 守护 ink-design 的 tree-shaking 配置
//
// 为什么值得专门守：这里的收益不是「少几个字节」，而是首屏 328.6 → 36.8 KB，
// 代价是三条**隐含契约**同时被押上——①包内继续带 `src/`；②源码内部继续用 `@/`
// 互引；③两份配置的别名不漂移。任何一条静默失效，结果都不是报错，而是首屏
// 悄悄胖回去 290 KB（构建与测试全绿）。
//
// P2-4（2026-09-19）又加了一层：样式改成「跟着组件走」，所以源码里**应当**有
// CSS 副作用导入——下面那条断言的方向因此与改写前相反（详见用例内的说明），
// 更细的样式契约在 inkui-css-parts.test.ts。
//
// 所以这里的断言全部打在**契约**上，而不是打在体积数字上：体积靠构建后实测
// （见交付记录），契约靠本文件。每条断言都验证过「先改坏会红」。

import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'

import viteConfig from '../../vite.config'
import vitestConfig from '../../vitest.config'

const WEB_ROOT = fileURLToPath(new URL('../../', import.meta.url))
const APP_SRC = join(WEB_ROOT, 'src')
const INK_SRC = join(WEB_ROOT, 'node_modules/ink-design/src')

interface AliasEntry {
  find: string | RegExp
  replacement: string
}

/** 路径断言一律先把分隔符归一成 `/`，避免 Windows 混合分隔符造成的假失败。 */
function slash(value: string): string {
  return value.replace(/\\/g, '/')
}

/** 把 array 形态与对象形态的 alias 统一成有序的 [find, replacement] 列表。 */
function normalizeAlias(alias: unknown): [string, string][] {
  if (Array.isArray(alias)) {
    return alias.map((entry) => {
      const item = entry as AliasEntry
      // 正则取 .source 而不是 String()——后者的结果自带两条斜杠（'/^x$/'），
      // 再拿去 new RegExp 就永远匹配不上真实说明符。
      return [item.find instanceof RegExp ? item.find.source : item.find, item.replacement]
    })
  }
  return Object.entries(alias as Record<string, string>).map(([find, replacement]) => [find, replacement])
}

/** 递归收集目录下匹配扩展名的文件（跳过测试目录）。 */
function collectFiles(root: string, extensions: readonly string[], skip: readonly string[] = []): string[] {
  const found: string[] = []
  for (const entry of readdirSync(root, { withFileTypes: true })) {
    const full = join(root, entry.name)
    if (entry.isDirectory()) {
      if (skip.includes(entry.name)) continue
      found.push(...collectFiles(full, extensions, skip))
      continue
    }
    if (extensions.some((extension) => entry.name.endsWith(extension))) found.push(full)
  }
  return found
}

/**
 * ink-design 自带的测试文件（`*.test.ts(x)`）不进包、也不进站点产物：扫描
 * 依赖图时必须排除。0.1.4 新增的 `src/styles/css-imports.test.ts` 会 import
 * `node:fs` / `node:path`，把它们算进「裸依赖」会让下面那条断言误报。
 */
function isInkTestFile(file: string): boolean {
  return /\.test\.tsx?$/.test(file)
}

/** 从源码里收集 `import { A, B } from 'ink-design'` 的具名导入与再导出。 */
function collectAppInkImports(): Set<string> {
  const names = new Set<string>()
  // 新版本组件既可能被直接 import，也可能被站点层再导出（见 lib/dialog.tsx 与
  // lib/motion.ts），两种都算「用到了这个符号」。
  const pattern = /(?:import|export)\s*(?:type\s*)?\{([^}]*)\}\s*from\s*['"]ink-design['"]/g
  for (const file of collectFiles(APP_SRC, ['.ts', '.tsx'], ['test'])) {
    for (const match of readFileSync(file, 'utf8').matchAll(pattern)) {
      for (const raw of match[1].split(',')) {
        const name = raw.trim().split(/\s+as\s+/)[0].trim()
        if (name) names.add(name)
      }
    }
  }
  return names
}

/** 从 ink-design 的入口里收集 `export { A, B } from '...'` 的值导出名。 */
function collectInkExports(): Set<string> {
  const names = new Set<string>()
  const source = readFileSync(join(INK_SRC, 'index.ts'), 'utf8')
  for (const match of source.matchAll(/export\s*\{([^}]*)\}\s*from/g)) {
    for (const raw of match[1].split(',')) {
      const name = raw.trim().split(/\s+as\s+/).pop()?.trim()
      if (name) names.add(name)
    }
  }
  return names
}

describe('ink-design 别名：两份配置必须逐条一致', () => {
  it('vite（构建）与 vitest（测试）的别名清单完全相同', () => {
    const viteAlias = normalizeAlias(viteConfig.resolve?.alias)
    const vitestAlias = normalizeAlias(vitestConfig.resolve?.alias)
    // 顺序也参与比较：别名是按序首匹配，顺序不同等于语义不同。
    expect(vitestAlias).toEqual(viteAlias)
  })

  it('两份配置的 ink-design 条目都指向 node_modules/ink-design/src/index.ts', () => {
    const expected = slash(join(INK_SRC, 'index.ts'))
    for (const config of [viteConfig, vitestConfig]) {
      expect(normalizeAlias(config.resolve?.alias).map(([find, replacement]) => [find, slash(replacement)])).toContainEqual([
        '^ink-design$',
        expected,
      ])
    }
    // 正则条目只能是数组形态；退化成对象会把这两条别名整个丢掉。
    expect(viteConfig.resolve?.alias).toBeInstanceOf(Array)
    expect(vitestConfig.resolve?.alias).toBeInstanceOf(Array)
  })

  it('twilight-blog-api 指向 SDK 的 src 而非 dist 产物', () => {
    // 这条不只是「别名还在」（下面「匹配范围」里已有那条），而是「指向哪儿」。
    // 两者的差别会以最误导人的方式表现：SDK 包的 exports 把包名解析到
    // ./dist/index.js，而 `dist/` 是 **gitignore 掉、未被跟踪** 的构建产物。
    // 于是「实际被打进包里的代码」取决于本机有没有、以及什么时候跑过 SDK 构建：
    //   ① 改完 src 忘了重建 dist → 别名指 dist 时会打进旧逻辑（测试却全绿）；
    //   ② 全新克隆没有 dist → 别名指 dist 时构建直接解析失败。
    // 指 src 则两种都不成立：源码即唯一事实来源。
    //
    // 有真实来由：首页每页篇数那个 bug（请求体漏了 home_page_size）修好源码后，
    // 单测全绿、**打出来的包里却还是旧代码**——因为嵌入的 bundle 早于修复。
    // 复盘时发现「别名到底指 src 还是 dist」当时没有任何断言兜着。
    const expected = slash(join(WEB_ROOT, '../twilight-blog-api/src/index.ts'))
    for (const config of [viteConfig, vitestConfig]) {
      expect(
        normalizeAlias(config.resolve?.alias).map(([find, replacement]) => [find, slash(replacement)]),
      ).toContainEqual(['twilight-blog-api', expected])
    }
  })
})

describe('ink-design 别名：匹配范围', () => {
  const entries = normalizeAlias(viteConfig.resolve?.alias)

  /**
   * 复现 Vite/@rollup/plugin-alias 的语义：把 `find` 当正则对 **说明符做子串替换**，
   * 替换结果与原串不同即视为命中，并按声明顺序取第一条命中。
   */
  function resolveWith(specifier: string): string | null {
    for (const [find, replacement] of entries) {
      const replaced = specifier.replace(new RegExp(find), replacement)
      if (replaced !== specifier) return replaced
    }
    return null
  }

  it('包名本身被映射到源码入口', () => {
    // 归一化分隔符再比：replacement 由 fileURLToPath 拼出，是 Windows 混合分隔符
    // （与工程里既有的 twilight-blog-api 别名同一风格），Vite 侧两种都能解析。
    expect(slash(resolveWith('ink-design') ?? '')).toBe(slash(join(INK_SRC, 'index.ts')))
  })

  it('styles.css 子路径不被劫持（它必须继续走包 exports 映射到 dist/inkui.css）', () => {
    expect(resolveWith('ink-design/styles.css')).toBeNull()
  })

  it('不误伤其它作用域包、相对路径与同前缀包', () => {
    expect(resolveWith('@testing-library/react')).toBeNull()
    expect(resolveWith('ink-design-extra')).toBeNull()
    expect(resolveWith('./local-module')).toBeNull()
    expect(resolveWith('twilight-blog-api')).not.toBeNull() // 既有别名仍在
  })

  it('源码内部的 @/ 前缀被映射到 ink-design 的 src', () => {
    expect(slash(resolveWith('@/utils/cx') ?? '')).toBe(slash(join(INK_SRC, 'utils/cx')))
  })
})

describe('ink-design tree-shaking：安全前提', () => {
  it('组件源码里**有** CSS 副作用导入（样式跟着组件走，丢组件就该丢样式）', () => {
    // 这条契约在 P2-4（2026-09-19）**反转**了。改写之前是「源码里没有任何 CSS
    // 副作用导入，所以丢组件不会丢样式」——那时样式全部来自
    // `import 'ink-design/styles.css'` 这张整表，组件与样式是解耦的。
    //
    // P2-4 把样式拆成 src/styles/parts/*.css 并让各组件只 import 自己那几段，
    // 于是「引 1 个组件」和「引 145 个组件」的样式代价才第一次真正不同。
    // 代价是这条断言的方向必须跟着反过来：**没有** CSS 导入才是有问题的
    // （说明片段拆分丢了，或者又装回了不带 parts 的注册表副本）。
    // 更细的守卫（路径可解析、深度一致、app.css 在最后）见 inkui-css-parts.test.ts。
    const consumers: string[] = []
    for (const file of collectFiles(INK_SRC, ['.ts', '.tsx']).filter((item) => !isInkTestFile(item))) {
      const source = readFileSync(file, 'utf8')
      // 只认「不带 from 的裸导入」：`import './x.css'`。
      if (/(?:^|\n)\s*import\s+['"][^'"]+\.css['"]/.test(source)) consumers.push(file)
    }
    expect(consumers.length).toBeGreaterThan(0)
  })

  it('源码的裸依赖只有 react / react-dom（别名指过来必须能解析到）', () => {
    const bare = new Set<string>()
    for (const file of collectFiles(INK_SRC, ['.ts', '.tsx']).filter((item) => !isInkTestFile(item))) {
      const source = readFileSync(file, 'utf8')
      for (const match of source.matchAll(/(?:from|import)\s+['"]([^'".][^'"]*)['"]/g)) {
        const specifier = match[1]
        if (specifier.startsWith('@/')) continue
        bare.add(specifier.startsWith('@') ? specifier.split('/').slice(0, 2).join('/') : specifier.split('/')[0])
      }
    }
    // 库自带的测试文件已被排除（见 isInkTestFile），剩下就是真正会被打进站点
    // 产物的裸依赖：只允许包的 peerDependencies 这两个。
    expect([...bare].sort()).toEqual(['react', 'react-dom'])
  })

  it('moduleSideEffects 只对 ink-design 源码返回 false，其余一律 true（= Rollup 默认）', () => {
    const treeshake = viteConfig.build?.rollupOptions?.treeshake as
      | { moduleSideEffects?: (id: string) => boolean }
      | undefined
    expect(typeof treeshake?.moduleSideEffects).toBe('function')
    const decide = treeshake!.moduleSideEffects!
    expect(decide(join(INK_SRC, 'Button', 'index.tsx').replace(/\\/g, '/'))).toBe(false)
    // true 是 Rollup 的默认取值（每个模块都可能有副作用），等价于「交回默认判断」。
    // 回到 `null` 会让 tsc 报错（类型是 `(id) => boolean`），从而废掉 tsc -b 这道门。
    expect(decide('D:/app/src/App.tsx')).toBe(true)
    expect(decide('D:/app/node_modules/react/index.js')).toBe(true)
    // 前缀相近的包不得被误判成 ink-design。
    expect(decide('D:/app/node_modules/ink-design-extra/src/index.ts')).toBe(true)

    // .css 必须**先放行**：P2-4 之后组件源码里的 `import '../styles/parts/x.css'`
    // 就是「用到这个组件就带上这段样式」的表达。要是 .css 也被判成无副作用，
    // Rollup 会把它连同未用组件一起摇掉——构建不报错、类型检查不报错、单测也
    // 不报错，只有页面上样式消失。ink-design 自己的片段同样要放行。
    expect(decide(join(INK_SRC, 'styles/parts/06-extend-modal-mask.css').replace(/\\/g, '/'))).toBe(true)
    expect(decide('D:/app/src/styles/app.css')).toBe(true)
  })
})

const INK_DIST_DTS = join(WEB_ROOT, 'node_modules/ink-design/dist/index.d.ts')
const TYPE_PATCH = join(APP_SRC, 'types/ink-design.d.ts')

describe('ink-design 契约：应用用到的导出必须在别名指向的入口里', () => {
  it('每个具名导入都能在 src/index.ts 找到', () => {
    const used = collectAppInkImports()
    const exported = collectInkExports()
    // 兜底断言：集合为空时下面的对比会「全过」，那是最危险的假绿。
    expect(used.size).toBeGreaterThan(0)
    expect(exported.size).toBeGreaterThan(0)
    expect([...used].filter((name) => !exported.has(name))).toEqual([])
  })

  // 0.1.3 的发布产物没重建：`dist/index.d.ts` 还是 0.1.2 的，站点当时靠
  // src/types/ink-design.d.ts 这层本地补丁让 tsc 看到新组件（见
  // INKDESIGN-0.1.3-FINDINGS.md 第 1 条）。0.1.4 已重建 dist，补丁随之删除。
  // 这里守两件事：
  //   ① dist 的声明覆盖站点在用的每一个符号——上游再次漏建时立刻红，而不是
  //      等到某个页面在运行时拿到 `undefined`；
  //   ② 本地补丁不再存在——它一旦回来，就说明类型层面又分叉出了一个 API。
  it('dist/index.d.ts 覆盖站点用到的每一个具名导入', () => {
    const dist = readFileSync(INK_DIST_DTS, 'utf8')
    const missing = [...collectAppInkImports()].filter((name) => !new RegExp(`\\b${name}\\b`).test(dist))
    expect(missing).toEqual([])
  })

  it('本地类型补丁已删除（0.1.4 重建 dist 后不再需要）', () => {
    expect(
      existsSync(TYPE_PATCH),
      'src/types/ink-design.d.ts 又出现了：先确认上游 dist 是否真的漏了符号，别默认用它补',
    ).toBe(false)
  })
})
