// admin-content.test.tsx —— Posts / PagesAdmin(+PageEdit/NewPage) / MomentsAdmin / Editor / Profile / SiteSettings
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

import Posts from '../pages/admin/Posts'
import PagesAdmin, { NewPage, PageEdit } from '../pages/admin/PagesAdmin'
import MomentsAdmin from '../pages/admin/MomentsAdmin'
import Editor from '../pages/admin/Editor'
import Profile from '../pages/admin/Profile'
import SiteSettings from '../pages/admin/SiteSettings'
import { DialogProvider } from '../lib/dialog'
import { ToastProvider } from '../lib/toast'
import { resetSiteCacheForTests } from '../lib/site'
import { NOW, adminMe, getSharedClientMock, makeArticle, makeMedia, makeMoment, makePage, resetClientMock, visitorMe } from './helpers'

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
            <Route path="/admin/posts" element={<div>POSTS-LIST</div>} />
            <Route path="/admin/pages" element={<div>PAGES-LIST</div>} />
            <Route path="/admin/editor/:id" element={<div>EDITOR-REPLACED</div>} />
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

beforeEach(() => {
  resetClientMock(client)
  resetSiteCacheForTests() // site 模块单例跨用例泄漏
  client.setCurrentUser(null)
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
})

describe('Posts', () => {
  it('active 软删（确认弹窗）后整页刷新；回收站可恢复与彻底删除', async () => {
    client.blog.listArticles
      .mockResolvedValueOnce(paged([makeArticle({ id: 1, title: '文章甲' })]))
      .mockResolvedValue(paged([makeArticle({ id: 1, title: '重载后' })]))
    client.blog.listTrashArticles.mockResolvedValue(paged([makeArticle({ id: 9, title: '回收站文章' })]))

    renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    await waitFor(() => expect(screen.getByText('文章甲')).toBeTruthy())

    // 软删：confirm 弹窗 → 确定 → deleteArticle → reload 显示「重载后」
    client.blog.deleteArticle.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(client.blog.deleteArticle).toHaveBeenCalledWith(1))
    await waitFor(() => expect(screen.getByText('重载后')).toBeTruthy())

    // 回收站：恢复与彻底删除走各自接口
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('回收站文章')).toBeTruthy())
    client.blog.restoreArticle.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '恢复' }))
    await waitFor(() => expect(client.blog.restoreArticle).toHaveBeenCalledWith(9))
    client.blog.permanentlyDeleteArticle.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(client.blog.permanentlyDeleteArticle).toHaveBeenCalledWith(9))
  })

  it('批量操作：软删与彻底删除走各自接口；选中项随翻页清空', async () => {
    client.blog.listArticles
      .mockResolvedValueOnce(paged([makeArticle({ id: 1 }), makeArticle({ id: 2 })], 1, 2, 2))
      .mockResolvedValueOnce(paged([makeArticle({ id: 3 })], 2, 2, 2))
      .mockResolvedValue(paged([], 1, 1, 0))

    const { container } = renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    await waitFor(() => expect(screen.getAllByText('测试文章').length).toBe(2))

    // 勾选两行
    const checkboxes = container.querySelectorAll('tbody input[type=checkbox]')
    fireEvent.click(checkboxes[0])
    fireEvent.click(checkboxes[1])
    expect(screen.getByText('已选 2 篇')).toBeTruthy()

    client.blog.batchDeleteArticles.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '移入回收站' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(client.blog.batchDeleteArticles).toHaveBeenCalledWith({ ids: [1, 2] }))

    // 翻页清空选中（changePage）
    fireEvent.click(screen.getByRole('button', { name: 'Page 2' }))
    await waitFor(() => expect(client.blog.listArticles).toHaveBeenLastCalledWith({ page: 2, pageSize: 12 }))
    expect(screen.queryByText(/已选/)).toBeNull()
  })

  it('空列表与加载失败', async () => {
    client.blog.listArticles
      .mockResolvedValueOnce(paged([], 1, 1, 0))
      .mockRejectedValueOnce(new Error('列表挂了'))

    const first = renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    await waitFor(() => expect(screen.getByText('没有符合条件的文章')).toBeTruthy())
    first.unmount()

    renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    await waitFor(() => expect(screen.getAllByText('列表挂了').length).toBeGreaterThan(0))
  })
})

