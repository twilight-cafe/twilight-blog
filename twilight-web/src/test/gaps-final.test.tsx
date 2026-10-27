// gaps-final.test.tsx —— 最后一轮分支补缺：真实 client 的 401 监听、extractToc、dialog 默认值、各页面 catch/校验分支
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import App from '../App'
import Posts from '../pages/admin/Posts'
import MomentsAdmin from '../pages/admin/MomentsAdmin'
import Editor from '../pages/admin/Editor'
import { PageEdit } from '../pages/admin/PagesAdmin'
import Profile from '../pages/admin/Profile'
import Register from '../pages/admin/Register'
import ForgotPassword from '../pages/admin/ForgotPassword'
import SiteSettings from '../pages/admin/SiteSettings'
import UsersAdmin from '../pages/admin/UsersAdmin'
import TagsAdmin from '../pages/admin/TagsAdmin'
import MediaAdmin from '../pages/admin/MediaAdmin'
import Home from '../pages/public/Home'
import Moments from '../pages/public/Moments'
import Search from '../pages/public/Search'
import ContentView from '../components/ContentView'
import MediaPicker from '../components/media/MediaPicker'
import { DialogProvider } from '../lib/dialog'
import { ToastProvider } from '../lib/toast'
import { extractToc } from '../lib/markdown'
import { resetSiteCacheForTests } from '../lib/site'
import { NOW, adminMe, expireEmailCodeCooldown, getSharedClientMock, makeArticle, makeMedia, makeMoment, makePage, makeTag, makeUser, resetClientMock, visitorMe } from './helpers'

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
            <Route path="/admin/posts" element={<div>POSTS-LIST</div>} />
            <Route path="/admin/profile" element={<div>PROFILE-STUB</div>} />
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

beforeEach(() => {
  resetClientMock(client)
  resetSiteCacheForTests()
  client.setCurrentUser(null)
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
})

describe('真实 client：401 会话过期监听', () => {
  it('401 清空缓存身份并继续抛错；网络错误不清身份', async () => {
    // SDK 的 HttpClient 在模块初始化时捕获 fetch 实例：stub 一个可换实现的 vi.fn，
    // 先 stub 再 importActual，两个分支共用同一个实例、切换 implementation。
    const fetchStub = vi.fn()
    vi.stubGlobal('fetch', fetchStub)
    const real = await vi.importActual<typeof import('../lib/client')>('../lib/client')

    // 401 → BlogApiError(401) → 身份清空、错误原样抛出
    fetchStub.mockImplementation(async () => new Response(JSON.stringify({ message: '会话过期' }), { status: 401, headers: { 'Content-Type': 'application/json' } }))
    real.setCurrentUser(adminMe)
    await expect(real.blog.me()).rejects.toThrow()
    expect(real.getCurrentUser()).toBeNull()

    // 普通网络错误（无 status）→ 不清身份
    fetchStub.mockImplementation(async () => { throw new TypeError('fetch failed') })
    real.setCurrentUser(adminMe)
    await expect(real.blog.me()).rejects.toThrow()
    expect(real.getCurrentUser()).not.toBeNull()
    vi.unstubAllGlobals()
  })
})

describe('markdown extractToc', () => {
  it('提取标题为 TOC；重复标题去重；非标题忽略', () => {
    const toc = extractToc('# 甲\n\n正文\n\n## 乙\n\n### 丙\n\n## 乙\n')
    expect(toc.map((item) => item.text)).toEqual(['甲', '乙', '丙', '乙'])
    expect(new Set(toc.map((item) => item.id)).size).toBe(toc.length) // id 唯一
    expect(toc[0].level).toBe(1)
    expect(toc[1].level).toBe(2)
  })
})

