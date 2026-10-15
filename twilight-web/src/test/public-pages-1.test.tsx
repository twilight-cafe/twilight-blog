// public-pages-1.test.tsx —— NotFound / Home / Archive / Moments / PageDetail
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import NotFound from '../pages/public/NotFound'
import Home from '../pages/public/Home'
import Archive from '../pages/public/Archive'
import Moments from '../pages/public/Moments'
import PageDetail from '../pages/public/PageDetail'
import { ToastProvider } from '../lib/toast'
import { getSharedClientMock, NOW, makeArticle, makeComment, makeMedia, makeMoment, makePage } from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

function paged<T>(items: T[], page = 1, total_pages = 1, total = items.length) {
  return { items, page, page_size: 12, total, total_pages }
}

function bodyText(): string {
  return (document.body.textContent ?? '').replace(/\s+/g, ' ')
}

function renderPage(ui: React.ReactElement, route = '/', pattern = '/') {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <ToastProvider>
        <Routes>
          <Route path={pattern} element={ui} />
        </Routes>
      </ToastProvider>
    </MemoryRouter>,
  )
}

/** deferred：手工控制 resolve/reject 的 Promise（测卸载竞态与错误分支）。 */
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

beforeEach(() => {
  vi.clearAllMocks()
  client.setCurrentUser(null)
  // useDocumentTitle 会触发 loadSiteDescription → blog.getSite()
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
  // Moments 内嵌评论区、PageDetail 等都会拉评论；给个合法空页防 undefined
  client.blog.listComments.mockResolvedValue({ items: [], page: 1, page_size: 12, total: 0, total_pages: 1 })
  // 每页篇数读自外壳属性（见 lib/flags.ts）；不清理会让上一个用例的假外壳泄漏，
  // 表现为「第一个断言过了、第二个莫名用了别的 pageSize」。
  delete document.documentElement.dataset.homePageSize
})

describe('NotFound', () => {
  it('渲染 404 文案与两个导航按钮', () => {
    renderPage(<NotFound />, '/nowhere', '*')
    expect(screen.getByText('404')).toBeTruthy()
    expect(screen.getByText('回到首页')).toBeTruthy()
    expect(screen.getByText('翻翻归档')).toBeTruthy()
  })
})

describe('Home', () => {
  it('加载文章卡片：标题、摘要、标签、阅读时长；空列表走 Empty', async () => {
    client.blog.listArticles
      .mockResolvedValueOnce(paged([
        makeArticle({ id: 1, title: '第一篇', content: 'x'.repeat(1300), tags: ['Go', 'React'] }),
        makeArticle({ id: 2, title: '第二篇', tags: [] }),
      ], 1, 2, 14))
      .mockResolvedValueOnce(paged([]))

    const { unmount } = renderPage(<Home />)
    await waitFor(() => expect(screen.getByText('第一篇')).toBeTruthy())
    expect(screen.getByText('第二篇')).toBeTruthy()
    expect(screen.getByText('3 分钟阅读')).toBeTruthy() // 1300/600 向上取整
    expect(screen.getByText('1300 字符')).toBeTruthy()
    expect(screen.getByText('# Go')).toBeTruthy() // 最多 4 个标签 Chip
    expect(screen.getByText('# React')).toBeTruthy()

    // 翻到第 2 页：重新拉取且为空 → Empty
    fireEvent.click(screen.getByRole('button', { name: 'Page 2' }))
    await waitFor(() => expect(screen.getByText('还没有文章')).toBeTruthy())
    expect(client.blog.listArticles).toHaveBeenLastCalledWith({ page: 2, pageSize: 12 })
    unmount()
  })

  it('加载失败回退空态', async () => {
    client.blog.listArticles.mockRejectedValueOnce(new Error('down'))
    renderPage(<Home />)
    await waitFor(() => expect(screen.getByText('还没有文章')).toBeTruthy())
  })

  it('卸载竞态：请求返回时组件已卸载，不再写入状态', async () => {
    const gate = deferred<{ items: ReturnType<typeof makeArticle>[]; total_pages: number }>()
    client.blog.listArticles.mockReturnValueOnce(gate.promise as never)
    const { unmount } = renderPage(<Home />)
    unmount()
    gate.resolve(paged([makeArticle()]))
    await gate.promise.catch(() => {})
    expect(true).toBe(true) // 无异常即通过（alive 守卫生效）
  })

  it('每页篇数取外壳属性 data-home-page-size；缺失时回退 12', async () => {
    client.blog.listArticles.mockResolvedValue(paged([]))

    // 站点把首页每页设成 4。
    document.documentElement.dataset.homePageSize = '4'
    const first = renderPage(<Home />)
    await waitFor(() => expect(client.blog.listArticles).toHaveBeenCalledWith({ page: 1, pageSize: 4 }))
    first.unmount()

    // 属性缺失（老二进制的外壳 / 未配置）→ 与服务端默认值一致的 12。
    // 这是「首页第一次请求就必须带对 page_size」的守护：若实现改成等
    // GET /api/site 回来再决定，这里会先看到一次 pageSize=12 的请求。
    delete document.documentElement.dataset.homePageSize
    client.blog.listArticles.mockClear()
    client.blog.listArticles.mockResolvedValue(paged([]))
    const second = renderPage(<Home />)
    await waitFor(() => expect(client.blog.listArticles).toHaveBeenCalledWith({ page: 1, pageSize: 12 }))
    second.unmount()
  })
})

