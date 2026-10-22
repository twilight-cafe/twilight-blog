// public-pages-2.test.tsx —— Post / Tags / Search
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import Post from '../pages/public/Post'
import Tags from '../pages/public/Tags'
import Search from '../pages/public/Search'
import { ToastProvider } from '../lib/toast'
import { NOW, getSharedClientMock, makeArticle, makeTag } from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

function paged<T>(items: T[], page = 1, total_pages = 1, total = items.length) {
  return { items, page, page_size: 12, total, total_pages }
}

function makeResult(overrides: { type?: 'article' | 'page'; id?: number; title?: string; tags?: string[]; cover_media_id?: number | null } = {}): import('twilight-blog-api').SearchResult {
  // cover_media_id 单独取出来：文章命中必须带上它（Article 声明为必填），
  // 而页面命中不该带——多一个字段会让 PageSearchResult 的形状与契约不符。
  const { tags, cover_media_id, ...base } = { id: 1, title: '命中文章', content: '正文片段', title_en: '', content_en: '', ai_generated: false, created_at: NOW, updated_at: NOW, tags: ['Go'] as string[] | undefined, tags_en: [''] as string[], cover_media_id: null as number | null, ...overrides }
  return base.type === 'page'
    ? { ...base, type: 'page' }
    : { ...base, type: 'article', tags: tags ?? [], cover_media_id: cover_media_id ?? null }
}

function renderAt(ui: React.ReactElement, route: string, pattern: string) {
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

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => { resolve = res })
  return { promise, resolve }
}

beforeEach(() => {
  vi.clearAllMocks()
  client.setCurrentUser(null)
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
  client.blog.listComments.mockResolvedValue({ items: [], page: 1, page_size: 12, total: 0, total_pages: 1 })
  // 功能开关由外壳写在 <html> 的 data-search-enabled 上；各用例显式设置，
  // 缺省时拆掉避免泄漏。
  delete document.documentElement.dataset.searchEnabled
})

describe('Post', () => {
  it('成功渲染：标签 Chip、发布状态、标题、日期、评论区', async () => {
    client.blog.getArticle.mockResolvedValueOnce(makeArticle({ id: 3, title: '文章标题', content: '# 一级\n正文' }))
    renderAt(<Post />, '/post/3', '/post/:id')
    await waitFor(() => expect(screen.getByText('文章标题')).toBeTruthy())
    expect(screen.getByText('# Go')).toBeTruthy()
    expect(screen.getByText('已发布')).toBeTruthy()
    expect(screen.getByText('一级')).toBeTruthy() // ContentView 渲染出 markdown 标题
    await waitFor(() => expect(screen.getByText('还没有评论')).toBeTruthy()) // 评论区空态存在
    expect(client.blog.getArticle).toHaveBeenCalledWith(3)
  })

  it('非法 id：不请求接口，就地 404 文案', () => {
    renderAt(<Post />, '/post/abc', '/post/:id')
    expect(screen.getByText('文章不存在')).toBeTruthy()
    expect(screen.getByText('回到首页')).toBeTruthy()
    expect(client.blog.getArticle).not.toHaveBeenCalled()
  })

  it('加载失败：显示错误文案；未加载时先显示 Loading', async () => {
    client.blog.getArticle.mockRejectedValueOnce(new Error('拉取失败'))
    renderAt(<Post />, '/post/9', '/post/:id')
    await waitFor(() => expect(screen.getByText('拉取失败')).toBeTruthy())

    const gate = deferred<ReturnType<typeof makeArticle>>()
    client.blog.getArticle.mockReturnValueOnce(gate.promise as never)
    const second = renderAt(<Post />, '/post/10', '/post/:id')
    expect(second.container.querySelector('.kicker')?.textContent ?? document.body.textContent).toContain('')
    gate.resolve(makeArticle({ id: 10, title: '稍后到达' }))
    await waitFor(() => expect(screen.getByText('稍后到达')).toBeTruthy())
    second.unmount()
  })
})

