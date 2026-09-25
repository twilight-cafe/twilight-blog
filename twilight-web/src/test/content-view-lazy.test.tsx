// content-view-lazy.test.tsx —— ContentView 的三个懒加载状态
//
// 单独一个文件：这里必须 vi.resetModules() 把加载器打回「还没拿到渲染器」的起点，
// 而 setup.ts 的全局预取（为了让别的用例能同步断言正文）正好与之互斥。
//
// 三态各自守一件事：
//   1. 未到位 → 占位，而不是空白；
//   2. 已在手 → **首次渲染就是内容**（这是 peek 优化存在的意义，也是其余测试
//      能继续同步断言的前提）；
//   3. 拿不到 → 降级成转义后的原文。这条是安全相关的：评论是不可信输入，
//      降级路径一旦直接注入原文，懒加载就成了一个 XSS 后门。

import { act, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { getSharedClientMock } from './helpers'

/** 重新登记 client 替身——vi.resetModules() 会把相对路径的 vi.mock 注册一并清掉。 */
function mockClient(): void {
  vi.doMock('../lib/client', async () => {
    const { getSharedClientMock: shared } = await import('./helpers')
    return shared()
  })
}

/**
 * 每个用例都从干净的注册表起步。
 * `rendererUnavailable` 让惰性块「下载失败」；`stubLoader` 则把加载器整个换掉，
 * 好把「渲染器还在路上」那一瞬间固定下来（真块的加载速度没法拿来当时序控制）。
 */
async function freshContentView(
  options: { rendererUnavailable?: boolean; stubLoader?: () => Promise<unknown> } = {},
): Promise<typeof import('../components/ContentView').default> {
  vi.resetModules()
  mockClient()
  if (options.rendererUnavailable) {
    vi.doMock('../lib/markdown', () => {
      throw new Error('chunk 下载失败')
    })
  }
  if (options.stubLoader) {
    vi.doMock('../lib/markdown-loader', () => ({
      peekMarkdown: () => null,
      loadMarkdown: options.stubLoader,
      prefetchMarkdown: () => {},
      loadToc: async () => [],
    }))
  }
  const module = await import('../components/ContentView')
  return module.default
}

beforeEach(() => {
  vi.resetModules()
  const client = getSharedClientMock()
  client.blog.getSite.mockResolvedValue({ description: '测试站', avatar_url: '', code_block_max_lines: 20 })
})

afterEach(() => {
  vi.doUnmock('../lib/markdown')
  vi.doUnmock('../lib/markdown-loader')
  vi.doUnmock('../lib/client')
})

describe('ContentView 懒加载状态', () => {
  it('渲染器还没到位时给占位；到位后渲染出内容', async () => {
    const ContentView = await freshContentView()
    const { container } = render(<ContentView text="**粗体**" />)

    // 占位那一步必须能被看见：这里断言「还没有正文」，而不是断言某个内部标记。
    expect(container.querySelector('.plain-body')).toBeNull()
    expect(container.textContent).toContain('LOADING')

    await waitFor(() => expect(container.querySelector('strong')?.textContent).toBe('粗体'))
    // 到位之后仍要带上外壳（CSS 变量决定代码块限行，别在异步路径上弄丢）。
    expect(container.querySelector('.plain-body')).toBeTruthy()
  })

  it('渲染器已在手时首次渲染就是内容——不再先闪一帧占位', async () => {
    const ContentView = await freshContentView()
    const { loadMarkdown } = await import('../lib/markdown-loader')
    await loadMarkdown()

    const { container } = render(<ContentView text="**粗体**" />)
    // 同步断言：这一条就是「其余用例还能用 getByText 而不是 findByText」的保证。
    expect(container.querySelector('strong')?.textContent).toBe('粗体')
    expect(container.textContent).not.toContain('LOADING')
  })

  it('渲染器下载失败：降级成转义后的原文，绝不把原文当 HTML 注进去', async () => {
    const ContentView = await freshContentView({ rendererUnavailable: true })
    const hostile = '<img src=x onerror="alert(1)"> **粗体**'
    const { container } = render(<ContentView text={hostile} />)

    await waitFor(() => expect(container.querySelector('.plain-body')?.textContent).toContain('<img src=x'))
    // 关键：既没有被浏览器当成标签解析（没有真的 img 元素），也没有走 Markdown 渲染。
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('strong')).toBeNull()
    expect(container.innerHTML).toContain('&lt;img')
    // 外壳照旧，降级不该顺手把代码块限行这样的展示参数丢掉。
    expect(container.querySelector('.plain-body')).toBeTruthy()
  })

  it('降级路径遇到空文本也不炸（空串要走兜底分支）', async () => {
    const ContentView = await freshContentView({ rendererUnavailable: true })
    const { container } = render(<ContentView text="" />)

    await waitFor(() => expect(container.querySelector('.plain-body')).toBeTruthy())
    expect(container.querySelector('.plain-body')?.innerHTML).toBe('<p></p>')
  })

  it('渲染器还在路上就卸载：落定之后不再 setState', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    let settle: (module: unknown) => void = () => {}
    const renderer = await vi.importActual('../lib/markdown')
    const ContentView = await freshContentView({
      stubLoader: () => new Promise((resolve) => { settle = resolve }),
    })

    const { container, unmount } = render(<ContentView text="**粗体**" />)
    expect(container.textContent).toContain('LOADING')
    unmount()
    settle(renderer)
    await act(async () => { await Promise.resolve() })

    // 卸载后落定：既没把内容塞回来，也没在控制台留下「已卸载组件上 setState」的痕迹。
    expect(container.innerHTML).toBe('')
    expect(errorSpy).not.toHaveBeenCalled()
    errorSpy.mockRestore()
  })

  it('渲染器下载失败且组件已卸载：不落地降级', async () => {
    let fail: (reason: unknown) => void = () => {}
    const ContentView = await freshContentView({
      stubLoader: () => new Promise((_resolve, reject) => { fail = reject }),
    })

    const { container, unmount } = render(<ContentView text="x" />)
    unmount()
    fail(new Error('chunk 下载失败'))
    await act(async () => { await Promise.resolve() })

    expect(container.innerHTML).toBe('')
  })

  it('默认（评论）管线：原始 HTML 整段丢弃，既不成元素也不留可执行文本', async () => {
    const ContentView = await freshContentView()
    await (await import('../lib/markdown-loader')).loadMarkdown()
    // 必须空一行：`<script>` 起头的那一行会被当成整个 HTML 块（块规则一直吃到行尾），
    // 连在同一行上的正文会跟着一起被丢掉——那样测到的就不是「白名单生效」而是
    // 「HTML 块规则生效」了。
    const { container } = render(<ContentView text={'<script>alert(1)</script>\n\n带**粗体**的正文'} />)
    await waitFor(() => expect(container.querySelector('strong')?.textContent).toBe('粗体'))
    // 极性守住：没传 trusted 就得走评论白名单管线。实测这一条管线里原始 HTML 在
    // 解析阶段就被丢掉（不是转义成文本），所以 alert(1) 连痕迹都不该留。
    expect(container.querySelector('script')).toBeNull()
    expect(container.textContent).toContain('带粗体的正文')
    expect(container.textContent).not.toContain('alert(1)')
  })
})

