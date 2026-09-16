// admin-gaps.test.tsx —— admin 页面分支补缺：回收站批量、失败 toast、工具栏、注册/找回分支
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import Posts from '../pages/admin/Posts'
import MomentsAdmin from '../pages/admin/MomentsAdmin'
import { NewPage, PageEdit } from '../pages/admin/PagesAdmin'
import Editor from '../pages/admin/Editor'
import Profile from '../pages/admin/Profile'
import Register from '../pages/admin/Register'
import ForgotPassword from '../pages/admin/ForgotPassword'
import SiteSettings from '../pages/admin/SiteSettings'
import CommentsAdmin from '../pages/admin/CommentsAdmin'
import UsersAdmin from '../pages/admin/UsersAdmin'
import TagsAdmin from '../pages/admin/TagsAdmin'
import MediaAdmin from '../pages/admin/MediaAdmin'
import { DialogProvider } from '../lib/dialog'
import { ToastProvider } from '../lib/toast'
import { resetSiteCacheForTests } from '../lib/site'
import { adminMe, getSharedClientMock, makeArticle, makeComment, makeMedia, makeMoment, makePage, makeTag, makeUser, resetClientMock, visitorMe } from './helpers'

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
            <Route path="/admin/pages" element={<div>PAGES-LIST</div>} />
            <Route path="/admin/login" element={<div>LOGIN-STUB</div>} />
            <Route path="/admin/profile" element={<div>PROFILE-STUB</div>} />
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
  resetSiteCacheForTests()
  client.setCurrentUser(null)
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
})

describe('Posts 回收站批量', () => {
  it('trash tab：批量恢复与批量彻底删除（各自确认路径）；取消选择', async () => {
    client.blog.listArticles.mockResolvedValue(paged([], 1, 1, 0))
    client.blog.listTrashArticles
      .mockResolvedValueOnce(paged([makeArticle({ id: 11, title: '废稿一' }), makeArticle({ id: 12, title: '废稿二' })]))
      .mockResolvedValue(paged([makeArticle({ id: 11, title: '废稿一' })]))

    const { container } = renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    await waitFor(() => expect(screen.getByText('没有符合条件的文章')).toBeTruthy()) // active 空列表加载完成
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('废稿一')).toBeTruthy())
    await waitFor(() => expect(screen.getByText('废稿二')).toBeTruthy())

    // 勾选两行 → trash 的 bulkbar 是「恢复 + 彻底删除」
    const checkboxes = container.querySelectorAll('tbody input[type=checkbox]')
    fireEvent.click(checkboxes[0])
    fireEvent.click(checkboxes[1])
    expect(screen.getByText('已选 2 篇')).toBeTruthy()

    client.blog.batchRestoreArticles.mockResolvedValueOnce({ message: 'ok' })
    const bulkRestore = (Array.from(container.querySelectorAll('.bulkbar button')) as HTMLButtonElement[])
      .find((b) => b.textContent === '恢复') as HTMLButtonElement // bulkbar：取消/恢复
    fireEvent.click(bulkRestore)
    await waitFor(() => expect(client.blog.batchRestoreArticles).toHaveBeenCalledWith({ ids: [11, 12] }))
    await waitFor(() => expect(screen.getAllByText('已批量恢复').length).toBeGreaterThan(0))

    // 重进 trash 再勾选 → 彻底删除（confirm）
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('废稿一')).toBeTruthy())
    fireEvent.click(container.querySelectorAll('tbody input[type=checkbox]')[0])
    client.blog.batchPermanentlyDeleteArticles.mockResolvedValueOnce({ message: 'ok' })
    const bulkPermanent = (Array.from(container.querySelectorAll('.bulkbar button')) as HTMLButtonElement[])
      .find((b) => b.textContent === '彻底删除') as HTMLButtonElement
    fireEvent.click(bulkPermanent)
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(client.blog.batchPermanentlyDeleteArticles).toHaveBeenCalledWith({ ids: [11] }))
  })
})