describe('Tags', () => {
  it('标签云：字号分级 t1..t4；选中 ?id= 拉取相关文章', async () => {
    client.blog.listTags
      .mockResolvedValueOnce(paged([
        makeTag({ id: 1, name: '大', article_count: 10 }), // ratio 1 → t4
        makeTag({ id: 2, name: '中大', article_count: 6 }), // 0.6 → t3
        makeTag({ id: 3, name: '中小', article_count: 4 }), // 0.4 → t2
        makeTag({ id: 4, name: '小', article_count: 1 }), // 0.1 → t1
      ], 1, 1, 4))
    client.blog.getTagArticles.mockResolvedValueOnce(paged([makeArticle({ id: 5, title: '相关文章甲' })], 1, 1, 1))

    renderAt(<Tags />, '/tags?id=1', '/tags')
    await waitFor(() => expect(screen.getByText('相关文章甲')).toBeTruthy())
    expect(document.querySelector('.chip.t4')).toBeTruthy()
    expect(document.querySelector('.chip.t3')).toBeTruthy()
    expect(document.querySelector('.chip.t2')).toBeTruthy()
    expect(document.querySelector('.chip.t1')).toBeTruthy()
    expect(client.blog.getTagArticles).toHaveBeenCalledWith(1, { page: 1, pageSize: 12 })
  })

  it('未选中标签：不请求文章；空标签文章显示占位', async () => {
    client.blog.listTags.mockResolvedValueOnce(paged([makeTag({ id: 1 })], 1, 1, 1))
    renderAt(<Tags />, '/tags', '/tags')
    await waitFor(() => expect(screen.getByText('Go')).toBeTruthy())
    await waitFor(() => expect(client.blog.getTagArticles).not.toHaveBeenCalled())
    expect(document.body.textContent).toContain('个标签')
  })

  it('标签加载失败：重试按钮刷新（reloadKey）', async () => {
    client.blog.listTags
      .mockRejectedValueOnce(new Error('标签服务挂了'))
      .mockResolvedValueOnce(paged([makeTag({ id: 1 })], 1, 1, 1))

    renderAt(<Tags />, '/tags', '/tags')
    await waitFor(() => expect(screen.getByText('标签服务挂了')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await waitFor(() => expect(client.blog.listTags).toHaveBeenCalledTimes(2))
  })

  it('相关文章：失败重试后恢复；成功空态显示占位', async () => {
    client.blog.listTags.mockResolvedValue(paged([makeTag({ id: 1 })], 1, 1, 1))
    client.blog.getTagArticles
      .mockRejectedValueOnce(new Error('文章挂了'))
      .mockResolvedValueOnce(paged([makeArticle({ id: 2, title: '恢复的' })], 1, 1, 1))

    renderAt(<Tags />, '/tags?id=1', '/tags')
    await waitFor(() => expect(screen.getByText('文章挂了')).toBeTruthy())

    // 重试按钮只在错误分支存在；reloadKey 重跑两个 effect
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await waitFor(() => expect(screen.getByText('恢复的')).toBeTruthy())

    // 成功但为空 → 占位文案
    client.blog.getTagArticles.mockResolvedValue(paged([], 1, 1, 0))
    const second = renderAt(<Tags />, '/tags?id=1', '/tags')
    await waitFor(() => expect(screen.getAllByText('该标签下暂无文章').length).toBeGreaterThan(0))
    second.unmount()
  })

  it('翻页后换标签：页码收回第 1 页，不去请求新标签的第 2 页', async () => {
    // 回归：换标签时若不把 page 归 1，会带着上一个标签的页码去请求新标签，
    // 静默跳过它的第 1 页。3 篇文章的标签在第 2 页是空的，用户看到的是
    // 「该标签下暂无文章」——一个看起来正常、实际上骗人的页面。
    client.blog.listTags.mockResolvedValue(paged([makeTag({ id: 1, name: 'Go' }), makeTag({ id: 2, name: '前端' })], 1, 1, 2))
    client.blog.getTagArticles.mockResolvedValue(paged([makeArticle({ id: 1, title: '第 1 页' })], 1, 3, 3))

    const view = renderAt(<Tags />, '/tags?id=1', '/tags')
    await waitFor(() => expect(client.blog.getTagArticles).toHaveBeenCalledWith(1, { page: 1, pageSize: 12 }))

    // 翻到第 2 页（Pagination 的按钮名是 "Page N"）。
    client.blog.getTagArticles.mockResolvedValue(paged([makeArticle({ id: 2, title: '第 2 页' })], 2, 3, 3))
    fireEvent.click(screen.getByRole('button', { name: 'Page 2' }))
    await waitFor(() => expect(client.blog.getTagArticles).toHaveBeenCalledWith(1, { page: 2, pageSize: 12 }))

    // 换到「前端」（id=2）：必须从第 1 页取，而不是接着第 2 页。
    // 也不能出现「新标签 + page 2」的请求——那正是修掉的那个 bug：
    // 它会多打一次接口，且响应后到的话会把列表盖成第 2 页（可能是空的）。
    client.blog.getTagArticles.mockClear()
    fireEvent.click(screen.getByRole('link', { name: /前端/ }))
    await waitFor(() => expect(client.blog.getTagArticles).toHaveBeenCalledWith(2, { page: 1, pageSize: 12 }))

    const requestedPages = client.blog.getTagArticles.mock.calls.filter(([tagId]) => tagId === 2).map(([, opts]) => opts.page)
    expect(requestedPages).toEqual([1])
    view.unmount()
  })
})

describe('Search', () => {
  it('空关键词：不请求，显示引导空态', () => {
    renderAt(<Search />, '/search', '/search')
    expect(screen.getByText('输入关键词开始搜索')).toBeTruthy()
    expect(client.blog.search).not.toHaveBeenCalled()
  })

  it('有结果：文章与页面混排、标签拼进 where 行', async () => {
    client.blog.search.mockResolvedValue(paged([
      makeResult({ type: 'article', id: 1, title: '搜到文章', tags: ['Go', 'React', 'Vite', 'TS'] }),
      makeResult({ type: 'page', id: 2, title: '搜到页面', tags: [] }),
    ], 1, 2, 12))

    renderAt(<Search />, '/search?q=墨', '/search')
    await waitFor(() => expect(screen.getByText('搜到文章')).toBeTruthy())
    expect(screen.getByText('搜到页面')).toBeTruthy()
    const where = screen.getByText('搜到文章').previousElementSibling as HTMLElement
    expect(where.textContent).toContain('文章 · ')
    expect(where.textContent).toContain('Go / React / Vite') // 最多 3 个标签
    const where2 = screen.getByText('搜到页面').previousElementSibling as HTMLElement
    expect(where2.textContent).toContain('页面 · ')

    // 翻页
    fireEvent.click(screen.getByRole('button', { name: 'Page 2' }))
    await waitFor(() => expect(client.blog.search).toHaveBeenLastCalledWith({ q: '墨', page: 2, pageSize: 12 }))
  })

  it('无结果与失败分支', async () => {
    client.blog.search
      .mockResolvedValueOnce(paged([], 1, 1, 0))
      .mockRejectedValueOnce(new Error('搜索服务挂了'))

    const first = renderAt(<Search />, '/search?q=不存在', '/search')
    await waitFor(() => expect(screen.getByText('没有找到与「不存在」相关的内容')).toBeTruthy())
    first.unmount()

    renderAt(<Search />, '/search?q=炸', '/search')
    await waitFor(() => expect(screen.getByText('搜索服务挂了')).toBeTruthy())
  })

  it('翻页：退场期间旧结果保持；提交后新页未到显示骨架，分页器仍在', async () => {
    vi.useFakeTimers()
    try {
      const gate = deferred<{ items: ReturnType<typeof makeResult>[]; page: number; page_size: number; total: number; total_pages: number }>()
      client.blog.search
        .mockResolvedValueOnce(paged([makeResult({ id: 1, title: '第一页命中' })], 1, 2, 2))
        .mockReturnValueOnce(gate.promise as never)

      const { container, unmount } = renderAt(<Search />, '/search?q=墨', '/search')
      await act(async () => {})
      expect(screen.getByText('第一页命中')).toBeTruthy()

      fireEvent.click(screen.getByRole('button', { name: 'Page 2' }))
      await act(async () => {})
      // 退场还没播完：旧结果必须留着，不能提前换成新页。
      expect(screen.getByText('第一页命中')).toBeTruthy()

      act(() => { vi.advanceTimersByTime(180) })
      expect(screen.queryByText('第一页命中')).toBeNull()
      expect(container.querySelector('.skl-results')).toBeTruthy()
      // 分页器留在原地并已指向刚点的第 2 页：它消失再出现等于把点击目标抽走。
      expect(screen.getByRole('button', { name: 'Page 2' })).toBeTruthy()

      await act(async () => {
        gate.resolve(paged([makeResult({ id: 2, title: '第二页命中' })], 2, 2, 2))
      })
      expect(screen.getByText('第二页命中')).toBeTruthy()
      unmount()
    } finally {
      vi.useRealTimers()
    }
  })

  it('表单提交：空关键词清空 q；有关键词写回 URL 并搜索', async () => {
    client.blog.search.mockResolvedValue(paged([makeResult()], 1, 1, 1))
    renderAt(<Search />, '/search?q=旧词', '/search')
    await waitFor(() => expect(client.blog.search).toHaveBeenCalled())

    const input = screen.getByPlaceholderText('输入关键词搜索…') as HTMLInputElement
    // 清空提交 → q 被移除 → 空态
    fireEvent.change(input, { target: { value: '   ' } })
    fireEvent.submit(input.closest('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getByText('输入关键词开始搜索')).toBeTruthy())

    // 输入新词提交 → 触发搜索
    fireEvent.change(input, { target: { value: '  新词  ' } })
    fireEvent.submit(input.closest('form') as HTMLFormElement)
    await waitFor(() => expect(client.blog.search).toHaveBeenLastCalledWith({ q: '新词', page: 1, pageSize: 12 }))
  })

  it('搜索关闭：渲染禁用提示页，且 URL 带 q 也不发起任何搜索请求', () => {
    // 能到达这一页的只剩手输 URL 与收藏夹（导航与页脚的入口都摘掉了），
    // 但页面仍然不能对已关闭的后端发起注定 404 的请求。
    document.documentElement.dataset.searchEnabled = 'false'
    renderAt(<Search />, '/search?q=anything', '/search')

    expect(screen.getByText('本站未启用搜索功能')).toBeTruthy()
    expect(screen.queryByPlaceholderText('输入关键词搜索…')).toBeNull()
    expect(client.blog.search).not.toHaveBeenCalled()
  })
})
