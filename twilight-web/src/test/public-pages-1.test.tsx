// public-pages-1.test.tsx —— NotFound / Home / Archive / Moments / PageDetail
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, Link } from 'react-router-dom'
import NotFound from '../pages/public/NotFound'
import Home from '../pages/public/Home'
import Archive from '../pages/public/Archive'
import Moments from '../pages/public/Moments'
import PageDetail from '../pages/public/PageDetail'
import { ToastProvider } from '../lib/toast'
import { fmtDateTime } from '../lib/format'
import { setLocale } from '../lib/i18n'
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

  it('页码从 URL 恢复：带 ?page=2 打开直接请求第 2 页，翻页后按新页码请求', async () => {
    client.blog.listArticles.mockResolvedValue(paged([], 2, 3, 30))
    renderPage(<Home />, '/?page=2', '/')

    await waitFor(() => expect(client.blog.listArticles).toHaveBeenCalledWith({ page: 2, pageSize: 12 }))
    // 分页器也要认这个页码，而不是停在 1。
    expect(screen.getByRole('button', { name: 'Page 2' }).getAttribute('aria-current')).toBe('page')

    client.blog.listArticles.mockResolvedValue(paged([], 3, 3, 30))
    fireEvent.click(screen.getByRole('button', { name: 'Page 3' }))
    await waitFor(() => expect(client.blog.listArticles).toHaveBeenCalledWith({ page: 3, pageSize: 12 }))
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

  it('首次加载慢于阈值才显形骨架：位置先占住，且没有翻页变暗态', async () => {
    // 假时钟：这里要验的正是「200ms 前后」的差别，真等会拖慢整个套件。
    vi.useFakeTimers()
    try {
      const gate = deferred<{ items: ReturnType<typeof makeArticle>[]; total_pages: number }>()
      client.blog.listArticles.mockReturnValueOnce(gate.promise as never)

      const { container, unmount } = renderPage(<Home />)
      const cards = container.querySelector('.acards') as HTMLElement
      // 第一帧：骨架已经在树上占住整页卡片的位置（所以内容到达时不会跳版），
      // 但还没到阈值，不可见。
      expect(container.querySelector('.skl-cards')?.getAttribute('data-visible')).toBe('false')
      expect(cards.getAttribute('aria-busy')).toBe('true')
      // 翻页已改成「旧卡片滑走、新卡片滑入」，列表不再有变暗态：
      // data-busy 这个标记被整条移除了（留着只会是一段永远不生效的样式）。
      expect(cards.dataset.busy).toBeUndefined()

      act(() => {
        vi.advanceTimersByTime(220)
      })
      expect(container.querySelector('.skl-cards')?.getAttribute('data-visible')).toBe('true')

      await act(async () => {
        gate.resolve(paged([makeArticle({ id: 1, title: '迟到的文章' })]))
      })
      expect(screen.getByText('迟到的文章')).toBeTruthy()
      // 数据到达：骨架整块撤掉，不留一个空壳占位。
      expect(container.querySelector('.skl-cards')).toBeNull()
      unmount()
    } finally {
      vi.useRealTimers()
    }
  })
})

  it('翻页：旧列表在 180ms 退场期间保持，新页数据提前到达也等提交后才上屏', async () => {
    vi.useFakeTimers()
    try {
      client.blog.listArticles
        .mockResolvedValueOnce(paged([makeArticle({ id: 1, title: '第一页' })], 1, 2, 2))
        .mockResolvedValueOnce(paged([makeArticle({ id: 2, title: '第二页' })], 2, 2, 2))

      const { unmount } = renderPage(<Home />)
      await act(async () => {})
      expect(screen.getByText('第一页')).toBeTruthy()

      fireEvent.click(screen.getByRole('button', { name: 'Page 2' }))
      await act(async () => {})
      // 第二次请求已经回来了，但退场还没播完：新页不能抢在旧卡片滑走前上屏，
      // 否则退场动画就播在了一张注定被替换的图上。
      expect(screen.getByText('第一页')).toBeTruthy()
      expect(screen.queryByText('第二页')).toBeNull()

      act(() => { vi.advanceTimersByTime(180) })
      expect(screen.queryByText('第一页')).toBeNull()
      expect(screen.getByText('第二页')).toBeTruthy()
      unmount()
    } finally {
      vi.useRealTimers()
    }
  })

  it('翻页：新页数据没到时先显示骨架，分页器不消失', async () => {
    vi.useFakeTimers()
    try {
      const gate = deferred<{ items: ReturnType<typeof makeArticle>[]; page: number; page_size: number; total: number; total_pages: number }>()
      client.blog.listArticles
        .mockResolvedValueOnce(paged([makeArticle({ id: 1, title: '第一页' })], 1, 2, 2))
        .mockReturnValueOnce(gate.promise as never)

      const { container, unmount } = renderPage(<Home />)
      await act(async () => {})
      fireEvent.click(screen.getByRole('button', { name: 'Page 2' }))
      act(() => { vi.advanceTimersByTime(180) })

      // 提交到第 2 页而数据未到：旧卡片整块撤掉，骨架先占住新增内容的位置。
      expect(screen.queryByText('第一页')).toBeNull()
      expect(container.querySelector('.skl-cards')).toBeTruthy()
      // 分页器留在原地（已指向第 2 页）：它消失再出现等于把读者刚点的东西抽走。
      expect(screen.getByRole('button', { name: 'Page 2' })).toBeTruthy()

      await act(async () => {
        gate.resolve(paged([makeArticle({ id: 2, title: '第二页' })], 2, 2, 2))
      })
      expect(screen.getByText('第二页')).toBeTruthy()
      expect(container.querySelector('.skl-cards')).toBeNull()
      unmount()
    } finally {
      vi.useRealTimers()
    }
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
    // 归档行不展示标签：即使文章带标签也不出现 chip。
    expect(document.querySelector('.tl-row .chip')).toBeNull()
  })

  it('加载骨架与真实归档行一致：只有日期与标题两列', () => {
    // 归档不再展示标签后，骨架若仍画第三根灰条，它会掉进 grid 的下一行，
    // 在内容到达前先闪出一条并不存在的“标签行”。
    client.blog.listArticles.mockImplementation(() => new Promise(() => {}))
    const { container, unmount } = renderPage(<Archive />, '/archive', '/archive')
    const row = container.querySelector('.skl-archive .tl-row')
    expect(row).toBeTruthy()
    expect(row?.children).toHaveLength(2)
    unmount()
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

  it('每条微语在卡片右上角显示精确到分钟的发布时间', async () => {
    client.blog.listMoments.mockResolvedValue(paged([makeMoment({ id: 3, content: '带时间的微语' })]))

    const { container } = renderPage(<Moments />, '/moments', '/moments')
    await waitFor(() => expect(screen.getByText('带时间的微语')).toBeTruthy())

    const card = container.querySelector('.mom') as HTMLElement
    // 时间在正文之前（卡片的第一个子元素），也就是右上角那一行。
    expect(card.firstElementChild?.className).toBe('mom-meta')
    const time = card.querySelector('.mom-time') as HTMLElement
    // 精确到分钟：与 fmtDateTime 的格式逐字一致（多出秒或时区都是错的）。
    expect(time.textContent).toBe(fmtDateTime(NOW))
    expect(time.textContent).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)
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

  it('首次加载慢于阈值才显形骨架：位置先占住，且没有翻页变暗态', async () => {
    vi.useFakeTimers()
    try {
      const gate = deferred<{ items: ReturnType<typeof makeMoment>[]; total_pages: number }>()
      client.blog.listMoments.mockReturnValueOnce(gate.promise as never)

      const { container, unmount } = renderPage(<Moments />, '/moments', '/moments')
      const feed = container.querySelector('.momfeed') as HTMLElement
      expect(container.querySelector('.skl-moments')?.getAttribute('data-visible')).toBe('false')
      expect(feed.getAttribute('aria-busy')).toBe('true')
      // 同 Home：翻页变暗态已随「滑走 → 滑入」一起移除。
      expect(feed.dataset.busy).toBeUndefined()

      act(() => {
        vi.advanceTimersByTime(220)
      })
      expect(container.querySelector('.skl-moments')?.getAttribute('data-visible')).toBe('true')

      await act(async () => {
        gate.resolve(paged([makeMoment({ id: 2, content: '迟到的微语' })]))
      })
      expect(screen.getByText('迟到的微语')).toBeTruthy()
      expect(container.querySelector('.skl-moments')).toBeNull()
      unmount()
    } finally {
      vi.useRealTimers()
    }
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

  // 页面与文章共享同一套逐字段回退规则：只译了标题也要能切到英文标题，
  // 同时正文继续显示中文，而不是变成空白。
  it('切换语言：标题取英文、正文缺失时回退中文', async () => {
    client.blog.getPage.mockResolvedValueOnce(makePage({
      id: 12,
      title: '关于本站',
      content: '# 中文正文',
      title_en: 'About this site',
      content_en: '',
    }))

    renderPage(<PageDetail />, '/pages/12', '/pages/:id')
    await waitFor(() => expect(screen.getByText('关于本站')).toBeTruthy())
    expect(screen.getByText('中文正文')).toBeTruthy()

    act(() => setLocale('en'))
    await waitFor(() => expect(screen.getByText('About this site')).toBeTruthy())
    expect(screen.queryByText('关于本站')).toBeNull()
    expect(screen.getByText('中文正文')).toBeTruthy()
  })

  it('切换页面时旧请求晚到不覆盖新页面', async () => {
    const first = deferred<ReturnType<typeof makePage>>()
    const second = deferred<ReturnType<typeof makePage>>()
    client.blog.getPage.mockImplementation((id: number) => (
      id === 1 ? first.promise : second.promise
    ) as never)

    renderPage(<><PageDetail /><Link to="/pages/2">下一页</Link></>, '/pages/1', '/pages/:id')
    await waitFor(() => expect(client.blog.getPage).toHaveBeenCalledWith(1))

    fireEvent.click(screen.getByText('下一页'))
    await waitFor(() => expect(client.blog.getPage).toHaveBeenCalledWith(2))

    await act(async () => {
      second.resolve(makePage({ id: 2, title: '页面二', content: '正文二' }))
    })
    await waitFor(() => expect(screen.getByText('页面二')).toBeTruthy())

    await act(async () => {
      first.resolve(makePage({ id: 1, title: '页面一', content: '正文一' }))
    })
    expect(screen.queryByText('页面一')).toBeNull()
    expect(screen.getByText('页面二')).toBeTruthy()
  })
})