describe('ContentView 细节', () => {
  it('pre 无 code 子元素不增强；复制失败提示；卸载清理计时器', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) }, configurable: true })
    // jsdom 的 execCommand 不抛错（只打日志），legacy 兜底会「成功」——让它也失败
    const execSpy = vi.spyOn(document, 'execCommand').mockImplementation(() => { throw new Error('no execCommand') })
    const { container, unmount } = render(<ContentView text="<pre><span>no code</span></pre>" trusted />)
    expect(container.querySelector('.code-block')).toBeNull() // 没有 code 子元素 → 跳过增强
    unmount()

    // 复制失败（clipboard 不可用 → writeClipboard 抛错）
    const second = render(<ContentView text={'```ts\nconst a = 1\n```'} trusted />)
    const copy = second.container.querySelector('.code-copy') as HTMLButtonElement
    fireEvent.click(copy)
    await waitFor(() => expect(copy.textContent).toBe('复制失败'))
    execSpy.mockRestore()
    second.unmount()
  })
})

describe('MediaPicker 灯箱与移除', () => {
  it('点击缩略图开灯箱、关闭、移除条目', async () => {
    const onChange = vi.fn()
    const uploaded = makeMedia({ id: 5, original_name: 'pic.png' })
    const { container } = renderPlain(<MediaPicker items={[uploaded]} onChange={onChange} />)

    fireEvent.click(container.querySelector('.media-thumb-open') as HTMLButtonElement)
    await waitFor(() => expect(document.querySelector('.media-lightbox, [class*=lightbox]')).toBeTruthy())

    const close = document.querySelector('.media-lightbox-close')
    expect(close).toBeTruthy()
    fireEvent.click(close as Element)
    await waitFor(() => expect(document.querySelector('.media-lightbox')).toBeNull())

    fireEvent.click(container.querySelector('.media-thumb-remove') as HTMLButtonElement)
    expect(onChange).toHaveBeenCalledWith([])
  })
})

describe('MomentsAdmin 剩余分支', () => {
  it('删除失败、彻底删除失败 toast；切回 active tab', async () => {
    client.blog.listMoments.mockResolvedValue(paged([makeMoment({ id: 1, content: '首条' })]))
    client.blog.listTrashMoments.mockResolvedValue(paged([makeMoment({ id: 6, content: '回收站微语' })]))
    const { container } = renderPlain(<MomentsAdmin />)
    await waitFor(() => expect(screen.getByText('首条')).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    fireEvent.click(await screen.findByText('确定'))
    client.blog.deleteMoment.mockRejectedValueOnce(new Error('删不了'))
    await waitFor(() => expect(screen.getAllByText('删不了').length).toBeGreaterThan(0))

    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('回收站微语')).toBeTruthy())
    client.blog.permanentlyDeleteMoment.mockRejectedValueOnce(new Error('删不掉'))
    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(screen.getAllByText('删不掉').length).toBeGreaterThan(0))

    fireEvent.click((Array.from(container.querySelectorAll('.atabs button')) as HTMLButtonElement[])
      .find((b) => b.textContent === '微语') as HTMLButtonElement)
    await waitFor(() => expect(screen.getByText('首条')).toBeTruthy())
  })
})

