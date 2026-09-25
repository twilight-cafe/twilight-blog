// admin-auth.test.tsx —— Setup / SignIn / Register / ForgotPassword
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import Setup from '../pages/Setup'
import SignIn from '../pages/admin/SignIn'
import Register from '../pages/admin/Register'
import ForgotPassword from '../pages/admin/ForgotPassword'
import { ToastProvider } from '../lib/toast'
import { BlogApiError } from 'twilight-blog-api'
import { adminMe, expireEmailCodeCooldown, getSharedClientMock, resetClientMock, visitorMe } from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

const VALID_PASSWORD = 'abc12345' // ≥8 位、含字母与数字

function renderAuth(ui: React.ReactElement, route: string, pattern: string) {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <ToastProvider>
        <Routes>
          <Route path={pattern} element={ui} />
          <Route path="/admin" element={<div>ADMIN-HOME</div>} />
          <Route path="/admin/profile" element={<div>PROFILE-HOME</div>} />
          <Route path="/admin/login" element={<div>LOGIN-HOME</div>} />
        </Routes>
      </ToastProvider>
    </MemoryRouter>,
  )
}

/** ink-design Input 不做 label 关联，用选择器取输入框。 */
function inputAt(container: HTMLElement, selector: string): HTMLInputElement {
  return container.querySelector(selector) as HTMLInputElement
}

function formOf(container: HTMLElement): HTMLFormElement {
  return container.querySelector('form') as HTMLFormElement
}

beforeEach(() => {
  resetClientMock(client) // 连 once 队列一起清，防跨用例泄漏
  client.setCurrentUser(null)
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
})

describe('Setup', () => {
  it('弱密码被拦截，不发起安装', () => {
    const { container } = renderAuth(<Setup />, '/install', '/install')
    const password = inputAt(container, 'input[type=password]')
    fireEvent.change(password, { target: { value: 'short' } })
    fireEvent.submit(formOf(container))
    expect(screen.getAllByText(/至少 8 个字符/).length).toBeGreaterThan(0)
    expect(client.blog.install).not.toHaveBeenCalled()
  })

  it('安装成功 → 登录 → 进后台；installed=false 视为未完成', async () => {
    client.blog.install.mockResolvedValueOnce({ installed: true })
    client.blog.login.mockResolvedValueOnce(adminMe)
    const first = renderAuth(<Setup onInstalled={() => {}} />, '/install', '/install')
    const password = inputAt(first.container, 'input[type=password]')
    fireEvent.change(password, { target: { value: VALID_PASSWORD } })
    fireEvent.submit(formOf(first.container))
    await waitFor(() => expect(screen.getByText('ADMIN-HOME')).toBeTruthy())
    expect(client.blog.install).toHaveBeenCalledWith({ password: VALID_PASSWORD })
    expect(client.blog.login).toHaveBeenCalledWith({ username: 'admin', password: VALID_PASSWORD })
    first.unmount()

    // installed=false：提示未完成，不登录
    client.blog.install.mockResolvedValueOnce({ installed: false })
    const second = renderAuth(<Setup />, '/install', '/install')
    fireEvent.change(inputAt(second.container, 'input[type=password]'), { target: { value: VALID_PASSWORD } })
    fireEvent.submit(formOf(second.container))
    await waitFor(() => expect(screen.getAllByText('安装未完成，请稍后重试').length).toBeGreaterThan(0))
    expect(client.blog.login).toHaveBeenCalledTimes(1) // 只有第一次
  })

  it('登录失败 ≠ 安装失败：只提示错误信息', async () => {
    client.blog.install.mockResolvedValueOnce({ installed: true })
    client.blog.login.mockRejectedValueOnce(new Error('密码不对'))
    const { container } = renderAuth(<Setup />, '/install', '/install')
    fireEvent.change(inputAt(container, 'input[type=password]'), { target: { value: VALID_PASSWORD } })
    fireEvent.submit(formOf(container))
    await waitFor(() => expect(screen.getAllByText('密码不对').length).toBeGreaterThan(0))
    expect(screen.queryByText('ADMIN-HOME')).toBeNull()
  })
})

