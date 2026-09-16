// comments.test.tsx —— 评论区：加载/分页/树形/登录/发帖/点赞/回复
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import Comments from '../components/Comments'
import { ToastProvider } from '../lib/toast'
import { resetSiteCacheForTests } from '../lib/site'
import { getSharedClientMock, adminMe, visitorMe, makeComment } from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

function paged(items: ReturnType<typeof makeComment>[], page = 1, total_pages = 1, total = items.length) {
  return { items, page, page_size: 12, total, total_pages }
}

function renderComments(props: Partial<Parameters<typeof Comments>[0]> = {}) {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <Comments targetType="article" targetId={1} {...props} />
      </ToastProvider>
    </MemoryRouter>,
  )
}

/** h3 的文本被嵌套 span 拆开，按 textContent 归一化后断言。 */
function headingText(): string {
  const h3 = document.querySelector('.cmt-sec h3')
  return (h3?.textContent ?? '').replace(/\s+/g, ' ').trim()
}

beforeEach(() => {
  vi.clearAllMocks()
  client.setCurrentUser(null)
  // 评论区里的 ContentView（渲染评论文本）现在订阅站点快照（代码块行数）；
  // 不给 getSite 实现的话懒建的 vi.fn() 返回 undefined，loadMeta 直接崩。
  resetSiteCacheForTests()
  client.blog.getSite.mockResolvedValue({ description: '', avatar_url: '', code_block_max_lines: 20 })
})

describe('加载与展示', () => {
  it('成功加载根评论并建树；作者标签按身份渲染', async () => {
    client.blog.listComments.mockResolvedValueOnce(paged([
      makeComment({ id: 1, author: { type: 'admin', name: '站长' }, content: '管理员楼' }),
      makeComment({ id: 2, parent_id: 1, content: '子回复' }),
    ]))
    renderComments()
    await waitFor(() => expect(screen.getByText('管理员楼')).toBeTruthy())
    expect(screen.getByText('子回复')).toBeTruthy()
    expect(screen.getByText('站长')).toBeTruthy()
    expect(screen.getByText('作者')).toBeTruthy()
    expect(headingText()).toBe('评论 · 2 条')
  })

  it('访客无昵称回退「访客」', async () => {
    client.blog.listComments.mockResolvedValueOnce(paged([
      makeComment({ id: 1, author: { type: 'user', id: 3, nickname: '  ' } }),
    ]))
    const { container } = renderComments()
    await waitFor(() => expect(screen.getAllByText('访客').length).toBeGreaterThan(0))
    expect(container.querySelector('.reply-btn')).toBeNull() // 未登录不渲染回复按钮
  })

  it('加载失败（replace）清空列表', async () => {
    client.blog.listComments.mockRejectedValueOnce(new Error('boom'))
    renderComments()
    await waitFor(() => expect(screen.getByText('还没有评论')).toBeTruthy())
  })

  it('评论关闭时不渲染表单', async () => {
    client.blog.listComments.mockResolvedValueOnce(paged([]))
    renderComments({ enabled: false })
    await waitFor(() => expect(screen.getByText('评论已关闭')).toBeTruthy())
    expect(screen.queryByText('发布评论')).toBeNull()
  })
})

describe('加载更多', () => {
  it('追加去重；失败 toast 且保留旧数据', async () => {
    client.blog.listComments.mockResolvedValueOnce(paged([makeComment({ id: 1 })], 1, 3, 3))
    const { container } = renderComments()
    const moreBtn = () => screen.getByRole('button', { name: '加载更多' })
    await waitFor(() => expect(moreBtn()).toBeTruthy())

    client.blog.listComments.mockResolvedValueOnce(paged([makeComment({ id: 1 }), makeComment({ id: 2, content: '第二条' })], 2, 3, 3))
    fireEvent.click(moreBtn())
    await waitFor(() => expect(screen.getByText('第二条')).toBeTruthy())
    expect(container.querySelectorAll('.cmt').length).toBe(2) // id=1 去重后共两条

    client.blog.listComments.mockRejectedValueOnce(new Error('net down'))
    fireEvent.click(moreBtn())
    await waitFor(() => expect(screen.getAllByText('net down').length).toBeGreaterThan(0))
    expect(container.querySelectorAll('.cmt').length).toBe(2)
  })
})

