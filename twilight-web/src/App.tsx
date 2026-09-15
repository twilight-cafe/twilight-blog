// App.tsx —— 应用根组件：启动探测 + 路由表 + 守卫
// 启动分两步：① getInstallStatus 探测后端是否已安装（loading/installed/
// not-installed/error 四态，API 不可达单列 error 并提供重试）；② 已安装时再
// refreshAuth() 等 /api/me 解析出登录态（authReady）后才真正渲染路由——否则刷新
// 页面会把已登录的管理员先弹到登录页再跳回。
// 路由守卫：
//   - 未安装且不在 /install → 跳 /install；已安装却访问 /install → 跳回首页。
//   - /admin/* 全部套在 AdminLayout 下，由它在组件内做登录态/角色守卫（游客账号
//     只放行 /admin/profile）。
//   - 公开区 catch-all 渲染 NotFound；admin 区 catch-all 直接重定向到首页。

import { useEffect, useState } from 'react'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { Button } from 'ink-design'
import { blog, getErrorMessage, refreshAuth } from './lib/client'
import { DialogProvider } from './lib/dialog'
import { ToastProvider } from './lib/toast'
import PublicShell from './components/PublicShell'
import AdminLayout from './components/AdminLayout'
import Home from './pages/public/Home'
import Post from './pages/public/Post'
import Archive from './pages/public/Archive'
import Tags from './pages/public/Tags'
import Search from './pages/public/Search'
import Moments from './pages/public/Moments'
import PageDetail from './pages/public/PageDetail'
import NotFound from './pages/public/NotFound'
import Setup from './pages/Setup'
import SignIn from './pages/admin/SignIn'
import Register from './pages/admin/Register'
import ForgotPassword from './pages/admin/ForgotPassword'
import Profile from './pages/admin/Profile'
import UsersAdmin from './pages/admin/UsersAdmin'
import SiteSettings from './pages/admin/SiteSettings'
import Dashboard from './pages/admin/Dashboard'
import Posts from './pages/admin/Posts'
import Editor from './pages/admin/Editor'
import PagesAdmin, { NewPage, PageEdit } from './pages/admin/PagesAdmin'
import TagsAdmin from './pages/admin/TagsAdmin'
import MomentsAdmin from './pages/admin/MomentsAdmin'
import CommentsAdmin from './pages/admin/CommentsAdmin'
import MediaAdmin from './pages/admin/MediaAdmin'

type InstallState = 'loading' | 'installed' | 'not-installed' | 'error'

export default function App() {
  const [state, setState] = useState<InstallState>('loading')
  const [error, setError] = useState('')
  const [probeKey, setProbeKey] = useState(0)
  // The session lives in a cookie the script cannot read, so identity is only
  // known after `GET /api/me`. Routing waits for that answer: rendering first
  // would bounce a signed-in operator to the sign-in page on every reload.
  const [authReady, setAuthReady] = useState(false)
  const location = useLocation()

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
        setError(getErrorMessage(reason, '无法连接后台服务'))
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
            <h1>Twilight · 无法启动</h1>
          </div>
          <p className="sub">API UNREACHABLE</p>
          <p className="sec">无法连接后台服务，站点状态未知。请确认后端已启动后重试。</p>
          <p className="sec" style={{ color: 'var(--fg)', wordBreak: 'break-word' }}>{error}</p>
          <Button primary type="button" style={{ width: '100%', justifyContent: 'center' }} onClick={() => setProbeKey((value) => value + 1)}>
            重试
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

  // An installed blog whose session has not been resolved yet shows the same
  // neutral loading screen rather than a sign-in form it may have to retract.
  if (state === 'installed' && !authReady) {
    return (
      <div className="login-bg">
        <div className="kicker">TWILIGHT · LOADING</div>
      </div>
    )
  }

  return (
    <DialogProvider>
      <ToastProvider>
        <Routes>
          <Route path="/install" element={<Setup onInstalled={() => setState('installed')} />} />
          <Route element={<PublicShell />}>
            <Route path="/" element={<Home />} />
            <Route path="/post/:id" element={<Post />} />
            <Route path="/archive" element={<Archive />} />
            <Route path="/tags" element={<Tags />} />
            <Route path="/search" element={<Search />} />
            <Route path="/moments" element={<Moments />} />
            <Route path="/pages/:id" element={<PageDetail />} />
            <Route path="*" element={<NotFound />} />
          </Route>
          {/* One session, two identities: the same guard routes each role to the
              console it owns. */}
          <Route path="/admin/login" element={<SignIn />} />
          <Route path="/admin/register" element={<Register />} />
          <Route path="/admin/forgot" element={<ForgotPassword />} />
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
        </Routes>
      </ToastProvider>
    </DialogProvider>
  )
}
