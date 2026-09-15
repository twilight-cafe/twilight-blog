// article-cover.test.tsx —— 文章头图（CoverPicker / 编辑器接入 / 公开页呈现）与编辑器侧发布时间
//
// 这个文件钉的是「引用 vs 所有权」的用户可见侧：头图是**引用**，上传新图、摘除头图
// 都只解除引用，绝不删掉媒体库里已有的文件——用户可能只是换张试试，而媒体是硬删、
// 没有回收站。所以这里只在一种情况下断言 deleteMedia：前端自己刚上传了非图片，
// 那份文件还没被任何人引用过，留着它就是媒体库里一个来历不明的孤儿。
//
// 唯一会「删掉头图那份文件」的地方是服务端永久删除文章（Go 侧
// article_service.HardDelete），不在这个文件的职责里。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { createClient, mediaUrl } from 'twilight-blog-api'
import CoverPicker from '../components/media/CoverPicker'
import Editor from '../pages/admin/Editor'
import Home from '../pages/public/Home'
import Post from '../pages/public/Post'
import { DialogProvider } from '../lib/dialog'
import { ToastProvider } from '../lib/toast'
import { resetSiteCacheForTests } from '../lib/site'
import { NOW, getSharedClientMock, makeArticle, makeMedia, resetClientMock } from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => { resolve = res })
  return { promise, resolve }
}

function paged<T>(items: T[], page = 1, total_pages = 1, total = items.length) {
  return { items, page, page_size: 12, total, total_pages }
}

/** 给隐藏的 file input 塞文件再触发 change——jsdom 里 files 只读，必须 defineProperty。 */
function chooseFiles(container: HTMLElement, files: File[] | null) {
  const input = container.querySelector('input[type="file"]') as HTMLInputElement
  Object.defineProperty(input, 'files', { value: files, configurable: true })
  fireEvent.change(input)
}

function renderPicker(props: { value?: number | null; disabled?: boolean; withBusyCallback?: boolean } = {}) {
  const onChange = vi.fn()
  const onBusyChange = vi.fn()
  const utils = render(
    <ToastProvider>
      <CoverPicker
        value={props.value ?? null}
        onChange={onChange}
        // withBusyCallback: false 用来走 `onBusyChange?.()` 的空值分支（该 prop 可选）。
        {...(props.withBusyCallback === false ? {} : { onBusyChange })}
        disabled={props.disabled}
      />
    </ToastProvider>,
  )
  return { ...utils, onChange, onBusyChange }
}

beforeEach(() => {
  vi.clearAllMocks()
  resetClientMock(client)
  client.setCurrentUser(null)
  resetSiteCacheForTests()
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
  client.blog.listComments.mockResolvedValue({ items: [], page: 1, page_size: 12, total: 0, total_pages: 1 })
})