describe('未登录：InlineAuth', () => {
  it('空用户名/密码 toast；登录失败提示；登录成功后就地切换为评论框', async () => {
    client.blog.listComments.mockResolvedValue(paged([]))
    renderComments()
    await waitFor(() => expect(screen.getByText('登录后即可发言、点赞与回复。你的昵称取自账号资料，评论署名与账号保持一致。')).toBeTruthy())

    const form = screen.getByRole('button', { name: '登录' }).closest('form') as HTMLFormElement
    const username = screen.getByPlaceholderText('用户名或注册邮箱') as HTMLInputElement
    const password = screen.getByPlaceholderText('密码') as HTMLInputElement

    // 空用户名
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getAllByText('请输入用户名（或注册邮箱）').length).toBeGreaterThan(0))

    // 空密码
    fireEvent.change(username, { target: { value: 'alice' } })
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getAllByText('请输入密码').length).toBeGreaterThan(0))

    // 登录失败（用户名密码都已填）
    fireEvent.change(password, { target: { value: 'wrong-pass' } })
    client.blog.login.mockRejectedValueOnce(new Error('密码不对'))
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getAllByText('密码不对').length).toBeGreaterThan(0))

    // 登录成功 → 评论框就地出现
    client.blog.login.mockResolvedValueOnce(visitorMe)
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getByText('发布评论')).toBeTruthy())
    expect(screen.getByText('以 爱丽丝 的身份评论')).toBeTruthy()
  })
})

