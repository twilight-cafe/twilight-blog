// admin-content.test.tsx —— Posts / PagesAdmin(+PageEdit/NewPage) / MomentsAdmin / Editor / Profile / SiteSettings
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'

import Posts from '../pages/admin/Posts'
import PagesAdmin, { NewPage, PageEdit } from '../pages/admin/PagesAdmin'
import MomentsAdmin from '../pages/admin/MomentsAdmin'
import Editor from '../pages/admin/Editor'
import Profile from '../pages/admin/Profile'
import SiteSettings from '../pages/admin/SiteSettings'
import { DialogProvider } from '../lib/dialog'
import { ToastProvider } from '../lib/toast'
import { resetSiteCacheForTests } from '../lib/site'
import { setLocale } from '../lib/i18n'
import { NOW, adminMe, getSharedClientMock, makeArticle, makeMedia, makeMoment, makePage, makeTag, resetClientMock, visitorMe } from './helpers'

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

function renderPlain(ui: React.ReactElement, route = '/') {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <ToastProvider>
        <DialogProvider>{ui}</DialogProvider>
      </ToastProvider>
    </MemoryRouter>,
  )
}

function LocationProbe() {
  return <div data-testid="location-search">{useLocation().search}</div>
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

  it('页码从 URL 恢复：带 ?page=2 打开后台文章列表直接请求第 2 页', async () => {
    client.blog.listArticles.mockResolvedValue(paged([makeArticle({ id: 5, title: '第二页文章' })], 2, 3, 25))

    renderRouted(<Posts />, '/admin/posts?page=2', '/admin/posts')
    await waitFor(() => expect(client.blog.listArticles).toHaveBeenCalledWith({ page: 2, pageSize: 12 }))
    expect(screen.getByText('第二页文章')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Page 2' }).getAttribute('aria-current')).toBe('page')
  })

  // 这条用例覆盖「选中项裁剪」这个 effect（Posts.tsx 列表刷新后剔除已消失的 id）。
  //
  // 之前它只被「批量操作」那条顺带擦到，而那条刷新后选中项恰好都还在屏上
  // ——到底有没有走到该 effect，取决于 React 何时 flush 掉 list 的状态更新。
  // 实测出现过 Posts.tsx 该 effect 零覆盖的抖动（同一批 438 个用例时绿时红）。
  //
  // 关键：必须挑一条**刷新列表但不清空选中**的路径。trash/permanent/batchTrash
  // 都会先 `setSelected([])`，于是 effect 里 selected 已空、裁剪逻辑形同虚设；
  // 只有回收站的 `restore()` 是「写操作后只 reload()、不动 selected」。
  // 本用例的判别力来自：勾选的那篇**自己**被恢复掉（restore 不清 selected），
  // 刷新后它从回收站列表消失 —— 真守卫必须把它剔除，坏守卫则会留下一个
  // 指向已不在屏上的幽灵选中项。
  it('列表刷新后已消失的选中项被剔除', async () => {
    client.blog.listTrashArticles
      .mockResolvedValueOnce(paged([makeArticle({ id: 1 }), makeArticle({ id: 2 })]))
      // 恢复 id=2 后重新拉取：只剩 id=1
      .mockResolvedValue(paged([makeArticle({ id: 1 })]))

    const { container } = renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getAllByText('测试文章').length).toBe(2))

    // 勾选**第二行**（id=2）
    fireEvent.click(container.querySelectorAll<HTMLInputElement>('tbody input[type=checkbox]')[1])
    await waitFor(() => expect(screen.getByText('已选 1 篇')).toBeTruthy())

    // 恢复它自己：restore() 只 reload()、**不清 selected**，刷新后 id=2 已消失
    client.blog.restoreArticle.mockResolvedValueOnce({ message: 'ok' })
    const restores = screen.getAllByRole('button', { name: '恢复' })
    fireEvent.click(restores[restores.length - 1])
    await waitFor(() => expect(client.blog.restoreArticle).toHaveBeenCalledWith(2))
    await waitFor(() => expect(client.blog.listTrashArticles).toHaveBeenCalledTimes(2))

    // 裁剪 effect 必须把已消失的 id=2 从选中项里剔除 → 批量条消失
    await waitFor(() => expect(screen.queryByText(/已选/)).toBeNull())
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
    await waitFor(() => expect(client.blog.updatePage).toHaveBeenCalledWith(4, { title: '新标题', content: '旧内容', titleEn: '', contentEn: '' }))
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
    await waitFor(() => expect(client.blog.createPage).toHaveBeenCalledWith({ title: '新页面', content: '正文', titleEn: '', contentEn: '' }))
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
    await waitFor(() => expect(client.blog.createArticle).toHaveBeenCalledWith({ title: '标题X', content: '****', titleEn: '', contentEn: '', aiGenerated: false, aiGeneratedEn: false, tags: [], coverMediaId: null }))
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
    await waitFor(() => expect(client.blog.updateArticle).toHaveBeenCalledWith(7, { title: '旧文', content: '旧正文', titleEn: '', contentEn: '', aiGenerated: false, aiGeneratedEn: false, tags: ['Go'], coverMediaId: null, publishedAt: NOW / 1000 }))
    first.unmount()

    renderRouted(<Editor />, '/admin/editor/404', '/admin/editor/:id')
    await waitFor(() => expect(screen.getByText('POSTS-LIST')).toBeTruthy(), { timeout: 3000 })
    expect(screen.getAllByText('没了').length).toBeGreaterThan(0)
  })

  it('标签输入：已有标签进入下拉建议并可点选；读取失败仍能手输创建', async () => {
    client.blog.listTags.mockResolvedValueOnce(paged([
      makeTag({ id: 1, name: '前端', name_en: 'Frontend' }),
      makeTag({ id: 2, name: 'Go', name_en: 'Golang' }),
    ]))
    const first = renderRouted(<Editor />, '/admin/editor', '/admin/editor')
    await waitFor(() => expect(client.blog.listTags).toHaveBeenCalledWith({ page: 1, pageSize: 12 }))

    // 输入过滤词：下拉里应该出现匹配的已有标签，而不是恒为「无数据」。
    const combo = screen.getByRole('combobox')
    fireEvent.change(combo, { target: { value: '前' } })
    const option = screen.getByRole('option')
    expect(option.textContent).toBe('前端')
    fireEvent.click(option)
    expect(first.container.querySelector('.select-tag')?.textContent).toContain('前端')
    first.unmount()

    // 标签列表读不到：建议为空，但输入 + 回车创建新标签的主路径不受影响。
    client.blog.listTags.mockRejectedValueOnce(new Error('down'))
    const second = renderRouted(<Editor />, '/admin/editor', '/admin/editor')
    const manual = screen.getByRole('combobox')
    fireEvent.change(manual, { target: { value: '新标签' } })
    fireEvent.keyDown(manual, { key: 'Enter' })
    expect(second.container.querySelector('.select-tag')?.textContent).toContain('新标签')
    second.unmount()

    // 标签请求在卸载之后才落地（成功 / 失败各一次）：alive 守卫必须阻止写状态。
    let resolveTags!: (value: unknown) => void
    client.blog.listTags.mockImplementationOnce(() => new Promise((resolve) => { resolveTags = resolve }) as never)
    const third = renderRouted(<Editor />, '/admin/editor', '/admin/editor')
    third.unmount()
    await act(async () => { resolveTags(paged([makeTag({ id: 3, name: 'React' })])) })

    let rejectTags!: (reason: unknown) => void
    client.blog.listTags.mockImplementationOnce(() => new Promise((_, reject) => { rejectTags = reject }) as never)
    const fourth = renderRouted(<Editor />, '/admin/editor', '/admin/editor')
    fourth.unmount()
    await act(async () => { rejectTags(new Error('late down')) })
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
  it('页签：默认站点文案；外观与 AI 翻译写入 ?tab=，切回默认页签删除参数', async () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/admin/site-settings']}>
        <ToastProvider>
          <DialogProvider>
            <LocationProbe />
            <SiteSettings />
          </DialogProvider>
        </ToastProvider>
      </MemoryRouter>,
    )
    await screen.findByRole('tab', { name: '站点文案' })
    // 默认页签不写 URL：地址保持干净。
    expect(screen.getByTestId('location-search').textContent).toBe('')
    expect(container.querySelectorAll('textarea')).toHaveLength(2)

    fireEvent.click(screen.getByRole('tab', { name: '外观参数' }))
    await waitFor(() => expect(screen.getByTestId('location-search').textContent).toBe('?tab=appearance'))
    expect(container.querySelectorAll('textarea')).toHaveLength(0)
    expect(container.querySelectorAll('input[type=number]')).toHaveLength(4)

    fireEvent.click(screen.getByRole('tab', { name: 'AI 翻译' }))
    await waitFor(() => expect(screen.getByTestId('location-search').textContent).toBe('?tab=translate'))
    expect(screen.getByRole('button', { name: '保存翻译设置' })).toBeTruthy()

    fireEvent.click(screen.getByRole('tab', { name: '站点文案' }))
    await waitFor(() => expect(screen.getByTestId('location-search').textContent).toBe(''))
    expect(container.querySelectorAll('textarea')).toHaveLength(2)
  })

  it('页签：无法识别的 ?tab= 回退到站点文案', async () => {
    renderPlain(<SiteSettings />, '/admin/site-settings?tab=bogus')
    const tab = await screen.findByRole('tab', { name: '站点文案' })
    expect(tab.getAttribute('aria-selected')).toBe('true')
  })

  it('跨页签校验：描述超长时从「外观参数」保存会跳回文案页签并提示', async () => {
    const { container } = renderPlain(<SiteSettings />)
    await screen.findByRole('tab', { name: '站点文案' })
    fireEvent.change(container.querySelector('textarea') as HTMLTextAreaElement, {
      target: { value: '长'.repeat(201) },
    })

    fireEvent.click(screen.getByRole('tab', { name: '外观参数' }))
    // 本页签的字段都合法：按钮不能被另一页签的错误无声禁用。
    expect(screen.getByRole('button', { name: '保存' }).hasAttribute('disabled')).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(screen.getAllByText('网站描述不能超过 200 个字符').length).toBeGreaterThan(0))
    expect(screen.getByRole('tab', { name: '站点文案' }).getAttribute('aria-selected')).toBe('true')
    expect(client.blog.updateSite).not.toHaveBeenCalled()
  })

  it('跨页签校验：外观参数非法时从「站点文案」保存会跳到外观页签并提示', async () => {
    const { container } = renderPlain(<SiteSettings />)
    await screen.findByRole('tab', { name: '站点文案' })

    fireEvent.click(screen.getByRole('tab', { name: '外观参数' }))
    const radiusInput = () => container.querySelectorAll('input[type=number]')[2] as HTMLInputElement
    fireEvent.change(radiusInput(), { target: { value: '25' } })
    // 出错的字段就在当前页签：按钮禁用是合理的。
    expect(screen.getByRole('button', { name: '保存' }).hasAttribute('disabled')).toBe(true)

    fireEvent.click(screen.getByRole('tab', { name: '站点文案' }))
    fireEvent.change(container.querySelector('textarea') as HTMLTextAreaElement, {
      target: { value: '新的描述' },
    })
    // 副本页签自身合法：保存按钮可用，点击后会自动跳到出错的页签。
    const save = screen.getByRole('button', { name: '保存' })
    expect(save.hasAttribute('disabled')).toBe(false)
    fireEvent.click(save)

    await waitFor(() => expect(screen.getAllByText('圆角必须是 0 到 24 之间的整数').length).toBeGreaterThan(0))
    expect(screen.getByRole('tab', { name: '外观参数' }).getAttribute('aria-selected')).toBe('true')
    expect(client.blog.updateSite).not.toHaveBeenCalled()
  })

  it('保存进行中重复提交只发一次请求', async () => {
    let resolveSave: (value: { description: string }) => void = () => {}
    client.blog.updateSite.mockReturnValueOnce(new Promise((resolve) => { resolveSave = resolve }))
    const { container } = renderPlain(<SiteSettings />)
    await screen.findByRole('tab', { name: '站点文案' })
    fireEvent.change(container.querySelector('textarea') as HTMLTextAreaElement, {
      target: { value: '新描述' },
    })
    const form = container.querySelector('form') as HTMLFormElement
    fireEvent.submit(form)
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledTimes(1))

    // Enter 可以绕过 disabled 的按钮：busy 期间再提交一次不能再发请求。
    fireEvent.submit(form)
    expect(client.blog.updateSite).toHaveBeenCalledTimes(1)

    await act(async () => { resolveSave({ description: '新描述' }) })
  })

  it('加载现有描述 → 编辑计数按折叠空白 → 保存后服务端值回填', async () => {
    client.blog.updateSite.mockResolvedValue({ description: '保存后的描述' })
    const { container } = renderPlain(<SiteSettings />)
    await waitFor(() => expect((container.querySelector('textarea') as HTMLTextAreaElement).value).toBe('测试站'))

    const textarea = container.querySelector('textarea') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: '  多   段空白\n描述  ' } })
    expect(document.body.textContent).toContain('有未保存的修改')

    fireEvent.submit(textarea.closest('form') as HTMLFormElement)
    // code_block_max_lines 与 home_page_size 一并提交：同一张设置卡、同一次保存
    // （两个数都来自 getSite 回的缺省值：20 行 / 12 篇）。
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledWith({
      description: '多 段空白 描述',
      code_block_max_lines: 20,
      home_page_size: 12,
      border_radius: 4,
      post_banner_height: 400,
      moments_enabled: true,
      comment_notify_to: '',
      description_en: '',
    }))
    await waitFor(() => expect((container.querySelector('textarea') as HTMLTextAreaElement).value).toBe('保存后的描述'))
    await waitFor(() => expect(screen.getAllByText('已保存').length).toBeGreaterThan(0))
  })

  it('清空按钮同时清空中英文描述；保存空值走「已清空」提示', async () => {
    client.blog.getSite.mockResolvedValue({ description: '测试站', description_en: 'Test site' })
    client.blog.updateSite.mockResolvedValue({ description: '' })
    const { container } = renderPlain(<SiteSettings />)
    const descriptionEN = () => container.querySelectorAll('textarea')[1] as HTMLTextAreaElement
    await waitFor(() => expect((container.querySelector('textarea') as HTMLTextAreaElement).value).toBe('测试站'))
    await waitFor(() => expect(descriptionEN().value).toBe('Test site'))

    const clear = screen.getByRole('button', { name: '清空描述' }) as HTMLButtonElement
    expect(clear.disabled).toBe(false)
    fireEvent.click(clear)
    // 「清空描述」清的是站点文案整张卡：英文描述不能留在原地，
    // 否则英文界面会继续展示刚被「清空」的旧文案。
    expect((container.querySelector('textarea') as HTMLTextAreaElement).value).toBe('')
    expect(descriptionEN().value).toBe('')
    // 两份都空时按钮再次点击没有意义，置灰。
    expect(clear.disabled).toBe(true)
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    // 圆角也随同一次保存下发（来自 getSite 回的缺省值 4）。
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledWith({
      description: '',
      code_block_max_lines: 20,
      home_page_size: 12,
      border_radius: 4,
      post_banner_height: 400,
      moments_enabled: true,
      comment_notify_to: '',
      description_en: '',
    }))
    await waitFor(() => expect(screen.getAllByText('已清空网站描述，前台将使用内置文案').length).toBeGreaterThan(0))
  })

  it('只清空中文描述时英文仍在：提示「已更新」而不是「已清空」', async () => {
    client.blog.getSite.mockResolvedValue({ description: '中文简介', description_en: 'English intro' })
    client.blog.updateSite.mockResolvedValue({ description: '', description_en: 'English intro' })
    const { container } = renderPlain(<SiteSettings />)
    const description = () => container.querySelector('textarea') as HTMLTextAreaElement
    await waitFor(() => expect(description().value).toBe('中文简介'))

    fireEvent.change(description(), { target: { value: '' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledWith(expect.objectContaining({
      description: '',
      description_en: 'English intro',
    })))
    // 英文描述还在，前台并没有回退到内置文案——不能给出「已清空」的错误结论。
    await waitFor(() => expect(screen.getAllByText('站点设置已更新').length).toBeGreaterThan(0))
    expect(screen.queryByText('已清空网站描述，前台将使用内置文案')).toBeNull()
  })

  it('英文界面预览显示英文描述；英文为空时回退中文', async () => {
    setLocale('en')
    client.blog.getSite.mockResolvedValue({ description: '中文简介', description_en: 'English intro' })
    const first = renderPlain(<SiteSettings />)
    await waitFor(() => expect(first.container.querySelector('.set-slogan')?.textContent).toBe('English intro'))
    first.unmount()

    // 第二次渲染会命中 site.ts 的模块缓存，必须先清掉才能换一份服务端响应。
    resetSiteCacheForTests()
    client.blog.getSite.mockResolvedValue({ description: '中文简介', description_en: '' })
    const second = renderPlain(<SiteSettings />)
    await waitFor(() => expect(second.container.querySelector('.set-slogan')?.textContent).toBe('中文简介'))
  })

  it('英文描述超限：中文计数不误报，英文计数单独变红', async () => {
    const longEN = 'E'.repeat(201)
    client.blog.getSite.mockResolvedValue({ description: '中文简介', description_en: longEN })
    const { container } = renderPlain(<SiteSettings />)
    await waitFor(() => expect((container.querySelectorAll('textarea')[1] as HTMLTextAreaElement).value).toBe(longEN))

    const counts = container.querySelectorAll('.set-count')
    // 第一份是中文计数的标题栏，第二份是英文描述下方的计数。
    expect(counts[0].classList.contains('over')).toBe(false)
    expect(counts[1].classList.contains('over')).toBe(true)
    expect((screen.getByRole('button', { name: '保存' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('站点设置加载失败：显示错误，编辑后也不能保存', async () => {
    client.blog.getSite.mockRejectedValue(new Error('down'))
    const { container } = renderPlain(<SiteSettings />)
    const textarea = () => container.querySelector('textarea') as HTMLTextAreaElement
    await waitFor(() => expect(screen.getByText(/站点设置加载失败/)).toBeTruthy())

    // 表单里只是默认值；哪怕用户改了描述，也不能把其余默认值一起写回服务端。
    fireEvent.change(textarea(), { target: { value: '新描述' } })
    const save = screen.getByRole('button', { name: '保存' }) as HTMLButtonElement
    expect(save.disabled).toBe(true)
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() =>
      expect(screen.getAllByText('站点设置尚未加载成功，请刷新页面后重试').length).toBeGreaterThan(0),
    )
    expect(client.blog.updateSite).not.toHaveBeenCalled()
  })

  it('英文描述：加载回显、编辑后随站点设置一并保存、服务端值回填', async () => {
    client.blog.getSite.mockResolvedValue({ description: '中文简介', description_en: 'English intro' })
    client.blog.updateSite.mockResolvedValue({ description: '中文简介', description_en: 'New English' })
    const { container } = renderPlain(<SiteSettings />)
    // 第一张表单里依次是中文描述与英文描述两个多行输入框。
    const descriptionEN = () => container.querySelectorAll('textarea')[1] as HTMLTextAreaElement
    await waitFor(() => expect(descriptionEN().value).toBe('English intro'))

    fireEvent.change(descriptionEN(), { target: { value: '  New   English  ' } })
    expect(document.body.textContent).toContain('有未保存的修改')
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)

    // 英文描述与中文描述同一次保存；空白按同一规则折叠。
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledWith({
      description: '中文简介',
      description_en: 'New English',
      code_block_max_lines: 20,
      home_page_size: 12,
      border_radius: 4,
      post_banner_height: 400,
      moments_enabled: true,
      comment_notify_to: '',
    }))
    await waitFor(() => expect(descriptionEN().value).toBe('New English'))
  })

  it('代码块行数：加载回填、修改后随描述一并保存、越界拦截', async () => {
    client.blog.getSite.mockResolvedValue({ description: '测试站', code_block_max_lines: 35 })
    client.blog.updateSite.mockResolvedValue({ description: '测试站', code_block_max_lines: 35 })
    const { container } = renderPlain(<SiteSettings />)
    // 数字输入框都在「外观参数」页签里。
    fireEvent.click(await screen.findByRole('tab', { name: '外观参数' }))
    // 页面上有两个数字输入框（行数在前、首页每页篇数在后），按顺序取更稳。
    const numberInputs = () => container.querySelectorAll('input[type=number]')
    const linesInput = () => numberInputs()[0] as HTMLInputElement
    await waitFor(() => expect(linesInput().value).toBe('35'))

    // 保存按钮被「描述未修改」挡住 → 改行数后应放行（行数也计入 dirty）。
    fireEvent.change(linesInput(), { target: { value: '60' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledWith({
      description: '测试站',
      code_block_max_lines: 60,
      home_page_size: 12,
      border_radius: 4,
      post_banner_height: 400,
      moments_enabled: true,
      comment_notify_to: '',
      description_en: '',
    }))
    // 服务端返回的最终值回填输入框（mock 返回 35）。
    await waitFor(() => expect(linesInput().value).toBe('35'))

    // 越界（500 之外）→ 保存按钮禁用 + 直接提交被拦截
    fireEvent.change(linesInput(), { target: { value: '501' } })
    expect(container.querySelector('.act-row button[type=submit]')?.hasAttribute('disabled')).toBe(true)

    // 小数不能被 parseInt 静默截成 60：输入什么就应当按什么校验。
    fireEvent.change(linesInput(), { target: { value: '60.5' } })
    expect(container.querySelector('.act-row button[type=submit]')?.hasAttribute('disabled')).toBe(true)
    // 指数写法同理不能被 Number() 读成 1000。
    fireEvent.change(linesInput(), { target: { value: '1e3' } })
    expect(container.querySelector('.act-row button[type=submit]')?.hasAttribute('disabled')).toBe(true)
    expect(client.blog.updateSite).toHaveBeenCalledTimes(1)

    // 按钮 disabled 挡不住 form 的 submit 事件：防御分支用 toast 拦截、不发请求。
    // 这里直接提交的是非法数字；随后再覆盖空串路径。
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getAllByText('代码块行数必须是 3 到 500 之间的整数').length).toBeGreaterThan(0))
    fireEvent.change(linesInput(), { target: { value: '' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getAllByText('代码块行数必须是 3 到 500 之间的整数').length).toBeGreaterThan(0))
    expect(client.blog.updateSite).toHaveBeenCalledTimes(1)
  })

  it('首页每页文章数：加载回填、修改后随描述一并保存、越界拦截', async () => {
    client.blog.getSite.mockResolvedValue({ description: '测试站', home_page_size: 6 })
    client.blog.updateSite.mockResolvedValue({ description: '测试站', home_page_size: 6 })
    const { container } = renderPlain(<SiteSettings />)
    fireEvent.click(await screen.findByRole('tab', { name: '外观参数' }))
    const pageSizeInput = () => container.querySelectorAll('input[type=number]')[1] as HTMLInputElement
    await waitFor(() => expect(pageSizeInput().value).toBe('6'))

    // 只改这一项也应放行保存（它计入 dirty）。
    fireEvent.change(pageSizeInput(), { target: { value: '9' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledWith({
      description: '测试站',
      code_block_max_lines: 20,
      home_page_size: 9,
      border_radius: 4,
      post_banner_height: 400,
      moments_enabled: true,
      comment_notify_to: '',
      description_en: '',
    }))
    // 服务端返回的最终值回填输入框（mock 返回 6）。
    await waitFor(() => expect(pageSizeInput().value).toBe('6'))

    // 越界（1..12 之外）→ 保存按钮禁用；直接提交被 toast 拦截、不发请求。
    fireEvent.change(pageSizeInput(), { target: { value: '13' } })
    expect(container.querySelector('.act-row button[type=submit]')?.hasAttribute('disabled')).toBe(true)
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getAllByText('首页每页文章数必须是 1 到 12 之间的整数').length).toBeGreaterThan(0))

    // 下界同样拦：0 与非整数都不该进请求体。
    fireEvent.change(pageSizeInput(), { target: { value: '0' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getAllByText('首页每页文章数必须是 1 到 12 之间的整数').length).toBeGreaterThan(0))
    expect(client.blog.updateSite).toHaveBeenCalledTimes(1)
  })

  it('圆角：加载回填、0 是合法值不是空值、越界拦截', async () => {
    client.blog.getSite.mockResolvedValue({ description: '测试站', border_radius: 8 })
    client.blog.updateSite.mockResolvedValue({ description: '测试站', border_radius: 8 })
    const { container } = renderPlain(<SiteSettings />)
    fireEvent.click(await screen.findByRole('tab', { name: '外观参数' }))
    // 圆角输入框是第三个数字框（行数、每页篇数之后）。
    const radiusInput = () => container.querySelectorAll('input[type=number]')[2] as HTMLInputElement
    await waitFor(() => expect(radiusInput().value).toBe('8'))

    // 改一个合法值 → 放行保存（它计入 dirty）。
    fireEvent.change(radiusInput(), { target: { value: '16' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledWith({
      description: '测试站',
      code_block_max_lines: 20,
      home_page_size: 12,
      border_radius: 16,
      post_banner_height: 400,
      moments_enabled: true,
      comment_notify_to: '',
      description_en: '',
    }))
    // 服务端返回 8 → 回填。
    await waitFor(() => expect(radiusInput().value).toBe('8'))

    // 0 是**合法值**（直角）。这是本项设置最容易写错的地方：若校验写成
    // `radius <= 0` 早退、或 dirty 用 `!radius` 判断，0 就会被当成「没填」，
    // 站长永远设不出直角且界面不给任何提示。所以这里必须能提交、且原样透传 0。
    client.blog.updateSite.mockResolvedValueOnce({ description: '测试站', border_radius: 0 })
    fireEvent.change(radiusInput(), { target: { value: '0' } })
    expect(container.querySelector('.act-row button[type=submit]')?.hasAttribute('disabled')).toBe(false)
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenLastCalledWith({
      description: '测试站',
      code_block_max_lines: 20,
      home_page_size: 12,
      border_radius: 0,
      post_banner_height: 400,
      moments_enabled: true,
      comment_notify_to: '',
      description_en: '',
    }))
  })

  it('圆角越界（24 之外）拦截：按钮禁用，直接提交被 toast 挡下', async () => {
    client.blog.getSite.mockResolvedValue({ description: '测试站', border_radius: 4 })
    const { container } = renderPlain(<SiteSettings />)
    fireEvent.click(await screen.findByRole('tab', { name: '外观参数' }))
    const radiusInput = () => container.querySelectorAll('input[type=number]')[2] as HTMLInputElement
    await waitFor(() => expect(radiusInput().value).toBe('4'))

    // 上界外
    fireEvent.change(radiusInput(), { target: { value: '25' } })
    expect(container.querySelector('.act-row button[type=submit]')?.hasAttribute('disabled')).toBe(true)
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getAllByText('圆角必须是 0 到 24 之间的整数').length).toBeGreaterThan(0))

    // 负数与非整数同样拦（空串 parseInt 出 NaN）。
    fireEvent.change(radiusInput(), { target: { value: '-1' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getAllByText('圆角必须是 0 到 24 之间的整数').length).toBeGreaterThan(0))
    fireEvent.change(radiusInput(), { target: { value: '' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getAllByText('圆角必须是 0 到 24 之间的整数').length).toBeGreaterThan(0))
    expect(client.blog.updateSite).not.toHaveBeenCalled()
  })
})
