// @vitest-environment node
//
// inkui-css-parts.test.ts —— 守护 P2-4 的「样式跟着组件走」契约
//
// 背景：P2-4 之前，InkUI 的全部样式由 main.tsx 里一句
// `import 'ink-design/styles.css'` 一次性带进首屏（108.6 KB）。P2-4 把它按组件
// 拆成 src/styles/parts/*.css，各组件只 import 自己那几个片段——于是「丢掉未
// 使用的组件」现在会**连带丢掉它的样式**（首屏 CSS 108.6 → 45.0 KB）。
//
// 代价是三条**隐式契约**同时被押上，任何一条静默失效都不会报错、只会让页面
// **没样式**（构建绿、`tsc -b` 绿、单测绿）：
//   ① 站点消费的必须是带 parts 的那份 ink-design（本地路径依赖），而不是注册表
//      上的发布副本——P2-4 实施时就真的踩过一次：站点装的是 0.1.0 注册表副本，
//      它没有 parts/，于是所有 InkUI 样式凭空消失、main.css 只剩 app.css；
//   ② 每个组件的片段导入路径必须真的解析得到——`src/X.tsx` 要用 `./styles/parts/`，
//      `src/dir/X.tsx` 要用 `../styles/parts/`，算反一次就整棵样式树失效；
//   ③ app.css 必须是 main.tsx 里**最后**一条样式导入（两边特异性常常相同，
//      站点样式盖住组件默认样式靠的就是这个顺序）。
//
// 所以断言全部打在**契约**上，不放在体积数字上：体积靠构建后实测，契约靠本文件。
// 本文件只读文件系统，用 node 环境（jsdom 既不参与也不必要）。

import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'

const WEB_ROOT = fileURLToPath(new URL('../../', import.meta.url))
const APP_SRC = join(WEB_ROOT, 'src')
const PKG_ROOT = join(WEB_ROOT, 'node_modules/ink-design')
const INK_SRC = join(PKG_ROOT, 'src')
const PARTS = join(INK_SRC, 'styles/parts')

/** 路径断言一律先归一成 `/`，避免 Windows 混合分隔符造成假失败。 */
function slash(value: string): string {
  return value.replace(/\\/g, '/')
}

/** 递归收集目录下匹配扩展名的文件。 */
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

/** 收集一个源文件里的**裸样式导入**（`import './x.css'`，不带 from）。 */
function cssImportsIn(file: string): string[] {
  const source = readFileSync(file, 'utf8')
  return [...source.matchAll(/(?:^|\n)[ \t]*import\s+['"]([^'"]+\.css)['"]/g)].map((match) => match[1])
}

describe('P2-4 契约：站点消费的必须是「带 parts 的」ink-design', () => {
  it('依赖声明为本地路径（file:），而不是注册表版本区间', () => {
    const pkg = JSON.parse(readFileSync(join(WEB_ROOT, 'package.json'), 'utf8')) as {
      dependencies: Record<string, string>
    }
    // 用 ^0.1.0 这类区间时，pnpm 会去注册表取发布副本；那份产物没有 parts/，
    // 而且它的组件源码里也没有片段导入 —— 页面会**静默变成无样式**。
    expect(pkg.dependencies['ink-design']).toMatch(/^file:/)
  })

  it('已安装的包里有 src/styles/parts，且组件源码确实在 import 片段', () => {
    expect(existsSync(PARTS)).toBe(true)
    const parts = readdirSync(PARTS).filter((name) => name.endsWith('.css'))
    // 数量只做下界约束：片段内部怎么再拆是 InkUI 自己的事，少到只剩一两个
    // 就说明装错了包（注册表副本里一个都没有）。
    expect(parts.length).toBeGreaterThanOrEqual(10)

    const consumers = collectFiles(INK_SRC, ['.tsx', '.ts'], ['test'])
      .filter((file) => cssImportsIn(file).some((specifier) => specifier.includes('styles/parts/')))
      .map((file) => slash(file.slice(INK_SRC.length + 1)))
    expect(consumers.length).toBeGreaterThan(0)
    // 拼错路径时 consumers 会是空数组，这里连「有没有人引」一起兜住。
    expect(consumers).toContain('Button.tsx')
  })
})

