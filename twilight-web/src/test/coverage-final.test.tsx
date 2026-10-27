// coverage-final.test.tsx —— 最后一轮覆盖率补缺
// 三类补法：
//  1. 卸载后落地的响应（alive/requestId 守卫的「已失效」分支）：pending promise →
//     unmount → resolve/reject；
//  2. 探针直呼（markdown 清洗、enhanceCodeBlocks 等渲染路径
//     不可达的守卫，导出口见各源文件注释）；
//  3. 常规交互分支（确认弹窗取消、错误 toast、标签多页拉取、计数缓存 TTL 等）。
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { act, fireEvent, render, screen, waitFor, cleanup } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import App from '../App'
import Editor from '../pages/admin/Editor'
import MediaAdmin from '../pages/admin/MediaAdmin'
import MomentsAdmin from '../pages/admin/MomentsAdmin'
import CommentsAdmin from '../pages/admin/CommentsAdmin'
import PagesAdmin, { NewPage, PageEdit } from '../pages/admin/PagesAdmin'
import Posts from '../pages/admin/Posts'
import Profile from '../pages/admin/Profile'
import SiteSettings from '../pages/admin/SiteSettings'
import UsersAdmin from '../pages/admin/UsersAdmin'
import ForgotPassword from '../pages/admin/ForgotPassword'
import ModeToggle from '../components/ModeToggle'
import PublicShell from '../components/PublicShell'
import Home from '../pages/public/Home'
import Moments from '../pages/public/Moments'
import Search from '../pages/public/Search'
import Tags from '../pages/public/Tags'
import Comments from '../components/Comments'
import MediaPicker from '../components/media/MediaPicker'
import MediaLightbox from '../components/media/MediaLightbox'
import { enhanceCodeBlocks, enhanceImages } from '../components/ContentView'
import AdminLayout from '../components/AdminLayout'
import { collectToc, extractToc, isSafeUrl, nodeText, rehypeCommentSanitize, rehypeHeadingIds, renderCommentMarkdown } from '../lib/markdown'
import { saveSiteDescription, saveSiteSettings, resetSiteCacheForTests } from '../lib/site'
import { excerpt } from '../lib/format'
import { DialogProvider } from '../lib/dialog'
import { ToastProvider, useToast } from '../lib/toast'
import { adminMe, expireEmailCodeCooldown, getSharedClientMock, makeArticle, makeComment, makeMedia, makeMoment, makePage, makeTag, makeUser, resetClientMock, visitorMe } from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

function paged<T>(items: T[], page = 1, total_pages = 1, total = items.length) {
  return { items, page, page_size: 12, total, total_pages }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
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
            <Route path="/admin/login" element={<div>LOGIN-STUB</div>} />
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

/** PagesAdmin/Comments 等组件含 Link/useNavigate，直渲染必须有 Router 上下文。 */
function renderWithRouter(ui: React.ReactElement, route = '/') {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <ToastProvider>
        <DialogProvider>{ui}</DialogProvider>
      </ToastProvider>
    </MemoryRouter>,
  )
}

/** 弹窗内的按钮（与 bulkbar 的同名按钮区分：Modal 的操作按钮都在 .modal-footer）。 */
function dialogButton(name: string): HTMLButtonElement {
  const footer = document.querySelector('.modal-footer')
  if (!footer) throw new Error(`弹窗未打开，找不到按钮「${name}」`)
  const button = Array.from(footer.querySelectorAll('button')).find((b) => b.textContent === name)
  if (!button) throw new Error(`弹窗底部没有按钮「${name}」`)
  return button as HTMLButtonElement
}

const emptyLists = () => {
  client.blog.listArticles.mockResolvedValue(paged([]))
  client.blog.listPages.mockResolvedValue(paged([]))
  client.blog.listTags.mockResolvedValue(paged([]))
  client.blog.listMoments.mockResolvedValue(paged([]))
  client.blog.listAllComments.mockResolvedValue(paged([]))
}

beforeEach(() => {
  resetClientMock(client)
  resetSiteCacheForTests()
  client.setCurrentUser(null)
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
  client.blog.getInstallStatus.mockResolvedValue({ installed: true })
  // 功能开关由外壳写在 <html> 的 data-search-enabled 上；各用例显式设置，
  // 缺省时拆掉避免泄漏。
  delete document.documentElement.dataset.searchEnabled
  emptyLists()
})

/* --------------------------- lib：探针与纯函数 --------------------------- */

describe('markdown 探针（渲染路径不可达的守卫）', () => {
  it('nodeText 认识 inlineCode', () => {
    expect(nodeText({ type: 'inlineCode', value: 'const a' })).toBe('const a')
  })

  it('nodeText：text 节点缺 value 时按空串', () => {
    expect(nodeText({ type: 'text' })).toBe('')
  })

  it('rehypeHeadingIds：缺 tagName 的元素不参与编号也不炸', () => {
    const transform = rehypeHeadingIds() as (tree: unknown) => void
    const tree = { type: 'element', children: [{ type: 'text', value: '甲' }] }
    expect(() => transform(tree)).not.toThrow()
  })

  it('collectToc：空/非对象子节点跳过；heading 缺 depth 回退 1 级', () => {
    const items = collectToc({
      type: 'root',
      children: [
        null,
        '不是节点',
        { type: 'heading', children: [{ type: 'text', value: '无层级' }] },
        { type: 'heading', depth: 3, children: [] },
      ],
    })
    expect(items.map((item) => item.text)).toEqual(['无层级', ''])
    expect(items[0].level).toBe(1)
    expect(items[1].level).toBe(3)
  })

  it('rehypeCommentSanitize：缺 tagName 的元素只保留子节点；无 children 的未知元素静默丢弃', () => {
    const sanitize = rehypeCommentSanitize() as (tree: unknown) => void

    const unknown = { type: 'element', children: [{ type: 'text', value: '内' }] }
    const root = { type: 'root', children: [unknown] }
    sanitize(root)
    // 未知元素的子节点被上提到父级，元素本身丢弃
    expect(root.children[0]).toEqual({ type: 'text', value: '内' })

    const bare = { type: 'element', tagName: 'figure' }
    expect(() => sanitize({ type: 'root', children: [bare] })).not.toThrow()

    const badId = { type: 'element', tagName: 'code', properties: { id: undefined } }
    sanitize({ type: 'root', children: [badId] })
    expect(badId.properties).toEqual({})

    const input = { type: 'element', tagName: 'input', properties: {} }
    const inputRoot = { type: 'root', children: [input] }
    sanitize(inputRoot)
    expect(inputRoot.children.length).toBe(0) // 非 checkbox 的 input 被丢弃
  })

  it('renderCommentMarkdown 对畸形 id 属性照常输出安全结果', () => {
    expect(renderCommentMarkdown('正文')).toContain('正文')
    expect(isSafeUrl('https://example.com')).toBe(true)
  })

  it('extractToc 正常路径不回归', () => {
    expect(extractToc('## 甲').length).toBe(1)
  })
})

describe('ContentView enhanceCodeBlocks 探针', () => {
  it('root 为空直接返回', () => {
    expect(() => enhanceCodeBlocks(null, new Set())).not.toThrow()
  })

  it('已包裹的 pre 不二次包裹；无 code 子元素不增强', () => {
    const wrapped = document.createElement('div')
    wrapped.innerHTML = '<div class="code-block"><pre><code class="language-ts">a</code></pre></div>'
    enhanceCodeBlocks(wrapped, new Set())
    expect(wrapped.querySelectorAll('.code-block').length).toBe(1)

    const bare = document.createElement('div')
    bare.innerHTML = '<pre><span>no code</span></pre>'
    enhanceCodeBlocks(bare, new Set())
    expect(bare.querySelector('.code-block')).toBeNull()
  })

  it('空代码块复制成功（textContent 空串路径）', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockResolvedValue(undefined) }, configurable: true })
    const root = document.createElement('div')
    root.innerHTML = '<pre><code></code></pre>'
    enhanceCodeBlocks(root, new Set())
    const copy = root.querySelector('.code-copy') as HTMLButtonElement
    fireEvent.click(copy)
    await waitFor(() => expect(copy.textContent).toBe('已复制'))
  })

  // React 19 会用相同内容重设 dangerouslySetInnerHTML（真机实测），把包好的
  // 壳冲掉；修复靠「每次提交后重跑增强」。这个用例模拟的正是那个循环：
  // 壳被外部重置（innerHTML 覆写）后，重跑 enhanceCodeBlocks 必须能恢复壳，
  // 而不是留下裸 pre——限行 CSS 选择器 .plain-body .code-block pre 只认包壳后的结构。
  it('壳被外部重置后重跑增强能完整恢复（React 19 重设 innerHTML 场景）', () => {
    const raw = '<pre><code class="language-py">x = 1</code></pre>'
    const root = document.createElement('div')
    root.innerHTML = raw
    enhanceCodeBlocks(root, new Set())
    expect(root.querySelector('.code-block')).not.toBeNull()

    // 模拟 React 19 的重设：同内容覆写 innerHTML，壳消失、pre 裸奔。
    root.innerHTML = raw
    expect(root.querySelector('.code-block')).toBeNull()

    enhanceCodeBlocks(root, new Set())
    const restored = root.querySelector('.code-block')
    expect(restored).not.toBeNull()
    expect(restored?.querySelector('pre')).not.toBeNull()
    expect(root.querySelectorAll('.code-block').length).toBe(1) // 无嵌套双包
    expect(restored?.querySelector('.code-copy')).not.toBeNull()
  })
})

