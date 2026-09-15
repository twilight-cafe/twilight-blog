// app-shell.test.tsx —— App 状态机 + 路由守卫 + 三个壳层（PublicShell/PublicSidebar/AdminLayout）+ main 入口 + node shim
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useOutletContext } from 'react-router-dom'
import type { TocItem } from '../lib/markdown'

import App from '../App'
import PublicShell, { type PublicOutletContext } from '../components/PublicShell'
import PublicSidebar from '../components/PublicSidebar'
import { resetSiteCacheForTests } from '../lib/site'
import { readFile } from '../shims/fs-promises'
import { adminMe, getSharedClientMock, makePage, resetClientMock, visitorMe } from './helpers'

// App 引用全部页面；集成测试只关心 App 的状态机与路由表，页面统一换成探针 stub。
// Setup 的 stub 会触发 onInstalled，用于覆盖「安装完成 → 跳回首页」分支。
vi.mock('../pages/Setup', () => ({ default: ({ onInstalled }: { onInstalled: () => void }) => <button onClick={onInstalled}>SETUP-STUB</button> }))
vi.mock('../pages/public/Home', () => ({ default: () => <div>HOME-STUB</div> }))
vi.mock('../pages/public/Post', () => ({ default: () => <div>POST-STUB</div> }))
vi.mock('../pages/public/Archive', () => ({ default: () => <div>ARCHIVE-STUB</div> }))
vi.mock('../pages/public/Tags', () => ({ default: () => <div>TAGS-STUB</div> }))
vi.mock('../pages/public/Search', () => ({ default: () => <div>SEARCH-STUB</div> }))
vi.mock('../pages/public/Moments', () => ({ default: () => <div>MOMENTS-STUB</div> }))
vi.mock('../pages/public/PageDetail', () => ({ default: () => <div>PAGEDETAIL-STUB</div> }))
vi.mock('../pages/public/NotFound', () => ({ default: () => <div>NOTFOUND-STUB</div> }))
vi.mock('../pages/admin/SignIn', () => ({ default: () => <div>SIGNIN-STUB</div> }))
vi.mock('../pages/admin/Register', () => ({ default: () => <div>REGISTER-STUB</div> }))
vi.mock('../pages/admin/ForgotPassword', () => ({ default: () => <div>FORGOT-STUB</div> }))
vi.mock('../pages/admin/Profile', () => ({ default: () => <div>PROFILE-STUB</div> }))
vi.mock('../pages/admin/UsersAdmin', () => ({ default: () => <div>USERS-STUB</div> }))
vi.mock('../pages/admin/SiteSettings', () => ({ default: () => <div>SITESET-STUB</div> }))
vi.mock('../pages/admin/Dashboard', () => ({ default: () => <div>DASHBOARD-STUB</div> }))
vi.mock('../pages/admin/Posts', () => ({ default: () => <div>POSTS-STUB</div> }))
vi.mock('../pages/admin/Editor', () => ({ default: () => <div>EDITOR-STUB</div> }))
vi.mock('../pages/admin/PagesAdmin', () => ({ default: () => <div>PAGES-STUB</div>, NewPage: () => <div>NEWPAGE-STUB</div>, PageEdit: () => <div>PAGEEDIT-STUB</div> }))
vi.mock('../pages/admin/TagsAdmin', () => ({ default: () => <div>TAGSADMIN-STUB</div> }))
vi.mock('../pages/admin/MomentsAdmin', () => ({ default: () => <div>MOMENTSADMIN-STUB</div> }))
vi.mock('../pages/admin/CommentsAdmin', () => ({ default: () => <div>COMMENTSADMIN-STUB</div> }))
vi.mock('../pages/admin/MediaAdmin', () => ({ default: () => <div>MEDIAADMIN-STUB</div> }))

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

beforeEach(() => {
  resetClientMock(client)
  resetSiteCacheForTests()
  client.setCurrentUser(null)
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
  client.blog.getInstallStatus.mockResolvedValue({ installed: true })
  client.blog.listPages.mockResolvedValue({ items: [makePage({ id: 3, title: '独立页' })], page: 1, page_size: 12, total: 1, total_pages: 1 })
})