describe('PagesAdmin', () => {
  it('active 删除（确认）与回收站恢复/彻底删除', async () => {
    client.blog.listPages
      .mockResolvedValue(paged([makePage({ id: 1, title: '关于我们' })]))
    client.blog.listTrashPages.mockResolvedValue(paged([makePage({ id: 8, title: '废弃页' })]))

    renderRouted(<PagesAdmin />, '/admin/pages', '/admin/pages') // 列表里有 Link，需要 Router
    await waitFor(() => expect(screen.getByText('关于我们')).toBeTruthy())

    client.blog.deletePage.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(client.blog.deletePage).toHaveBeenCalledWith(1))

    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('废弃页')).toBeTruthy())
    client.blog.restorePage.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '恢复' }))
    await waitFor(() => expect(client.blog.restorePage).toHaveBeenCalledWith(8))
    client.blog.permanentlyDeletePage.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(client.blog.permanentlyDeletePage).toHaveBeenCalledWith(8))
  })

  it('PageEdit：加载回填 → 保存跳回列表；非法 id 直接退出', async () => {
    client.blog.getPage.mockResolvedValueOnce(makePage({ id: 4, title: '旧标题', content: '旧内容' }))
    const first = renderRouted(<PageEdit />, '/admin/pages/4', '/admin/pages/:id')
    await waitFor(() => expect((document.querySelector('input[placeholder=页面标题]') as HTMLInputElement).value).toBe('旧标题'))
    const textarea = document.querySelector('textarea') as HTMLTextAreaElement
    expect(textarea.value).toBe('旧内容')

    fireEvent.change((document.querySelector('input[placeholder=页面标题]') as HTMLInputElement), { target: { value: '新标题' } })
    client.blog.updatePage.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(client.blog.updatePage).toHaveBeenCalledWith(4, { title: '新标题', content: '旧内容' }))
    await waitFor(() => expect(screen.getByText('PAGES-LIST')).toBeTruthy(), { timeout: 3000 })
    first.unmount()

    // 非法 id（0）：不请求，直接导航回列表。注意不能让 PageEdit 挂在 '/admin/pages'
    // pattern 下——那样 navigate('/admin/pages') 会重新匹配它自己，永远到不了列表路由
    client.blog.getPage.mockClear()
    renderRouted(<PageEdit />, '/admin/pages/0', '/admin/pages/:id')
    await waitFor(() => expect(screen.getByText('PAGES-LIST')).toBeTruthy(), { timeout: 3000 })
    expect(client.blog.getPage).not.toHaveBeenCalled()
  })

  it('NewPage：空标题拦截；成功创建跳回列表', async () => {
    renderRouted(<NewPage />, '/admin/pages/new', '/admin/pages/new')
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getAllByText('标题不能为空').length).toBeGreaterThan(0))

    fireEvent.change(document.querySelector('input[placeholder=页面标题]') as HTMLInputElement, { target: { value: '新页面' } })
    const textarea = document.querySelector('textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: '正文' } })
    client.blog.createPage.mockResolvedValueOnce(makePage({ id: 5 }))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(client.blog.createPage).toHaveBeenCalledWith({ title: '新页面', content: '正文' }))
    await waitFor(() => expect(screen.getByText('PAGES-LIST')).toBeTruthy(), { timeout: 3000 })
  })
})

describe('MomentsAdmin', () => {
  it('发布：空内容拦截；带媒体发布成功刷新；带媒体项的微语渲染缩略图', async () => {
    client.blog.listMoments
      .mockResolvedValueOnce(paged([makeMoment({ id: 1, content: '第一条', media_items: [] })]))
      .mockResolvedValue(paged([makeMoment({ id: 2, content: '第二条' })]))

    const { container } = renderPlain(<MomentsAdmin />)
    const textarea = () => container.querySelector('textarea') as HTMLTextAreaElement
    await waitFor(() => expect(textarea()).toBeTruthy())

    // 空内容拦截
    fireEvent.click(screen.getByRole('button', { name: '发布' }))
    await waitFor(() => expect(screen.getAllByText('内容不能为空').length).toBeGreaterThan(0))

    // 挑一张媒体（走 MediaPicker 的文件输入，uploadMedia 返回带 id 的 MediaItem）
    const uploaded = makeMedia({ id: 99, original_name: 'up.png' })
    client.blog.uploadMedia.mockResolvedValueOnce([uploaded])
    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement
    Object.defineProperty(fileInput, 'files', { value: [new File(['x'], 'up.png', { type: 'image/png' })], configurable: true })
    fireEvent.change(fileInput)
    await waitFor(() => expect(container.querySelector('.media-picker-row .mthumb, .media-thumb, img')).toBeTruthy(), { timeout: 3000 })

    // 发布：内容 + mediaIds
    fireEvent.change(textarea(), { target: { value: '第二条' } })
    client.blog.createMoment.mockResolvedValueOnce(makeMoment({ id: 2 }))
    fireEvent.click(screen.getByRole('button', { name: '发布' }))
    await waitFor(() => expect(client.blog.createMoment).toHaveBeenCalledWith({ content: '第二条', mediaIds: [99] }))
    await waitFor(() => expect(screen.getByText('第二条')).toBeTruthy()) // reload 后新列表
  })
})