// jsdom 不做布局，也复现不了 React 19 真机路径上「同内容重设 innerHTML」的
// 行为（并发提交在 act 环境里不会发生），所以这个 bug 在组件测试里天然不可见。
// 能钉住它的只有源码形态本身：增强 effect 必须是无依赖的 layout effect——
// 一旦有人改回 useEffect(..., [html])，真机上壳丢失的缺陷就悄悄回来了。
describe('ContentView 增强 effect 源码形态（React 19 重设 innerHTML 的守护）', () => {
  const source = readFileSync(resolve(process.cwd(), 'src/components/ContentView.tsx'), 'utf8')

  it('增强走无依赖的 useLayoutEffect', () => {
    expect(source).toContain('useLayoutEffect(() => {')
    expect(source).toContain('enhanceCodeBlocks(ref.current, timers.current)')
  })

  it('增强 effect 不得依赖 [html]（真机壳丢失的根源）', () => {
    expect(source).not.toMatch(/\},\s*\[html\]\)/)
    expect(source).not.toContain('}, [html])')
  })
})

// JetBrains Mono 已本地打包（取代 Google Fonts 外链）：app.css 的 @font-face
// 必须指向 vite 打包的本地产物，Go 模板不得再出现任何字体 CDN 链接。
describe('字体本地化（零外链守护）', () => {
  const appCss = readFileSync(resolve(process.cwd(), 'src/styles/app.css'), 'utf8')
  const goTemplate = readFileSync(
    resolve(process.cwd(), '../twilight-blog/internal/view/template/index.html'),
    'utf8',
  )

  it('三个字重都有本地 @font-face', () => {
    for (const weight of [400, 500, 600]) {
      expect(appCss).toContain(`font-weight: ${weight}`)
      expect(appCss).toContain(`jetbrains-mono-latin-${weight}-normal.woff2`)
    }
  })

  it('app.css 与 Go 模板都没有 fonts.googleapis / fonts.gstatic', () => {
    expect(appCss).not.toContain('fonts.googleapis.com')
    expect(appCss).not.toContain('fonts.gstatic.com')
    expect(goTemplate).not.toContain('fonts.googleapis.com')
    expect(goTemplate).not.toContain('fonts.gstatic.com')
    expect(goTemplate).not.toContain('rel="preconnect"')
  })
})

// 搜索页回车后信息区的横向跳动：页面从不滚动变成可滚动时，经典滚动条占去
// 10~15px 宽，居中的内容整体横移几个像素。这类变化不派发 resize（innerWidth
// 没变），也没有 scroll 事件——0.1.3 时它顺带把 ink-design 的自绘光标留在旧
// 视口坐标上（根因已在上游 0.1.4 修掉，见 INKDESIGN-0.1.3-FINDINGS.md 第 7 条）。
// 站点侧的处置是让内容宽度恒定：给根元素预留滚动条槽位，连跳动本身也去掉。
// 这段位移只有真浏览器能算（jsdom 不做布局），所以钉住的是 CSS 契约本身。
describe('滚动条槽位（自绘光标等依赖视口坐标的浮层）', () => {
  const appCss = readFileSync(resolve(process.cwd(), 'src/styles/app.css'), 'utf8')

  function ruleBlock(selector: string): string {
    const start = appCss.indexOf(`${selector} {`)
    if (start === -1) return ''
    return appCss.slice(start, appCss.indexOf('}', start))
  }

  it('根元素预留 scrollbar-gutter，内容宽度不随滚动条出现而变化', () => {
    const htmlRule = ruleBlock('html')
    expect(htmlRule, '找不到 html 规则').not.toBe('')
    expect(htmlRule).toMatch(/scrollbar-gutter:\s*stable/)
  })

  it('槽位在根元素上：body 预留只影响 body 自己的盒子，挡不住视口宽度变化', () => {
    expect(ruleBlock('body')).not.toMatch(/scrollbar-gutter/)
  })
})

describe('format excerpt 空串', () => {
  it('空内容走 || 兜底', () => {
    expect(excerpt('')).toBe('')
  })
})

describe('site saveSiteDescription', () => {
  it('保存后回传服务端值；缺 description 回退空串', async () => {
    client.blog.updateSite.mockResolvedValue({ description: '新描述' })
    await expect(saveSiteDescription('新描述')).resolves.toBe('新描述')

    client.blog.updateSite.mockResolvedValue({})
    await expect(saveSiteDescription('任意')).resolves.toBe('')
  })
})

/* ------------------------------ client 纯函数 ------------------------------ */

describe('client 纯函数（真实模块）', () => {
  it('isUnauthorizedError 的非 BlogApiError 分支', async () => {
    const real = await vi.importActual<typeof import('../lib/client')>('../lib/client')
    expect(real.isUnauthorizedError(null)).toBe(false)
    expect(real.isUnauthorizedError(new Error('普通错误'))).toBe(false)
    expect(real.isUnauthorizedError({ status: 401 })).toBe(true)
    expect(real.isUnauthorizedError({ status: 500 })).toBe(false)
  })

  it('getErrorMessage：非 5xx 且 message 为空时回退调用方文案', async () => {
    const { BlogApiError } = await import('twilight-blog-api')
    const real = await vi.importActual<typeof import('../lib/client')>('../lib/client')
    const empty = new BlogApiError('', { status: 400, method: 'GET', path: '/api/x' })
    expect(real.getErrorMessage(empty, '回退文案')).toBe('回退文案')
  })
})

/* ------------------------------- dialog/toast ------------------------------- */

describe('toast 默认上下文（Provider 外调用为 noop）', () => {
  it('Provider 外 show 不炸', () => {
    function Consumer() {
      const show = useToast()
      return <button onClick={() => show('hello')}>go</button>
    }
    render(<Consumer />)
    expect(() => fireEvent.click(screen.getByText('go'))).not.toThrow()
  })
})

/* --------------------------------- App.tsx --------------------------------- */

describe('App 启动补缺', () => {
  it('未安装：访问任意路径重定向到 /install', async () => {
    client.blog.getInstallStatus.mockResolvedValue({ installed: false })
    render(
      <MemoryRouter initialEntries={['/']}>
        <App />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByRole('button', { name: '设置密码并进入后台 →' })).toBeTruthy())
    // Setup 页有密码输入框
    expect(document.querySelector('input[type=password]')).toBeTruthy()
  })

  it('探测 promise 在卸载后才落地：不写状态不报错', async () => {
    const gate = deferred<{ installed: boolean }>()
    client.blog.getInstallStatus.mockImplementation(() => gate.promise as never)
    const { unmount } = render(
      <MemoryRouter initialEntries={['/']}>
        <App />
      </MemoryRouter>,
    )
    unmount()
    await act(async () => {
      gate.reject(new Error('down'))
    })
  })

  it('探测 promise 在卸载后才成功落地：同样不写状态', async () => {
    const gate = deferred<{ installed: boolean }>()
    client.blog.getInstallStatus.mockImplementation(() => gate.promise as never)
    const { unmount } = render(
      <MemoryRouter initialEntries={['/']}>
        <App />
      </MemoryRouter>,
    )
    unmount()
    await act(async () => {
      gate.resolve({ installed: true })
    })
  })

  it('refreshAuth 在卸载后才 resolve：authReady 不再写', async () => {
    const gate = deferred<typeof visitorMe>()
    client.refreshAuth.mockImplementation(() => gate.promise as never)
    const { unmount } = render(
      <MemoryRouter initialEntries={['/']}>
        <App />
      </MemoryRouter>,
    )
    await waitFor(() => expect(screen.getByText('TWILIGHT · LOADING')).toBeTruthy())
    unmount()
    await act(async () => {
      gate.resolve(visitorMe)
    })
  })
})

/* ------------------------------- AdminLayout ------------------------------- */

