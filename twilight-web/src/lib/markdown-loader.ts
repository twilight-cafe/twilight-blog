// markdown-loader.ts —— Markdown 渲染器的懒加载入口
//
// 为什么要把整条渲染管线拆出去：它连着两台「大件」——KaTeX 与
// lowlight/common（37 种语言的 highlight 语法），外加 KaTeX 的样式表与 62 个字体
// 文件。这些东西只在**内容真的要被渲染**时才有用，以前却和路由一起进首屏包，
// 于是只打开归档页、搜索结果页或后台列表的人也要白下载一遍。
//
// 两条刻意的约定：
//
//  1. **不做内容判断**。不先扫一遍正文找 `$` 或 ``` 再决定要不要加载——那种扫描
//     本身就是一次全量正则，而且猜错就得回退重渲染。一律懒加载，判断权交给
//     「有没有 ContentView 被挂载」。
//  2. **peek 优先**。已经加载过之后，后续挂载要在**首次渲染**就拿到渲染器
//     （`useState(peekMarkdown)`），否则每次翻页、切文章、列表里每条评论都要
//     先空一帧再填内容。
//
// 类型只在类型位引用 ./markdown（`import type` 会被完全擦除）：写成普通 import
// 就等于把整个重块重新拉回首屏包，这一层拆分就白做了。

import type { TocItem } from './markdown'

export type MarkdownModule = typeof import('./markdown')

let loaded: MarkdownModule | null = null
let inFlight: Promise<MarkdownModule> | null = null

/** 已加载完成的渲染器；没加载过返回 null。调用方据此决定「直接渲染」还是「先占位」。 */
export function peekMarkdown(): MarkdownModule | null {
  return loaded
}

/**
 * 取渲染器。同一次会话里只真正下载一次，并发的调用共享同一个请求。
 *
 * **失败不缓存**：一次网络抖动就让整站正文永久降级成纯文本是不可接受的，
 * 所以失败时把 in-flight 清掉，下一次挂载会重新尝试。
 */
export function loadMarkdown(): Promise<MarkdownModule> {
  if (loaded) return Promise.resolve(loaded)
  if (!inFlight) {
    inFlight = import('./markdown')
      .then((module) => {
        loaded = module
        return module
      })
      .catch((error: unknown) => {
        inFlight = null
        throw error
      })
  }
  return inFlight
}

/**
 * 只是想「早点开始下载」时用它：不会 reject，调用方不需要为失败写处置代码
 * （真正失败的处理在 ContentView 那里，它才是渲染方）。
 */
export function prefetchMarkdown(): void {
  void loadMarkdown().catch(() => {})
}

/**
 * 抽取目录（文章的侧栏用它）。
 *
 * 失败给**空目录**而不是 reject：目录拿不到只是侧栏空着，正文该照常渲染，
 * 所以这条失败不该冒泡到页面组件里去。把它收在这里的另一个好处是——「失败时
 * 怎么办」只有一处，页面里就只剩一个「卸载后别再写」的守卫，不必每个页面各写
 * 一份 catch。
 */
export function loadToc(content: string): Promise<TocItem[]> {
  return loadMarkdown()
    .then((module) => module.extractToc(content))
    .catch(() => [])
}

/** 仅供测试：清掉模块级缓存（与 head.ts / site.ts 的 reset 同一先例）。 */
export function resetMarkdownLoaderForTests(): void {
  loaded = null
  inFlight = null
}