describe('Editor', () => {
  it('新建：空标题拦截；成功创建后 replace 到编辑路由；工具栏插入标记', async () => {
    const { container } = renderRouted(<Editor />, '/admin/editor', '/admin/editor')
    const textarea = container.querySelector('textarea') as HTMLTextAreaElement

    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getAllByText('标题不能为空').length).toBeGreaterThan(0))

    // 工具栏加粗插入（ToolbarButton 的 tooltip 渲染为 data-tip）
    fireEvent.click(container.querySelector('.tbtn[data-tip="加粗"]') as HTMLButtonElement)
    expect(textarea.value).toBe('****')

    fireEvent.change(container.querySelector('input.ed-title, .ed-title input, input') as HTMLInputElement, { target: { value: '标题X' } })
    client.blog.createArticle.mockResolvedValueOnce(makeArticle({ id: 12 }))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    // coverMediaId: null 必须出现：它是「这篇没有头图」的**指令**，缺了字段服务端
    // 会当成「别动头图」。新建时两者恰好等价，编辑时不等价（见下一条用例）。
    await waitFor(() => expect(client.blog.createArticle).toHaveBeenCalledWith({ title: '标题X', content: '****', tags: [], coverMediaId: null }))
    await waitFor(() => expect(screen.getByText('EDITOR-REPLACED')).toBeTruthy(), { timeout: 3000 })
  })

  it('编辑：加载回填后 updateArticle；加载失败回列表', async () => {
    client.blog.getArticle
      .mockResolvedValueOnce(makeArticle({ id: 7, title: '旧文', content: '旧正文', tags: ['Go'] }))
      .mockRejectedValueOnce(new Error('没了'))

    const first = renderRouted(<Editor />, '/admin/editor/7', '/admin/editor/:id')
    await waitFor(() => {
      const el = document.querySelector('.ed-title input, input.ed-title') as HTMLInputElement | null
      expect(el?.value ?? document.querySelector('input')?.value).toBe('旧文')
    })
    client.blog.updateArticle.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    // 两个新字段都在编辑路径上：
    //  - coverMediaId: null —— 这篇本来就没头图，保存后必须还是没头图；
    //  - publishedAt: NOW/1000 —— 编辑器把 created_at 回填进日期选择器，保存时按
    //    「所见即所存」原样送回，单位从毫秒收敛成秒（服务端 created_at 就是秒）。
    await waitFor(() => expect(client.blog.updateArticle).toHaveBeenCalledWith(7, { title: '旧文', content: '旧正文', tags: ['Go'], coverMediaId: null, publishedAt: NOW / 1000 }))
    first.unmount()

    renderRouted(<Editor />, '/admin/editor/404', '/admin/editor/:id')
    await waitFor(() => expect(screen.getByText('POSTS-LIST')).toBeTruthy(), { timeout: 3000 })
    expect(screen.getAllByText('没了').length).toBeGreaterThan(0)
  })
})