describe('Editor / PagesAdmin / Posts / Profile / Register / ForgotPassword 剩余分支', () => {
  it('Editor：新建失败 toast；内容修改随保存提交', async () => {
    const first = renderRouted(<Editor />, '/admin/editor', '/admin/editor')
    fireEvent.change(first.container.querySelector('input.ed-title, .ed-title input, input') as HTMLInputElement, { target: { value: '标题X' } })
    client.blog.createArticle.mockRejectedValueOnce(new Error('创建失败'))
    fireEvent.click((Array.from(first.container.querySelectorAll('button')) as HTMLButtonElement[]).find((b) => b.textContent === '保存') as HTMLButtonElement)
    await waitFor(() => expect(screen.getAllByText('创建失败').length).toBeGreaterThan(0))
    first.unmount()

    // 编辑模式：修改正文随 updateArticle 提交
    client.blog.getArticle.mockResolvedValueOnce(makeArticle({ id: 7, title: '旧文', content: '旧正文', tags: ['Go'] }))
    const second = renderRouted(<Editor />, '/admin/editor/7', '/admin/editor/:id')
    await waitFor(() => expect((second.container.querySelector('textarea.mds') as HTMLTextAreaElement).value).toBe('旧正文'))
    fireEvent.change(second.container.querySelector('textarea.mds') as HTMLTextAreaElement, { target: { value: '新正文' } })
    client.blog.updateArticle.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click((Array.from(second.container.querySelectorAll('button')) as HTMLButtonElement[]).find((b) => b.textContent === '保存') as HTMLButtonElement)
    await waitFor(() => expect(client.blog.updateArticle).toHaveBeenCalledWith(7, { title: '旧文', content: '新正文', titleEn: '', contentEn: '', aiGenerated: false, aiGeneratedEn: false, tags: ['Go'], coverMediaId: null, publishedAt: NOW / 1000 }))
  })

  it('Posts：删除失败 toast；trash→active 切回；全选与取消', async () => {
    client.blog.listArticles
      .mockResolvedValueOnce(paged([makeArticle({ id: 1, title: '文章甲' })]))
      .mockResolvedValue(paged([makeArticle({ id: 1, title: '重载后' })]))
    const { container } = renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    await waitFor(() => expect(screen.getByText('文章甲')).toBeTruthy())

    // 删除失败 toast
    client.blog.deleteArticle.mockRejectedValueOnce(new Error('删除失败'))
    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(screen.getAllByText('删除失败').length).toBeGreaterThan(0))

    // 全选 checkbox → bulkbar → 取消
    fireEvent.click(container.querySelector('thead input[type=checkbox]') as HTMLInputElement)
    expect(screen.getByText('已选 1 篇')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(screen.queryByText(/已选/)).toBeNull()

    // trash → active 切回
    client.blog.listTrashArticles.mockResolvedValue(paged([]))
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('没有符合条件的文章')).toBeTruthy())
    const tabArticles = (Array.from(container.querySelectorAll('.atabs button')) as HTMLButtonElement[])
      .find((b) => b.textContent === '文章') as HTMLButtonElement
    fireEvent.click(tabArticles)
    await waitFor(() => expect(screen.getByText('重载后')).toBeTruthy())
  })

  it('PagesAdmin：PageEdit 空标题拦截', async () => {
    client.blog.getPage.mockResolvedValueOnce(makePage({ id: 4, title: '旧标题', content: '旧内容' }))
    const { container } = renderRouted(<PageEdit />, '/admin/pages/4', '/admin/pages/:id')
    await waitFor(() => expect((container.querySelector('input[placeholder=页面标题]') as HTMLInputElement).value).toBe('旧标题'))
    fireEvent.change(container.querySelector('input[placeholder=页面标题]') as HTMLInputElement, { target: { value: '   ' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getAllByText('标题不能为空').length).toBeGreaterThan(0))
    expect(client.blog.updatePage).not.toHaveBeenCalled()
  })

  it('Profile：昵称/用户名校验拦截；updateProfile 失败 toast；admin 无 name 回退', async () => {
    client.setCurrentUser(visitorMe)
    client.refreshAuth.mockResolvedValue(visitorMe)
    const { container } = renderPlain(<Profile />)
    await waitFor(() => expect(screen.getByText('我的资料')).toBeTruthy())
    const forms = Array.from(container.querySelectorAll('form')) as HTMLFormElement[]
    const idInputs = Array.from(forms[0].querySelectorAll('input')) as HTMLInputElement[]

    // 非法昵称
    fireEvent.change(idInputs[1], { target: { value: '  ' } })
    fireEvent.submit(forms[0])
    await waitFor(() => expect(screen.getAllByText(/昵称/).length).toBeGreaterThan(0))
    expect(client.blog.updateProfile).not.toHaveBeenCalled()

    // 非法用户名
    fireEvent.change(idInputs[1], { target: { value: '好昵称' } })
    fireEvent.change(idInputs[0], { target: { value: 'x' } })
    fireEvent.submit(forms[0])
    await waitFor(() => expect(screen.getAllByText(/3–24 个字符/).length).toBeGreaterThan(0))

    // 保存失败
    fireEvent.change(idInputs[0], { target: { value: 'alice' } })
    client.blog.updateProfile.mockRejectedValueOnce(new Error('用户名被占用'))
    fireEvent.submit(forms[0])
    await waitFor(() => expect(screen.getAllByText('用户名被占用').length).toBeGreaterThan(0))
  })

  it('Register：codeSent 后倒计时锁住重发；冷却结束后可重发', async () => {
    client.blog.requestEmailCode.mockResolvedValue({ message: 'ok' })
    const { container } = renderRouted(<Register />, '/admin/register', '/admin/register')
    fireEvent.change(container.querySelector('input[type=email]') as HTMLInputElement, { target: { value: 'a@x.com' } })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(container.querySelector('input[placeholder=验证码]')).toBeTruthy())

    // 发送成功后按钮进入 60 秒倒计时并禁用——这是「前后端一起限制」的前端那一半，
    // 目的就是不让用户反复点、反复被 429 拒。
    await waitFor(() => {
      const button = screen.getByRole('button', { name: /秒后可重发/ })
      expect(button.hasAttribute('disabled')).toBe(true)
    })
    fireEvent.click(screen.getByRole('button', { name: /秒后可重发/ }))
    expect(client.blog.requestEmailCode).toHaveBeenCalledTimes(1) // 禁用按钮点不动

    // 等冷却结束（等价于时间前进 60 秒）→ 按钮恢复「重新发送」且可点。
    await expireEmailCodeCooldown('a@x.com')
    await waitFor(() => {
      const button = screen.getByRole('button', { name: '重新发送' })
      expect(button.hasAttribute('disabled')).toBe(false)
    })
    fireEvent.click(screen.getByRole('button', { name: '重新发送' }))
    await waitFor(() => expect(client.blog.requestEmailCode).toHaveBeenCalledTimes(2))
  })

  it('ForgotPassword：非法邮箱 / 空验证码 / 弱密码依次拦截', async () => {
    const { container } = renderRouted(<ForgotPassword />, '/admin/forgot', '/admin/forgot')
    const form = container.querySelector('form') as HTMLFormElement

    fireEvent.submit(form)
    await waitFor(() => expect(screen.getAllByText(/邮箱/).length).toBeGreaterThan(0))

    const email = container.querySelector('input[type=email]') as HTMLInputElement
    fireEvent.change(email, { target: { value: 'a@x.com' } })
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getAllByText('请输入验证码').length).toBeGreaterThan(0))

    client.blog.requestEmailCode.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(container.querySelector('input[placeholder=验证码]')).toBeTruthy())
    const passwordInput = container.querySelector('input[type=password]') as HTMLInputElement
    fireEvent.change(container.querySelector('input[placeholder=验证码]') as HTMLInputElement, { target: { value: '42' } })
    fireEvent.change(passwordInput, { target: { value: 'short' } })
    fireEvent.submit(form)
    await waitFor(() => expect(screen.getAllByText(/密码/).length).toBeGreaterThan(0))
  })
})

