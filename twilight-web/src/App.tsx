// App.tsx —— 应用根组件：启动探测 + 路由表 + 守卫
//
// 启动分两步：① getInstallStatus 探测后端是否已安装（loading/installed/
// not-installed/error 四态，API 不可达单列 error 并提供重试）；② 已安装后
// refreshAuth() 解析登录态（authReady）。
//
// authReady **只门控 /admin/***，公开页不等它。以前它把公开页也拦在登录态之后，
// 于是匿名访客看首页要等三次串行往返（install_status → /api/me → 文章接口）才见到
// 内容，LCP 与收录双输；而 /api/me 对匿名访客毫无用处。代价是页脚账号入口会先显示
// 「登录 / 注册」再变成「控制台」——这是有意接受的：公开页内容不因身份而变，那个入口
// 的正确性由 useCurrentUser() 在解析完成后自行修正（见 PublicShell 的 Footer）。
// admin 区反过来必须等：渲染早了会把已登录的管理员先弹到登录页再跳回来。
//
// 路由守卫：
//   - 未安装且不在 /install → 跳 /install；已安装却访问 /install → 跳回首页。
//   - /admin/* 全部套在 AdminLayout 下，由它在组件内做登录态/角色守卫（游客账号
//     只放行 /admin/profile）。
//   - 公开区 catch-all 渲染 NotFound；admin 区 catch-all 直接重定向到首页。
//
// 打包策略：公开区**静态**导入（首屏就要用到），admin 区全部 React.lazy。
// 实测「仅公开区」与「全部路由」相差 81.7 KB，而匿名访客一行 admin 代码都用不到。
// AdminLayout 保持静态——它是守门的，不能等自己的 chunk 加载完才开始守。
//
// 换页：整棵路由树包在 RouteTransition 下——它负责「先让旧页面退场，再提交新
// 地址」的编排（见 components/RouteTransition.tsx）。App 自己读到的仍是真实
// location：安装状态与登录态守卫不能等退场动画播完才生效。

import { Suspense, lazy, useEffect, useState, type ReactNode } from 'react'
import { Navigate, Route, useLocation } from 'react-router-dom'
import { Button } from 'ink-design'
import { blog, getErrorMessage, refreshAuth } from './lib/client'
import { DialogProvider } from './lib/dialog'
import { ToastProvider } from './lib/toast'
import PublicShell from './components/PublicShell'
import AdminLayout from './components/AdminLayout'
import RouteTransition from './components/RouteTransition'
import DocCommentsLayout from './components/DocCommentsLayout'
import { Loading } from './components/common'
import { useI18n } from './lib/i18n'
import Home from './pages/public/Home'
import Post from './pages/public/Post'
import Archive from './pages/public/Archive'
import Tags from './pages/public/Tags'
import Search from './pages/public/Search'
import Moments from './pages/public/Moments'
import PageDetail from './pages/public/PageDetail'
import NotFound from './pages/public/NotFound'
import Setup from './pages/Setup'

// 后台每一个页面各自成 chunk。PagesAdmin 另导出 NewPage / PageEdit，用 .then 取具名
// 导出——React.lazy 只认 default，直接 lazy(() => import(...)) 会拿到 undefined。
const SignIn = lazy(() => import('./pages/admin/SignIn'))
const Register = lazy(() => import('./pages/admin/Register'))
const ForgotPassword = lazy(() => import('./pages/admin/ForgotPassword'))
const Profile = lazy(() => import('./pages/admin/Profile'))
const UsersAdmin = lazy(() => import('./pages/admin/UsersAdmin'))
const SiteSettings = lazy(() => import('./pages/admin/SiteSettings'))
const Dashboard = lazy(() => import('./pages/admin/Dashboard'))
const Posts = lazy(() => import('./pages/admin/Posts'))
const Editor = lazy(() => import('./pages/admin/Editor'))
const PagesAdmin = lazy(() => import('./pages/admin/PagesAdmin'))
const NewPage = lazy(() => import('./pages/admin/PagesAdmin').then((module) => ({ default: module.NewPage })))
const PageEdit = lazy(() => import('./pages/admin/PagesAdmin').then((module) => ({ default: module.PageEdit })))
const TagsAdmin = lazy(() => import('./pages/admin/TagsAdmin'))
const MomentsAdmin = lazy(() => import('./pages/admin/MomentsAdmin'))
const CommentsAdmin = lazy(() => import('./pages/admin/CommentsAdmin'))
const MediaAdmin = lazy(() => import('./pages/admin/MediaAdmin'))

type InstallState = 'loading' | 'installed' | 'not-installed' | 'error'

/**
 * 登录 / 注册 / 找回密码不在 AdminLayout 之下，各自的 chunk 需要自己的 Suspense
 * 边界。兜底用 Loading 而不是留白：chunk 慢的时候至少要让人看见「在加载」。
 *
 * 页面 chunk 加载失败（网络中断、部署换了指纹）不在这里处理：lazy 会把错误抛给
 * 最近的错误边界，也就是 main.tsx 里那层 ErrorBoundary，用户拿到的是可读提示
 * 而不是白屏——这是顶层边界存在的原意。
 */
function AuthRoute({ children }: { children: ReactNode }) {
  return <Suspense fallback={<Loading height={320} />}>{children}</Suspense>
}