describe('AdminLayout 计数缓存与守卫补缺', () => {
  it('首次挂载：五个计数请求共用一个 in-flight；卸载后落地不写状态', async () => {
    const gates = [deferred<ReturnType<typeof paged>>(), deferred<ReturnType<typeof paged>>(), deferred<ReturnType<typeof paged>>(), deferred<ReturnType<typeof paged>>(), deferred<ReturnType<typeof paged>>()]
    client.blog.listArticles.mockImplementation(() => gates[0].promise as never)
    client.blog.listPages.mockImplementation(() => gates[1].promise as never)
    client.blog.listTags.mockImplementation(() => gates[2].promise as never)
    client.blog.listMoments.mockImplementation(() => gates[3].promise as never)
    client.blog.listAllComments.mockImplementation(() => gates[4].promise as never)
    client.setCurrentUser(adminMe)

    const first = renderRouted(<AdminLayout />, '/admin/posts', '/admin/posts')
    // 卸载第一个实例，再挂第二个：第二次 fetchCounts 应复用 in-flight
    first.unmount()
    const second = renderRouted(<AdminLayout />, '/admin/tags', '/admin/tags')
    await act(async () => {
      gates.forEach((gate) => gate.resolve(paged([], 1, 1, 7)))
    })
    await waitFor(() => expect(second.container.querySelector('.bdg')).toBeTruthy())
    expect(second.container.textContent).toContain('7')
  })

  it('TTL 过期后重新拉取计数', async () => {
    client.setCurrentUser(adminMe)
    const first = renderRouted(<AdminLayout />, '/admin/posts', '/admin/posts')
    await waitFor(() => expect(first.container.querySelector('.bdg')).toBeTruthy())

    const realNow = Date.now()
    const nowSpy = vi.spyOn(Date, 'now').mockReturnValue(realNow + 61_000)
    client.blog.listArticles.mockResolvedValue(paged([], 1, 1, 99))
    const second = renderRouted(<AdminLayout />, '/admin/tags', '/admin/tags')
    await waitFor(() => expect(second.container.textContent).toContain('99'))
    nowSpy.mockRestore()
    second.unmount()
    first.unmount()
  })

  it('无身份直接挂载：重定向登录页且不提示过期', () => {
    client.refreshAuth.mockResolvedValue(null)
    const { container } = renderRouted(<AdminLayout />, '/admin/posts', '/admin/posts')
    expect(container.textContent).toContain('LOGIN-STUB')
  })
})

describe('AdminLayout 外壳开关的会话内更新', () => {
  it('保存站点设置关闭微语后，侧栏菜单立即消失（无需换页或刷新）', async () => {
    // dataset 变化不会触发 React 重渲染，菜单必须靠 notifyShellFlagsChanged
    // 的订阅事件更新——这正是本次修复的缺陷。
    document.documentElement.dataset.momentsEnabled = 'true'
    client.setCurrentUser(adminMe)
    client.blog.getSite.mockResolvedValue({ description: 'd', moments_enabled: true })
    client.blog.updateSite.mockResolvedValue({ description: 'd', moments_enabled: false })
    const { container, unmount } = renderRouted(<AdminLayout />, '/admin/posts', '/admin/posts')
    try {
      await waitFor(() => expect(container.querySelector('a[href="/admin/moments"]')).toBeTruthy())

      await act(async () => {
        await saveSiteSettings({ description: 'd', momentsEnabled: false })
      })

      await waitFor(() => expect(container.querySelector('a[href="/admin/moments"]')).toBeNull())
    } finally {
      unmount()
      delete document.documentElement.dataset.momentsEnabled
    }
  })
})

describe('AdminLayout 访客侧显示补缺', () => {
  it('访客身份：显示名取昵称、roleLabel 邮箱为空回退「普通用户」', async () => {
    client.setCurrentUser({ role: 'user', user: makeUser({ nickname: '小明', email: '' }) })
    client.refreshAuth.mockResolvedValue({ role: 'user', user: makeUser({ nickname: '小明', email: '' }) })
    client.blog.getInstallStatus.mockResolvedValue({ installed: true })
    const { container } = render(
      <MemoryRouter initialEntries={['/admin/profile']}>
        <ToastProvider>
          <DialogProvider>
            <App />
          </DialogProvider>
        </ToastProvider>
      </MemoryRouter>,
    )
    await waitFor(() => expect(container.querySelector('.aside')).toBeTruthy(), { timeout: 3000 })
    expect(container.querySelector('.aside')?.textContent).toContain('小明')
    expect(container.querySelector('.aside')?.textContent).toContain('普通用户')
  })

  it('访客昵称为空：显示名回退「用户」', async () => {
    const guest = { role: 'user' as const, user: makeUser({ nickname: '', email: 'a@b.c' }) }
    client.setCurrentUser(guest)
    client.refreshAuth.mockResolvedValue(guest)
    const { container } = render(
      <MemoryRouter initialEntries={['/admin/profile']}>
        <ToastProvider>
          <DialogProvider>
            <App />
          </DialogProvider>
        </ToastProvider>
      </MemoryRouter>,
    )
    await waitFor(() => expect(container.querySelector('.aside .info b')).toBeTruthy(), { timeout: 3000 })
    expect(container.querySelector('.aside .info b')?.textContent).toBe('用户')
  })

  it('访客访问非资料页：标题用「账号」并重定向到我的资料', async () => {
    client.setCurrentUser(visitorMe)
    const { container } = renderRouted(<AdminLayout />, '/admin/editor/5', '/admin/editor/:id')
    await waitFor(() => expect(document.title).toBe('账号 · Twilight'))
    // 访客被重定向到 /admin/profile，本 Routes 未定义该路径 → 渲染为空
    await waitFor(() => expect(container.textContent).toBe(''))
  })
})

describe('AdminLayout 侧栏头像', () => {
  it('站长设了头像：渲染 <img>，而不是永远的首字母占位块', async () => {
    client.setCurrentUser(adminMe)
    client.blog.getSite.mockResolvedValue({ description: 'd', avatar_url: '/api/media/7', code_block_max_lines: 20 })
    const { container } = renderRouted(<AdminLayout />, '/admin/posts', '/admin/posts')

    const img = await waitFor(() => {
      const found = container.querySelector('.a-user img.avatar') as HTMLImageElement | null
      if (!found) throw new Error('avatar image not rendered yet')
      return found
    })
    expect(img.getAttribute('src')).toBe('/api/media/7')
    // alt 用显示名（adminMe.name='站长'），空占位块被替换掉。
    expect(img.getAttribute('alt')).toBe('站长')
    expect(container.querySelector('.a-user .cav')).toBeNull()
  })

  it('没设头像：回退首字母占位块', async () => {
    client.setCurrentUser(adminMe)
    client.blog.getSite.mockResolvedValue({ description: 'd', avatar_url: '', code_block_max_lines: 20 })
    const { container } = renderRouted(<AdminLayout />, '/admin/posts', '/admin/posts')
    await waitFor(() => expect(container.querySelector('.a-user .info b')).toBeTruthy())
    expect(container.querySelector('.a-user .cav')).toBeTruthy()
    expect(container.querySelector('.a-user img.avatar')).toBeNull()
  })
})

/* ------------------------------- PublicShell ------------------------------- */

describe('PublicShell 独立页面导航', () => {
  it('卸载后 promise 落地不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listPages.mockImplementation(() => gate.promise as never)
    const { unmount } = renderWithRouter(<PublicShell />)
    unmount()
    await act(async () => {
      gate.resolve(paged([makePage()]))
    })
  })

  it('页脚搜索链接跟随开关：关闭时连页脚也不给出死链接', async () => {
    document.documentElement.dataset.searchEnabled = 'false'
    const { container } = renderWithRouter(<PublicShell />)
    await waitFor(() => expect(container.querySelector('footer')).toBeTruthy())
    const footer = Array.from(container.querySelectorAll('footer a')).map((a) => a.getAttribute('href'))
    expect(footer).not.toContain('/search')
    expect(footer).toContain('/archive')

    // 开启（与外壳缺省）时页脚恢复搜索链接。
    document.documentElement.dataset.searchEnabled = 'true'
    cleanup()
    const second = renderWithRouter(<PublicShell />)
    await waitFor(() => expect(second.container.querySelector('footer')).toBeTruthy())
    expect(
      Array.from(second.container.querySelectorAll('footer a')).map((a) => a.getAttribute('href')),
    ).toContain('/search')
  })
})

/* --------------------------------- 公开页 --------------------------------- */

describe('公开页卸载补缺', () => {
  it('Home：列表失败（挂载中）→ 空态', async () => {
    client.blog.listArticles.mockRejectedValue(new Error('首页炸了'))
    renderRouted(<Home />, '/', '/')
    await waitFor(() => expect(screen.getByText('还没有文章')).toBeTruthy())
  })

  it('Home：卸载后列表响应不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listArticles.mockImplementation(() => gate.promise as never)
    const { unmount } = renderRouted(<Home />, '/', '/')
    unmount()
    await act(async () => {
      gate.resolve(paged([makeArticle()]))
    })
  })

  it('Home：卸载后列表失败也不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listArticles.mockImplementation(() => gate.promise as never)
    const { unmount } = renderRouted(<Home />, '/', '/')
    unmount()
    await act(async () => {
      gate.reject(new Error('down'))
    })
  })

  it('Moments：卸载后响应不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listMoments.mockImplementation(() => gate.promise as never)
    const { unmount } = renderRouted(<Moments />, '/moments', '/moments')
    unmount()
    await act(async () => {
      gate.reject(new Error('down'))
    })
  })

  it('Search：卸载后成功/失败响应都不写状态', async () => {
    const okGate = deferred<ReturnType<typeof paged>>()
    client.blog.search.mockImplementation(() => okGate.promise as never)
    const first = renderRouted(<Search />, '/search?q=x', '/search')
    first.unmount()
    await act(async () => {
      okGate.resolve(paged([makeArticle()]))
    })

    const errGate = deferred<ReturnType<typeof paged>>()
    client.blog.search.mockImplementation(() => errGate.promise as never)
    cleanup()
    const second = renderRouted(<Search />, '/search?q=y', '/search')
    second.unmount()
    await act(async () => {
      errGate.reject(new Error('down'))
    })
  })
})