describe('MomentsAdmin 分支', () => {
  it('发布失败 toast；删除（确认）→ 重载', async () => {
    client.blog.listMoments
      .mockResolvedValueOnce(paged([makeMoment({ id: 1, content: '首条' })]))
      .mockResolvedValue(paged([makeMoment({ id: 1, content: '首条' })]))

    const { container } = renderPlain(<MomentsAdmin />)
    const textarea = () => container.querySelector('textarea') as HTMLTextAreaElement
    await waitFor(() => expect(textarea()).toBeTruthy())

    fireEvent.change(textarea(), { target: { value: '新的微语' } })
    client.blog.createMoment.mockRejectedValueOnce(new Error('发布失败'))
    fireEvent.click(screen.getByRole('button', { name: '发布' }))
    await waitFor(() => expect(screen.getAllByText('发布失败').length).toBeGreaterThan(0))

    client.blog.deleteMoment.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(client.blog.deleteMoment).toHaveBeenCalledWith(1))
  })

  it('trash tab：恢复与彻底删除', async () => {
    client.blog.listMoments.mockResolvedValue(paged([], 1, 1, 0))
    client.blog.listTrashMoments.mockResolvedValue(paged([makeMoment({ id: 6, content: '回收站微语' })]))
    renderPlain(<MomentsAdmin />)
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('回收站微语')).toBeTruthy())

    client.blog.restoreMoment.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '恢复' }))
    await waitFor(() => expect(client.blog.restoreMoment).toHaveBeenCalledWith(6))

    client.blog.permanentlyDeleteMoment.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(client.blog.permanentlyDeleteMoment).toHaveBeenCalledWith(6))
  })
})

describe('Editor 工具栏', () => {
  it('九个插入按钮逐一写入成对标记', async () => {
    const { container } = renderRouted(<Editor />, '/admin/editor', '/admin/editor')
    await waitFor(() => expect(container.querySelector('textarea.mds')).toBeTruthy())
    const textarea = container.querySelector('textarea.mds') as HTMLTextAreaElement

    const inserted = ['\n## \n', '\n### \n', '****', '**', '``', '\n```\n\n```\n', '\n> \n', '\n- [ ] \n', '\n| 列1 | 列2 |\n| --- | --- |\n| 值 | 值 |\n']
    const tips = ['标题 2', '标题 3', '加粗', '斜体', '行内代码', '代码块', '引用', '任务列表', '表格']
    let acc = ''
    for (let index = 0; index < tips.length; index++) {
      fireEvent.click(container.querySelector(`.tbtn[data-tip="${tips[index]}"]`) as HTMLButtonElement)
      acc += inserted[index]
      expect(textarea.value).toBe(acc) // 每次在光标处（末尾）追加
    }
  })
})

describe('PagesAdmin 分支', () => {
  it('NewPage：创建失败 toast；返回按钮跳回列表', async () => {
    const { container } = renderRouted(<NewPage />, '/admin/pages/new', '/admin/pages/new')
    fireEvent.change(container.querySelector('input[placeholder=页面标题]') as HTMLInputElement, { target: { value: '失败页' } })
    client.blog.createPage.mockRejectedValueOnce(new Error('创建失败'))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getAllByText('创建失败').length).toBeGreaterThan(0))

    fireEvent.click(screen.getByRole('button', { name: '← 返回' }))
    await waitFor(() => expect(screen.getByText('PAGES-LIST')).toBeTruthy())
  })

  it('PageEdit：保存失败 toast 不跳转', async () => {
    client.blog.getPage.mockResolvedValueOnce(makePage({ id: 4, title: '旧标题', content: '旧内容' }))
    const { container } = renderRouted(<PageEdit />, '/admin/pages/4', '/admin/pages/:id')
    await waitFor(() => expect((container.querySelector('input[placeholder=页面标题]') as HTMLInputElement).value).toBe('旧标题'))

    client.blog.updatePage.mockRejectedValueOnce(new Error('保存失败'))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getAllByText('保存失败').length).toBeGreaterThan(0))
    expect((container.querySelector('input[placeholder=页面标题]') as HTMLInputElement).value).toBe('旧标题')
  })
})