describe('懒加载样式与 app.css 的先后关系', () => {
  it('app.css 里提到 KaTeX / hljs 的规则一律落在 .plain-body 之下', () => {
    // KaTeX 与 highlight.js 的主题现在跟着懒加载块**后**到（原先写在 main.tsx 里、
    // 排在 app.css 之前）。顺序一翻，同特异性的规则就会反过来覆盖——所以这条
    // 断言把前提钉住：app.css 与主题的竞争必须靠**特异性**（多一层 .plain-body），
    // 而不是靠先后。哪天有人加一条裸 `.katex`，它就是会红的那一条。
    const css = readFileSync(resolve(process.cwd(), 'src/styles/app.css'), 'utf8')
    const selectors = css
      .split('{')
      .slice(0, -1)
      .map((block) => block.slice(block.lastIndexOf('}') + 1).trim())
      .filter((selector) => /katex|hljs/i.test(selector))
      // 兜底：一处都没匹配到时下面那条会「全过」，那是最危险的假绿。
      .filter((selector) => !selector.includes('.plain-body'))

    expect(selectors).toEqual([])
    // 上面那条在「一处都没匹配到」时也会全过，所以再确认 app.css 确实管着这两族样式。
    expect(css).toMatch(/\.plain-body[^{}]*\.katex/)
    expect(css).toMatch(/\.plain-body[^{}]*\.hljs/)
  })
})