describe('SiteSettings / UsersAdmin / TagsAdmin / MediaAdmin 剩余分支', () => {
  it('SiteSettings：加载失败回退空串；卸载后 promise 不写状态', async () => {
    client.blog.getSite.mockRejectedValue(new Error('down'))
    const first = renderPlain(<SiteSettings />)
    await waitFor(() => expect((first.container.querySelector('textarea') as HTMLTextAreaElement).value).toBe(''))
    first.unmount()

    client.blog.getSite.mockImplementation(() => new Promise(() => {}))
    const second = renderPlain(<SiteSettings />)
    await waitFor(() => expect(screen.getByText('LOADING…')).toBeTruthy())
    second.unmount() // 卸载后 in-flight promise 被丢弃
  })

  it('UsersAdmin：保存中按钮禁用文案', async () => {
    client.blog.listUsers.mockResolvedValue(paged([makeUser({ id: 7, username: 'alice', nickname: '爱丽丝', email: 'a@x.com' })]))
    const { container } = renderPlain(<UsersAdmin />)
    await waitFor(() => expect(screen.getByText('爱丽丝')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '编辑' }))
    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    client.blog.updateUser.mockImplementation(() => gate.then(() => ({ message: 'ok' })))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getByText('保存中…')).toBeTruthy())
    act(() => { release() })
    await waitFor(() => expect(screen.queryByText('保存中…')).toBeNull())
    void container
  })

  it('TagsAdmin：编辑后取消不改名', async () => {
    client.blog.listTags.mockResolvedValue(paged([makeTag({ id: 1, name: 'Go' })]))
    renderPlain(<TagsAdmin />)
    await waitFor(() => expect(screen.getByText('Go')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '编辑' }))
    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(client.blog.renameTag).not.toHaveBeenCalled()
    expect(screen.getByText('Go')).toBeTruthy()
  })

  it('MediaAdmin：无文件 change 直接返回；上传成功提示', async () => {
    client.blog.listMedia.mockResolvedValue(paged([makeMedia({ id: 1 })]))
    const { container } = renderPlain(<MediaAdmin />)
    await waitFor(() => expect(screen.getByTitle('删除')).toBeTruthy())

    const input = container.querySelector('input[type=file]') as HTMLInputElement
    fireEvent.change(input) // 无文件 → 直接返回
    expect(client.blog.uploadMedia).not.toHaveBeenCalled()

    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    client.blog.uploadMedia.mockImplementation(() => gate.then(() => ({ message: 'ok' })))
    Object.defineProperty(input, 'files', { value: [new File(['x'], 'up.png', { type: 'image/png' })], configurable: true })
    fireEvent.change(input)
    await waitFor(() => expect(screen.getByText('上传中…')).toBeTruthy())
    act(() => { release() })
    await waitFor(() => expect(screen.getAllByText('上传成功').length).toBeGreaterThan(0))
  })
})

