// admin-manage.test.tsx —— Dashboard / TagsAdmin / CommentsAdmin / UsersAdmin / MediaAdmin
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { createClient } from 'twilight-blog-api'
import Dashboard from '../pages/admin/Dashboard'
import TagsAdmin from '../pages/admin/TagsAdmin'
import CommentsAdmin from '../pages/admin/CommentsAdmin'
import UsersAdmin from '../pages/admin/UsersAdmin'
import MediaAdmin from '../pages/admin/MediaAdmin'
import { DialogProvider } from '../lib/dialog'
import { ToastProvider } from '../lib/toast'
import { getSharedClientMock, NOW, makeComment, makeMedia, makeTag, makeUser, resetClientMock } from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

function paged<T>(items: T[], page = 1, total_pages = 1, total = items.length) {
  return { items, page, page_size: 12, total, total_pages }
}

function renderNode(ui: React.ReactElement) {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <DialogProvider>{ui}</DialogProvider>
      </ToastProvider>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  resetClientMock(client)
  client.setCurrentUser(null)
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
})

describe('Dashboard', () => {
  it('allSettled：失败项计 0，其余取 total', async () => {
    client.blog.listArticles.mockResolvedValue(paged([], 1, 1, 7))
    client.blog.listPages.mockResolvedValue(paged([], 1, 1, 2))
    client.blog.listTags.mockResolvedValue(paged([], 1, 1, 5))
    client.blog.listMoments.mockResolvedValue(paged([], 1, 1, 9))
    client.blog.listAllComments.mockRejectedValue(new Error('评论挂了'))

    renderNode(<Dashboard />)
    await waitFor(() => expect(document.querySelector('.stat-grid')).toBeTruthy())
    const values = Array.from(document.querySelectorAll('.stat b')).map((el) => el.textContent)
    expect(values).toEqual(['7', '2', '5', '9', '0']) // 评论接口挂了 → 0
    expect(screen.getByText('快捷入口')).toBeTruthy()
  })
})

describe('TagsAdmin', () => {
  it('列表展示；编辑 → 保存成功刷新；空名拦截', async () => {
    client.blog.listTags
      .mockResolvedValueOnce(paged([makeTag({ id: 1, name: 'Go', article_count: 3 })], 1, 2, 4))
      .mockResolvedValueOnce(paged([makeTag({ id: 2, name: 'React', article_count: 1 })], 2, 2, 4))
      .mockResolvedValue(paged([makeTag({ id: 1, name: 'Golang', article_count: 3 })], 1, 1, 1))

    renderNode(<TagsAdmin />)
    await waitFor(() => expect(screen.getByText('Go')).toBeTruthy())
    await waitFor(() => expect(screen.getByText('React')).toBeTruthy())

    fireEvent.click(screen.getAllByRole('button', { name: '编辑' })[0])
    const input = document.querySelector('.frow input') as HTMLInputElement
    expect(input.value).toBe('Go')

    // 空名拦截
    fireEvent.change(input, { target: { value: '   ' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getAllByText('标签名不能为空').length).toBeGreaterThan(0))
    expect(client.blog.renameTag).not.toHaveBeenCalled()

    // 保存成功 → 重载
    fireEvent.change(input, { target: { value: '  Golang  ' } })
    client.blog.renameTag.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getByText('Golang')).toBeTruthy())
    expect(client.blog.renameTag).toHaveBeenCalledWith(1, { name: 'Golang', nameEn: '' })
  })

  it('加载失败回退空列表', async () => {
    client.blog.listTags.mockRejectedValue(new Error('down'))
    renderNode(<TagsAdmin />)
    await waitFor(() => expect(screen.getByText('暂无标签')).toBeTruthy())
  })
})