describe('Tags 页补缺', () => {
  it('多页标签全量拉取后一起渲染', async () => {
    client.blog.listTags.mockImplementation(({ page }: { page: number }) => {
      if (page === 1) return Promise.resolve({ items: [makeTag({ id: 1, name: 'Go', article_count: 9 })], page: 1, page_size: 12, total: 2, total_pages: 2 })
      return Promise.resolve({ items: [makeTag({ id: 2, name: 'React', article_count: 3 })], page: 2, page_size: 12, total: 2, total_pages: 2 })
    })
    renderRouted(<Tags />, '/tags', '/tags')
    await waitFor(() => expect(screen.getByText('React')).toBeTruthy())
    expect(screen.getByText('Go')).toBeTruthy()
  })

  it('标签请求：卸载后成功/失败都不写状态', async () => {
    const okGate = deferred<ReturnType<typeof paged>>()
    client.blog.listTags.mockImplementation(() => okGate.promise as never)
    const first = renderRouted(<Tags />, '/tags', '/tags')
    first.unmount()
    await act(async () => {
      okGate.resolve(paged([makeTag()]))
    })

    cleanup()
    const errGate = deferred<ReturnType<typeof paged>>()
    client.blog.listTags.mockImplementation(() => errGate.promise as never)
    const second = renderRouted(<Tags />, '/tags', '/tags')
    second.unmount()
    await act(async () => {
      errGate.reject(new Error('down'))
    })
  })

  it('标签文章请求：卸载后不写状态；失败展示错误与重试', async () => {
    client.blog.listTags.mockResolvedValue(paged([makeTag({ id: 1, name: 'Go' })]))
    const okGate = deferred<ReturnType<typeof paged>>()
    client.blog.getTagArticles.mockImplementation(() => okGate.promise as never)
    const first = renderWithRouter(<Tags />, '/tags?id=1')
    await waitFor(() => expect(screen.getAllByText('Go').length).toBeGreaterThan(0))
    first.unmount()
    await act(async () => {
      okGate.resolve(paged([makeArticle()]))
    })

    cleanup()
    client.blog.getTagArticles.mockRejectedValue(new Error('down'))
    const second = renderWithRouter(<Tags />, '/tags?id=1')
    // getErrorMessage 对普通 Error 原样透出 message
    await waitFor(() => expect(screen.getByText('down')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    second.unmount()
  })
})

/* --------------------------------- ModeToggle --------------------------------- */

describe('ModeToggle 系统深色偏好', () => {
  it('matchMedia 命中 dark 时初始化为暗色', () => {
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: query.includes('dark'),
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }))
    renderPlain(<ModeToggle />)
    expect(document.documentElement.dataset.mode).toBe('dark')
    vi.unstubAllGlobals()
  })
})

/* --------------------------------- 编辑器 --------------------------------- */

describe('Editor 补缺', () => {
  it('非法 id：toast 后回文章列表', async () => {
    const { container } = renderRouted(<Editor />, '/admin/editor/abc', '/admin/editor/:id')
    await waitFor(() => expect(container.textContent).toContain('POSTS-LIST'))
    expect(screen.getAllByText('文章不存在').length).toBeGreaterThan(0)
  })

  it('返回按钮回文章列表', async () => {
    const { container } = renderRouted(<Editor />, '/admin/editor', '/admin/editor')
    fireEvent.click(screen.getByRole('button', { name: '← 返回' }))
    await waitFor(() => expect(container.textContent).toContain('POSTS-LIST'))
  })

  it('标签选择器输入新标签随保存提交', async () => {
    const { container } = renderRouted(<Editor />, '/admin/editor', '/admin/editor')
    const selectInput = container.querySelector('.ed-meta input') as HTMLInputElement
    expect(selectInput).toBeTruthy()
    await act(async () => {
      fireEvent.change(selectInput, { target: { value: '新标签' } })
      fireEvent.keyDown(selectInput, { key: 'Enter' })
    })
    fireEvent.change(container.querySelector('input.ed-title, .ed-title input, input') as HTMLInputElement, { target: { value: '标题T' } })
    client.blog.createArticle.mockResolvedValueOnce(makeArticle({ id: 3 }))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(client.blog.createArticle).toHaveBeenCalledWith({ title: '标题T', content: '', titleEn: '', contentEn: '', aiGenerated: false, aiGeneratedEn: false, tags: ['新标签'], coverMediaId: null }))
  })
})

/* -------------------------------- 忘记密码 -------------------------------- */

describe('ForgotPassword 补缺', () => {
  it('邮箱非法时点「发送验证码」被拦截', async () => {
    const { container } = renderRouted(<ForgotPassword />, '/admin/forgot', '/admin/forgot')
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(container.querySelector('input[placeholder=验证码]')).toBeNull())
    expect(screen.getAllByText(/邮箱/).length).toBeGreaterThan(0)
    expect(client.blog.requestEmailCode).not.toHaveBeenCalled()
  })

  it('发送成功后被 60 秒冷却锁住；冷却结束后可「重新发送」', async () => {
    client.blog.requestEmailCode.mockResolvedValue({ message: 'ok' })
    const { container } = renderRouted(<ForgotPassword />, '/admin/forgot', '/admin/forgot')
    fireEvent.change(container.querySelector('input[type=email]') as HTMLInputElement, { target: { value: 'a@x.com' } })
    fireEvent.click(screen.getByRole('button', { name: '发送验证码' }))
    await waitFor(() => expect(container.querySelector('input[placeholder=验证码]')).toBeTruthy())

    // 发送成功 → 按钮立刻进入倒计时且禁用（「前后端一起限制」的前端那一半）。
    await waitFor(() => {
      const button = screen.getByRole('button', { name: /秒后可重发/ })
      expect(button.hasAttribute('disabled')).toBe(true)
    })

    // 等冷却结束 → 恢复「重新发送」并可再次发码。
    await expireEmailCodeCooldown('a@x.com')
    await waitFor(() => expect(screen.getByRole('button', { name: '重新发送' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '重新发送' }))
    await waitFor(() => expect(client.blog.requestEmailCode).toHaveBeenCalledTimes(2))
  })
})

/* --------------------------------- 媒体库 --------------------------------- */

describe('MediaAdmin 补缺', () => {
  it('列表失败且已卸载：不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listMedia.mockImplementation(() => gate.promise as never)
    const { unmount } = renderPlain(<MediaAdmin />)
    unmount()
    await act(async () => {
      gate.reject(new Error('down'))
    })
  })

  it('上传在卸载后落地：finally 不再触碰已卸载的 input', async () => {
    const gate = deferred<unknown>()
    client.blog.uploadMedia.mockImplementation(() => gate.promise as never)
    const { container, unmount } = renderPlain(<MediaAdmin />)
    const input = container.querySelector('input[type=file]') as HTMLInputElement
    Object.defineProperty(input, 'files', { value: [new File(['x'], 'a.png', { type: 'image/png' })], configurable: true })
    fireEvent.change(input)
    unmount()
    await act(async () => {
      gate.resolve({ message: 'ok' })
    })
  })

  it('上传失败 toast；「上传」按钮触发文件选择；删除取消不调接口', async () => {
    client.blog.listMedia.mockResolvedValue(paged([makeMedia({ id: 1 })]))
    client.blog.uploadMedia.mockRejectedValue(new Error('太大'))
    const { container } = renderPlain(<MediaAdmin />)
    await waitFor(() => expect(screen.getByTitle('删除')).toBeTruthy())

    const clickSpy = vi.spyOn(HTMLInputElement.prototype, 'click').mockImplementation(() => {})
    fireEvent.click(screen.getByRole('button', { name: '上传' }))
    expect(clickSpy).toHaveBeenCalled()
    clickSpy.mockRestore()

    fireEvent.click(screen.getByTitle('删除'))
    fireEvent.click(await screen.findByText('取消'))
    expect(client.blog.deleteMedia).not.toHaveBeenCalled()

    const input = container.querySelector('input[type=file]') as HTMLInputElement
    Object.defineProperty(input, 'files', { value: [new File(['x'], 'up.png', { type: 'image/png' })], configurable: true })
    fireEvent.change(input)
    await waitFor(() => expect(screen.getAllByText('太大').length).toBeGreaterThan(0))
  })
})

/* -------------------------------- 微语管理 -------------------------------- */