export default function App() {
  const { t } = useI18n()
  const [state, setState] = useState<InstallState>('loading')
  const [error, setError] = useState('')
  const [probeKey, setProbeKey] = useState(0)
  // The session lives in a cookie the script cannot read, so identity is only
  // known after `GET /api/me`. The console waits for that answer: rendering it
  // first would bounce a signed-in operator to the sign-in page on every reload.
  const [authReady, setAuthReady] = useState(false)
  const location = useLocation()

  // 只有控制台需要等登录态，公开页立刻渲染（理由见文件头）。
  const needsAuth = location.pathname.startsWith('/admin')

  useEffect(() => {
    let alive = true
    setState('loading')
    setError('')

    blog.getInstallStatus()
      .then((status) => {
        if (alive) setState(status.installed ? 'installed' : 'not-installed')
      })
      .catch((reason) => {
        // Never guess: an unreachable API is its own state, with a retry.
        if (!alive) return
        setError(getErrorMessage(reason, t('无法连接后台服务', 'Cannot reach the API')))
        setState('error')
      })

    return () => {
      alive = false
    }
  }, [probeKey])

  useEffect(() => {
    if (state !== 'installed') return
    let alive = true
    // A network failure here only leaves the visitor anonymous; the console still
    // has its own guard, so the error is not worth blocking the whole site for.
    // It is reported all the same, so an unreachable /api/me is diagnosable
    // rather than indistinguishable from "nobody is signed in".
    refreshAuth()
      .catch((reason) => {
        console.warn('[app] 刷新登录状态失败，按匿名访客继续', reason)
      })
      .finally(() => {
        if (alive) setAuthReady(true)
      })
    return () => {
      alive = false
    }
  }, [state])

  if (state === 'loading') {
    return (
      <div className="login-bg">
        <div className="kicker">TWILIGHT · LOADING</div>
      </div>
    )
  }

  if (state === 'error') {
    return (
      <div className="login-bg">
        <div className="login">
          <div className="lg">
            <span className="sq">T</span>
            <h1>Twilight · {t('无法启动', 'Cannot start')}</h1>
          </div>
          <p className="sub">API UNREACHABLE</p>
          <p className="sec">{t('无法连接后台服务，站点状态未知。请确认后端已启动后重试。', 'The API is unreachable, so the site state is unknown. Make sure the backend is running and try again.')}</p>
          <p className="sec" style={{ color: 'var(--fg)', wordBreak: 'break-word' }}>{error}</p>
          <Button primary type="button" style={{ width: '100%', justifyContent: 'center' }} onClick={() => setProbeKey((value) => value + 1)}>
            {t('重试', 'Retry')}
          </Button>
        </div>
      </div>
    )
  }

  if (state === 'not-installed' && location.pathname !== '/install') {
    return <Navigate to="/install" replace />
  }

  if (state === 'installed' && location.pathname === '/install') {
    return <Navigate to="/" replace />
  }

  // The console shows the same neutral loading screen while the session is still
  // unresolved, rather than a sign-in form it may have to retract.
  if (state === 'installed' && needsAuth && !authReady) {
    return (
      <div className="login-bg">
        <div className="kicker">TWILIGHT · LOADING</div>
      </div>
    )
  }

  return (
    <DialogProvider>
      <ToastProvider>
        <RouteTransition>
          <Route path="/install" element={<Setup onInstalled={() => setState('installed')} />} />
          <Route element={<PublicShell />}>
            <Route path="/" element={<Home />} />
            {/* 文章与独立页面共用一个无路径布局：评论框在两者之间切换时不卸载，
                只更换评论对象（见 components/DocCommentsLayout.tsx）。 */}
            <Route element={<DocCommentsLayout />}>
              <Route path="/post/:id" element={<Post />} />
              <Route path="/pages/:id" element={<PageDetail />} />
            </Route>
            <Route path="/archive" element={<Archive />} />
            <Route path="/tags" element={<Tags />} />
            <Route path="/search" element={<Search />} />
            <Route path="/moments" element={<Moments />} />
            <Route path="*" element={<NotFound />} />
          </Route>
          {/* One session, two identities: the same guard routes each role to the
              console it owns. */}
          <Route path="/admin/login" element={<AuthRoute><SignIn /></AuthRoute>} />
          <Route path="/admin/register" element={<AuthRoute><Register /></AuthRoute>} />
          <Route path="/admin/forgot" element={<AuthRoute><ForgotPassword /></AuthRoute>} />
          <Route element={<AdminLayout />}>
            <Route path="/admin" element={<Dashboard />} />
            <Route path="/admin/posts" element={<Posts />} />
            <Route path="/admin/editor" element={<Editor />} />
            <Route path="/admin/editor/:id" element={<Editor />} />
            <Route path="/admin/pages" element={<PagesAdmin />} />
            <Route path="/admin/pages/new" element={<NewPage />} />
            <Route path="/admin/pages/:id" element={<PageEdit />} />
            <Route path="/admin/tags" element={<TagsAdmin />} />
            <Route path="/admin/moments" element={<MomentsAdmin />} />
            <Route path="/admin/comments" element={<CommentsAdmin />} />
            <Route path="/admin/media" element={<MediaAdmin />} />
            <Route path="/admin/users" element={<UsersAdmin />} />
            <Route path="/admin/site-settings" element={<SiteSettings />} />
            <Route path="/admin/profile" element={<Profile />} />
          </Route>
          <Route path="*" element={<Navigate to="/" replace />} />
        </RouteTransition>
      </ToastProvider>
    </DialogProvider>
  )
}