describe('CommentsAdmin', () => {
  it('全部 tab：列表 + 删除（确认弹窗）→ 重载；切回收站 tab 页码归 1', async () => {
    client.blog.listAllComments
      .mockResolvedValueOnce(paged([
        makeComment({ id: 1, content: '前台留言', author: { type: 'user', id: 7, nickname: '访客甲', email: 'v@x.com' } as never }),
        makeComment({ id: 2, content: '管理员的话', author: { type: 'admin', name: '站长' } as never, parent_id: 1 }),
      ]))
      .mockResolvedValue(paged([makeComment({ id: 1 })], 1, 1, 1))

    renderNode(<CommentsAdmin />)
    await waitFor(() => expect(screen.getByText('前台留言')).toBeTruthy())
    expect(document.body.textContent).toContain('v@x.com') // 访客邮箱展示
    expect(document.body.textContent).toContain('回复') // parent_id 徽标

    // 删除 → confirm 弹窗 → 确定
    client.blog.deleteComment.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getAllByRole('button', { name: '删除' })[0])
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(client.blog.deleteComment).toHaveBeenCalledWith(1))
    await waitFor(() => expect(screen.getAllByText('已移入回收站').length).toBeGreaterThan(0))

    // 切回收站
    client.blog.listTrashComments.mockResolvedValue(paged([makeComment({ id: 9, content: '回收站内容' })]))
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('回收站内容')).toBeTruthy())
    expect(client.blog.listTrashComments).toHaveBeenCalledWith({ page: 1, pageSize: 12 })
  })

  it('回收站 tab：恢复与彻底删除', async () => {
    client.blog.listAllComments.mockResolvedValue(paged([], 1, 1, 0))
    client.blog.listTrashComments.mockResolvedValue(paged([makeComment({ id: 9, content: '待处理' })]))
    renderNode(<CommentsAdmin />)
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('待处理')).toBeTruthy())

    client.blog.restoreComment.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '恢复' }))
    await waitFor(() => expect(client.blog.restoreComment).toHaveBeenCalledWith(9))

    client.blog.permanentlyDeleteComment.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(client.blog.permanentlyDeleteComment).toHaveBeenCalledWith(9))
  })
})

describe('UsersAdmin', () => {
  it('列表 → 编辑 → 保存成功；失败保持编辑态', async () => {
    client.blog.listUsers
      .mockResolvedValueOnce(paged([makeUser({ id: 7, username: 'alice', nickname: '爱丽丝', email: 'a@x.com', has_password: false })], 1, 1, 1))
      .mockResolvedValue(paged([makeUser({ id: 7, username: 'alice2', nickname: '爱丽丝2', email: 'a2@x.com', has_password: true })], 1, 1, 1))

    const { container } = renderNode(<UsersAdmin />)
    await waitFor(() => expect(screen.getByText('爱丽丝')).toBeTruthy())
    expect(screen.getByText('未设密码')).toBeTruthy() // has_password=false 徽标

    fireEvent.click(screen.getByRole('button', { name: '编辑' }))
    const inputs = Array.from(container.querySelectorAll('.uedit input')) as HTMLInputElement[]
    expect(inputs.length).toBe(3)

    // 非法用户名拦截
    fireEvent.change(inputs[0], { target: { value: 'x' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getAllByText(/3–24 个字符/).length).toBeGreaterThan(0))

    // 保存失败（邮箱冲突）→ 行保持编辑态
    fireEvent.change(inputs[0], { target: { value: 'alice2' } })
    fireEvent.change(inputs[1], { target: { value: '爱丽丝2' } })
    fireEvent.change(inputs[2], { target: { value: 'a2@x.com' } })
    client.blog.updateUser.mockRejectedValueOnce(new Error('邮箱已被占用'))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getAllByText('邮箱已被占用').length).toBeGreaterThan(0))
    expect(container.querySelector('.uedit')).toBeTruthy() // 仍在编辑态

    // 再保存成功（失败后的 busy=false 与 toast 是两帧，等按钮恢复「保存」再点）。
    await waitFor(() => expect(screen.getByRole('button', { name: '保存' })).toBeTruthy())
    client.blog.updateUser.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(client.blog.updateUser).toHaveBeenCalledWith(7, { username: 'alice2', nickname: '爱丽丝2', email: 'a2@x.com' }))
    await waitFor(() => expect(screen.getByText('爱丽丝2')).toBeTruthy())
    expect(screen.queryByText('未设密码')).toBeNull() // 新数据 has_password=true
  })

  it('加载失败 toast + 空态', async () => {
    client.blog.listUsers.mockRejectedValue(new Error('down'))
    renderNode(<UsersAdmin />)
    await waitFor(() => expect(screen.getAllByText('down').length).toBeGreaterThan(0))
    expect(screen.getByText('还没有注册用户')).toBeTruthy()
  })
})