function renderApp(route: string) {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <App />
    </MemoryRouter>,
  )
}

describe('App 启动状态机', () => {
  it('探测进行中显示 LOADING', async () => {
    client.blog.getInstallStatus.mockImplementation(() => new Promise(() => {}))
    renderApp('/')
    expect(document.body.textContent).toContain('TWILIGHT · LOADING')
  })

  it('探测失败：错误界面 + 重试按钮恢复到站点', async () => {
    client.blog.getInstallStatus
      .mockRejectedValueOnce(new Error('ECONNREFUSED'))
      .mockResolvedValueOnce({ installed: true })

    renderApp('/')
    await waitFor(() => expect(screen.getByText('API UNREACHABLE')).toBeTruthy())
    expect(document.body.textContent).toContain('ECONNREFUSED') // 原始错误可见，可诊断

    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await waitFor(() => expect(screen.getByText('HOME-STUB')).toBeTruthy(), { timeout: 3000 })
  })

  it('未安装：/ 跳 /install；安装完成后访问 /install 跳回 /', async () => {
    client.blog.getInstallStatus.mockResolvedValue({ installed: false })
    renderApp('/')
    await waitFor(() => expect(screen.getByText('SETUP-STUB')).toBeTruthy())

    fireEvent.click(screen.getByText('SETUP-STUB')) // 触发 onInstalled
    await waitFor(() => expect(screen.getByText('HOME-STUB')).toBeTruthy(), { timeout: 3000 })
  })

  it('已安装：/api/me 解析完成前显示 LOADING，解析后渲染路由', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    client.refreshAuth.mockImplementation(() => gate)
    const { container } = renderApp('/')

    await waitFor(() => expect(screen.getAllByText('TWILIGHT · LOADING').length).toBeGreaterThan(0))
    expect(container.textContent).not.toContain('HOME-STUB')

    // 放行 /api/me：refreshAuth 的 finally 把 authReady 置真，路由才第一次渲染
    await act(async () => {
      release()
    })
    await waitFor(() => expect(screen.getByText('HOME-STUB')).toBeTruthy(), { timeout: 3000 })
  })
})