describe('Profile 分支', () => {
  it('管理员改登录名：按钮旁提示 + 保存携带 username', async () => {
    client.setCurrentUser(adminMe)
    const { container } = renderPlain(<Profile />)
    await waitFor(() => expect(screen.getByText('管理员账号')).toBeTruthy())
    // 排除头像选择器藏在表单里的 file input（见 media/AvatarPicker）。
    const inputs = Array.from(container.querySelectorAll('form input:not([type="file"])')) as HTMLInputElement[]

    fireEvent.change(inputs[0], { target: { value: '新站名' } })
    fireEvent.change(inputs[1], { target: { value: 'newadmin' } })
    expect(document.body.textContent).toContain('显示名称有改动')
    expect(document.body.textContent).toContain('登录名有改动')

    client.blog.updateAdminProfile.mockResolvedValueOnce(adminMe)
    fireEvent.submit(inputs[0].closest('form') as HTMLFormElement)
    await waitFor(() => expect(client.blog.updateAdminProfile).toHaveBeenCalledWith({ name: '新站名', username: 'newadmin' }))
    await waitFor(() => expect(screen.getAllByText('已更新，下次登录请使用新的登录名').length).toBeGreaterThan(0))
  })

  it('密码卡：新密码不合规拦截；修改失败 toast；成功后清空表单', async () => {
    client.setCurrentUser(visitorMe)
    client.refreshAuth.mockResolvedValue(visitorMe)
    const { container } = renderPlain(<Profile />)
    await waitFor(() => expect(screen.getByText('我的资料')).toBeTruthy())
    const forms = Array.from(container.querySelectorAll('form')) as HTMLFormElement[]
    const pwdInputs = Array.from(forms[1].querySelectorAll('input')) as HTMLInputElement[]

    // 新密码不合规
    fireEvent.change(pwdInputs[0], { target: { value: 'old12345' } })
    fireEvent.change(pwdInputs[1], { target: { value: 'short' } })
    fireEvent.change(pwdInputs[2], { target: { value: 'short' } })
    fireEvent.submit(forms[1])
    await waitFor(() => expect(screen.getAllByText(/密码/).length).toBeGreaterThan(0))
    expect(client.blog.changePassword).not.toHaveBeenCalled()

    // 修改失败
    fireEvent.change(pwdInputs[1], { target: { value: 'new12345' } })
    fireEvent.change(pwdInputs[2], { target: { value: 'new12345' } })
    client.blog.changePassword.mockRejectedValueOnce(new Error('密码错误'))
    fireEvent.submit(forms[1])
    await waitFor(() => expect(screen.getAllByText('密码错误').length).toBeGreaterThan(0))

    // 成功 → 表单清空
    client.blog.changePassword.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.submit(forms[1])
    await waitFor(() => expect(screen.getAllByText('密码已更新，其他设备需要重新登录').length).toBeGreaterThan(0))
    expect((pwdInputs[0]).value).toBe('')
  })
})

describe('注册与找回密码分支', () => {
  it('Register：验证码发送失败；邮箱/空验证码拦截；发送后可重发', async () => {
    client.blog.requestEmailCode.mockRejectedValueOnce(new Error('发送验证码失败'))
    const { container } = renderRouted(<Register />, '/admin/register', '/admin/register')
    const email = container.querySelector('input[type=email]') as HTMLInputElement
    fireEvent.change(email, { target: { value: 'a@x.com' } })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(screen.getAllByText('发送验证码失败').length).toBeGreaterThan(0))

    // 再发一次成功 → codeSent 置真，出现验证码输入与「重新发送」按钮
    client.blog.requestEmailCode.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(container.querySelector('input[placeholder=验证码]')).toBeTruthy())

    // 邮箱非法
    const form = container.querySelector('form') as HTMLFormElement
    fireEvent.change(email, { target: { value: 'bad' } })
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getAllByText(/邮箱/).length).toBeGreaterThan(0))

    // 空验证码
    fireEvent.change(email, { target: { value: 'a@x.com' } })
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getAllByText('请输入验证码').length).toBeGreaterThan(0))
  })

  it('ForgotPassword：发送失败与重发；重置成功回登录页', async () => {
    client.blog.requestEmailCode.mockRejectedValueOnce(new Error('发送验证码失败'))
    const { container } = renderRouted(<ForgotPassword />, '/admin/forgot', '/admin/forgot')
    const email = container.querySelector('input[type=email]') as HTMLInputElement
    fireEvent.change(email, { target: { value: 'a@x.com' } })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(screen.getAllByText('发送验证码失败').length).toBeGreaterThan(0))

    client.blog.requestEmailCode.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(container.querySelector('input[placeholder=验证码]')).toBeTruthy())

    const form = container.querySelector('form') as HTMLFormElement
    const codeInput = container.querySelector('input[placeholder=验证码]') as HTMLInputElement
    const passwordInput = container.querySelector('input[type=password]') as HTMLInputElement
    fireEvent.change(codeInput, { target: { value: '42' } })
    fireEvent.change(passwordInput, { target: { value: 'NewPass123' } })

    // 重置失败
    client.blog.resetPassword.mockRejectedValueOnce(new Error('验证码无效'))
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getAllByText('验证码无效').length).toBeGreaterThan(0))

    // 重置成功 → 清身份 → 回登录页
    client.blog.resetPassword.mockResolvedValueOnce({ message: 'ok' })
    client.setCurrentUser(visitorMe)
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getByText('LOGIN-STUB')).toBeTruthy(), { timeout: 3000 })
    await waitFor(() => expect(screen.getAllByText('密码已重置，请使用新密码登录').length).toBeGreaterThan(0))
  })
})