describe('已登录：发帖与回复', () => {
  it('空内容/超长内容被拦截；成功后清空并刷新第一页；失败 toast', async () => {
    client.setCurrentUser(visitorMe)
    client.blog.listComments.mockResolvedValue(paged([makeComment({ id: 1 })]))
    renderComments()
    await waitFor(() => expect(screen.getByText('发布评论')).toBeTruthy())

    const box = screen.getByPlaceholderText('写下你的想法…') as HTMLTextAreaElement

    fireEvent.click(screen.getByText('发布评论'))
    await waitFor(() => expect(screen.getAllByText('内容不能为空').length).toBeGreaterThan(0))

    fireEvent.change(box, { target: { value: 'x'.repeat(2001) } })
    fireEvent.click(screen.getByText('发布评论'))
    await waitFor(() => expect(screen.getAllByText('评论不能超过 2000 个字符').length).toBeGreaterThan(0))
    expect(client.blog.createComment).not.toHaveBeenCalled()

    fireEvent.change(box, { target: { value: '新评论' } })
    client.blog.createComment.mockResolvedValueOnce({ message: 'ok' })
    client.blog.listComments.mockResolvedValueOnce(paged([makeComment({ id: 5, content: '刚发的' })]))
    fireEvent.click(screen.getByText('发布评论'))
    await waitFor(() => expect(client.blog.createComment).toHaveBeenCalledWith({ targetType: 'article', targetId: 1, content: '新评论' }))
    await waitFor(() => expect(screen.getByText('刚发的')).toBeTruthy())

    fireEvent.change(box, { target: { value: '再来一条' } })
    client.blog.createComment.mockRejectedValueOnce(new Error('被风控'))
    fireEvent.click(screen.getByText('发布评论'))
    await waitFor(() => expect(screen.getAllByText('被风控').length).toBeGreaterThan(0))
  })

  it('管理员身份显示管理员评论', async () => {
    client.setCurrentUser(adminMe)
    client.blog.listComments.mockResolvedValue(paged([]))
    renderComments()
    await waitFor(() => expect(screen.getByText('管理员评论')).toBeTruthy())
  })

  it('回复：取消只关表单不刷新（回归守护）；发布成功后刷新第一页；空内容拦截', async () => {
    client.setCurrentUser(visitorMe)
    client.blog.listComments.mockResolvedValue(paged([makeComment({ id: 1, content: '首楼' })]))
    renderComments()
    await waitFor(() => expect(screen.getByText('首楼')).toBeTruthy())

    const replyButtons = () => screen.getAllByRole('button', { name: '回复' })

    // 打开回复表单：此时只有楼层的「回复」切换钮
    fireEvent.click(replyButtons()[0])
    await waitFor(() => expect(screen.getByPlaceholderText('写下回复…')).toBeTruthy())

    // 取消：仅关闭表单，不触发第一页刷新（onDone/onPosted 曾混用导致刷新）
    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(screen.queryByPlaceholderText('写下回复…')).toBeNull()
    expect(client.blog.listComments).toHaveBeenCalledTimes(1)

    // 重新打开，空内容拦截（[0]=楼层切换钮，[1]=表单提交钮）
    fireEvent.click(replyButtons()[0])
    const replyBox = screen.getByPlaceholderText('写下回复…') as HTMLTextAreaElement
    fireEvent.click(replyButtons()[1])
    await waitFor(() => expect(screen.getAllByText('内容不能为空').length).toBeGreaterThan(0))

    // 成功发布 → 刷新第一页
    fireEvent.change(replyBox, { target: { value: '回复内容' } })
    client.blog.createComment.mockResolvedValueOnce({ message: 'ok' })
    client.blog.listComments.mockResolvedValueOnce(paged([
      makeComment({ id: 1, content: '首楼' }),
      makeComment({ id: 6, parent_id: 1, content: '楼中楼' }),
    ]))
    fireEvent.click(replyButtons()[1])
    await waitFor(() => expect(client.blog.createComment).toHaveBeenCalledWith({ targetType: 'article', targetId: 1, parentId: 1, content: '回复内容' }))
    await waitFor(() => expect(screen.getByText('楼中楼')).toBeTruthy())
  })

  it('访客点赞 / 取消点赞：精准替换该条；服务端返回 null 不更新；失败 toast 且状态保持', async () => {
    client.setCurrentUser(visitorMe)
    client.blog.listComments.mockResolvedValue(paged([makeComment({ id: 1, like_count: 2, liked_by_me: false })]))
    const { container } = renderComments()
    const likeBtn = () => container.querySelector('.like-btn') as HTMLButtonElement
    await waitFor(() => expect(likeBtn().textContent).toBe('♡ 2'))

    // 点赞成功
    client.blog.likeComment.mockResolvedValueOnce(makeComment({ id: 1, like_count: 3, liked_by_me: true }))
    fireEvent.click(likeBtn())
    await waitFor(() => expect(likeBtn().textContent).toBe('♥ 3'))

    // 取消点赞失败：toast，状态保持
    client.blog.unlikeComment.mockRejectedValueOnce(new Error('点赞失败'))
    fireEvent.click(likeBtn())
    await waitFor(() => expect(screen.getAllByText('点赞失败').length).toBeGreaterThan(0))
    expect(likeBtn().textContent).toBe('♥ 3')

    // 服务端返回 null：不更新本地状态
    client.blog.unlikeComment.mockResolvedValueOnce(null)
    fireEvent.click(likeBtn())
    await waitFor(() => expect(client.blog.unlikeComment).toHaveBeenCalledTimes(2))
    expect(likeBtn().textContent).toBe('♥ 3')

    // 取消点赞成功
    client.blog.unlikeComment.mockResolvedValueOnce(makeComment({ id: 1, like_count: 2, liked_by_me: false }))
    fireEvent.click(likeBtn())
    await waitFor(() => expect(likeBtn().textContent).toBe('♡ 2'))
  })

  it('管理员：点赞只读（提示 + 点击无效），但保留回复按钮', async () => {
    client.setCurrentUser(adminMe)
    client.blog.listComments.mockResolvedValue(paged([makeComment({ id: 1 })]))
    const { container } = renderComments()
    const likeBtn = () => container.querySelector('.like-btn') as HTMLButtonElement
    await waitFor(() => expect(likeBtn().dataset.tip).toBe('管理员仅可查看点赞数，不能点赞'))
    expect(likeBtn().getAttribute('aria-disabled')).toBe('true')
    expect(container.querySelector('.reply-btn')).toBeTruthy() // 管理员可以回复

    fireEvent.click(likeBtn())
    expect(client.blog.likeComment).not.toHaveBeenCalled()
  })
})
