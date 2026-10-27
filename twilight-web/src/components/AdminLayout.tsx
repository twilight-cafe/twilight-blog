// AdminLayout.tsx —— 后台控制台框架（含路由守卫）
// 角色分流：admin 看到完整 ADMIN_MENU；visitor 账号只能看自己的「我的资料」，
// 且会被重定向到 USER_HOME。真正的权限仍由后端 403 兜底，这里只是避免渲染
// 注定会 403 的菜单项。
// 侧栏计数：用 listXxx({pageSize:1}) 只取 total，结果按 30s TTL 缓存（模块级
// countsCache），避免每次切换路由都打 5 个请求；用 allSettled 保证单个接口
// 403/失败不会拖垮其余计数。
// 会话过期检测：hadSession ref 记录「曾经登录过」，中途身份变 null 才提示过期、
// 而不是当成首次陌生访问。App 在渲染路由前已解析过会话，所以这里 user 为 null
// 即「已登出」而非「还在加载」。

import { Suspense, useCallback, useEffect, useRef, useState } from 'react'
import { Link, Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { AdminNav, IconButton } from 'ink-design'
import { blog, logout, useCurrentUser } from '../lib/client'
import { useDocumentTitle, useSiteAvatar } from '../lib/site'
import { useMomentsEnabled } from '../lib/flags'
import { useI18n } from '../lib/i18n'
import LanguageSwitcher from './LanguageSwitcher'
import { useCanonicalPath, usePageHead } from '../lib/head'
import { useRouteMotion } from '../lib/motion'
import { useRouteTransition } from './RouteTransition'
import { useToast } from '../lib/toast'
import Icon from '../lib/icons'
import ModeToggle from './ModeToggle'
import ProgressiveImage from './ProgressiveImage'
import { Loading } from './common'

const ADMIN_MENU = [
  { key: 'posts', to: '/admin/posts', icon: 'doc', zh: '文章', en: 'Posts' },
  { key: 'pages', to: '/admin/pages', icon: 'page', zh: '页面', en: 'Pages' },
  { key: 'tags', to: '/admin/tags', icon: 'tag', zh: '标签', en: 'Tags' },
  { key: 'moments', to: '/admin/moments', icon: 'zap', zh: '微语', en: 'Moments' },
  { key: 'comments', to: '/admin/comments', icon: 'msg', zh: '评论', en: 'Comments' },
  { key: 'media', to: '/admin/media', icon: 'img', zh: '媒体', en: 'Media' },
  { key: 'users', to: '/admin/users', icon: 'set', zh: '用户', en: 'Users' },
  { key: 'site', to: '/admin/site-settings', icon: 'pal', zh: '站点', en: 'Site' },
  { key: 'profile', to: '/admin/profile', icon: 'user', zh: '账号', en: 'Account' },
]

/**
 * A visitor account owns only its own profile, so the console collapses to that
 * single page. The server enforces the same split; this only avoids rendering
 * navigation that would answer 403.
 */
const USER_MENU = [{ key: 'profile', to: '/admin/profile', icon: 'user', zh: '我的资料', en: 'My profile' }]

const USER_HOME = '/admin/profile'

/**
 * Sidebar counts change rarely, so they are cached for a short window instead
 * of firing five `pageSize: 1` requests on every navigation.
 */
const COUNTS_TTL_MS = 30_000

let countsCache: { at: number; value: Record<string, number> } | null = null
let countsInFlight: Promise<Record<string, number>> | null = null

/**
 * 仅供测试：清空计数缓存。
 *
 * 它是模块级缓存且有 30s TTL，跨用例存活——一个用例渲染过控制台之后，下一个用例
 * 拿到的是上一条用例的计数（表现为「徽章怎么没了」，且只在某些用例顺序下出现）。
 * 与 site.ts 的 resetSiteCacheForTests 同一先例。
 */
export function resetCountsCacheForTests(): void {
  countsCache = null
  countsInFlight = null
}

function fetchCounts(): Promise<Record<string, number>> {
  const now = Date.now()
  if (countsCache && now - countsCache.at < COUNTS_TTL_MS) return Promise.resolve(countsCache.value)
  if (countsInFlight) return countsInFlight

  countsInFlight = Promise.allSettled([
    blog.listArticles({ page: 1, pageSize: 1 }),
    blog.listPages({ page: 1, pageSize: 1 }),
    blog.listTags({ page: 1, pageSize: 1 }),
    blog.listMoments({ page: 1, pageSize: 1 }),
    blog.listAllComments({ page: 1, pageSize: 1 }),
  ]).then((results) => {
    // value 兜底：接口 2xx 但响应体缺 total（或非对象）时按 0 计，不让单个
    // 异常形状的响应把整个 fetchCounts 变成 rejected（调用方未接 catch）。
    const totals = results.map(
      (result) => (result.status === 'fulfilled' ? (result.value?.total ?? 0) : 0),
    )
    const value = {
      posts: totals[0],
      pages: totals[1],
      tags: totals[2],
      moments: totals[3],
      comments: totals[4],
    }
    countsCache = { at: Date.now(), value }
    return value
  }).finally(() => {
    countsInFlight = null
  })

  return countsInFlight
}

export default function AdminLayout() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const location = useLocation()
  const toast = useToast()
  const user = useCurrentUser()
  const [counts, setCounts] = useState<Record<string, number>>({})
  const [loggingOut, setLoggingOut] = useState(false)

  const isAdmin = user?.role === 'admin'
  // Remembers that a console was rendered, so losing the identity mid-session
  // can be reported as an expiry rather than as a fresh visit by a stranger.
  const hadSession = useRef(false)
  // 内容区在路由切换时先退场再入场；侧栏与顶栏不动，与公开站同一个观感
  // （方向、高度接力与滚动位置见 lib/motion.ts 的 useRouteMotion，
  // 三阶段编排见 components/RouteTransition.tsx）。
  // 钩子必须在下面的守卫早退之前调用（早退会让 hook 数量变化）。
  const panelRef = useRef<HTMLDivElement | null>(null)
  const { phase } = useRouteTransition()
  useRouteMotion(panelRef, phase)

  useEffect(() => {
    if (user) {
      hadSession.current = true
      return
    }
    if (hadSession.current) {
      hadSession.current = false
      countsCache = null
      toast(t('登录已过期，请重新登录', 'Your session expired. Please sign in again.'))
    }
  }, [user, toast])

  useEffect(() => {
    if (!isAdmin) {
      setCounts({})
      return
    }
    let alive = true
    // allSettled 保证单个接口失败不会拖垮其余计数，fetchCounts 因此不可能 reject，
    // 无需 catch。卸载后（alive=false）迟到的结果直接丢弃。
    void fetchCounts()
      .then((value) => {
        if (alive) setCounts(value)
      })
    return () => {
      alive = false
    }
  }, [isAdmin, location.pathname])

  const signOut = useCallback(async () => {
    setLoggingOut(true)
    try {
      // Revoking the session is best effort; `logout()` always drops the cached
      // identity, so a network failure cannot leave the console looking signed in.
      await logout()
      countsCache = null
      hadSession.current = false
      toast(t('已退出登录', 'Signed out'))
      navigate('/admin/login', { replace: true })
    } finally {
      setLoggingOut(false)
    }
  }, [navigate, toast])

  // 菜单与当前页标签在守卫之前算出来，好让 document.title 也走同一条来源。
  // 菜单标签就是使用者对这一页最自然的称呼，比在每个页面里各写一次可靠。
  // 后台各页面**不要**再自行设置 document.title：React 的子 effect 先于父 effect
  // 执行，这里会把它覆盖掉，只会留下一条看起来生效、其实永远看不到的语句。
  // 微语关闭时后台菜单也一并隐藏：那个入口打开的是一个所有接口都返回 404 的页面。
  // 路由本身仍保留，直接输入地址时会由页面内的「未启用」提示兜住。
  // 这里用响应式版本：站长在「站点设置」里关掉微语后，侧栏菜单要立即消失，
  // 而不是等下一次换页才重读外壳属性（dataset 变化不会触发 React 重渲染）。
  const momentsEnabled = useMomentsEnabled()
  const menu = (isAdmin ? ADMIN_MENU : USER_MENU)
    .filter((item) => item.key !== 'moments' || momentsEnabled)
    .map((item) => ({ ...item, label: t(item.zh, item.en) }))
  const current = menu.find((item) => item.to === location.pathname)?.label
    || (location.pathname === '/admin' ? t('概览', 'Overview') : (isAdmin ? t('管理', 'Manage') : t('账号', 'Account')))
  useDocumentTitle(current)
  // 控制台整体 noindex, nofollow：这些页面本身要求登录，没有任何被收录的理由，
  // 而 nufollow 也让「/admin/* 下的链接」不再被抓取器顺着走。
  usePageHead({ path: useCanonicalPath(), title: current, robots: 'noindex, nofollow' })
  // 站长头像走 /api/site 的公开快照（useSiteAvatar），与前台侧栏作者卡同源；
  // 换头像后 refreshSiteMeta 会更新这份缓存，后台侧栏因此无需再单独拉取。
  // hook 必须在下面的早退之前调用：身份从有到无的重渲染会少执行 hook，
  // React 直接报「Rendered fewer hooks than expected」。
  const avatarUrl = useSiteAvatar()

  // App resolves the session before rendering any route, so a missing identity
  // here means "signed out", not "still loading".
  if (!user) {
    return <Navigate to="/admin/login" replace />
  }

  if (!isAdmin && location.pathname !== USER_HOME) {
    return <Navigate to={USER_HOME} replace />
  }

  const displayName = isAdmin ? (user.name || t('管理员', 'Admin')) : (user.user?.nickname || t('用户', 'User'))
  const roleLabel = isAdmin ? t('管理员', 'Administrator') : (user.user?.email || t('普通用户', 'Visitor'))

  return (
    <div className="shell">
      <aside className="aside">
        <Link className="logo" to={isAdmin ? '/admin' : USER_HOME}>
          <span className="sq">T</span>
          <span>Twilight<small>{isAdmin ? 'ADMIN CONSOLE' : 'ACCOUNT'}</small></span>
        </Link>
        <AdminNav>
          {menu.map((item) => (
            <Link key={item.to} to={item.to} className={location.pathname === item.to || (item.to !== '/admin' && location.pathname.startsWith(item.to + '/')) ? 'on' : ''}>
              <Icon name={item.icon} size={16} />
              <span className="lb">{item.label}</span>
              {counts[item.key] ? <span className="bdg">{counts[item.key]}</span> : null}
            </Link>
          ))}
        </AdminNav>
        <div className="a-user">
          {/* displayName 恒非空（后备「管理员」/「用户」兜底），无需再留空串守卫。
              有头像就显示头像，没有才回退首字母占位——之前这里永远只渲染占位块，
              站长换了头像后台也看不到（本次修复的缺陷）。 */}
          {avatarUrl
            // 与前台的作者卡同一个组件：同一张图在两处都走骨架 → 淡入。
            ? <ProgressiveImage className="avatar" frameClassName="round" src={avatarUrl} alt={displayName} />
            : <span className="cav">{displayName.slice(0, 1).toUpperCase()}</span>}
          <div className="info"><b>{displayName}</b><span>{roleLabel}</span></div>
          <IconButton size="sm" title={t('退出登录', 'Sign out')} disabled={loggingOut} onClick={() => void signOut()}>
            <Icon name="out" size={14} />
          </IconButton>
        </div>
      </aside>
      <div className="main">
        <header className="topbar">
          {/* 面包屑后半段随路由变化：key 绑在当前页名上，换页时它重新挂载，
              淡入一次——侧栏与顶栏原地不动，只有这一个词跟着内容换。 */}
          <span className="bc">{isAdmin ? t('后台', 'Console') : t('账号', 'Account')} / <b key={current}>{current}</b></span>
          <span className="sp" />
          <Link className="sbtn" to="/" target="_blank" rel="noreferrer">
            <Icon name="eye" size={13} />{t('查看网页', 'View site')}
          </Link>
          <LanguageSwitcher />
          <ModeToggle />
        </header>
        <div className="panel" ref={panelRef}>
          {/* 后台页面按路由懒加载（见 App.tsx）。边界放在这一层而不是整个 Routes
              外面：加载期间侧栏与顶栏保持挂载，只有内容区显示加载态，不会整屏闪。 */}
          <Suspense fallback={<Loading height={300} />}>
            <Outlet />
          </Suspense>
        </div>
      </div>
    </div>
  )
}