describe('MomentsAdmin 补缺', () => {
  it('空列表显示空态；列表失败（挂载中）→ toast', async () => {
    renderPlain(<MomentsAdmin />)
    await waitFor(() => expect(screen.getByText('还没有微语')).toBeTruthy())
    cleanup()

    client.blog.listMoments.mockRejectedValue(new Error('微语炸了'))
    renderPlain(<MomentsAdmin />)
    await waitFor(() => expect(screen.getAllByText('微语炸了').length).toBeGreaterThan(0))
  })

  it('带媒体的微语：缩略图开灯箱、关闭', async () => {
    client.blog.listMoments.mockResolvedValue(paged([makeMoment({ id: 1, media_items: [makeMedia({ id: 9, original_name: 'pic.png' })] })]))
    const { container } = renderPlain(<MomentsAdmin />)
    await waitFor(() => expect(container.querySelector('.media-thumb-open')).toBeTruthy())

    fireEvent.click(container.querySelector('.media-thumb-open') as HTMLButtonElement)
    await waitFor(() => expect(document.querySelector('.media-lightbox')).toBeTruthy())
    fireEvent.keyDown(window, { key: 'Enter' }) // 非 Escape 不关闭
    expect(document.querySelector('.media-lightbox')).toBeTruthy()
    fireEvent.click(document.querySelector('.media-lightbox-close') as Element)
    await waitFor(() => expect(document.querySelector('.media-lightbox')).toBeNull())
  })

  it('列表失败且已卸载：不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listMoments.mockImplementation(() => gate.promise as never)
    const { unmount } = renderPlain(<MomentsAdmin />)
    unmount()
    await act(async () => {
      gate.reject(new Error('down'))
    })
  })

  it('删除取消、恢复失败、彻底删除取消', async () => {
    client.blog.listMoments.mockResolvedValue(paged([makeMoment({ id: 1 })]))
    client.blog.listTrashMoments.mockResolvedValue(paged([makeMoment({ id: 6 })]))
    const { container } = renderPlain(<MomentsAdmin />)
    await waitFor(() => expect(screen.getByRole('button', { name: '删除' })).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    fireEvent.click(await screen.findByText('取消'))
    expect(client.blog.deleteMoment).not.toHaveBeenCalled()

    fireEvent.click((Array.from(container.querySelectorAll('.atabs button')) as HTMLButtonElement[]).find((b) => b.textContent === '回收站') as HTMLButtonElement)
    await waitFor(() => expect(screen.getByRole('button', { name: '恢复' })).toBeTruthy())
    expect(screen.queryByRole('button', { name: '发布' })).toBeNull() // 回收站没有发布卡片

    client.blog.restoreMoment.mockRejectedValueOnce(new Error('恢复不了'))
    fireEvent.click(screen.getByRole('button', { name: '恢复' }))
    await waitFor(() => expect(screen.getAllByText('恢复不了').length).toBeGreaterThan(0))

    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(await screen.findByText('取消'))
    expect(client.blog.permanentlyDeleteMoment).not.toHaveBeenCalled()
  })
})

/* -------------------------------- 评论管理 -------------------------------- */

describe('CommentsAdmin 补缺', () => {
  it('署名回退：管理员无名显示「管理员」，访客无名显示「访客」', async () => {
    client.blog.listAllComments.mockResolvedValue(paged([
      makeComment({ id: 1, author: { type: 'admin', name: '' } }),
      makeComment({ id: 2, author: { type: 'user', id: 3, nickname: '  ' } }),
    ]))
    renderPlain(<CommentsAdmin />)
    await waitFor(() => expect(screen.getByText('管理员')).toBeTruthy())
    expect(screen.getByText('访客')).toBeTruthy()
  })

  it('访客署名昵称空串回退「访客」', async () => {
    client.blog.listAllComments.mockResolvedValue(paged([
      makeComment({ id: 1, author: { type: 'user', id: 3, nickname: '' } }),
    ]))
    renderPlain(<CommentsAdmin />)
    await waitFor(() => expect(screen.getByText('访客')).toBeTruthy())
  })

  it('列表失败（挂载中）→ toast 且清空列表', async () => {
    client.blog.listAllComments.mockRejectedValue(new Error('评论炸了'))
    renderPlain(<CommentsAdmin />)
    await waitFor(() => expect(screen.getAllByText('评论炸了').length).toBeGreaterThan(0))
    expect(screen.getByText('当前列表没有评论')).toBeTruthy()
  })

  it('列表失败且已卸载：不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listAllComments.mockImplementation(() => gate.promise as never)
    const { unmount } = renderPlain(<CommentsAdmin />)
    unmount()
    await act(async () => {
      gate.reject(new Error('down'))
    })
  })

  it('点当前 tab 不重复拉取；删除取消/失败；恢复失败；彻底删除取消', async () => {
    client.blog.listAllComments.mockResolvedValue(paged([makeComment({ id: 1 })]))
    client.blog.listTrashComments.mockResolvedValue(paged([makeComment({ id: 2 })]))
    const { container } = renderPlain(<CommentsAdmin />)
    await waitFor(() => expect(screen.getByRole('button', { name: '删除' })).toBeTruthy())
    const callsAfterLoad = (client.blog.listAllComments as Mock).mock.calls.length

    fireEvent.click((Array.from(container.querySelectorAll('.atabs button')) as HTMLButtonElement[]).find((b) => b.textContent === '全部') as HTMLButtonElement)
    expect((client.blog.listAllComments as Mock).mock.calls.length).toBe(callsAfterLoad)

    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    fireEvent.click(await screen.findByText('取消'))
    expect(client.blog.deleteComment).not.toHaveBeenCalled()

    client.blog.deleteComment.mockRejectedValueOnce(new Error('删不掉'))
    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    fireEvent.click(await screen.findByText('确定'))
    await waitFor(() => expect(screen.getAllByText('删不掉').length).toBeGreaterThan(0))

    fireEvent.click((Array.from(container.querySelectorAll('.atabs button')) as HTMLButtonElement[]).find((b) => b.textContent === '回收站') as HTMLButtonElement)
    await waitFor(() => expect(screen.getByRole('button', { name: '恢复' })).toBeTruthy())
    client.blog.restoreComment.mockRejectedValueOnce(new Error('回不去'))
    fireEvent.click(screen.getByRole('button', { name: '恢复' }))
    await waitFor(() => expect(screen.getAllByText('回不去').length).toBeGreaterThan(0))

    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(await screen.findByText('取消'))
    expect(client.blog.permanentlyDeleteComment).not.toHaveBeenCalled()
  })
})

/* -------------------------------- 文章管理 -------------------------------- */