describe('SignIn', () => {
  it('空字段拦截；成功后管理员进后台、访客进资料页', async () => {
    const first = renderAuth(<SignIn />, '/admin/login', '/admin/login')
    const username = inputAt(first.container, 'input[autocomplete=username]')
    const password = inputAt(first.container, 'input[type=password]')
    const form = formOf(first.container)

    fireEvent.submit(form)
    await waitFor(() => expect(screen.getAllByText('请输入用户名（或注册邮箱）').length).toBeGreaterThan(0))

    fireEvent.change(username, { target: { value: 'admin' } })
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getAllByText('请输入密码').length).toBeGreaterThan(0))

    client.blog.login.mockResolvedValueOnce(adminMe)
    fireEvent.change(password, { target: { value: 'pw123456' } })
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getByText('ADMIN-HOME')).toBeTruthy())
    expect(client.blog.login).toHaveBeenCalledWith({ username: 'admin', password: 'pw123456' })
    first.unmount()

    // 访客 → /admin/profile（上一步已把身份缓存成 admin，先清掉）
    client.setCurrentUser(null)
    client.blog.login.mockResolvedValueOnce(visitorMe)
    const second = renderAuth(<SignIn />, '/admin/login', '/admin/login')
    fireEvent.change(inputAt(second.container, 'input[autocomplete=username]'), { target: { value: 'alice' } })
    fireEvent.change(inputAt(second.container, 'input[type=password]'), { target: { value: 'pw123456' } })
    fireEvent.submit(formOf(second.container))
    await waitFor(() => expect(screen.getByText('PROFILE-HOME')).toBeTruthy())
  })

  it('登录失败 toast；已登录访问直接跳走', async () => {
    client.blog.login.mockRejectedValueOnce(new Error('限流'))
    const first = renderAuth(<SignIn />, '/admin/login', '/admin/login')
    fireEvent.change(inputAt(first.container, 'input[autocomplete=username]'), { target: { value: 'admin' } })
    fireEvent.change(inputAt(first.container, 'input[type=password]'), { target: { value: 'x' } })
    fireEvent.submit(formOf(first.container))
    await waitFor(() => expect(screen.getAllByText('限流').length).toBeGreaterThan(0))
    first.unmount()

    // 已登录 → 重定向
    client.setCurrentUser(adminMe)
    const second = renderAuth(<SignIn />, '/admin/login', '/admin/login')
    await waitFor(() => expect(screen.getByText('ADMIN-HOME')).toBeTruthy())
    second.unmount()
  })
})

describe('Register', () => {
  function fillForm(container: HTMLElement, values: Record<string, string>) {
    if ('email' in values) fireEvent.change(inputAt(container, 'input[type=email]'), { target: { value: values.email } })
    if ('code' in values) fireEvent.change(inputAt(container, 'input[placeholder=验证码]'), { target: { value: values.code } })
    if ('password' in values) fireEvent.change(inputAt(container, 'input[type=password]'), { target: { value: values.password } })
    if ('username' in values) fireEvent.change(inputAt(container, 'input[placeholder="今后登录用，不可与昵称混用"]'), { target: { value: values.username } })
    if ('nickname' in values) fireEvent.change(inputAt(container, 'input[placeholder="评论等场合对外展示"]'), { target: { value: values.nickname } })
  }

  it('发码：邮箱非法拦截；成功后切换到验证码输入', async () => {
    const { container } = renderAuth(<Register />, '/admin/register', '/admin/register')
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(screen.getAllByText('请输入正确的邮箱').length).toBeGreaterThan(0))

    fillForm(container, { email: 'a@example.com' })
    client.blog.requestEmailCode.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(inputAt(container, 'input[placeholder=验证码]')).toBeTruthy())
    expect(client.blog.requestEmailCode).toHaveBeenCalledWith({ email: 'a@example.com' })
  })

  it('服务端 429 时按钮也进入冷却（前端节奏与服务端记录重新对齐）', async () => {
    const { container } = renderAuth(<Register />, '/admin/register', '/admin/register')
    fillForm(container, { email: 'a@example.com' })
    // 必须等 email 提交进 state 后再点：否则 requestCode 读到的是空邮箱，
    // 会在本地校验就拦下来（收不到 429 分支）。
    await waitFor(() => expect(inputAt(container, 'input[type=email]').value).toBe('a@example.com'))

    // 服务端说「太频繁」——此时**不能**把按钮留在可点状态，否则用户会继续点、
    // 继续被拒。前端必须自己数一个周期，与后端的判定对齐。
    // 必须是真正的 BlogApiError 实例：isRateLimited 用 instanceof 收窄，
    // 一个恰好带 status 的普通对象会被判成 false（这正是那个类型守卫的价值）。
    const tooFrequent = new BlogApiError('验证码发送过于频繁，请 60 秒后再试。', {
      status: 429, method: 'POST', path: '/api/auth/email-code',
    })
    client.blog.requestEmailCode.mockRejectedValueOnce(tooFrequent)
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => {
      const button = screen.getByRole('button', { name: /秒后可重发/ })
      expect(button.hasAttribute('disabled')).toBe(true)
    })
    // 429 不改变 codeSent：验证码输入框不该因为一次被拒就出现。
    expect(inputAt(container, 'input[placeholder=验证码]')).toBeNull()

    // 非 429 的失败（网络抖动）**不**启动冷却：宁可让用户能重试，
    // 也不要因为一次断网把按钮锁住 60 秒却什么都没发出去。
    await expireEmailCodeCooldown('a@example.com')
    await waitFor(() => {
      const button = screen.getByRole('button', { name: '发送验证码' })
      expect(button.hasAttribute('disabled')).toBe(false)
    })
    client.blog.requestEmailCode.mockRejectedValueOnce(new Error('网络错误'))
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(screen.getAllByText('网络错误').length).toBeGreaterThan(0))
    await waitFor(() => {
      const button = screen.getByRole('button', { name: '发送验证码' })
      expect(button.hasAttribute('disabled')).toBe(false)
    })
  })

  it('逐项校验 → 成功注册自动登录进资料页', async () => {
    client.blog.requestEmailCode.mockResolvedValue({ message: 'ok' })
    const { container } = renderAuth(<Register />, '/admin/register', '/admin/register')
    fillForm(container, { email: 'a@example.com' })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(inputAt(container, 'input[placeholder=验证码]')).toBeTruthy())

    const form = formOf(container)
    const submit = () => fireEvent.submit(form)

    fillForm(container, { code: '' })
    submit()
    await waitFor(() => expect(screen.getAllByText('请输入验证码').length).toBeGreaterThan(0))

    fillForm(container, { code: '123456', password: 'weak' })
    submit()
    await waitFor(() => expect(screen.getAllByText(/至少 8 个字符/).length).toBeGreaterThan(0))

    fillForm(container, { password: VALID_PASSWORD, username: '1bad' })
    submit()
    await waitFor(() => expect(screen.getAllByText(/以字母开头/).length).toBeGreaterThan(0))

    fillForm(container, { username: 'alice', nickname: '' })
    submit()
    await waitFor(() => expect(screen.getAllByText('请输入昵称').length).toBeGreaterThan(0))

    fillForm(container, { nickname: '爱丽丝' })
    client.blog.register.mockResolvedValueOnce(visitorMe)
    submit()
    await waitFor(() => expect(screen.getByText('PROFILE-HOME')).toBeTruthy())
    expect(client.blog.register).toHaveBeenCalledWith({
      email: 'a@example.com', code: '123456', username: 'alice', password: VALID_PASSWORD, nickname: '爱丽丝',
    })
  })

  it('注册失败 toast', async () => {
    client.blog.requestEmailCode.mockResolvedValue({ message: 'ok' })
    const { container } = renderAuth(<Register />, '/admin/register', '/admin/register')
    fillForm(container, { email: 'a@example.com' })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(inputAt(container, 'input[placeholder=验证码]')).toBeTruthy())
    fillForm(container, { code: '1', password: VALID_PASSWORD, username: 'alice', nickname: '爱丽丝' })
    client.blog.register.mockRejectedValueOnce(new Error('验证码过期'))
    fireEvent.submit(formOf(container))
    await waitFor(() => expect(screen.getAllByText('验证码过期').length).toBeGreaterThan(0))
  })
})