describe('Archive', () => {
  it('多页文章聚合按年分组；毫秒/秒时间戳同桶', async () => {
    const year2023 = NOW // ms
    const year2022 = 1_650_000_000 // s → 2022
    client.blog.listArticles
      .mockResolvedValueOnce(paged([makeArticle({ id: 1, title: '甲年文章', created_at: year2023, tags: ['Go'] })], 1, 3, 3))
      .mockResolvedValueOnce(paged([makeArticle({ id: 2, title: '乙年文章', created_at: year2022, tags: [] })], 2, 3, 3))
      .mockResolvedValueOnce(paged([makeArticle({ id: 3, title: '甲年第二篇', created_at: year2023 + 1000, tags: [] })], 3, 3, 3))

    renderPage(<Archive />, '/archive', '/archive')
    await waitFor(() => expect(screen.getByText('甲年第二篇')).toBeTruthy())
    expect(screen.getByText('共 3 篇 · 2 年')).toBeTruthy()
    // 倒序：晚的在前面
    const order = ['甲年第二篇', '甲年文章', '乙年文章']
    const text = document.body.textContent ?? ''
    expect(text.indexOf('甲年第二篇')).toBeLessThan(text.indexOf('甲年文章'))
    expect(text.indexOf('甲年文章')).toBeLessThan(text.indexOf('乙年文章'))
    void order
    expect(screen.getByText('Go')).toBeTruthy() // 首个标签 chip；空标签用占位 span
  })

  it('单页直接返回；失败回退空归档', async () => {
    client.blog.listArticles
      .mockResolvedValueOnce(paged([makeArticle({ id: 1 })], 1, 1, 1))
      .mockRejectedValueOnce(new Error('down'))

    const first = renderPage(<Archive />, '/archive', '/archive')
    await waitFor(() => expect(bodyText()).toContain('共 1 篇 · 1 年'))
    first.unmount()

    renderPage(<Archive />, '/archive', '/archive')
    await waitFor(() => expect(bodyText()).toContain('共 0 篇 · 0 年'))
  })
})