describe('Posts 补缺', () => {
  it('列表失败且已卸载：不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listArticles.mockImplementation(() => gate.promise as never)
    const { unmount } = renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    unmount()
    await act(async () => {
      gate.reject(new Error('down'))
    })
  })

  it('行勾选/取消；批量恢复成功后失效勾选被过滤', async () => {
    client.blog.listArticles.mockResolvedValue(paged([makeArticle({ id: 1, title: '甲' })]))
    // 恢复成功重载后回收站变空，才能触发「失效勾选被过滤」
    client.blog.listTrashArticles
      .mockResolvedValueOnce(paged([makeArticle({ id: 2, title: '乙' })]))
      .mockResolvedValue(paged([]))
    const { container } = renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    await waitFor(() => expect(screen.getByText('甲')).toBeTruthy())

    // 表头全选 → 已选 1 篇 → 再点取消全选 → bulkbar 消失
    const headCb = container.querySelector('thead input[type=checkbox]') as HTMLInputElement
    fireEvent.click(headCb)
    expect(screen.getByText('已选 1 篇')).toBeTruthy()
    fireEvent.click(headCb)
    expect(screen.queryByText(/已选/)).toBeNull()

    // 行勾选 → 取消勾选（filter 分支）
    const rowCb = container.querySelector('tbody input[type=checkbox]') as HTMLInputElement
    fireEvent.click(rowCb)
    expect(screen.getByText('已选 1 篇')).toBeTruthy()
    fireEvent.click(rowCb)
    expect(screen.queryByText(/已选/)).toBeNull()

    // 回收站：勾选后从 bulkbar 批量恢复成功 → 重载为空 → 失效勾选被过滤
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('乙')).toBeTruthy())
    fireEvent.click(container.querySelector('tbody input[type=checkbox]') as HTMLInputElement)
    expect(screen.getByText('已选 1 篇')).toBeTruthy()
    client.blog.restoreArticle.mockResolvedValue({ message: 'ok' })
    // bulkbar 在表格之前渲染，恢复按钮的第一枚是 bulkbar 的
    fireEvent.click(screen.getAllByRole('button', { name: '恢复' })[0])
    await waitFor(() => expect(screen.getByText('没有符合条件的文章')).toBeTruthy())
    expect(screen.queryByText(/已选/)).toBeNull()
  })

  it('批量与单个操作的取消/失败分支', async () => {
    client.blog.listArticles.mockResolvedValue(paged([makeArticle({ id: 1, title: '甲' })]))
    client.blog.listTrashArticles.mockResolvedValue(paged([makeArticle({ id: 2, title: '乙' })]))
    const { container } = renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    await waitFor(() => expect(screen.getByText('甲')).toBeTruthy())

    // active：删除取消
    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    fireEvent.click(dialogButton('取消'))
    expect(client.blog.deleteArticle).not.toHaveBeenCalled()

    // active：批量移入回收站取消 + 失败
    fireEvent.click(container.querySelector('thead input[type=checkbox]') as HTMLInputElement)
    fireEvent.click(screen.getByRole('button', { name: '移入回收站' }))
    fireEvent.click(dialogButton('取消'))
    expect(client.blog.batchDeleteArticles).not.toHaveBeenCalled()
    client.blog.batchDeleteArticles.mockRejectedValueOnce(new Error('批量失败'))
    fireEvent.click(screen.getByRole('button', { name: '移入回收站' }))
    fireEvent.click(dialogButton('确定'))
    await waitFor(() => expect(screen.getAllByText('批量失败').length).toBeGreaterThan(0))
    fireEvent.click(screen.getByRole('button', { name: '取消' })) // bulkbar 的取消

    // trash：单个彻底删除取消/失败（此时无勾选，bulkbar 不渲染，按钮不重名）
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('乙')).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(dialogButton('取消'))
    expect(client.blog.permanentlyDeleteArticle).not.toHaveBeenCalled()

    client.blog.permanentlyDeleteArticle.mockRejectedValueOnce(new Error('删不了'))
    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(dialogButton('确定'))
    await waitFor(() => expect(screen.getAllByText('删不了').length).toBeGreaterThan(0))

    // 勾选后 bulkbar 出现；bulkbar 在表格之前渲染，恢复/彻底删除的第一枚都是 bulkbar 的
    fireEvent.click(container.querySelector('tbody input[type=checkbox]') as HTMLInputElement)
    expect(screen.getByText('已选 1 篇')).toBeTruthy()

    const batchRestoreBtn = screen.getAllByRole('button', { name: '恢复' })[0] as HTMLButtonElement
    client.blog.batchRestoreArticles.mockRejectedValueOnce(new Error('批量恢复失败'))
    fireEvent.click(batchRestoreBtn)
    await waitFor(() => expect(screen.getAllByText('批量恢复失败').length).toBeGreaterThan(0))

    const batchPermanentBtn = screen.getAllByRole('button', { name: '彻底删除' })[0] as HTMLButtonElement
    fireEvent.click(batchPermanentBtn)
    fireEvent.click(dialogButton('取消'))
    expect(client.blog.batchPermanentlyDeleteArticles).not.toHaveBeenCalled()
    client.blog.batchPermanentlyDeleteArticles.mockRejectedValueOnce(new Error('批量删不掉'))
    fireEvent.click(screen.getAllByRole('button', { name: '彻底删除' })[0] as HTMLButtonElement)
    fireEvent.click(dialogButton('确定'))
    await waitFor(() => expect(screen.getAllByText('批量删不掉').length).toBeGreaterThan(0))
  })
  it('单个恢复失败 toast；列表变化时勾选随列表过滤（cond 走 next 分支）', async () => {
    client.blog.listTrashArticles
      .mockResolvedValueOnce(paged([makeArticle({ id: 2, title: '乙' })]))
      .mockResolvedValue(paged([]))
    const { container } = renderRouted(<Posts />, '/admin/posts', '/admin/posts')
    fireEvent.click(screen.getByText('回收站'))
    await waitFor(() => expect(screen.getByText('乙')).toBeTruthy())

    // 单个恢复失败（无勾选 → bulkbar 不渲染，行内按钮唯一）
    client.blog.restoreArticle.mockRejectedValueOnce(new Error('恢复不动'))
    fireEvent.click(screen.getByRole('button', { name: '恢复' }))
    await waitFor(() => expect(screen.getAllByText('恢复不动').length).toBeGreaterThan(0))

    // 勾选乙 → 行内恢复成功（bulkbar 之前渲染，行内按钮是 .pop()）→ 重载为空 → 失效勾选被过滤
    fireEvent.click(container.querySelector('tbody input[type=checkbox]') as HTMLInputElement)
    expect(screen.getByText('已选 1 篇')).toBeTruthy()
    client.blog.restoreArticle.mockResolvedValueOnce({ message: 'ok' })
    fireEvent.click(screen.getAllByRole('button', { name: '恢复' }).pop() as HTMLButtonElement)
    await waitFor(() => expect(screen.getByText('没有符合条件的文章')).toBeTruthy())
    await waitFor(() => expect(screen.queryByText(/已选/)).toBeNull())
  })
})

/* -------------------------------- 页面管理 -------------------------------- */

