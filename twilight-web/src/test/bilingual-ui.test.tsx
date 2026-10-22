// bilingual-ui.test.tsx —— 双语内容、AI 标记与三项站点开关在界面上的落点
//
// 这一组用例刻意走**真实组件 + 共享 client mock**，而不是只测纯函数：
// 纯函数（localize / flags / i18n）各自有自己的用例，这里要证明的是
// 「组件确实读了它们」——标题、标签、提示条、开关、快捷入口都真的跟着变。
// 只测纯函数的风险是：函数写对了，但页面里还在用旧的读取路径。

import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import Post from '../pages/public/Post'
import Moments from '../pages/public/Moments'
import Editor from '../pages/admin/Editor'
import Posts from '../pages/admin/Posts'
import TagsAdmin from '../pages/admin/TagsAdmin'
import SiteSettings from '../pages/admin/SiteSettings'
import Dashboard from '../pages/admin/Dashboard'
import { DialogProvider } from '../lib/dialog'
import { ToastProvider } from '../lib/toast'
import { resetSiteCacheForTests } from '../lib/site'
import { setLocale } from '../lib/i18n'
import {
  getSharedClientMock,
  makeArticle,
  makeTag,
  resetClientMock,
} from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

function paged<T>(items: T[], page = 1, total_pages = 1, total = items.length) {
  return { items, page, page_size: 12, total, total_pages }
}

function renderRouted(ui: React.ReactElement, route: string, pattern: string) {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <ToastProvider>
        <DialogProvider>
          <Routes>
            <Route path={pattern} element={ui} />
            <Route path="/admin/editor/:id" element={<div>EDITOR-REPLACED</div>} />
            <Route path="/admin/posts" element={<div>POSTS-LIST</div>} />
          </Routes>
        </DialogProvider>
      </ToastProvider>
    </MemoryRouter>,
  )
}

function renderPlain(ui: React.ReactElement) {
  return render(
    <ToastProvider>
      <DialogProvider>{ui}</DialogProvider>
    </ToastProvider>,
  )
}

function titleInput(): HTMLInputElement {
  return document.querySelector('.ed-title input, input.ed-title, input') as HTMLInputElement
}

beforeEach(() => {
  resetClientMock(client)
  resetSiteCacheForTests()
  client.setCurrentUser(null)
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
  delete document.documentElement.dataset.momentsEnabled
})

describe('Post 双语与 AI 标记', () => {
  it('中文界面取中文；AI 标记显示提示条；切到英文后标题、正文、标签全部跟随', async () => {
    client.blog.getArticle.mockResolvedValue(makeArticle({
      id: 1,
      title: '中文标题',
      title_en: 'English title',
      content: '中文正文',
      content_en: 'English body',
      tags: ['Go', '前端'],
      tags_en: ['Go', 'Frontend'],
      ai_generated: true,
    }))
    client.blog.listComments.mockResolvedValue(paged([]))

    renderRouted(<Post />, '/post/1', '/post/:id')

    await waitFor(() => expect(screen.getByText('中文标题')).toBeTruthy())
    // AI 提示条：role=note 是读屏会主动播报的语义，别退化成普通 div。
    expect(screen.getByRole('note').textContent).toContain('本文由 AI 生成')
    expect(screen.getByText('# 前端')).toBeTruthy()

    act(() => setLocale('en'))

    await waitFor(() => expect(screen.getByText('English title')).toBeTruthy())
    // 正文走 Markdown 管线，断言渲染后的文本节点。
    expect(screen.getByText('English body')).toBeTruthy()
    expect(screen.getByText('# Frontend')).toBeTruthy()
    expect(screen.queryByText('# 前端')).toBeNull()
  })

  it('未标记 AI 的文章不显示提示条', async () => {
    client.blog.getArticle.mockResolvedValue(makeArticle({ id: 2, title: '人工写的', ai_generated: false }))
    client.blog.listComments.mockResolvedValue(paged([]))

    renderRouted(<Post />, '/post/2', '/post/:id')
    await waitFor(() => expect(screen.getByText('人工写的')).toBeTruthy())
    expect(screen.queryByRole('note')).toBeNull()
  })
})

describe('Editor 双语文档与 AI 开关', () => {
  it('标签页各自保存标题；AI 开关随保存下发', async () => {
    renderRouted(<Editor />, '/admin/editor', '/admin/editor')

    fireEvent.change(titleInput(), { target: { value: '中文标题' } })

    // 切到 English：占位符与可编辑内容一起换，中文标题不受影响。
    fireEvent.click(screen.getByRole('tab', { name: 'English' }))
    expect(screen.getByRole('tab', { name: 'English' }).getAttribute('aria-selected')).toBe('true')
    expect(screen.getByRole('tab', { name: '中文' }).getAttribute('aria-selected')).toBe('false')
    fireEvent.change(titleInput(), { target: { value: 'English title' } })

    // 切回中文标签：中文标题仍在，两份内容互不覆盖。
    fireEvent.click(screen.getByRole('tab', { name: '中文' }))
    expect(titleInput().value).toBe('中文标题')
    fireEvent.click(screen.getByRole('tab', { name: 'English' }))
    expect(titleInput().value).toBe('English title')

    fireEvent.click(screen.getByRole('checkbox'))
    expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(true)

    client.blog.createArticle.mockResolvedValueOnce(makeArticle({ id: 9 }))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(client.blog.createArticle).toHaveBeenCalledWith(expect.objectContaining({
      title: '中文标题',
      titleEn: 'English title',
      aiGenerated: true,
    })))
  })
})

