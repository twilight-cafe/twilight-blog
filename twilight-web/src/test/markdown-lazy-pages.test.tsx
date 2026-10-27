// markdown-lazy-pages.test.tsx —— 内容页与懒加载渲染器的接线：侧栏目录
//
// 目录来自 `loadToc`（和正文同一块懒加载渲染器，见 markdown-loader.ts），并且只
// 通过 Outlet context 写给侧栏。所以这里搭一个最小的壳把 `setToc` 抓出来——不提供
// context 的话 `useOutletContext()` 拿到 undefined、回落到 noopSetToc，目录这条路
// 就完全不可观测，写了什么都是「绿」。
//
// 换掉整个加载器是为了固定住「目录还在路上」那一瞬间：真块加载得太快，撞不到。
// 但 `peekMarkdown` 仍要给出真渲染器——否则 ContentView 会先渲染占位，正文断言
// 就得跟着变成异步等待，测试的重点就偏了。

import { act, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Outlet, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { TocItem } from '../lib/markdown'
import { getSharedClientMock, makeArticle, makePage, resetClientMock } from './helpers'

const client = getSharedClientMock()

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

/** 从干净的注册表里取一份「加载器已被替换」的页面组件。 */
async function freshPages(loadToc: (content: string) => Promise<TocItem[]>) {
  vi.resetModules()
  vi.doMock('../lib/client', async () => {
    const { getSharedClientMock: shared } = await import('./helpers')
    return shared()
  })
  const renderer = await vi.importActual<typeof import('../lib/markdown')>('../lib/markdown')
  vi.doMock('../lib/markdown-loader', () => ({
    peekMarkdown: () => renderer,
    loadMarkdown: async () => renderer,
    prefetchMarkdown: () => {},
    loadToc,
  }))
  return {
    Post: (await import('../pages/public/Post')).default,
    PageDetail: (await import('../pages/public/PageDetail')).default,
  }
}

/** 记录每一次 setToc 的入参（Post/PageDetail 写侧栏的唯一出口）。 */
function renderWithTocSink(element: React.ReactElement, route: string, pattern: string) {
  const writes: TocItem[][] = []
  const view = render(
    <MemoryRouter initialEntries={[route]}>
      <Routes>
        <Route element={<Outlet context={{ setToc: (items: TocItem[]) => { writes.push(items) } }} />}>
          <Route path={pattern} element={element} />
        </Route>
      </Routes>
    </MemoryRouter>,
  )
  return { writes, ...view }
}

beforeEach(() => {
  resetClientMock(client)
  client.blog.getSite.mockResolvedValue({ description: '测试站', avatar_url: '', code_block_max_lines: 20 })
  // Post/PageDetail 都挂着 `enabled` 的评论区；不打这个桩的话 jest.fn() 返回
  // undefined，Comments 在 .then 里读 result.items 直接炸。
  client.blog.listComments.mockResolvedValue({ items: [], page: 1, page_size: 12, total: 0, total_pages: 1 })
})

afterEach(() => {
  vi.doUnmock('../lib/markdown-loader')
  vi.doUnmock('../lib/client')
})

const OUTLINE: TocItem[] = [{ id: '标题', text: '标题', level: 1 }]

describe('内容页的目录写入', () => {
  it('文章页：拿到目录就交给侧栏，卸载时清空', async () => {
    client.blog.getArticle.mockResolvedValue(makeArticle({ id: 7, title: '标题文章', content: '# 标题' }))
    const { Post } = await freshPages(async () => OUTLINE)

    const { writes, unmount } = renderWithTocSink(<Post />, '/post/7', '/post/:id')
    await screen.findByText('标题文章')
    await waitFor(() => expect(writes).toContainEqual(OUTLINE))

    unmount()
    // 卸载必须清空：否则下一篇/上一页的侧栏会挂着这篇文章的目录。
    expect(writes[writes.length - 1]).toEqual([])
  })

  // 回归：目录只从 article.content（中文主列）抽取时，切到英文后正文换了、目录却没换。
  // 这里把两种语言的正文都写成不同的标题，并断言 loadToc 收到的是**当前语言**的正文。
  it('切换中英文后目录跟着正文一起更新', async () => {
    client.blog.getArticle.mockResolvedValue(makeArticle({
      id: 7,
      title: '中文标题',
      content: '# 中文标题',
      title_en: 'English Title',
      content_en: '# English Heading',
    }))
    const loadToc = vi.fn(async (content: string) => (
      content.includes('English')
        ? [{ id: 'english', text: 'English Heading', level: 1 }]
        : [{ id: 'zh', text: '中文标题', level: 1 }]
    ))
    const { Post } = await freshPages(loadToc)

    const { writes } = renderWithTocSink(<Post />, '/post/7', '/post/:id')
    await waitFor(() => expect(loadToc).toHaveBeenCalledWith('# 中文标题'))
    await waitFor(() => expect(writes[writes.length - 1]?.[0]?.text).toBe('中文标题'))

    // 用 freshPages 之后同一个模块注册表里的 i18n，否则切的是另一份模块级状态。
    const i18n = await import('../lib/i18n')
    act(() => i18n.setLocale('en'))

    await screen.findByText('English Title')
    await waitFor(() => expect(writes[writes.length - 1]?.[0]?.text).toBe('English Heading'))
    expect(loadToc).toHaveBeenLastCalledWith('# English Heading')
  })

  it('文章还在路上：目录落定时若已卸载就不再写侧栏', async () => {
    const pending = deferred<TocItem[]>()
    client.blog.getArticle.mockResolvedValue(makeArticle({ id: 7, title: '标题文章', content: '# 标题' }))
    const { Post } = await freshPages(() => pending.promise)

    const { writes, unmount } = renderWithTocSink(<Post />, '/post/7', '/post/:id')
    // 先等正文渲染出来：那才证明 article 已经提交、目录 effect 已经跑到 await 那一步。
    await screen.findByText('标题文章')
    unmount()

    pending.resolve(OUTLINE)
    await act(async () => { await Promise.resolve() })

    // 只应剩下卸载时那一次空写；目录本身一次都没被写进去。
    expect(writes.every((items) => items.length === 0)).toBe(true)
  })

  it('独立页面：拿到目录就交给侧栏，卸载时清空', async () => {
    client.blog.getPage.mockResolvedValue(makePage({ id: 4, title: '关于我', content: '# 标题' }))
    const { PageDetail } = await freshPages(async () => OUTLINE)

    const { writes, unmount } = renderWithTocSink(<PageDetail />, '/pages/4', '/pages/:id')
    await screen.findByText('关于我')
    await waitFor(() => expect(writes).toContainEqual(OUTLINE))

    unmount()
    expect(writes[writes.length - 1]).toEqual([])
  })

  it('独立页面：切换语言后目录跟着英文正文重算', async () => {
    client.blog.getPage.mockResolvedValue(makePage({
      id: 4,
      title: '关于我',
      content: '# 中文标题',
      title_en: 'About me',
      content_en: '# English Heading',
    }))
    const loadToc = vi.fn(async (content: string) => (
      content.includes('English')
        ? [{ id: 'english', text: 'English Heading', level: 1 }]
        : [{ id: 'zh', text: '中文标题', level: 1 }]
    ))
    const { PageDetail } = await freshPages(loadToc)

    const { writes } = renderWithTocSink(<PageDetail />, '/pages/4', '/pages/:id')
    await waitFor(() => expect(loadToc).toHaveBeenCalledWith('# 中文标题'))
    await waitFor(() => expect(writes[writes.length - 1]?.[0]?.text).toBe('中文标题'))

    // 与 Post 同一回归：目录只依赖 page 字段的话，语言切换后侧栏会停在中文。
    const i18n = await import('../lib/i18n')
    act(() => i18n.setLocale('en'))

    await screen.findByText('About me')
    await waitFor(() => expect(writes[writes.length - 1]?.[0]?.text).toBe('English Heading'))
    expect(loadToc).toHaveBeenLastCalledWith('# English Heading')
  })

  it('独立页面：目录落定时若已卸载就不再写侧栏', async () => {
    const pending = deferred<TocItem[]>()
    client.blog.getPage.mockResolvedValue(makePage({ id: 4, title: '关于我', content: '# 标题' }))
    const { PageDetail } = await freshPages(() => pending.promise)

    const { writes, unmount } = renderWithTocSink(<PageDetail />, '/pages/4', '/pages/:id')
    await screen.findByText('关于我')
    unmount()

    pending.resolve(OUTLINE)
    await act(async () => { await Promise.resolve() })

    expect(writes.every((items) => items.length === 0)).toBe(true)
  })
})