describe('PagesAdmin 补缺', () => {
  it('列表失败且已卸载：不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listPages.mockImplementation(() => gate.promise as never)
    const { unmount } = renderWithRouter(<PagesAdmin />)
    unmount()
    await act(async () => {
      gate.reject(new Error('down'))
    })
  })

  it('列表成功但已卸载：同样不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listPages.mockImplementation(() => gate.promise as never)
    const { unmount } = renderWithRouter(<PagesAdmin />)
    unmount()
    await act(async () => {
      gate.resolve(paged([makePage({ id: 1, title: '迟到的页面' })]))
    })
  })

  it('删除/彻底删除取消；删除失败；恢复失败', async () => {
    client.blog.listPages.mockResolvedValue(paged([makePage({ id: 1, title: '关于' })]))
    client.blog.listTrashPages.mockResolvedValue(paged([makePage({ id: 2, title: '旧页' })]))
    const { container } = renderWithRouter(<PagesAdmin />)
    await waitFor(() => expect(screen.getByRole('button', { name: '删除' })).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    fireEvent.click(dialogButton('取消'))
    expect(client.blog.deletePage).not.toHaveBeenCalled()

    client.blog.deletePage.mockRejectedValueOnce(new Error('删不掉'))
    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    fireEvent.click(dialogButton('确定'))
    await waitFor(() => expect(screen.getAllByText('删不掉').length).toBeGreaterThan(0))

    fireEvent.click((Array.from(container.querySelectorAll('.atabs button')) as HTMLButtonElement[]).find((b) => b.textContent === '回收站') as HTMLButtonElement)
    await waitFor(() => expect(screen.getByRole('button', { name: '恢复' })).toBeTruthy())
    client.blog.restorePage.mockRejectedValueOnce(new Error('回不去'))
    fireEvent.click(screen.getByRole('button', { name: '恢复' }))
    await waitFor(() => expect(screen.getAllByText('回不去').length).toBeGreaterThan(0))

    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(dialogButton('取消'))
    expect(client.blog.permanentlyDeletePage).not.toHaveBeenCalled()
  })

  it('PageEdit：加载失败 toast 后回列表；返回按钮；加载中卸载', async () => {
    client.blog.getPage.mockRejectedValue(new Error('down'))
    const first = renderRouted(<PageEdit />, '/admin/pages/9', '/admin/pages/:id')
    await waitFor(() => expect(first.container.textContent).toContain('PAGES-LIST'))
    expect(screen.getAllByText('down').length).toBeGreaterThan(0)

    client.blog.getPage.mockResolvedValue(makePage({ id: 4, title: '旧标题' }))
    const second = renderRouted(<PageEdit />, '/admin/pages/4', '/admin/pages/:id')
    await waitFor(() => expect(second.container.querySelector('input[placeholder=页面标题]')).toBeTruthy())
    // 正文 textarea 受控输入（PageEdit 的 onChange 分支）
    fireEvent.change(second.container.querySelector('textarea') as HTMLTextAreaElement, { target: { value: '改过的正文' } })
    fireEvent.click(screen.getByRole('button', { name: '← 返回' }))
    await waitFor(() => expect(document.body.textContent).toContain('PAGES-LIST'))

    const gate = deferred<ReturnType<typeof makePage>>()
    client.blog.getPage.mockImplementation(() => gate.promise as never)
    const third = renderRouted(<PageEdit />, '/admin/pages/7', '/admin/pages/:id')
    third.unmount()
    await act(async () => {
      gate.resolve(makePage({ id: 7 }))
    })

    // 卸载后失败落地：catch 的 alive 守卫早退
    const errGate = deferred<ReturnType<typeof makePage>>()
    client.blog.getPage.mockImplementation(() => errGate.promise as never)
    const fourth = renderRouted(<PageEdit />, '/admin/pages/8', '/admin/pages/:id')
    fourth.unmount()
    await act(async () => {
      errGate.reject(new Error('late down'))
    })
  })

  it('NewPage：输入标题与内容后创建', async () => {
    client.blog.createPage.mockResolvedValue(makePage({ id: 5 }))
    renderRouted(<NewPage />, '/admin/pages/new', '/admin/pages/new')
    fireEvent.change(screen.getByPlaceholderText('页面标题'), { target: { value: '新页' } })
    fireEvent.change(screen.getByPlaceholderText('页面内容（Markdown）'), { target: { value: '正文内容' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(client.blog.createPage).toHaveBeenCalledWith({ title: '新页', content: '正文内容', titleEn: '', contentEn: '' }))
  })

  it('列表失败（挂载中）→ toast 且空态；trash 彻底删除失败；切回「页面」tab', async () => {
    client.blog.listPages.mockRejectedValue(new Error('页面炸了'))
    renderWithRouter(<PagesAdmin />)
    await waitFor(() => expect(screen.getAllByText('页面炸了').length).toBeGreaterThan(0))
    await waitFor(() => expect(screen.getByText('暂无页面')).toBeTruthy())
    cleanup()

    client.blog.listPages.mockResolvedValue(paged([makePage({ id: 1, title: '关于' })]))
    client.blog.listTrashPages.mockResolvedValue(paged([makePage({ id: 2, title: '旧页' })]))
    const { container } = renderWithRouter(<PagesAdmin />)
    await waitFor(() => expect(screen.getByRole('button', { name: '删除' })).toBeTruthy())

    fireEvent.click((Array.from(container.querySelectorAll('.atabs button')) as HTMLButtonElement[]).find((b) => b.textContent === '回收站') as HTMLButtonElement)
    await waitFor(() => expect(screen.getByRole('button', { name: '恢复' })).toBeTruthy())
    client.blog.permanentlyDeletePage.mockRejectedValueOnce(new Error('删不掉'))
    fireEvent.click(screen.getByRole('button', { name: '彻底删除' }))
    fireEvent.click(dialogButton('确定'))
    await waitFor(() => expect(screen.getAllByText('删不掉').length).toBeGreaterThan(0))

    // 切回「页面」tab（setTab('active') 分支）
    fireEvent.click((Array.from(container.querySelectorAll('.atabs button')) as HTMLButtonElement[]).find((b) => b.textContent === '页面') as HTMLButtonElement)
    await waitFor(() => expect(screen.getByRole('button', { name: '删除' })).toBeTruthy())
  })
})

/* --------------------------------- 账号页 --------------------------------- */

describe('Profile 补缺', () => {
  it('无身份渲染 null', () => {
    const { container } = renderPlain(<Profile />)
    expect(container.textContent).toBe('')
  })

  it('管理员：缺省显示名回退 admin；空显示名拦截；登录名校验通过后提交；失败 toast', async () => {
    client.setCurrentUser({ role: 'admin', username: 'root' } as typeof adminMe)
    const { container } = renderPlain(<Profile />)
    await waitFor(() => expect(screen.getByText('站长资料')).toBeTruthy())
    const forms = Array.from(container.querySelectorAll('form')) as HTMLFormElement[]
    // 排除头像选择器藏在表单里的 file input（见 media/AvatarPicker）。
    const idInputs = Array.from(forms[0].querySelectorAll('input:not([type="file"])')) as HTMLInputElement[]
    expect(idInputs[0].value).toBe('admin') // name 缺省 → 'admin'

    fireEvent.change(idInputs[0], { target: { value: '  ' } })
    fireEvent.submit(forms[0])
    await waitFor(() => expect(screen.getAllByText(/显示名称/).length).toBeGreaterThan(0))
    expect(client.blog.updateAdminProfile).not.toHaveBeenCalled()

    fireEvent.change(idInputs[0], { target: { value: '站长' } })
    fireEvent.change(idInputs[1], { target: { value: 'newroot' } })
    client.blog.updateAdminProfile.mockResolvedValueOnce({ role: 'admin', name: '站长', username: 'newroot' })
    fireEvent.submit(forms[0])
    await waitFor(() => expect(client.blog.updateAdminProfile).toHaveBeenCalledWith({ name: '站长', username: 'newroot' }))
    expect(screen.getAllByText('已更新，下次登录请使用新的登录名').length).toBeGreaterThan(0)

    client.blog.updateAdminProfile.mockRejectedValueOnce(new Error('被占用'))
    fireEvent.submit(forms[0])
    await waitFor(() => expect(screen.getAllByText('被占用').length).toBeGreaterThan(0))
  })

  it('管理员：username 缺省回退空；登录名非法被拦截', async () => {
    client.setCurrentUser({ role: 'admin', name: '站长' } as typeof adminMe)
    const { container } = renderPlain(<Profile />)
    await waitFor(() => expect(screen.getByText('站长资料')).toBeTruthy())
    const forms = Array.from(container.querySelectorAll('form')) as HTMLFormElement[]
    // 同上：排除 file input。
    const idInputs = Array.from(forms[0].querySelectorAll('input:not([type="file"])')) as HTMLInputElement[]
    expect(idInputs[1].value).toBe('') // username || '' 兜底

    fireEvent.change(idInputs[1], { target: { value: '1bad name' } })
    fireEvent.submit(forms[0])
    await waitFor(() => expect(screen.getAllByText(/用户名/).length).toBeGreaterThan(0))
    expect(client.blog.updateAdminProfile).not.toHaveBeenCalled()
  })

  it('访客：仅改昵称提示「昵称已更新」', async () => {
    client.setCurrentUser(visitorMe)
    const { container } = renderPlain(<Profile />)
    await waitFor(() => expect(screen.getByText('资料')).toBeTruthy())
    const forms = Array.from(container.querySelectorAll('form')) as HTMLFormElement[]
    const inputs = Array.from(forms[0].querySelectorAll('input')) as HTMLInputElement[]
    fireEvent.change(inputs[1], { target: { value: '新昵称' } })
    client.blog.updateProfile.mockResolvedValueOnce({ message: 'ok' })
    client.refreshAuth.mockResolvedValue(visitorMe)
    fireEvent.submit(forms[0])
    await waitFor(() => expect(screen.getAllByText('昵称已更新').length).toBeGreaterThan(0))
  })
})

/* -------------------------------- 站点设置 -------------------------------- */

describe('SiteSettings 卸载补缺', () => {
  it('加载在卸载后落地：不写状态不报错', async () => {
    const gate = deferred<{ description: string }>()
    client.blog.getSite.mockImplementation(() => gate.promise as never)
    const { unmount } = renderPlain(<SiteSettings />)
    unmount()
    await act(async () => {
      gate.resolve({ description: '迟到' })
    })
  })
})

/* -------------------------------- 用户管理 -------------------------------- */

describe('UsersAdmin 补缺', () => {
  it('列表失败且已卸载：不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listUsers.mockImplementation(() => gate.promise as never)
    const { unmount } = renderPlain(<UsersAdmin />)
    unmount()
    await act(async () => {
      gate.reject(new Error('down'))
    })
  })

  it('卸载后列表成功落地：同样不写状态', async () => {
    const gate = deferred<ReturnType<typeof paged>>()
    client.blog.listUsers.mockImplementation(() => gate.promise as never)
    const { unmount } = renderPlain(<UsersAdmin />)
    unmount()
    await act(async () => {
      gate.resolve(paged([makeUser()]))
    })
  })

  it('列表失败（挂载中）→ toast', async () => {
    client.blog.listUsers.mockRejectedValue(new Error('用户炸了'))
    renderPlain(<UsersAdmin />)
    await waitFor(() => expect(screen.getAllByText('用户炸了').length).toBeGreaterThan(0))
    expect(screen.getByText('还没有注册用户')).toBeTruthy()
  })

  it('空昵称/用户名的回退显示；编辑后取消', async () => {
    client.blog.listUsers.mockResolvedValue(paged([makeUser({ id: 3, nickname: '', username: '' })]))
    const { container } = renderPlain(<UsersAdmin />)
    await waitFor(() => expect(container.querySelector('.cav')).toBeTruthy())
    expect(container.querySelector('.cav')?.textContent).toBe('访')
    expect(container.querySelector('.uline')?.textContent).toContain('访客')
    expect(container.querySelector('.utag')?.textContent).toBe('@—')

    fireEvent.click(screen.getByRole('button', { name: '编辑' }))
    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(container.querySelector('.uedit')).toBeNull()
    expect(client.blog.updateUser).not.toHaveBeenCalled()
  })
})

/* --------------------------------- 评论区 --------------------------------- */