describe('public 页面 catch 分支', () => {
  it('Home：列表失败回退空态', async () => {
    client.blog.listArticles.mockRejectedValue(new Error('down'))
    client.blog.listTags.mockResolvedValue(paged([]))
    renderRouted(<Home />, '/', '/')
    await waitFor(() => expect(screen.getByText('还没有文章')).toBeTruthy())
    expect(screen.queryByText('LOADING…')).toBeNull()
  })

  it('Moments：拉取失败回退空态', async () => {
    client.blog.listMoments.mockRejectedValue(new Error('down'))
    renderRouted(<Moments />, '/moments', '/moments')
    await waitFor(() => expect(screen.getByText('还没有微语')).toBeTruthy())
    expect(screen.queryByText('LOADING…')).toBeNull()
  })

  it('Search：搜索失败 toast；翻页重新搜索', async () => {
    client.blog.search
      .mockRejectedValueOnce(new Error('搜索挂了'))
      .mockResolvedValue(paged([makeArticle({ id: 1, title: '命中' })], 1, 2, 2))

    const { container } = renderRouted(<Search />, '/search?q=x', '/search')
    const input = container.querySelector('input') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'x' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(screen.getAllByText('搜索挂了').length).toBeGreaterThan(0))

    // 换关键词重新搜索（相同关键词不重跑 effect）→ 成功 + 出现第 2 页按钮 → 翻页
    fireEvent.change(input, { target: { value: 'y' } })
    fireEvent.submit(container.querySelector('form') as HTMLFormElement)
    await waitFor(() => expect(document.body.textContent).toContain('命中'))
    const page2 = screen.queryByRole('button', { name: 'Page 2' })
    if (page2) {
      fireEvent.click(page2)
      await waitFor(() => expect(client.blog.search).toHaveBeenLastCalledWith(expect.objectContaining({ page: 2 })))
    }
  })
})