describe('Profile', () => {
  it('管理员：显示名/登录名分离；改登录名有提示；保存只发改动字段', async () => {
    client.setCurrentUser(adminMe)
    const { container } = renderPlain(<Profile />)
    await waitFor(() => expect(screen.getByText('管理员账号')).toBeTruthy())
    // 头像选择器在表单里藏了一个 file input（见 media/AvatarPicker），
    // 这里只关心文本框，用 :not 排除——按类型过滤而不是按位置，未来加控件也不会脆。
    const inputs = Array.from(container.querySelectorAll('form input:not([type="file"])')) as HTMLInputElement[]
    expect(inputs.length).toBe(2) // 站长资料卡：显示名 + 登录名（file input 已被排除）
    expect(inputs[0].value).toBe('站长') // 显示名
    expect(inputs[1].value).toBe('admin') // 登录名

    // 只改显示名：不携带 username
    fireEvent.change(inputs[0], { target: { value: '新站名' } })
    client.blog.updateAdminProfile.mockResolvedValueOnce(adminMe)
    fireEvent.submit(inputs[0].closest('form') as HTMLFormElement)
    await waitFor(() => expect(client.blog.updateAdminProfile).toHaveBeenCalledWith({ name: '新站名' }))
    await waitFor(() => expect(screen.getAllByText('显示名称已更新').length).toBeGreaterThan(0))
  })

  it('访客：邮箱只读；改昵称 + 用户名；改密码三卡校验', async () => {
    client.setCurrentUser(visitorMe)
    client.refreshAuth.mockResolvedValue(visitorMe)
    const { container } = renderPlain(<Profile />)
    await waitFor(() => expect(screen.getByText('我的资料')).toBeTruthy())
    const forms = Array.from(container.querySelectorAll('form')) as HTMLFormElement[]
    expect(forms.length).toBe(2) // 资料卡 + 密码卡

    const idInputs = Array.from(forms[0].querySelectorAll('input')) as HTMLInputElement[]
    expect(idInputs[2].disabled).toBe(true) // 邮箱只读
    fireEvent.change(idInputs[0], { target: { value: 'bob' } })
    fireEvent.change(idInputs[1], { target: { value: '鲍勃' } })
    client.blog.updateProfile.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.submit(forms[0])
    await waitFor(() => expect(client.blog.updateProfile).toHaveBeenCalledWith({ username: 'bob', nickname: '鲍勃' }))
    await waitFor(() => expect(client.refreshAuth).toHaveBeenCalled())

    // 密码卡：当前密码缺失
    fireEvent.submit(forms[1])
    await waitFor(() => expect(screen.getAllByText('请输入当前密码').length).toBeGreaterThan(0))

    const pwdInputs = Array.from(forms[1].querySelectorAll('input')) as HTMLInputElement[]
    fireEvent.change(pwdInputs[0], { target: { value: 'old12345' } })
    fireEvent.change(pwdInputs[1], { target: { value: 'new12345' } })
    fireEvent.change(pwdInputs[2], { target: { value: 'nope1234' } })
    fireEvent.submit(forms[1])
    await waitFor(() => expect(screen.getAllByText('两次输入的新密码不一致').length).toBeGreaterThan(0))

    fireEvent.change(pwdInputs[2], { target: { value: 'new12345' } })
    client.blog.changePassword.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.submit(forms[1])
    await waitFor(() => expect(client.blog.changePassword).toHaveBeenCalledWith({ currentPassword: 'old12345', newPassword: 'new12345' }))
    await waitFor(() => expect(screen.getAllByText('密码已更新，其他设备需要重新登录').length).toBeGreaterThan(0))
  })
})

describe('SiteSettings', () => {
  it('加载现有描述 → 编辑计数按折叠空白 → 保存后服务端值回填', async () => {
    client.blog.updateSite.mockResolvedValue({ description: '保存后的描述' })
    const { container } = renderPlain(<SiteSettings />)
    await waitFor(() => expect((container.querySelector('textarea') as HTMLTextAreaElement).value).toBe('测试站'))

    const textarea = container.querySelector('textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: '  多   段空白\n描述  ' } })
    expect(document.body.textContent).toContain('有未保存的修改')

    fireEvent.submit(textarea.closest('form') as HTMLFormElement)
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledWith({ description: '多 段空白 描述' }))
    await waitFor(() => expect((container.querySelector('textarea') as HTMLTextAreaElement).value).toBe('保存后的描述'))
    await waitFor(() => expect(screen.getAllByText('已保存').length).toBeGreaterThan(0))
  })

  it('清空按钮 + 保存空值走「已清空」提示', async () => {
    client.blog.updateSite.mockResolvedValue({ description: '' })
    const { container } = renderPlain(<SiteSettings />)
    await waitFor(() => expect((container.querySelector('textarea') as HTMLTextAreaElement).value).toBe('测试站'))

    fireEvent.click(screen.getByRole('button', { name: '清空' }))
    expect((container.querySelector('textarea') as HTMLTextAreaElement).value).toBe('')
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledWith({ description: '' }))
    await waitFor(() => expect(screen.getAllByText('已清空网站描述，前台将使用内置文案').length).toBeGreaterThan(0))
  })
})