describe('Comments 补缺', () => {
  it('卸载后响应落地：成功与失败都不写状态', async () => {
    const okGate = deferred<ReturnType<typeof paged>>()
    client.blog.listComments.mockImplementation(() => okGate.promise as never)
    const first = renderWithRouter(<Comments targetType="article" targetId={1} />)
    first.unmount()
    await act(async () => {
      okGate.resolve(paged([makeComment()]))
    })

    cleanup()
    const errGate = deferred<ReturnType<typeof paged>>()
    client.blog.listComments.mockImplementation(() => errGate.promise as never)
    const second = renderWithRouter(<Comments targetType="article" targetId={1} />)
    second.unmount()
    await act(async () => {
      errGate.reject(new Error('down'))
    })
  })

  it('结果缺 page 字段时回退请求页码', async () => {
    client.blog.listComments.mockResolvedValue({ items: [makeComment({ id: 1 })], total: 1, total_pages: 1 } as never)
    renderWithRouter(<Comments targetType="article" targetId={1} />)
    await waitFor(() => expect(screen.getByText('顶一个')).toBeTruthy())
  })

  it('点赞只更新目标评论（其余保持原样）；连点重入被守卫', async () => {
    client.setCurrentUser(visitorMe)
    client.blog.listComments.mockResolvedValue(paged([
      makeComment({ id: 1, content: '甲', like_count: 0 }),
      makeComment({ id: 2, content: '乙', like_count: 5 }),
    ]))
    renderWithRouter(<Comments targetType="article" targetId={1} />)
    await waitFor(() => expect(screen.getByText('甲')).toBeTruthy())

    const gate = deferred<ReturnType<typeof makeComment>>()
    client.blog.likeComment.mockImplementation(() => gate.promise as never)
    const likeButtons = screen.getAllByText(/♡|♥/)
    fireEvent.click(likeButtons[0])
    fireEvent.click(likeButtons[0]) // liking 中再点：重入守卫
    await act(async () => {
      gate.resolve(makeComment({ id: 1, content: '甲', like_count: 1, liked_by_me: true }))
    })
    await waitFor(() => expect(screen.getByText(/♥/)).toBeTruthy())
    expect(screen.getByText(/♡ 5/)).toBeTruthy() // 另一条未被改写
  })

  it('点赞接口返回空（无更新）：页面保持原样不报错', async () => {
    client.setCurrentUser(visitorMe)
    client.blog.listComments.mockResolvedValue(paged([makeComment({ id: 1, content: '甲', like_count: 3 })]))
    renderWithRouter(<Comments targetType="article" targetId={1} />)
    await waitFor(() => expect(screen.getByText(/♡ 3/)).toBeTruthy())

    client.blog.likeComment.mockResolvedValueOnce(undefined as never)
    // act 冲刷微任务，确保 toggleLike 的 if (updated) 真正执行完
    await act(async () => {
      fireEvent.click(screen.getByText(/♡ 3/))
    })
    expect(client.blog.likeComment).toHaveBeenCalledTimes(1)
    expect(screen.getByText(/♡ 3/)).toBeTruthy()
    expect(screen.queryByText(/操作失败/)).toBeNull()
  })

  it('署名回退与访客昵称缺失', async () => {
    client.blog.listComments.mockResolvedValue(paged([
      makeComment({ id: 1, author: { type: 'admin', name: '' } }),
      makeComment({ id: 2, author: { type: 'user', id: 3, nickname: '' } }),
    ]))
    renderWithRouter(<Comments targetType="article" targetId={1} />)
    await waitFor(() => expect(screen.getByText('管理员')).toBeTruthy())
    expect(screen.getAllByText('访客').length).toBeGreaterThan(0)
  })

  it('访客昵称为空时身份行回退「访客」；回复超长与失败分支', async () => {
    client.setCurrentUser({ role: 'user', user: makeUser({ nickname: '' }) })
    client.blog.listComments.mockResolvedValue(paged([makeComment({ id: 1 })]))
    const { container } = renderWithRouter(<Comments targetType="article" targetId={1} />)
    await waitFor(() => expect(screen.getByText('回复')).toBeTruthy())
    expect(screen.getByText(/以 访客 的身份评论/)).toBeTruthy()

    fireEvent.click(screen.getByText('回复'))
    const boxes = container.querySelectorAll('textarea')
    const replyBox = boxes[boxes.length - 1] as HTMLTextAreaElement
    expect(replyBox.placeholder).toBe('写下回复…')
    // 回复表单自己的提交按钮也叫「回复」，是 DOM 里的第二枚
    const replySubmit = () => screen.getAllByRole('button', { name: '回复' }).pop() as HTMLButtonElement
    // 用原型原生 setter 写值（保留 React 的 value tracker），再派发 change；
    // defineProperty 会顶掉 React 装在节点上的 tracker 访问器，污染后续事件
    const setReplyValue = (value: string) => {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value')?.set
      setter!.call(replyBox, value)
      fireEvent.change(replyBox)
    }

    // 超长回复被拦截（原生 setter 不受 maxLength 约束）
    setReplyValue('超'.repeat(2001))
    fireEvent.click(replySubmit())
    await waitFor(() => expect(screen.getAllByText(/回复不能超过/).length).toBeGreaterThan(0))
    expect(client.blog.createComment).not.toHaveBeenCalled()

    // 正常回复失败 → toast
    setReplyValue('好好好')
    client.blog.createComment.mockRejectedValueOnce(new Error('发不出去'))
    fireEvent.click(replySubmit())
    await waitFor(() => expect(screen.getAllByText('发不出去').length).toBeGreaterThan(0))
  })
})

/* ------------------------------- 媒体选择器 ------------------------------- */

describe('MediaPicker 补缺', () => {
  it('无文件 change 直接返回', () => {
    const onChange = vi.fn()
    const { container } = renderPlain(<MediaPicker items={[]} onChange={onChange} />)
    fireEvent.change(container.querySelector('input[type=file]') as HTMLInputElement)
    expect(onChange).not.toHaveBeenCalled()
    expect(client.blog.uploadMedia).not.toHaveBeenCalled()
  })

  it('files 属性缺失（undefined）时按空处理', () => {
    const onChange = vi.fn()
    const { container } = renderPlain(<MediaPicker items={[]} onChange={onChange} />)
    const input = container.querySelector('input[type=file]') as HTMLInputElement
    Object.defineProperty(input, 'files', { value: undefined, configurable: true })
    fireEvent.change(input)
    expect(onChange).not.toHaveBeenCalled()
    expect(client.blog.uploadMedia).not.toHaveBeenCalled()
  })

  it('上传在卸载后落地：finally 不再触碰已卸载的 input', async () => {
    type Media = ReturnType<typeof makeMedia>
    const gate = deferred<Media[]>()
    client.blog.uploadMedia.mockImplementation(() => gate.promise as never)
    const { container, unmount } = renderPlain(<MediaPicker items={[]} onChange={vi.fn()} />)
    const input = container.querySelector('input[type=file]') as HTMLInputElement
    Object.defineProperty(input, 'files', { value: [new File(['x'], 'a.png', { type: 'image/png' })], configurable: true })
    fireEvent.change(input)
    unmount()
    await act(async () => {
      gate.resolve([makeMedia()])
    })
  })
})

describe('MediaLightbox 非 Escape 键不关闭', () => {
  it('Enter 关不掉灯箱', async () => {
    const onClose = vi.fn()
    renderPlain(<MediaLightbox items={[makeMedia()]} index={0} onClose={onClose} />)
    fireEvent.keyDown(window, { key: 'Enter' })
    expect(onClose).not.toHaveBeenCalled()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

// enhanceImages 只在正文里真的出现 <img> 时才会走到包裹逻辑；正文图片不是 React
// 元素，没有组件测试的入口——与 enhanceCodeBlocks 同一先例：探针直呼 + 手搓 DOM。
// 缓存的两种情况靠改写原型上的 complete / naturalWidth 模拟，afterEach 原样还回。
describe('ContentView enhanceImages 探针', () => {
  const completeDescriptor = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'complete')
  const naturalWidthDescriptor = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'naturalWidth')

  function stubImage(complete: boolean, naturalWidth: number): void {
    Object.defineProperty(HTMLImageElement.prototype, 'complete', { configurable: true, get: () => complete })
    Object.defineProperty(HTMLImageElement.prototype, 'naturalWidth', { configurable: true, get: () => naturalWidth })
  }

  afterEach(() => {
    if (completeDescriptor) Object.defineProperty(HTMLImageElement.prototype, 'complete', completeDescriptor)
    if (naturalWidthDescriptor) Object.defineProperty(HTMLImageElement.prototype, 'naturalWidth', naturalWidthDescriptor)
  })

  it('root 为空直接返回', () => {
    expect(() => enhanceImages(null)).not.toThrow()
  })

  it('正文图片包进外框、挂上骨架并给出临时宽高比；重复执行不重复包裹', () => {
    const root = document.createElement('div')
    root.innerHTML = '<p>文字<img src="/body.png" alt="正文图">文字</p>'
    enhanceImages(root)

    const frame = root.querySelector('.img-frame') as HTMLElement
    expect(frame.className).toBe('img-frame loading')
    expect(frame.style.aspectRatio).toBe('16 / 9')
    expect(frame.querySelector('.img-skeleton')?.getAttribute('aria-hidden')).toBe('true')
    expect(frame.querySelector('img')?.getAttribute('src')).toBe('/body.png')

    enhanceImages(root)
    expect(root.querySelectorAll('.img-frame').length).toBe(1)
  })

  it('load 落地并清空占位比例；error 判为失败', () => {
    const root = document.createElement('div')
    root.innerHTML = '<img src="/a.png"><img src="/b.png">'
    enhanceImages(root)
    const frames = Array.from(root.querySelectorAll<HTMLElement>('.img-frame'))

    fireEvent.load(frames[0].querySelector('img') as HTMLImageElement)
    expect(frames[0].classList.contains('loaded')).toBe(true)
    expect(frames[0].style.aspectRatio).toBe('')

    fireEvent.error(frames[1].querySelector('img') as HTMLImageElement)
    expect(frames[1].classList.contains('failed')).toBe(true)
  })

  it('已缓存：complete 且 naturalWidth > 0 立刻落地；坏图判失败', () => {
    stubImage(true, 1024)
    const good = document.createElement('div')
    good.innerHTML = '<img src="/cached.png">'
    enhanceImages(good)
    expect(good.querySelector('.img-frame')?.classList.contains('loaded')).toBe(true)

    stubImage(true, 0)
    const broken = document.createElement('div')
    broken.innerHTML = '<img src="/broken.png">'
    enhanceImages(broken)
    expect(broken.querySelector('.img-frame')?.classList.contains('failed')).toBe(true)
  })
})