describe('后台列表与设置页', () => {
  it('文章列表给 AI 生成的文章打徽章', async () => {
    client.blog.listArticles.mockResolvedValue(paged([
      makeArticle({ id: 1, title: '机器写的', ai_generated: true }),
      makeArticle({ id: 2, title: '人写的', ai_generated: false }),
    ]))
    client.blog.listTrashArticles.mockResolvedValue(paged([]))

    renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    await waitFor(() => expect(screen.getByText('机器写的')).toBeTruthy())
    expect(screen.getAllByText('AI 生成').length).toBe(1)
  })

  it('标签编辑同时提交中英文名；列表回显英文名', async () => {
    client.blog.listTags.mockResolvedValue(paged([makeTag({ id: 1, name: 'Go', name_en: 'Golang' })]))
    client.blog.renameTag.mockResolvedValueOnce({ message: 'ok' })

    renderPlain(<TagsAdmin />)
    await waitFor(() => expect(screen.getByText('Golang')).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: '编辑' }))
    const inputs = document.querySelectorAll<HTMLInputElement>('.frow input')
    expect(inputs[0].value).toBe('Go')
    expect(inputs[1].value).toBe('Golang')

    fireEvent.change(inputs[0], { target: { value: 'Golang 语言' } })
    fireEvent.change(inputs[1], { target: { value: 'The Go language' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(client.blog.renameTag).toHaveBeenCalledWith(1, {
      name: 'Golang 语言',
      nameEn: 'The Go language',
    }))
  })

  it('站点设置：头图高度、微语开关、评论通知地址一起回读并一起保存', async () => {
    client.blog.getSite.mockResolvedValue({
      description: '测试站',
      post_banner_height: 300,
      moments_enabled: false,
    })
    client.blog.getAdminSite.mockResolvedValue({ comment_notify_to: 'owner@example.com' })
    client.blog.updateSite.mockResolvedValue({
      description: '测试站',
      post_banner_height: 500,
      moments_enabled: true,
    })

    const { container } = renderPlain(<SiteSettings />)
    const numbers = () => container.querySelectorAll<HTMLInputElement>('input[type=number]')
    const notify = () => container.querySelector('input[type=email]') as HTMLInputElement

    await waitFor(() => expect(numbers()[3].value).toBe('300'))
    expect(notify().value).toBe('owner@example.com')
    // 微语开关是页面里唯一的 checkbox。
    expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(false)

    fireEvent.change(numbers()[3], { target: { value: '500' } })
    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.change(notify(), { target: { value: '  new@example.com  ' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)

    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledWith(expect.objectContaining({
      post_banner_height: 500,
      moments_enabled: true,
      comment_notify_to: 'new@example.com',
    })))
    // 保存后用服务端返回值回填。
    await waitFor(() => expect(numbers()[3].value).toBe('500'))
    expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(true)
  })

  it('站点设置：头图越界与通知邮箱格式错误各自拦截，不发请求', async () => {
    client.blog.getSite.mockResolvedValue({ description: '测试站' })
    client.blog.getAdminSite.mockRejectedValue(new Error('forbidden'))

    const { container } = renderPlain(<SiteSettings />)
    const numbers = () => container.querySelectorAll<HTMLInputElement>('input[type=number]')
    await waitFor(() => expect(numbers()[3].value).toBe('400'))
    // 管理端快照失败只让通知框留空，其余设置照常可用。
    expect((container.querySelector('input[type=email]') as HTMLInputElement).value).toBe('')

    fireEvent.change(numbers()[3], { target: { value: '119' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getAllByText('头图高度必须是 120 到 800 之间的整数').length).toBeGreaterThan(0))

    fireEvent.change(numbers()[3], { target: { value: '400' } })
    fireEvent.change(container.querySelector('input[type=email]') as HTMLInputElement, { target: { value: 'not-an-email' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getAllByText('评论通知邮箱格式不正确').length).toBeGreaterThan(0))

    expect(client.blog.updateSite).not.toHaveBeenCalled()
  })
})

describe('微语开关在页面上的落点', () => {
  it('关闭时仪表盘不再显示「发布微语」快捷入口', async () => {
    for (const method of ['listArticles', 'listPages', 'listTags', 'listMoments', 'listAllComments'] as const) {
      client.blog[method].mockResolvedValue(paged([], 1, 1, 0))
    }

    const first = render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('仪表盘')).toBeTruthy())
    expect(screen.getByRole('link', { name: '发布微语' })).toBeTruthy()
    first.unmount()

    document.documentElement.dataset.momentsEnabled = 'false'
    render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('仪表盘')).toBeTruthy())
    expect(screen.queryByRole('link', { name: '发布微语' })).toBeNull()
  })

  it('关闭时公开微语页给出说明，且不发注定 404 的请求', async () => {
    document.documentElement.dataset.momentsEnabled = 'false'
    renderRouted(<Moments />, '/moments', '/moments')

    expect(screen.getByText('本站未启用微语功能')).toBeTruthy()
    expect(client.blog.listMoments).not.toHaveBeenCalled()
  })

  it('开启时正常拉取列表', async () => {
    client.blog.listMoments.mockResolvedValue(paged([{ id: 1, content: '一条微语', media_items: [], created_at: 1_700_000_000, updated_at: 1_700_000_000 }]))

    renderRouted(<Moments />, '/moments', '/moments')
    await waitFor(() => expect(screen.getByText('一条微语')).toBeTruthy())
    expect(client.blog.listMoments).toHaveBeenCalledWith({ page: 1, pageSize: 12 })
  })
})
