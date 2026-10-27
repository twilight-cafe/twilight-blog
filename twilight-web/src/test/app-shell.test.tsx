// app-shell.test.tsx —— App 状态机 + 路由守卫 + 三个壳层（PublicShell/PublicSidebar/AdminLayout）+ main 入口 + node shim
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useOutletContext } from 'react-router-dom'
import type { TocItem } from '../lib/markdown'

import App from '../App'
import PublicShell, { type PublicOutletContext } from '../components/PublicShell'
import RouteTransition from '../components/RouteTransition'
import PublicSidebar from '../components/PublicSidebar'
import { resetCountsCacheForTests } from '../components/AdminLayout'
import { resetSiteCacheForTests } from '../lib/site'
import { setLocale } from '../lib/i18n'
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
  resetCountsCacheForTests()
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

  it('公开页不被 /api/me 阻塞：身份未解析时首页已可见（这条以前断言相反）', async () => {
    // 永久 pending 的 /api/me：匿名访客从来拿不到答案，公开页也必须渲染。
    client.refreshAuth.mockImplementation(() => new Promise(() => {}))
    renderApp('/')

    await waitFor(() => expect(screen.getByText('HOME-STUB')).toBeTruthy(), { timeout: 3000 })
    expect(document.body.textContent).not.toContain('TWILIGHT · LOADING')
  })

  it('admin 区仍等 /api/me：解析前显示 LOADING，解析后才渲染控制台', async () => {
    client.setCurrentUser(adminMe)
    let release!: (value: unknown) => void
    const gate = new Promise((resolve) => { release = resolve })
    client.refreshAuth.mockImplementation(() => gate)

    const { container } = renderApp('/admin')
    await waitFor(() => expect(screen.getAllByText('TWILIGHT · LOADING').length).toBeGreaterThan(0))
    expect(container.textContent).not.toContain('DASHBOARD-STUB')

    // 放行 /api/me：refreshAuth 的 finally 把 authReady 置真，控制台才第一次渲染
    await act(async () => {
      release(adminMe)
    })
    await waitFor(() => expect(screen.getByText('DASHBOARD-STUB')).toBeTruthy(), { timeout: 3000 })
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
    // 标题是 effect 的产物，而 waitFor 是被 MutationObserver 唤醒的：DOM 变成本次
    // 提交的产物，effect 却可能在它之后才 flush。两者赛跑，全量并发（CPU 被抢）时
    // MutationObserver 会赢——于是「内容已出现但标题还是上一页」。断言副作用就必须
    // 用 waitFor 包住副作用本身，不能拿「另一个副作用出现了」当它的前置条件。
    await waitFor(() => expect(document.title).toBe('概览 · Twilight'))
    for (const label of ['文章', '页面', '标签', '微语', '评论', '媒体', '用户', '站点', '账号']) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0)
    }
    expect(screen.queryByText('0')).toBeNull()

    // 菜单跳转：点击「文章」→ /admin/posts
    fireEvent.click(screen.getByText('文章'))
    await waitFor(() => expect(screen.getByText('POSTS-STUB')).toBeTruthy())
    await waitFor(() => expect(document.title).toBe('文章 · Twilight'))

    // 深层路径匹配同一菜单项的高亮逻辑（startsWith）
    expect(container.querySelector('a[href="/admin/posts"]')?.className).toContain('on')
    void document.body
  })

  it('后台每个路由都能加载出对应页面（懒加载工厂逐个被真正执行）', async () => {
    client.setCurrentUser(adminMe)
    client.refreshAuth.mockResolvedValue(adminMe)

    // 后台页面全部走 React.lazy（见 App.tsx）：这里逐个访问一次，既是「路由可达」
    // 的守护，也保证每个惰性工厂与 PagesAdmin 的两处具名导出映射都被执行到——
    // 懒加载最典型的故障就是「路由配好了但工厂写错、页面永远停在 loading」。
    const routes: [string, string][] = [
      ['/admin', 'DASHBOARD-STUB'],
      ['/admin/posts', 'POSTS-STUB'],
      ['/admin/editor', 'EDITOR-STUB'],
      ['/admin/pages', 'PAGES-STUB'],
      ['/admin/pages/new', 'NEWPAGE-STUB'],
      ['/admin/pages/5', 'PAGEEDIT-STUB'],
      ['/admin/tags', 'TAGSADMIN-STUB'],
      ['/admin/moments', 'MOMENTSADMIN-STUB'],
      ['/admin/comments', 'COMMENTSADMIN-STUB'],
      ['/admin/media', 'MEDIAADMIN-STUB'],
      ['/admin/users', 'USERS-STUB'],
      ['/admin/site-settings', 'SITESET-STUB'],
      ['/admin/profile', 'PROFILE-STUB'],
      ['/admin/login', 'SIGNIN-STUB'],
      ['/admin/register', 'REGISTER-STUB'],
      ['/admin/forgot', 'FORGOT-STUB'],
    ]

    for (const [path, stub] of routes) {
      const view = renderApp(path)
      await waitFor(() => expect(screen.getAllByText(stub).length).toBeGreaterThan(0), { timeout: 3000 })
      view.unmount()
    }
    // 16 个路由 × 每次最多 3s：默认的 5s 在 8 worker 满载跑全量时会擦线。
    // 这里要守的是「路由可达」，不是速度，放宽总时限而不是放宽每条断言。
  }, 30000)

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

  it('回到顶部入口由 ink-design BackTop 提供：页首时移出 Tab 顺序', async () => {
    renderShell('/')
    await waitFor(() => expect(screen.getByText('OUTLET-PROBE')).toBeTruthy(), { timeout: 3000 })
    // 不可见时 aria-hidden + tabIndex=-1：一个看不见的按钮不该被读屏念出来，
    // 也不该让键盘用户停在屏幕右下角的空气上。
    const backTop = screen.getByTitle('回到顶部')
    expect(backTop.getAttribute('aria-hidden')).toBe('true')
    expect(backTop.tabIndex).toBe(-1)
  })

  it('首页：大标题、描述、导航、页脚（未登录 → 登录入口）', async () => {
    renderShell('/')
    await waitFor(() => expect(screen.getByText('OUTLET-PROBE')).toBeTruthy(), { timeout: 3000 })

    expect(document.querySelector('header[data-header="home"]')).toBeTruthy()
    expect(screen.getAllByText('测试站').length).toBeGreaterThan(0) // 页脚 / 作者卡仍展示站点描述
    // 首页大标题下不再有标语：那段描述已按设计移除。
    expect(document.querySelector('.mast .slogan')).toBeNull()
    expect(screen.getAllByText('归档').length).toBeGreaterThan(0) // 默认导航
    expect(screen.getAllByText('独立页').length).toBeGreaterThan(0) // listPages 拉回的独立页面
    expect(screen.getByText('登录 / 注册').getAttribute('href')).toBe('/admin/login')
    expect(document.body.textContent).toContain('Powered by Go + React')
  })

  it('内页：slim 头 + 品牌；setToc 注入目录；页脚独立页面链接', async () => {
    const { container } = renderShell('/archive')
    await waitFor(() => expect(screen.getByText('OUTLET-PROBE')).toBeTruthy(), { timeout: 3000 })

    expect(document.querySelector('header[data-header="home"]')).toBeNull()
    // 语言切换器已移入侧栏工具行，内页页头里不再保留第二份。
    expect(screen.getByRole('group', { name: '语言' })).toBeTruthy()
    expect(document.querySelector('.mast .lang-switch')).toBeNull()
    // 所有内页共用紧凑头（post/page 以及归档、标签等）。
    expect(document.querySelector('.mast.slim.compact')).toBeTruthy()
    expect(document.querySelector('.nav-slim.compact')).toBeTruthy()
    // 菜单并入紧凑头，紧跟在品牌右侧（不再单独占一行）。
    const slimMast = document.querySelector('.wrap.mast.slim') as HTMLElement
    const brand = slimMast.querySelector('.brand') as HTMLElement
    const nav = slimMast.querySelector('.nav-slim') as HTMLElement
    expect(brand.nextElementSibling).toBe(nav)
    expect(screen.getByRole('button', { name: '中' }).getAttribute('aria-pressed')).toBe('true')

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

    // 滚动高亮：jsdom 里所有 rect 都是 0（都在阅读线之上）→ 高亮最后一个，
    // 也就是「当前读到的最靠下的小节」。逻辑本身由 motion.test.tsx 守。
    const headingA = document.createElement('h2')
    headingA.id = 'h1'
    const headingB = document.createElement('h2')
    headingB.id = 'h2'
    document.body.append(headingA, headingB)
    try {
      act(() => {
        window.dispatchEvent(new Event('scroll'))
      })
      expect(tocLinks[1].className).toBe('on')
      expect(tocLinks[0].className).toBe('')
    } finally {
      headingA.remove()
      headingB.remove()
    }

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

  it('首页 → 内页：精简头第一帧钉在旧页头的高度上，再连续收到自己的高度', async () => {
    // 换页要走 RouteTransition（App 里就是这样包的），并让两个页头量出不同的高度：
    // 大标题 300px、精简头 120px。jsdom 里 Element.animate 不存在，这里也一并补上。
    vi.useFakeTimers()
    const rectDescriptor = Object.getOwnPropertyDescriptor(Element.prototype, 'getBoundingClientRect')!
    const animate = vi.fn(() => ({ cancel: vi.fn(), onfinish: null as (() => void) | null }))
    Object.defineProperty(Element.prototype, 'animate', { configurable: true, value: animate })
    Object.defineProperty(Element.prototype, 'getBoundingClientRect', {
      configurable: true,
      value: function (this: Element) {
        const big = this.matches?.('header[data-header="home"]') === true
        const height = big ? 300 : 120
        return { top: 0, left: 0, right: 0, bottom: height, width: 0, height, x: 0, y: 0, toJSON: () => ({}) }
      },
    })

    try {
      render(
        <MemoryRouter initialEntries={['/']}>
          <RouteTransition>
            <Route element={<PublicShell />}>
              <Route path="*" element={<OutletProbe />} />
            </Route>
          </RouteTransition>
        </MemoryRouter>,
      )
      await act(async () => {})
      expect(document.querySelector('header[data-header="home"]')).toBeTruthy()

      // 点导航进内页：180ms 退场之后才提交新地址，旧页头在退场时被量下 300px。
      fireEvent.click(screen.getAllByText('归档')[0])
      act(() => {
        vi.advanceTimersByTime(180)
      })

      const slim = document.querySelector('.wrap.mast.slim') as HTMLElement
      expect(slim).toBeTruthy()
      const head = slim.parentElement as HTMLElement
      // 新页头的盒子钉在旧高度上：正文从原来的位置起算，再被连续推上去；
      // 少了这一步，提交那一帧正文与页脚会被拽动 180px。
      expect(head.style.height).toBe('300px')
      expect(head.style.overflow).toBe('hidden')
    } finally {
      vi.useRealTimers()
      delete (Element.prototype as unknown as { animate?: unknown }).animate
      Object.defineProperty(Element.prototype, 'getBoundingClientRect', rectDescriptor)
    }
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

  it('语言切换器在侧栏工具行，紧挨亮/暗按钮左侧', () => {
    render(
      <MemoryRouter initialEntries={['/tags']}>
        <PublicSidebar pages={[]} />
      </MemoryRouter>,
    )
    const tools = document.querySelector('.public-sidebar .tools') as HTMLElement
    const lang = tools.querySelector('.lang-switch') as HTMLElement
    const mode = tools.querySelector('.iconbtn') as HTMLElement
    expect(lang).toBeTruthy()
    expect(mode).toBeTruthy()
    // 紧邻在左侧：两个显示偏好控件排在同一行，语言在前、明暗在后。
    expect(lang.nextElementSibling).toBe(mode)
    // 页头不再保留第二份切换器。
    expect(document.querySelector('.mast .lang-switch')).toBeNull()
  })

  it('页脚 Navigation 的独立页面名称随语言切换，缺译名回退中文', async () => {
    act(() => setLocale('zh'))
    client.blog.listPages.mockResolvedValue({
      items: [
        makePage({ id: 3, title: '关于', title_en: 'About' }),
        makePage({ id: 4, title: '友链', title_en: '' }),
      ],
      page: 1, page_size: 12, total: 2, total_pages: 1,
    })
    const { container } = render(
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route element={<PublicShell />}>
            <Route path="*" element={<div>OUTLET-PROBE</div>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('OUTLET-PROBE')).toBeTruthy(), { timeout: 3000 })
    const footer = container.querySelector('footer.site') as HTMLElement
    await waitFor(() => expect(footer.querySelector('a[href="/pages/3"]')?.textContent).toBe('关于'))

    act(() => setLocale('en'))
    await waitFor(() => expect(footer.querySelector('a[href="/pages/3"]')?.textContent).toBe('About'))
    // 没有英文译名的页面保留中文名（与页头、侧栏同一套回退规则）。
    expect(footer.querySelector('a[href="/pages/4"]')?.textContent).toBe('友链')
    // 回到顶部按钮的 aria-label / title 也跟随语言（库默认是中文）。
    expect(screen.getByTitle('Back to top')).toBeTruthy()
    act(() => setLocale('zh'))
  })

  it('侧栏独立页面名与目录标题随语言切换，无译名时保留中文', () => {
    render(
      <MemoryRouter initialEntries={['/tags']}>
        <PublicSidebar
          pages={[
            makePage({ id: 3, title: '关于', title_en: 'About' }),
            makePage({ id: 4, title: '友链', title_en: '' }),
          ]}
          toc={[{ id: 'h1', level: 1, text: '第一节' }]}
        />
      </MemoryRouter>,
    )
    expect(screen.getByText('关于')).toBeTruthy()
    expect(screen.getByText('目录')).toBeTruthy()
    expect(screen.getByLabelText('侧边栏')).toBeTruthy()
    expect(screen.getByRole('navigation', { name: '文章目录' })).toBeTruthy()

    act(() => setLocale('en'))
    expect(screen.getByText('About')).toBeTruthy()
    expect(screen.getByText('友链')).toBeTruthy()
    expect(screen.queryByText('关于')).toBeNull()
    // 目录标题与两个 aria-label 也必须跟着语言走。
    expect(screen.getByText('Contents')).toBeTruthy()
    expect(screen.queryByText('目录')).toBeNull()
    expect(screen.getByLabelText('Sidebar')).toBeTruthy()
    expect(screen.getByRole('navigation', { name: 'Table of contents' })).toBeTruthy()
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
