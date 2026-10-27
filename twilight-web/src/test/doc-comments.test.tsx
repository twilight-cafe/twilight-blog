// doc-comments.test.tsx —— 文章页 / 独立页面共用评论宿主
//
// 这条回归守的是「切换路由时评论框消失又出现」：两条路由共享同一个布局，
// Comments 不能因为正文页组件不同而被卸载重挂。DOM 节点 identity 是这里最硬的
// 断言——只看「评论区还在」无法区分「原样保留」和「刚重新挂载了一份」。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Link, MemoryRouter, Outlet, Route, Routes } from 'react-router-dom'
import DocCommentsLayout from '../components/DocCommentsLayout'
import Post from '../pages/public/Post'
import PageDetail from '../pages/public/PageDetail'
import { parseDocCommentTarget } from '../lib/doc-comments'
import { ToastProvider } from '../lib/toast'
import { adminMe, getSharedClientMock, makeArticle, makePage, resetClientMock } from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

function renderDocs(route: string) {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <ToastProvider>
        <Routes>
          <Route element={<DocCommentsLayout />}>
            <Route path="/post/:id" element={<><Post /><Link to="/pages/2">下一页</Link></>} />
            <Route path="/pages/:id" element={<PageDetail />} />
          </Route>
        </Routes>
      </ToastProvider>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  resetClientMock(client)
  client.setCurrentUser(adminMe)
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
  client.blog.listComments.mockResolvedValue({ items: [], page: 1, page_size: 12, total: 0, total_pages: 1 })
})

describe('parseDocCommentTarget', () => {
  it('只认 /post 与 /pages 下的合法正整数 id', () => {
    expect(parseDocCommentTarget('/post/3')).toEqual({ type: 'article', id: 3, key: 'article:3' })
    expect(parseDocCommentTarget('/pages/9')).toEqual({ type: 'page', id: 9, key: 'page:9' })
    expect(parseDocCommentTarget('/archive')).toBeNull()
    expect(parseDocCommentTarget('/post')).toBeNull()
    expect(parseDocCommentTarget('/post/abc')).toBeNull()
    expect(parseDocCommentTarget('/pages/0')).toBeNull()
    expect(parseDocCommentTarget('/post/-2')).toBeNull()
  })
})