describe('CoverPicker', () => {
  it('无头图：点占位框打开文件对话框；选中图片后上传并回传 id', async () => {
    const uploaded = makeMedia({ id: 77, media_type: 'image', original_name: 'cover.png' })
    client.blog.uploadMedia.mockResolvedValueOnce([uploaded])

    const { container, onChange, onBusyChange } = renderPicker()
    // 空态：显示「上传头图」而不是任何预览
    const empty = container.querySelector('.cover-empty') as HTMLButtonElement
    expect(empty.textContent).toContain('上传头图')
    expect(container.querySelector('.cover-preview')).toBeNull()

    // 点占位框应当去点隐藏的 file input——用一个 spy 证明「点了」，而不是只证明没报错。
    const click = vi.spyOn(HTMLInputElement.prototype, 'click')
    fireEvent.click(empty)
    expect(click).toHaveBeenCalledTimes(1)
    click.mockRestore()

    chooseFiles(container, [new File(['x'], 'cover.png', { type: 'image/png' })])
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(77))
    // busy 必须复位，否则外层表单会永远停在「保存」不可用的状态。
    expect(onBusyChange).toHaveBeenNthCalledWith(1, true)
    expect(onBusyChange).toHaveBeenLastCalledWith(false)
    // input.value 必须清空，否则再选同一个文件不会触发 change（浏览器不会为相同值派发事件）。
    expect((container.querySelector('input[type="file"]') as HTMLInputElement).value).toBe('')
  })

  it('files 为 null 或空选择：直接返回，不上传、不碰 busy', async () => {
    const { container, onChange, onBusyChange } = renderPicker()
    // files 为 null（覆盖 `files?.[0]` 的空值分支）
    chooseFiles(container, null)
    // files 为空列表（用户点了取消）
    chooseFiles(container, [])
    await act(async () => {})
    expect(client.blog.uploadMedia).not.toHaveBeenCalled()
    expect(onChange).not.toHaveBeenCalled()
    expect(onBusyChange).not.toHaveBeenCalled()
  })

  it('上传结果为空数组：提示失败且不改头图', async () => {
    client.blog.uploadMedia.mockResolvedValueOnce([])
    const { container, onChange } = renderPicker()
    chooseFiles(container, [new File(['x'], 'a.png', { type: 'image/png' })])
    await waitFor(() => expect(screen.getAllByText('上传失败').length).toBeGreaterThan(0))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('上传的是非图片：删掉刚上传的那一份并提示；清理失败也照样提示', async () => {
    const audio = makeMedia({ id: 88, media_type: 'audio', original_name: 'bgm.mp3' })
    client.blog.uploadMedia.mockResolvedValueOnce([audio]).mockResolvedValueOnce([audio])
    const { container, onChange } = renderPicker()

    chooseFiles(container, [new File(['x'], 'bgm.mp3', { type: 'audio/mpeg' })])
    await waitFor(() => expect(screen.getAllByText('头图必须是图片').length).toBeGreaterThan(0))
    expect(client.blog.deleteMedia).toHaveBeenCalledWith(88)
    expect(onChange).not.toHaveBeenCalled()

    // 第二次：deleteMedia 失败（清理是尽力而为，不能因此改变「不能当头图」这个结论，
    // 也不能把异常抛给调用方让整个保存流程崩掉）。
    client.blog.deleteMedia.mockRejectedValueOnce(new Error('删不掉'))
    chooseFiles(container, [new File(['x'], 'bgm.mp3', { type: 'audio/mpeg' })])
    await waitFor(() => expect(screen.getAllByText('头图必须是图片').length).toBeGreaterThan(1))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('上传抛错：按服务端信息提示，头图不变', async () => {
    client.blog.uploadMedia.mockRejectedValueOnce(new Error('文件太大'))
    const { container, onChange } = renderPicker()
    chooseFiles(container, [new File(['x'], 'huge.png', { type: 'image/png' })])
    await waitFor(() => expect(screen.getAllByText('文件太大').length).toBeGreaterThan(0))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('上传中：空态显示 loading；有头图时「更换」被替换成「上传中…」', async () => {
    const gate = deferred<ReturnType<typeof makeMedia>[]>()
    client.blog.uploadMedia.mockImplementation(() => gate.promise)
    const { container, rerender } = render(
      <ToastProvider>
        <CoverPicker value={null} onChange={vi.fn()} />
      </ToastProvider>,
    )

    chooseFiles(container, [new File(['x'], 'a.png', { type: 'image/png' })])
    await waitFor(() => expect(container.querySelector('.media-picker-loading')).toBeTruthy())
    expect(container.querySelector('.cover-empty')?.textContent).toContain('上传中…')
    // 上传中必须禁止再次点击，否则会并发上传出两张图。
    expect((container.querySelector('.cover-empty') as HTMLButtonElement).disabled).toBe(true)

    // 有头图时的上传中态：走的是另一支 JSX（预览 + 两个按钮），文案也不同。
    rerender(
      <ToastProvider>
        <CoverPicker value={42} onChange={vi.fn()} disabled />
      </ToastProvider>,
    )
    expect(container.querySelector('.cover-preview img')?.getAttribute('src')).toBe('/api/media/42')
    expect(container.querySelector('.cover-actions')?.textContent).toContain('上传中…')

    await act(async () => {
      gate.resolve([])
    })
  })

  it('有头图：预览、更换、移除；disabled 时两个按钮都禁用', async () => {
    const onChange = vi.fn()
    const { container, rerender } = render(
      <ToastProvider>
        <CoverPicker value={42} onChange={onChange} />
      </ToastProvider>,
    )

    expect(container.querySelector('.cover-empty')).toBeNull()
    expect(container.querySelector('.cover-preview img')?.getAttribute('src')).toBe('/api/media/42')
    expect(container.querySelector('.cover-preview img')?.getAttribute('alt')).toBe('文章头图')

    const click = vi.spyOn(HTMLInputElement.prototype, 'click')
    fireEvent.click(screen.getByRole('button', { name: '更换' }))
    expect(click).toHaveBeenCalledTimes(1)
    click.mockRestore()

    // 移除只解除引用：不调 deleteMedia（那张图还在媒体库里，用户可能只是换个试试）。
    fireEvent.click(screen.getByRole('button', { name: '移除' }))
    expect(onChange).toHaveBeenCalledWith(null)
    expect(client.blog.deleteMedia).not.toHaveBeenCalled()

    // disabled：两个按钮都不可点（外部正在保存，不能被改出新值）。
    rerender(
      <ToastProvider>
        <CoverPicker value={42} onChange={onChange} disabled />
      </ToastProvider>,
    )
    expect((screen.getByRole('button', { name: '更换' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '移除' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('上传还没回来就卸载：finally 里的 ref 守卫早退，不抛错', async () => {
    const gate = deferred<ReturnType<typeof makeMedia>[]>()
    client.blog.uploadMedia.mockImplementation(() => gate.promise)
    const { container, unmount } = render(
      <ToastProvider>
        <CoverPicker value={null} onChange={vi.fn()} />
      </ToastProvider>,
    )

    chooseFiles(container, [new File(['x'], 'a.png', { type: 'image/png' })])
    // 上传中离开页面（比如点了「返回」）：inputRef 已被 React 置空，收尾逻辑必须能容忍。
    unmount()
    await act(async () => {
      gate.resolve([])
    })
  })
})

/* --------------------------- 编辑器：头图 + 发布时间 --------------------------- */

function renderEditor(route: string) {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <ToastProvider>
        <DialogProvider>
          <Routes>
            {/* 新建成功后会 replace 到 /admin/editor/<id>，这里必须给一个落地路由，
                否则「保存 → 跳转」这条链路在测试里会渲染成空。 */}
            <Route path="/admin/editor/:id" element={<Editor />} />
            <Route path="/admin/editor" element={<Editor />} />
            <Route path="/admin/posts" element={<div>POSTS-LIST</div>} />
          </Routes>
        </DialogProvider>
      </ToastProvider>
    </MemoryRouter>,
  )
}

/** 打开日期面板并选当月 1 号再「确定」（showTime 模式必须点确定才提交）。 */
function pickFirstOfMonth(container: HTMLElement) {
  fireEvent.click(container.querySelector('.dp-trigger') as HTMLElement)
  const cells = Array.from(container.querySelectorAll('.dp-cell')) as HTMLElement[]
  // 过滤掉上/下月的补位格：buildCells 恒产出 42 格，所以下个月的 1 号必然也在里面。
  const inMonth = cells.filter((cell) => !cell.classList.contains('dp-cell-out'))
  const first = inMonth.find((cell) => cell.textContent === '1') as HTMLElement
  fireEvent.click(first)
  fireEvent.click(container.querySelector('.dp-ok') as HTMLElement)
}

/**
 * pickFirstOfMonth 之后应提交的时间戳。
 *
 * 不是「当月 1 号 00:00」：showTime 模式下点日期只改年月日，时分沿用当时的值
 * （DatePicker 的 handleCellClick 用 baseHour/baseMinute 构造草稿），秒归零。
 * 期望值用与组件同一套本地构造算出，因此不受跑测试的机器时区影响。
 */
function expectedAfterPickingFirstOfMonth(): number {
  const base = new Date(NOW)
  const picked = new Date(base.getFullYear(), base.getMonth(), 1, base.getHours(), base.getMinutes())
  return Math.floor(picked.getTime() / 1000)
}

/**
 * 新建（当前值为 null）时 pickFirstOfMonth 应提交的时间戳。
 *
 * 面板的视图月跟着「当前值」走，没有值就落在**今天**所在月——所以这里要按今天
 * 算，不能照抄 created_at 的月份；时分也因为没有基准而默认 00:00。
 */
function expectedAfterPickingFirstOfMonthWithNoValue(): number {
  const today = new Date()
  const picked = new Date(today.getFullYear(), today.getMonth(), 1, 0, 0)
  return Math.floor(picked.getTime() / 1000)
}

function saveButton(container: HTMLElement) {
  return (Array.from(container.querySelectorAll('button')) as HTMLButtonElement[])
    .find((button) => button.textContent === '保存') as HTMLButtonElement
}

describe('Editor 头图与发布时间', () => {
  it('编辑既有文章：回填头图与发布时间，改日期后按新值提交', async () => {
    client.blog.getArticle.mockResolvedValueOnce(makeArticle({ id: 7, title: '旧文', content: '旧正文', tags: ['Go'], cover_media_id: 42 }))
    const { container } = renderEditor('/admin/editor/7')

    await waitFor(() => expect(container.querySelector('.cover-preview img')?.getAttribute('src')).toBe('/api/media/42'))

    // 选当月 1 号：期望值用同一套本地构造算出来，与运行机器的时区无关。
    const expected = expectedAfterPickingFirstOfMonth()
    pickFirstOfMonth(container)

    client.blog.updateArticle.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(saveButton(container))
    await waitFor(() => expect(client.blog.updateArticle).toHaveBeenCalledWith(7, {
      title: '旧文',
      content: '旧正文',
      tags: ['Go'],
      coverMediaId: 42,
      publishedAt: expected,
    }))
  })

  it('摘除头图 + 清空日期：coverMediaId 为 null，且不下发 publishedAt', async () => {
    client.blog.getArticle.mockResolvedValueOnce(makeArticle({ id: 7, title: '旧文', cover_media_id: 42 }))
    const { container } = renderEditor('/admin/editor/7')

    await waitFor(() => expect(container.querySelector('.cover-preview img')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '移除' }))
    // 摘除后回到空态，说明前端状态真的改了而不是只发了个请求。
    expect(container.querySelector('.cover-preview')).toBeNull()
    expect(container.querySelector('.cover-empty')).toBeTruthy()

    fireEvent.click(container.querySelector('.dp-clear') as HTMLElement)
    expect(container.querySelector('.dp-value')).toBeNull()

    client.blog.updateArticle.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(saveButton(container))
    // 关键：publishedAt 这个键**不出现**。写成 publishedAt: undefined 会被
    // toHaveBeenCalledWith 判为不等价（这正是要钉的：省略 ≠ 传 null）。
    await waitFor(() => expect(client.blog.updateArticle).toHaveBeenCalledWith(7, {
      title: '旧文',
      content: '这是正文内容。',
      tags: ['Go'],
      coverMediaId: null,
    }))
    const payload = client.blog.updateArticle.mock.calls[0][1] as Record<string, unknown>
    expect(Object.prototype.hasOwnProperty.call(payload, 'publishedAt')).toBe(false)
  })

  it('新建：上传头图期间「保存」禁用，上传完成后新 id 随创建请求下发', async () => {
    const gate = deferred<ReturnType<typeof makeMedia>[]>()
    client.blog.uploadMedia.mockImplementation(() => gate.promise)
    client.blog.createArticle.mockResolvedValueOnce(makeArticle({ id: 9 }))
    // 创建成功会 replace 到 /admin/editor/9，落地页会再拉一次文章；不备好这份 mock，
    // 那个 effect 就会在未 mock 的 undefined 上 .then 而抛错。
    client.blog.getArticle.mockResolvedValue(makeArticle({ id: 9 }))

    const { container } = renderEditor('/admin/editor')
    fireEvent.change(container.querySelector('input.ed-title, .ed-title input, input') as HTMLInputElement, { target: { value: '带头图的新文' } })
    expect(saveButton(container).disabled).toBe(false)

    chooseFiles(container, [new File(['x'], 'cover.png', { type: 'image/png' })])
    // 上传没结束就允许保存，会存下一篇「看起来选了头图、其实没有」的文章。
    await waitFor(() => expect(saveButton(container).disabled).toBe(true))
    expect(container.querySelector('.media-picker-loading')).toBeTruthy()

    await act(async () => {
      gate.resolve([makeMedia({ id: 55, media_type: 'image' })])
    })
    await waitFor(() => expect(saveButton(container).disabled).toBe(false))

    fireEvent.click(saveButton(container))
    await waitFor(() => expect(client.blog.createArticle).toHaveBeenCalledWith({
      title: '带头图的新文',
      content: '',
      tags: [],
      coverMediaId: 55,
    }))
  })

  it('新建时选了日期：publishedAt 随创建请求下发', async () => {
    client.blog.createArticle.mockResolvedValueOnce(makeArticle({ id: 10 }))
    client.blog.getArticle.mockResolvedValue(makeArticle({ id: 10 }))
    const { container } = renderEditor('/admin/editor')

    const expected = expectedAfterPickingFirstOfMonthWithNoValue()
    pickFirstOfMonth(container)

    fireEvent.change(container.querySelector('input.ed-title, .ed-title input, input') as HTMLInputElement, { target: { value: '补录的旧文' } })
    fireEvent.click(saveButton(container))
    await waitFor(() => expect(client.blog.createArticle).toHaveBeenCalledWith({
      title: '补录的旧文',
      content: '',
      tags: [],
      coverMediaId: null,
      publishedAt: expected,
    }))
  })
})

/* ------------------------------ 公开页呈现 ------------------------------ */

describe('头图在公开页的呈现', () => {
  it('首页卡片：有头图才切两栏并渲染图片，没头图的卡片保持单栏', async () => {
    client.blog.listArticles.mockResolvedValueOnce(paged([
      makeArticle({ id: 1, title: '有图', cover_media_id: 31 }),
      makeArticle({ id: 2, title: '无图', cover_media_id: null }),
    ]))

    const { container } = render(
      <MemoryRouter>
        <ToastProvider>
          <Home />
        </ToastProvider>
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('有图')).toBeTruthy())

    const cards = Array.from(container.querySelectorAll('.acard')) as HTMLElement[]
    expect(cards[0].classList.contains('has-cover')).toBe(true)
    expect(cards[1].classList.contains('has-cover')).toBe(false)

    const img = cards[0].querySelector('.cover img') as HTMLImageElement
    expect(img.getAttribute('src')).toBe('/api/media/31')
    // alt 留空是刻意的：标题就在旁边，写 alt 会让读屏把同一件事念两遍。
    expect(img.getAttribute('alt')).toBe('')
    expect(img.getAttribute('loading')).toBe('lazy')
    expect(cards[1].querySelector('.cover')).toBeNull()
  })

  it('文章页：头图渲染在标签/标题之上，没有头图时整个容器不出现', async () => {
    client.blog.getArticle.mockResolvedValueOnce(makeArticle({ id: 3, title: '带图的文章', cover_media_id: 66 }))
    const first = render(
      <MemoryRouter initialEntries={['/post/3']}>
        <ToastProvider>
          <Routes>
            <Route path="/post/:id" element={<Post />} />
          </Routes>
        </ToastProvider>
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('带图的文章')).toBeTruthy())

    const cover = first.container.querySelector('.post-cover') as HTMLElement
    expect(cover.querySelector('img')?.getAttribute('src')).toBe('/api/media/66')
    expect(cover.querySelector('img')?.getAttribute('alt')).toBe('')
    // 位置契约：头图必须是正文栏的**第一个**子元素——这样它才会落在顶栏之下、
    // 标签之上，且与正文同宽（侧边栏仍在右侧，而不是被顶到图下面）。
    expect(first.container.querySelector('.post-wrap')?.firstElementChild).toBe(cover)
    first.unmount()

    client.blog.getArticle.mockResolvedValueOnce(makeArticle({ id: 4, title: '没图的文章', cover_media_id: null }))
    const second = render(
      <MemoryRouter initialEntries={['/post/4']}>
        <ToastProvider>
          <Routes>
            <Route path="/post/:id" element={<Post />} />
          </Routes>
        </ToastProvider>
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('没图的文章')).toBeTruthy())
    expect(second.container.querySelector('.post-cover')).toBeNull()
  })
})

/* --------------------------- SDK 下发的请求体形状 --------------------------- */

/**
 * 这一组刻意**不用** client mock：它要钉的正是封装层自己的三条下发规则，
 * 而 mock 会把封装整个换掉，从而让「省略 / null / 数字」的区别无处可测。
 *
 * 危险的那条是 null：塌成 `??` 或 `!= null` 之后，「摘除头图」就再也发不出去，
 * 用户点「移除」→ 保存 → 头图原样复活，而全程没有任何报错。所以这里逐条断言
 * **键是否存在**，而不是只看值。
 */
describe('SDK 下发的请求体形状（真实模块）', () => {
  function recordingClient() {
    const bodies: Record<string, unknown>[] = []
    const fetchStub = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      bodies.push(JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>)
      return new Response(JSON.stringify({ id: 1 }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })
    })
    return { api: createClient({ baseUrl: 'https://blog.test', fetch: fetchStub as unknown as typeof fetch }), bodies }
  }

  it('mediaUrl 拼出站内相对地址', () => {
    expect(mediaUrl(42)).toBe('/api/media/42')
  })

  it('createArticle：省略的两个字段**不出现**，传了才出现（含 null）', async () => {
    const { api, bodies } = recordingClient()

    await api.createArticle({ title: 'T', content: 'C' })
    expect(bodies[0]).toEqual({ title: 'T', content: 'C', tags: [] })
    expect('published_at' in bodies[0]).toBe(false)
    expect('cover_media_id' in bodies[0]).toBe(false)

    await api.createArticle({ title: 'T', content: 'C', tags: ['x'], publishedAt: 1698768000, coverMediaId: null })
    expect(bodies[1]).toEqual({
      title: 'T',
      content: 'C',
      tags: ['x'],
      // 创建时 null 表示「没有头图」，必须真的下发，否则服务端无从区分
      // 「明文说没有」与「调用方没提这件事」。
      cover_media_id: null,
      published_at: 1698768000,
    })
  })

  it('updateArticle：省略即不动；null 必须下发（摘除头图靠它）', async () => {
    const { api, bodies } = recordingClient()

    // 只改标题：两个字段都不该出现在请求体里，否则就成了「顺手把头图摘掉」。
    await api.updateArticle(7, { title: '新标题' })
    expect(bodies[0]).toEqual({ title: '新标题' })

    // 摘除头图：null 必须出现。
    await api.updateArticle(7, { title: '新标题', coverMediaId: null })
    expect(bodies[1]).toEqual({ title: '新标题', cover_media_id: null })

    // 换成另一张图。
    await api.updateArticle(7, { title: '新标题', coverMediaId: 9, publishedAt: 1698768000 })
    expect(bodies[2]).toEqual({ title: '新标题', cover_media_id: 9, published_at: 1698768000 })
  })
})

// 卡片头图的栅格。
//
// 这一组是**文本级**断言，看起来像在测实现细节，原因是别的做法都测不到它：
// jsdom 不做布局，任何 DOM 断言都看不见「卡片被头图撑高」这件事。而这个缺陷是真实
// 存在过的——640x1120 的竖图在 220px 定宽下固有高度 385px，仍处于文档流中的图片会
// 把它顶成行高，同长度的卡片从 279px 变成 387px，正文列留下 122px 空白；图越高越糟
// （1080x2400 会到 ~489px）。
//
// 真正的验证在真浏览器里，脚本是 .workbuddy/scripts/visual-measure.sh（量到的是
// 卡片高、正文内容高、浪费空间三者）。CI 里起不了浏览器，所以这里退一步：钉住那个
// 让「行高只由正文决定」成立的**唯一前提**——头图不在文档流里。它一被拿掉，上面
// 那个 122px 就会静默回来。
describe('卡片头图的栅格（行高必须由正文决定）', () => {
  // 直接读文件，不走 `import ... from '../styles/app.css?raw'`：vitest 默认不处理 CSS
  // （test.css 未开），`?raw` 在这个配置下拿到的是空串——而空串会让下面每个选择器都
  // 找不到、断言以「找不到规则」失败，看上去像 CSS 写错了。用 node:fs 读源码与打包器
  // 无关。（vitest 只把 `node:fs/promises` 别名成了浏览器替身，`node:fs` 是真实的。）
  //
  // 路径基于 process.cwd() 而不是 import.meta.url：后者在 jsdom 环境里是
  // `http://localhost/...`，new URL 出来的不是 file scheme，readFileSync 会直接报
  // 「The URL must be of scheme file」。vitest 的 cwd 恒为项目根（twilight-web）。
  const appCss = readFileSync(resolve(process.cwd(), 'src/styles/app.css'), 'utf8')

  // 取某个选择器的声明块。用 indexOf 而不是正则：选择器里的 `.` 与空格要逐个转义，
  // 容易写错，而写错的正则匹配不到东西时会**静默通过**——那正是这个测试要防的事。
  function ruleBlock(selector: string): string {
    const start = appCss.indexOf(`${selector} {`)
    if (start === -1) return ''
    return appCss.slice(start, appCss.indexOf('}', start))
  }

  it('头图必须脱离文档流，否则会按自身宽高比顶起行高', () => {
    const imgRule = ruleBlock('.acard .cover img')
    expect(imgRule, '找不到 .acard .cover img 规则').not.toBe('')
    expect(imgRule).toMatch(/position:\s*absolute/)
    // 脱离文档流之后必须自己填满容器，否则会塌成 0 高。
    expect(imgRule).toMatch(/inset:\s*0|top:\s*0/)
    expect(imgRule).toMatch(/width:\s*100%/)
    expect(imgRule).toMatch(/height:\s*100%/)
    // cover 而不是 fill：裁掉边缘，不要拉变形。
    expect(imgRule).toMatch(/object-fit:\s*cover/)

    // 绝对定位需要一个定位过的祖先。
    expect(ruleBlock('.acard .cover')).toMatch(/position:\s*relative/)
  })

  it('头图那一栏必须定宽且克制，且不留内边距（否则贴不到卡片边界）', () => {
    const cardRule = ruleBlock('.acard.has-cover')
    expect(cardRule).toMatch(/display:\s*grid/)
    // 定宽而不是按比例：按比例会让有图的卡片正文明显变窄、标题换行数随图长变化。
    const columns = /grid-template-columns:\s*(\d+)px/.exec(cardRule)
    expect(columns, '头图那一栏必须是 px 定宽').not.toBeNull()
    expect(Number(columns![1])).toBeLessThanOrEqual(280)

    // 上下左与卡片边界重合，靠的是这里没有内边距 + 卡片自身的 overflow: hidden。
    const coverRule = ruleBlock('.acard .cover')
    expect(coverRule).not.toMatch(/padding/)
  })
})