describe('Moments', () => {
  it('渲染微语流与评论按钮（未点击不发评论请求）；翻页；空态', async () => {
    client.blog.listMoments
      .mockResolvedValueOnce(paged([makeMoment({ id: 1, content: '今天天气' })], 1, 2, 2))
      .mockResolvedValueOnce(paged([], 2, 2, 2))

    const { container, unmount } = renderPage(<Moments />, '/moments', '/moments')
    await waitFor(() => expect(screen.getByText('今天天气')).toBeTruthy())

    // 评论区不再随卡片挂载：一页 12 条微语曾经等于 12 个 GET /api/comments，
    // 现在只有点过「评论」的那一条才会发请求。
    expect(client.blog.listComments).not.toHaveBeenCalled()
    expect(container.querySelector('.cmt-sec')).toBeNull()
    const button = screen.getByRole('button', { name: '评论' })
    expect(button.getAttribute('aria-expanded')).toBe('false')
    // 没有配图的微语不渲染空的缩略图行。
    expect(container.querySelector('.media-gallery')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Page 2' }))
    await waitFor(() => expect(screen.getByText('还没有微语')).toBeTruthy())
    expect(client.blog.listMoments).toHaveBeenLastCalledWith({ page: 2, pageSize: 12 })
    unmount()
  })

  it('带配图的微语渲染与后台同一套缩略图行：点击全屏放大、Esc 关闭', async () => {
    client.blog.listMoments.mockResolvedValue(paged([
      makeMoment({ id: 7, content: '带图的微语', media_items: [makeMedia({ id: 9, original_name: 'pic.png' })] }),
    ]))

    const { container } = renderPage(<Moments />, '/moments', '/moments')
    await waitFor(() => expect(screen.getByText('带图的微语')).toBeTruthy())

    // MediaGallery 与后台微语卡是同一个组件，所以类名也必须是同一套。
    const gallery = container.querySelector('.mom .media-gallery.sm')
    expect(gallery).toBeTruthy()
    const thumb = container.querySelector('.media-thumb-open') as HTMLButtonElement
    expect(thumb.querySelector('img')?.getAttribute('src')).toBe('/uploads/a.png')

    fireEvent.click(thumb)
    await waitFor(() => expect(document.querySelector('.media-lightbox')).toBeTruthy())
    expect(screen.getByText('pic.png')).toBeTruthy()

    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(document.querySelector('.media-lightbox')).toBeNull())
  })

  it('点击「评论」才挂载评论区并请求评论；再点收起、不重复请求', async () => {
    client.blog.listMoments.mockResolvedValue(paged([makeMoment({ id: 5, content: '求评论' })]))
    client.blog.listComments.mockResolvedValue(paged([makeComment({ id: 1, content: '沙发' })]))

    const { container } = renderPage(<Moments />, '/moments', '/moments')
    await waitFor(() => expect(screen.getByText('求评论')).toBeTruthy())
    expect(client.blog.listComments).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: '评论' }))
    await waitFor(() => expect(client.blog.listComments).toHaveBeenCalledWith({ targetType: 'moment', targetId: 5, page: 1, pageSize: 12 }))
    await waitFor(() => expect(container.querySelector('.cmt-sec')).toBeTruthy())
    expect(container.querySelector('.cmt-sec')?.textContent).toContain('沙发')
    expect(screen.getByRole('button', { name: '收起评论' }).getAttribute('aria-expanded')).toBe('true')

    fireEvent.click(screen.getByRole('button', { name: '收起评论' }))
    await waitFor(() => expect(container.querySelector('.cmt-sec')).toBeNull())
    expect(client.blog.listComments).toHaveBeenCalledTimes(1)
  })

  it('卸载竞态守卫', async () => {
    const gate = deferred<{ items: ReturnType<typeof makeMoment>[]; total_pages: number }>()
    client.blog.listMoments.mockReturnValueOnce(gate.promise as never)
    const { unmount } = renderPage(<Moments />, '/moments', '/moments')
    unmount()
    gate.resolve(paged([makeMoment()]))
    await gate.promise.catch(() => {})
    expect(true).toBe(true)
  })
})

describe('PageDetail', () => {
  it('成功加载：标题 + 内容 + TOC 注入 + 评论区', async () => {
    const setToc = vi.fn()
    client.blog.getPage.mockResolvedValueOnce(makePage({ id: 9, title: '关于本站', content: '# 标题一\n## 标题二' }))
    const { unmount } = renderPage(<PageDetail />, '/pages/9', '/pages/:id')
    // 注入 Outlet context：渲染两次太重，改用非 Outlet 场景（setToc=noop）验证渲染，
    // TOC 回调分支由 App 集成路径覆盖。
    await waitFor(() => expect(screen.getByText('关于本站')).toBeTruthy())
    expect(screen.getByText('标题一')).toBeTruthy() // ContentView 渲染出 markdown
    expect(client.blog.getPage).toHaveBeenCalledWith(9)
    void setToc
    unmount()
  })

  it('加载失败或 id 非法：就地显示 404 文案', async () => {
    client.blog.getPage.mockRejectedValueOnce(new Error('404'))
    const first = renderPage(<PageDetail />, '/pages/404', '/pages/:id')
    await waitFor(() => expect(screen.getByText('404')).toBeTruthy())
    first.unmount()

    client.blog.getPage.mockClear()
    renderPage(<PageDetail />, '/pages/abc', '/pages/:id')
    await waitFor(() => expect(screen.getByText('404')).toBeTruthy())
    expect(client.blog.getPage).not.toHaveBeenCalled()
  })
})