describe('ForgotPassword', () => {
  it('发码 → 校验 → 重置成功后跳登录页', async () => {
    const { container } = renderAuth(<ForgotPassword />, '/admin/forgot', '/admin/forgot')
    await fillEmailAndCode(container, { code: null })
    const form = formOf(container)
    const submit = () => fireEvent.submit(form)

    fireEvent.change(inputAt(container, 'input[placeholder=验证码]'), { target: { value: '   ' } })
    submit()
    await waitFor(() => expect(screen.getAllByText('请输入验证码').length).toBeGreaterThan(0))

    fireEvent.change(inputAt(container, 'input[placeholder=验证码]'), { target: { value: '42' } })
    fireEvent.change(inputAt(container, 'input[type=password]'), { target: { value: VALID_PASSWORD } })
    client.blog.resetPassword.mockResolvedValueOnce({ message: 'ok' })
    submit()
    await waitFor(() => expect(screen.getByText('LOGIN-HOME')).toBeTruthy())
    expect(client.blog.resetPassword).toHaveBeenCalledWith({ email: 'a@example.com', code: '42', password: VALID_PASSWORD })
  })

  it('重置失败 toast', async () => {
    const { container } = renderAuth(<ForgotPassword />, '/admin/forgot', '/admin/forgot')
    await fillEmailAndCode(container, { code: '42' })
    fireEvent.change(inputAt(container, 'input[type=password]'), { target: { value: VALID_PASSWORD } })
    client.blog.resetPassword.mockRejectedValueOnce(new Error('验证码错误'))
    fireEvent.submit(formOf(container))
    await waitFor(() => expect(screen.getAllByText('验证码错误').length).toBeGreaterThan(0))
  })

  function fillEmailAndCode(container: HTMLElement, { code }: { code: string | null }) {
    fireEvent.change(inputAt(container, 'input[type=email]'), { target: { value: 'a@example.com' } })
    client.blog.requestEmailCode.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    return waitFor(() => expect(inputAt(container, 'input[placeholder=验证码]')).toBeTruthy()).then(() => {
      if (code !== null) fireEvent.change(inputAt(container, 'input[placeholder=验证码]'), { target: { value: code } })
    })
  }
})