describe('P2-4 契约：片段导入路径必须真能解析', () => {
  const sources = collectFiles(INK_SRC, ['.tsx', '.ts'], ['test'])

  it('每个 `import "...css"` 都指向一个存在的文件', () => {
    const broken: string[] = []
    for (const file of sources) {
      for (const specifier of cssImportsIn(file)) {
        const target = resolve(dirname(file), specifier)
        if (!existsSync(target)) broken.push(`${slash(file.slice(INK_SRC.length + 1))} -> ${specifier}`)
      }
    }
    expect(broken).toEqual([])
  })

  it('parts 目录里的每个片段都有导入方，且没有导入了不存在的片段', () => {
    const referenced = new Set<string>()
    for (const file of sources) {
      for (const specifier of cssImportsIn(file)) {
        if (specifier.includes('styles/parts/')) referenced.add(specifier.split('/').pop()!)
      }
    }
    const present = new Set(readdirSync(PARTS).filter((name) => name.endsWith('.css')))
    // 孤儿片段 = 白白留在库里（组件库聚合产物 dist/inkui.css 会背上它）；
    // 悬空引用 = 组件 import 了一个不存在的文件，构建期才会炸。
    expect([...present].filter((name) => !referenced.has(name)).sort()).toEqual([])
    expect([...referenced].filter((name) => !present.has(name)).sort()).toEqual([])
  })

  it('导入前缀与文件深度一致（src/X.tsx 用 ./，src/dir/X.tsx 用 ../）', () => {
    // 这正是实施时真踩过的坑：脚本对**所有**组件都写 `../styles/parts/`，于是
    // 顶层组件（Icon.tsx、Button.tsx……）全部指向 src 之上，构建期才以
    // UNRESOLVED_IMPORT 炸出来。这里的断言让它在单测阶段就红。
    const offenders: string[] = []
    for (const file of sources) {
      const relative = slash(file.slice(INK_SRC.length + 1))
      const depth = relative.split('/').length - 1
      const expected = `${'../'.repeat(depth) || './'}styles/parts/`
      for (const specifier of cssImportsIn(file)) {
        if (!specifier.includes('styles/parts/')) continue
        if (!specifier.startsWith(expected)) offenders.push(`${relative} -> ${specifier}（期望前缀 ${expected}）`)
      }
    }
    expect(offenders).toEqual([])
  })
})

describe('P2-4 契约：站点侧的样式顺序与入口', () => {
  it('main.tsx 里 app.css 是最后一条 import（否则盖不住组件默认样式）', () => {
    const source = readFileSync(join(APP_SRC, 'main.tsx'), 'utf8')
    const specs = [...source.matchAll(/(?:^|\n)[ \t]*import[^\n]*?['"]([^'"]+)['"]/g)].map((match) => match[1])
    expect(specs.length).toBeGreaterThan(0)
    expect(specs.at(-1)).toBe('./styles/app.css')
    expect(specs.filter((specifier) => specifier.endsWith('app.css'))).toHaveLength(1)
  })

  it('站点源码里没有任何 ink-design/styles.css 导入（它是未拆分的整表）', () => {
    const offenders: string[] = []
    for (const file of collectFiles(APP_SRC, ['.ts', '.tsx'], ['test'])) {
      if (readFileSync(file, 'utf8').includes('ink-design/styles.css')) {
        offenders.push(slash(file.slice(APP_SRC.length + 1)))
      }
    }
    // 一旦有人加回来，首屏会一次性背上 80 KB 的未用样式，且没有任何提示。
    expect(offenders).toEqual([])
  })
})