describe('MediaAdmin', () => {
  it('网格展示 image/audio 两态；删除需确认；上传成功刷新', async () => {
    client.blog.listMedia
      .mockResolvedValueOnce(paged([
        makeMedia({ id: 1, media_type: 'image', url: '/uploads/a.png', original_name: 'a.png' }),
        makeMedia({ id: 2, media_type: 'audio', url: '/uploads/b.mp3', original_name: 'b.mp3', size: 2048 }),
      ]))
      .mockResolvedValue(paged([makeMedia({ id: 3, original_name: 'c.png' })]))

    const { container } = renderNode(<MediaAdmin />)
    await waitFor(() => expect(screen.getByText('a.png')).toBeTruthy())
    expect(screen.getByText('MP3')).toBeTruthy() // 非图片占位
    expect(container.querySelector('img[src="/uploads/a.png"]')).toBeTruthy()

    // 删除：确认 → 成功
    client.blog.deleteMedia.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getAllByTitle('删除')[0])
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(client.blog.deleteMedia).toHaveBeenCalledWith(1))
    await waitFor(() => expect(screen.getByText('c.png')).toBeTruthy()) // 重载后

    // 上传：hidden input 注入文件
    client.blog.uploadMedia.mockResolvedValueOnce({ message: 'ok' })
    const input = container.querySelector('input[type=file]') as HTMLInputElement
    await waitFor(async () => {
      fireEvent.change(input, { target: { files: [new File(['x'], 'd.png', { type: 'image/png' })] } })
      await waitFor(() => expect(client.blog.uploadMedia).toBeTruthy())
    })
    await waitFor(() => expect(client.blog.uploadMedia).toHaveBeenCalled())
    expect(screen.getAllByText('上传成功').length).toBeGreaterThan(0)
    void NOW
  })

  it('复制按钮给绝对地址（能直接贴进浏览器地址栏），img 仍用相对路径', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })

    client.blog.listMedia.mockResolvedValue(paged([
      makeMedia({ id: 7, url: '/api/media/7', original_name: 'p.png' }),
    ]))

    const { container } = renderNode(<MediaAdmin />)
    await waitFor(() => expect(screen.getByText('p.png')).toBeTruthy())

    // 展示层必须保持相对路径：写死 origin 会在反向代理/多域名/vite 代理下裂图
    expect(container.querySelector('img[src="/api/media/7"]')).toBeTruthy()

    fireEvent.click(container.querySelector('.mact .cpbtn') as HTMLElement)
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/api/media/7`),
    )
  })

  it('重命名：预填当前名字并全选；取消/空名/超长都不发请求；成功后刷新列表', async () => {
    client.blog.listMedia
      .mockResolvedValueOnce(paged([makeMedia({ id: 7, original_name: 'old.png' })]))
      .mockResolvedValue(paged([makeMedia({ id: 7, original_name: '猫咪.png' })]))

    renderNode(<MediaAdmin />)
    await waitFor(() => expect(screen.getByText('old.png')).toBeTruthy())

    // 取消：对话框返回 null，一个请求都不该发出去。
    fireEvent.click(screen.getByTitle('重命名'))
    const cancelled = (await screen.findByRole('textbox')) as HTMLInputElement
    expect(cancelled.value).toBe('old.png') // 预填的是当前名字
    expect(cancelled.selectionEnd).toBe('old.png'.length) // 打开即全选
    fireEvent.click(screen.getByText('取消'))
    await waitFor(() => expect(screen.queryByRole('textbox')).toBeNull())
    expect(client.blog.renameMedia).not.toHaveBeenCalled()

    // 空名：提示，不发请求（服务端也会 400，没必要白跑一趟）。
    fireEvent.click(screen.getByTitle('重命名'))
    const blank = (await screen.findByRole('textbox')) as HTMLInputElement
    fireEvent.change(blank, { target: { value: '   ' } })
    fireEvent.click(screen.getByText('确定'))
    await waitFor(() => expect(screen.getAllByText('名字不能为空').length).toBeGreaterThan(0))
    expect(client.blog.renameMedia).not.toHaveBeenCalled()

    // 超长：按码点算，201 个字符拦下（上限与后端 model.MaxMediaOriginalNameLength 镜像）。
    fireEvent.click(screen.getByTitle('重命名'))
    const tooLong = (await screen.findByRole('textbox')) as HTMLInputElement
    fireEvent.change(tooLong, { target: { value: '字'.repeat(201) } })
    fireEvent.click(screen.getByText('确定'))
    await waitFor(() => expect(screen.getAllByText('名字不能超过 200 个字符').length).toBeGreaterThan(0))
    expect(client.blog.renameMedia).not.toHaveBeenCalled()

    // 成功：把 trim 后的名字交给接口，并用服务端返回的结果刷新列表。
    client.blog.renameMedia.mockResolvedValueOnce(makeMedia({ id: 7, original_name: '猫咪.png' }))
    fireEvent.click(screen.getByTitle('重命名'))
    const input = (await screen.findByRole('textbox')) as HTMLInputElement
    fireEvent.change(input, { target: { value: '  猫咪.png  ' } })
    fireEvent.click(screen.getByText('确定'))

    await waitFor(() => expect(client.blog.renameMedia).toHaveBeenCalledWith(7, { original_name: '猫咪.png' }))
    await waitFor(() => expect(screen.getByText('猫咪.png')).toBeTruthy())
    expect(screen.getAllByText('已重命名').length).toBeGreaterThan(0)
  })

  it('重命名失败：toast 提示，界面保持原样', async () => {
    client.blog.listMedia.mockResolvedValue(paged([makeMedia({ id: 7, original_name: 'old.png' })]))
    client.blog.renameMedia.mockRejectedValueOnce(new Error('名字不能用'))

    renderNode(<MediaAdmin />)
    await waitFor(() => expect(screen.getByText('old.png')).toBeTruthy())

    fireEvent.click(screen.getByTitle('重命名'))
    const input = (await screen.findByRole('textbox')) as HTMLInputElement
    fireEvent.change(input, { target: { value: 'new.png' } })
    fireEvent.click(screen.getByText('确定'))

    await waitFor(() => expect(screen.getAllByText('名字不能用').length).toBeGreaterThan(0))
    expect(screen.getByText('old.png')).toBeTruthy()
  })
})

/* ------------------- renameMedia 的请求形状（真实模块） ------------------- */

/**
 * 与 article-cover.test.tsx 里 updateSite 那一组同源：这里用**真实 SDK** +
 * fetch 替身，断言的是封装层组装出来的请求本身。
 *
 * 为什么必须单独钉：改名只有 id 与 original_name 两个入参，其中 id 决定改哪一条
 * （写错了会改到别人的图），方法与路径决定服务端认不认这个请求。用 mock 掉
 * `blog.renameMedia` 的行为测试完全看不到这一层——mock 会把封装整个换掉。
 */
describe('SDK 下发的媒体改名请求形状（真实模块）', () => {
  it('PATCH /api/admin/media/:id，请求体只带 original_name', async () => {
    const calls: { url: string; method?: string; body: unknown }[] = []
    const fetchStub = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({
        url: String(input),
        method: init?.method,
        body: JSON.parse(String(init?.body ?? '{}')),
      })
      return new Response(JSON.stringify(makeMedia({ id: 7, original_name: '猫咪.png' })), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })
    })
    const api = createClient({ baseUrl: 'https://blog.test', fetch: fetchStub as unknown as typeof fetch })

    const updated = await api.renameMedia(7, { original_name: '猫咪.png' })

    expect(calls[0].url).toBe('https://blog.test/api/admin/media/7')
    expect(calls[0].method).toBe('PATCH')
    expect(calls[0].body).toEqual({ original_name: '猫咪.png' })
    expect(updated.original_name).toBe('猫咪.png')
  })
})