describe('App 路由表', () => {
  it('公开区与 catch-all：首页渲染、未知公开路径 → NotFound', async () => {
    const first = renderApp('/')
    await waitFor(() => expect(screen.getByText('HOME-STUB')).toBeTruthy(), { timeout: 3000 })
    first.unmount()

    renderApp('/nowhere')
    await waitFor(() => expect(screen.getByText('NOTFOUND-STUB')).toBeTruthy(), { timeout: 3000 })
  })

  it('admin 深层未知路径：AdminLayout 无 * 子路由，pathless 的 PublicShell branch 抢先匹配 → 公开壳 + NotFound（与 App 头注释「重定向首页」不符）', async () => {
    client.setCurrentUser(adminMe)
    const { container } = renderApp('/admin/nonexistent')
    await waitFor(() => expect(screen.getByText('NOTFOUND-STUB')).toBeTruthy(), { timeout: 3000 })
    expect(container.querySelector('.site-shell')).toBeTruthy()
    expect(screen.queryByText('ADMIN CONSOLE')).toBeNull()
  })

  it('管理员：/admin 渲染控制台壳层 + 仪表盘；各页面路由可达', async () => {
    client.setCurrentUser(adminMe)
    client.refreshAuth.mockResolvedValue(adminMe)
    // 侧栏计数：5 个 pageSize:1 的请求，allSettled 汇总 total
    client.blog.listArticles.mockResolvedValue({ items: [], page: 1, page_size: 1, total: 7, total_pages: 1 })
    client.blog.listPages.mockResolvedValue({ items: [], page: 1, page_size: 1, total: 2, total_pages: 1 })
    client.blog.listTags.mockResolvedValue({ items: [], page: 1, page_size: 1, total: 5, total_pages: 1 })
    client.blog.listMoments.mockResolvedValue({ items: [], page: 1, page_size: 1, total: 9, total_pages: 1 })
    client.blog.listAllComments.mockRejectedValue(new Error('评论挂了')) // 失败项计 0

    const { container } = renderApp('/admin')
    await waitFor(() => expect(screen.getByText('DASHBOARD-STUB')).toBeTruthy(), { timeout: 3000 })

    // 壳层：logo、面包屑、完整菜单、计数徽章（失败项 0 → 不渲染徽章）
    await waitFor(() => expect(screen.getByText('7').className).toContain('bdg'))
    expect(document.body.textContent).toContain('ADMIN CONSOLE')
    expect(document.body.textContent).toContain('后台 / 概览')
    expect(document.title).toBe('概览 · Twilight')
    for (const label of ['文章', '页面', '标签', '微语', '评论', '媒体', '用户', '站点', '账号']) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0)
    }
    expect(screen.queryByText('0')).toBeNull()

    // 菜单跳转：点击「文章」→ /admin/posts
    fireEvent.click(screen.getByText('文章'))
    await waitFor(() => expect(screen.getByText('POSTS-STUB')).toBeTruthy())
    expect(document.title).toBe('文章 · Twilight')

    // 深层路径匹配同一菜单项的高亮逻辑（startsWith）
    expect(container.querySelector('a[href="/admin/posts"]')?.className).toContain('on')
    void document.body
  })

  it('访客账号：/admin 重定向到我的资料；菜单折叠为单项', async () => {
    client.setCurrentUser(visitorMe)
    renderApp('/admin')
    await waitFor(() => expect(screen.getByText('PROFILE-STUB')).toBeTruthy(), { timeout: 3000 })
    expect(document.body.textContent).toContain('ACCOUNT')
    expect(document.body.textContent).toContain('账号 / 我的资料')
    expect(screen.getAllByText('我的资料').length).toBeGreaterThan(0)
    expect(screen.queryByText('文章')).toBeNull() // 不渲染会 403 的菜单
  })

  it('退出登录：logout → 提示 → 回登录页', async () => {
    client.setCurrentUser(adminMe)
    renderApp('/admin')
    await waitFor(() => expect(screen.getByText('DASHBOARD-STUB')).toBeTruthy(), { timeout: 3000 })

    fireEvent.click(screen.getByTitle('退出登录'))
    await waitFor(() => expect(screen.getByText('SIGNIN-STUB')).toBeTruthy(), { timeout: 3000 })
    await waitFor(() => expect(screen.getAllByText('已退出登录').length).toBeGreaterThan(0))
  })

  it('会话中途过期：toast 提示并跳登录页', async () => {
    client.setCurrentUser(adminMe)
    renderApp('/admin')
    await waitFor(() => expect(screen.getByText('DASHBOARD-STUB')).toBeTruthy(), { timeout: 3000 })

    act(() => {
      client.setCurrentUser(null)
    })
    await waitFor(() => expect(screen.getByText('SIGNIN-STUB')).toBeTruthy(), { timeout: 3000 })
    await waitFor(() => expect(screen.getAllByText('登录已过期，请重新登录').length).toBeGreaterThan(0))
  })
})