describe('SiteSettings 分支', () => {
  it('超长描述拦截保存；保存失败 toast', async () => {
    client.blog.updateSite.mockRejectedValueOnce(new Error('保存失败'))
    const { container } = renderPlain(<SiteSettings />)
    const textarea = () => container.querySelector('textarea') as HTMLTextAreaElement
    await waitFor(() => expect(textarea().value).toBe('测试站'))

    // 超长（>200 码点）→ 拦截 + 计数变红
    fireEvent.change(textarea(), { target: { value: '长'.repeat(201) } })
    expect(container.querySelector('.set-count.over')).toBeTruthy()
    fireEvent.submit(textarea().closest('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getAllByText('网站描述不能超过 200 个字符').length).toBeGreaterThan(0))
    expect(client.blog.updateSite).not.toHaveBeenCalled()

    // 合法长度但接口失败
    fireEvent.change(textarea(), { target: { value: '合法描述' } })
    fireEvent.submit(textarea().closest('form') as HTMLFormElement)
    await waitFor(() => expect(client.blog.updateSite).toHaveBeenCalledWith({ description: '合法描述', code_block_max_lines: 20 }))
    await waitFor(() => expect(screen.getAllByText('保存失败').length).toBeGreaterThan(0))
  })
})

describe('CommentsAdmin / UsersAdmin / TagsAdmin / MediaAdmin 失败分支', () => {
  it('CommentsAdmin：恢复与彻底删除失败 toast；三种 target 标签；空列表', async () => {
    client.blog.listAllComments.mockResolvedValue(paged([
      makeComment({ id: 1, target_type: 'article', target_id: 3 }),
      makeComment({ id: 2, target_type: 'moment', target_id: 4 }),
      makeComment({ id: 3, target_type: 'page', target_id: 5 }),
    ] as never))
    const { container } = renderPlain(<CommentsAdmin />)
    await waitFor(() => {
      const body = document.body.textContent ?? ''
      expect(body).toContain('文章 #3')
      expect(body).toContain('微语 #4')
      expect(body).toContain('页面 #5')
    })

    client.blog.restoreComment.mockRejectedValueOnce(new Error('恢复失败'))
    client.blog.listTrashComments.mockResolvedValue(paged([makeComment({ id: 9 })]))
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByRole('button', { name: '恢复' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '恢复' }))
    await waitFor(() => expect(screen.getAllByText('恢复失败').length).toBeGreaterThan(0))

    client.blog.permanentlyDeleteComment.mockRejectedValueOnce(new Error('删不掉'))
    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(screen.getAllByText('删不掉').length).toBeGreaterThan(0))
    void container
  })

  it('CommentsAdmin：全部 tab 空列表 Empty', async () => {
    client.blog.listAllComments.mockResolvedValue(paged([], 1, 1, 0))
    renderPlain(<CommentsAdmin />)
    await waitFor(() => expect(screen.getByText('当前列表没有评论')).toBeTruthy())
  })

  it('UsersAdmin：昵称与邮箱校验拦截', async () => {
    client.blog.listUsers.mockResolvedValue(paged([makeUser({ id: 7, username: 'alice', nickname: '爱丽丝', email: 'a@x.com' })]))
    const { container } = renderPlain(<UsersAdmin />)
    await waitFor(() => expect(screen.getByText('爱丽丝')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '编辑' }))
    const inputs = Array.from(container.querySelectorAll('.uedit input')) as HTMLInputElement[]

    fireEvent.change(inputs[1], { target: { value: '   ' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getAllByText(/昵称/).length).toBeGreaterThan(0))

    fireEvent.change(inputs[1], { target: { value: '爱丽丝' } })
    fireEvent.change(inputs[2], { target: { value: 'not-an-email' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getAllByText(/邮箱/).length).toBeGreaterThan(0))
    expect(client.blog.updateUser).not.toHaveBeenCalled()
  })

  it('TagsAdmin：重命名失败 toast', async () => {
    client.blog.listTags.mockResolvedValue(paged([makeTag({ id: 1, name: 'Go' })]))
    renderPlain(<TagsAdmin />)
    await waitFor(() => expect(screen.getByText('Go')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '编辑' }))
    client.blog.renameTag.mockRejectedValueOnce(new Error('改名失败'))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getAllByText('改名失败').length).toBeGreaterThan(0))
  })

  it('MediaAdmin：加载失败 toast + 空态', async () => {
    client.blog.listMedia.mockRejectedValue(new Error('媒体挂了'))
    renderPlain(<MediaAdmin />)
    await waitFor(() => expect(screen.getAllByText('媒体挂了').length).toBeGreaterThan(0)) // 普通 Error 原样展示
    await waitFor(() => expect(screen.getByText('媒体库为空')).toBeTruthy())
  })

  it('MediaAdmin：删除失败 toast', async () => {
    client.blog.listMedia.mockResolvedValue(paged([makeMedia({ id: 1 })]))
    renderPlain(<MediaAdmin />)
    await waitFor(() => expect(screen.getByTitle('删除')).toBeTruthy())
    client.blog.deleteMedia.mockRejectedValueOnce(new Error('删除失败'))
    fireEvent.click(screen.getByTitle('删除'))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(screen.getAllByText('删除失败').length).toBeGreaterThan(0))
  })
})