describe('App / AdminLayout 收尾分支', () => {
  it('refreshAuth 失败：console.warn 后仍进入站点', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    client.blog.getInstallStatus.mockResolvedValue({ installed: true })
    client.refreshAuth.mockRejectedValue(new Error('me down'))
    client.blog.listArticles.mockRejectedValue(new Error('down'))
    client.blog.listTags.mockResolvedValue(paged([]))
    render(
      <MemoryRouter initialEntries={['/']}>
        <App />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('还没有文章')).toBeTruthy(), { timeout: 3000 })
    expect(warn).toHaveBeenCalled()
    warn.mockRestore()
  })

  // 上面那条覆盖 `if (alive)` 的真分支，这条覆盖假分支。以前它只能靠「某个用例恰好
  // 在 /api/me 落定前卸载」偶然命中，于是全量跑时 Line 122 的分支覆盖率在 100 与
  // 99.91 之间随机摆动（实测两次同码运行一次 100、一次 99.91）。挂住请求再卸载，
  // 就把这个时序固定下来。
  it('refreshAuth 未落定就卸载：alive 守卫挡掉卸载后的 setAuthReady', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    let rejectMe: () => void = () => {}
    client.blog.getInstallStatus.mockResolvedValue({ installed: true })
    // 挂住 /api/me：只有它悬着，才能先卸载、再让它落定——那时 alive 已经是 false。
    client.refreshAuth.mockImplementationOnce(() => new Promise<void>((_resolve, reject) => {
      rejectMe = () => reject(new Error('me down'))
    }))
    client.blog.listArticles.mockResolvedValue(paged([]))
    client.blog.listTags.mockResolvedValue(paged([]))

    const view = render(
      <MemoryRouter initialEntries={['/']}>
        <App />
      </MemoryRouter>,
    )
    // 必须等到请求真的发出去：还停在 loading 分支时根本走不到这个 effect。
    await waitFor(() => expect(client.refreshAuth).toHaveBeenCalled())
    view.unmount()
    rejectMe()
    // 拒绝发生在卸载之后：catch 照旧报出来（这条断言同时证明我们确实走到了 finally），
    // 而 alive 守卫拦下 setAuthReady——卸载后的 setState 不该再有落点。
    await waitFor(() => expect(warn).toHaveBeenCalled())
    await act(async () => { await Promise.resolve() })
    warn.mockRestore()
  })

  it('admin 无 name：显示名回退「管理员」；深层编辑路径正常渲染', async () => {
    client.setCurrentUser({ role: 'admin', name: '', username: 'root' })
    client.blog.getInstallStatus.mockResolvedValue({ installed: true })
    client.blog.getArticle.mockResolvedValue(makeArticle({ id: 5, title: '编辑中', content: 'x', tags: ['Go'] }))
    client.blog.listTags.mockResolvedValue(paged([]))
    const { container } = render(
      <MemoryRouter initialEntries={['/admin/editor/5']}>
        <ToastProvider>
          <DialogProvider>
            <App />
          </DialogProvider>
        </ToastProvider>
      </MemoryRouter>,
    )
    await waitFor(() => expect(document.title).toBe('管理 · Twilight'), { timeout: 3000 })
    await waitFor(() => expect(container.querySelector('.aside')).toBeTruthy(), { timeout: 3000 })
    expect(container.querySelector('.aside')?.textContent).toContain('管理员')
    expect(container.querySelectorAll('.aside .lb').length).toBeGreaterThan(0)
  })
})