describe('PublicShell + PublicSidebar', () => {
  let tocSetter: ((items: TocItem[]) => void) | null = null

  function OutletProbe() {
    const { setToc } = useOutletContext<PublicOutletContext>()
    tocSetter = setToc
    return <div>OUTLET-PROBE</div>
  }

  function renderShell(route: string) {
    return render(
      <MemoryRouter initialEntries={[route]}>
        <Routes>
          <Route element={<PublicShell />}>
            <Route path="*" element={<OutletProbe />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    )
  }

  it('首页：大标题、描述、导航、页脚（未登录 → 登录入口）', async () => {
    renderShell('/')
    await waitFor(() => expect(screen.getByText('OUTLET-PROBE')).toBeTruthy(), { timeout: 3000 })

    expect(document.querySelector('header[data-header="home"]')).toBeTruthy()
    expect(screen.getAllByText('测试站').length).toBeGreaterThan(0) // 标语来自站点描述
    expect(screen.getAllByText('归档').length).toBeGreaterThan(0) // 默认导航
    expect(screen.getAllByText('独立页').length).toBeGreaterThan(0) // listPages 拉回的独立页面
    expect(screen.getByText('登录 / 注册').getAttribute('href')).toBe('/admin/login')
    expect(document.body.textContent).toContain('Powered by Go + React')
  })

  it('内页：slim 头 + 品牌；setToc 注入目录；页脚独立页面链接', async () => {
    const { container } = renderShell('/archive')
    await waitFor(() => expect(screen.getByText('OUTLET-PROBE')).toBeTruthy(), { timeout: 3000 })

    expect(document.querySelector('header[data-header="home"]')).toBeNull()
    expect(screen.getByText('TWILIGHT BLOG — INK & CODE')).toBeTruthy()

    // 模拟文章页回传目录 → 侧栏渲染 TOC（缩进按层级）
    await act(async () => {
      tocSetter?.([
        { id: 'h1', level: 1, text: '第一章' },
        { id: 'h2', level: 2, text: '小结' },
      ])
    })
    const tocLinks = Array.from(container.querySelectorAll('.toc-list a')) as HTMLAnchorElement[]
    expect(tocLinks.map((a) => a.textContent)).toEqual(['第一章', '小结'])
    expect(tocLinks[0].getAttribute('href')).toBe('#h1')
    expect(parseInt(tocLinks[1].style.paddingLeft, 10)).toBeGreaterThan(parseInt(tocLinks[0].style.paddingLeft, 10))

    // 当前路径高亮
    const navLinks = Array.from(container.querySelectorAll('.nl')) as HTMLAnchorElement[]
    expect(navLinks.find((a) => a.textContent === '归档')?.className).toContain('on')
  })

  it('页脚入口按身份切换；导航拉取失败保留默认导航', async () => {
    client.blog.listPages.mockRejectedValue(new Error('页面挂了'))
    const first = renderShell('/')
    await waitFor(() => expect(screen.getByText('OUTLET-PROBE')).toBeTruthy(), { timeout: 3000 })
    expect(screen.getAllByText('归档').length).toBeGreaterThan(0) // catch 后默认导航仍在
    expect(screen.getByText('登录 / 注册')).toBeTruthy()
    first.unmount()

    client.setCurrentUser(adminMe)
    const second = renderShell('/')
    await waitFor(() => expect(second.container.querySelector('a[href="/admin"]')?.textContent).toBe('控制台'))
    second.unmount()

    client.setCurrentUser(visitorMe)
    const third = renderShell('/')
    await waitFor(() => expect(third.container.querySelector('a[href="/admin/profile"]')?.textContent).toBe('我的资料'))
  })
})

describe('PublicSidebar 直渲染', () => {
  it('无目录时不渲染 TOC；作者卡与导航 on 态', () => {
    render(
      <MemoryRouter initialEntries={['/tags']}>
        <PublicSidebar pages={[makePage({ id: 3, title: '关于' })]} />
      </MemoryRouter>,
    )
    expect(document.querySelector('.toc-box')).toBeNull()
    expect(screen.getByText('Twilight')).toBeTruthy()
    expect(screen.getByText('关于')).toBeTruthy() // 独立页面进导航
    expect(screen.getByText('标签').className).toContain('on')
  })
})

describe('main 入口与 node shim', () => {
  it('fs-promises 替身：浏览器里传路径直接抛出可读异常', async () => {
    await expect(readFile()).rejects.toThrow('not supported in the browser')
  })

  it('main.tsx：把应用挂到 #root', async () => {
    document.body.innerHTML = '<div id="root"></div>'
    const createRoot = vi.fn(() => ({ render: vi.fn() }))
    vi.doMock('react-dom/client', () => ({ default: { createRoot }, createRoot }))
    await import('../main')
    expect(createRoot).toHaveBeenCalledWith(document.getElementById('root'))
    expect(createRoot.mock.results[0].value.render).toHaveBeenCalled()
  })
})