describe('DocCommentsLayout', () => {
  it('文章切独立页面：评论区与输入框保持同一份 DOM，只更换评论对象', async () => {
    client.blog.getArticle.mockResolvedValue(makeArticle({ id: 1, title: '文章一', content: '# 正文' }))
    client.blog.getPage.mockResolvedValue(makePage({ id: 2, title: '页面二', content: '页面正文' }))

    const { container } = renderDocs('/post/1')
    await waitFor(() => expect(screen.getByText('文章一')).toBeTruthy())
    await waitFor(() => expect(client.blog.listComments).toHaveBeenCalledWith({
      targetType: 'article', targetId: 1, page: 1, pageSize: 12,
    }))

    const section = container.querySelector('.cmt-sec')
    const form = container.querySelector('.cform')
    const textarea = screen.getByPlaceholderText('写下你的想法…') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: '上一页的草稿' } })
    expect(textarea.value).toBe('上一页的草稿')

    fireEvent.click(screen.getByText('下一页'))
    await waitFor(() => expect(screen.getByText('页面二')).toBeTruthy())
    await waitFor(() => expect(client.blog.listComments).toHaveBeenCalledWith({
      targetType: 'page', targetId: 2, page: 1, pageSize: 12,
    }))

    // 同一份节点而不是「另一个看起来一样的评论区」。
    expect(container.querySelector('.cmt-sec')).toBe(section)
    expect(container.querySelector('.cform')).toBe(form)
    // 评论对象换了，草稿不能跟着过去；但表单本身没有卸载。
    expect(screen.getByPlaceholderText('写下你的想法…')).toBe(textarea)
    expect(textarea.value).toBe('')
  })

  it('正文 404：评论区收掉；切到存在的页面后重新出现', async () => {
    client.blog.getArticle.mockRejectedValue(new Error('文章不见了'))
    client.blog.getPage.mockResolvedValue(makePage({ id: 2, title: '页面二', content: '页面正文' }))

    const { container } = renderDocs('/post/1')
    await waitFor(() => expect(screen.getByText('文章不见了')).toBeTruthy())
    await waitFor(() => expect(container.querySelector('.cmt-sec')).toBeNull())

    fireEvent.click(screen.getByText('下一页'))
    await waitFor(() => expect(screen.getByText('页面二')).toBeTruthy())
    await waitFor(() => expect(container.querySelector('.cmt-sec')).toBeTruthy())
    expect(client.blog.listComments).toHaveBeenCalledWith({
      targetType: 'page', targetId: 2, page: 1, pageSize: 12,
    })
  })

  it('非法 id 不挂评论区；跳到合法页面后才出现', async () => {
    client.blog.getPage.mockResolvedValue(makePage({ id: 2, title: '页面二', content: '页面正文' }))

    const { container } = renderDocs('/post/abc')
    await waitFor(() => expect(screen.getByText('文章不存在')).toBeTruthy())
    expect(container.querySelector('.cmt-sec')).toBeNull()
    expect(client.blog.listComments).not.toHaveBeenCalled()

    fireEvent.click(screen.getByText('下一页'))
    await waitFor(() => expect(screen.getByText('页面二')).toBeTruthy())
    await waitFor(() => expect(container.querySelector('.cmt-sec')).toBeTruthy())
  })

  it('旧对象的发布回调晚到：不会把评论列表拉回旧对象', async () => {
    client.blog.getArticle.mockResolvedValue(makeArticle({ id: 1, title: '文章一', content: '# 正文' }))
    client.blog.getPage.mockResolvedValue(makePage({ id: 2, title: '页面二', content: '页面正文' }))
    const gate = deferred<unknown>()
    client.blog.createComment.mockReturnValue(gate.promise as never)

    renderDocs('/post/1')
    await waitFor(() => expect(screen.getByText('文章一')).toBeTruthy())
    await waitFor(() => expect(client.blog.listComments).toHaveBeenCalledWith({
      targetType: 'article', targetId: 1, page: 1, pageSize: 12,
    }))

    fireEvent.change(screen.getByPlaceholderText('写下你的想法…'), { target: { value: '旧页评论' } })
    fireEvent.click(screen.getByRole('button', { name: '发布评论' }))
    await waitFor(() => expect(client.blog.createComment).toHaveBeenCalledWith({
      targetType: 'article', targetId: 1, content: '旧页评论',
    }))

    // 请求还在路上就切到另一页；迟到的 onPosted 仍持有旧 target 的 loadPage。
    fireEvent.click(screen.getByText('下一页'))
    await waitFor(() => expect(screen.getByText('页面二')).toBeTruthy())
    await waitFor(() => expect(client.blog.listComments).toHaveBeenCalledWith({
      targetType: 'page', targetId: 2, page: 1, pageSize: 12,
    }))

    await act(async () => {
      gate.resolve({})
      await Promise.resolve()
      await Promise.resolve()
    })

    const articleCalls = client.blog.listComments.mock.calls
      .filter(([options]) => (options as { targetType: string }).targetType === 'article')
    expect(articleCalls).toHaveLength(1)
    expect(client.blog.listComments).toHaveBeenLastCalledWith({
      targetType: 'page', targetId: 2, page: 1, pageSize: 12,
    })
  })

  it('转发外壳的 setToc，正文目录不会因为多了一层布局而丢失', async () => {
    const setToc = vi.fn()
    client.blog.getArticle.mockResolvedValue(makeArticle({ id: 1, title: '文章一', content: '# 目录标题' }))

    render(
      <MemoryRouter initialEntries={['/post/1']}>
        <ToastProvider>
          <Routes>
            <Route element={<Outlet context={{ setToc }} />}>
              <Route element={<DocCommentsLayout />}>
                <Route path="/post/:id" element={<Post />} />
              </Route>
            </Route>
          </Routes>
        </ToastProvider>
      </MemoryRouter>,
    )

    await waitFor(() => expect(setToc).toHaveBeenCalledWith(expect.arrayContaining([
      expect.objectContaining({ text: '目录标题' }),
    ])))
  })
})
