// PublicShell.tsx —— 公开站点整体布局
// 根据路径区分「首页大标题」与「内页精简头」，正文通过 Outlet 渲染，并把
// setToc 通过 Outlet context 传给子页面（文章页用它把目录回传给侧栏）。
// 导航用的独立页面列表由 loadPagesForNav 拉取；拉取失败时吞掉异常、保留默认导航，
// 不让整站布局崩掉。页脚是公开站点唯一指向控制台的入口，按当前角色跳不同地址。
// 页脚简介来自站长设置的「网站描述」（useSiteDescription），未设置或接口失败时
// 回退到内置文案。首页大标题下不再重复这段描述——标语已按设计移除，
// 描述仍出现在页脚、侧栏作者卡、首页标签页标题与搜索引擎摘要里。
// 路由切换时只有正文容器（.public-main）动：页头与侧栏原地不动，与 iOS 的
// 「推入新内容」同一个观感。老内容向左/右滑走淡出，新内容从来的方向推入，
// 容器高度从旧页连续接到新页（页脚被推过去而不是瞬移）；前进一律回到页首、
// 返回回到离开时的滚动位置（见 lib/motion.ts 的 useRouteMotion 与
// components/RouteTransition.tsx 的三阶段编排）。
// 页头本身不参与位移，但它的大小会变：首页是整幅大标题、内页是精简头，两者
// 差约 180px。这段空间同样要连续收放（useHeightRelay），否则正文与页脚会在
// 提交的那一帧被拽动——位移连续了、空间不连续，看起来一样是抖。

import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import { Link, Outlet, useLocation } from 'react-router-dom'
import { BackTop, NavRow } from 'ink-design'
import type { Page } from 'twilight-blog-api'
import type { TocItem } from '../lib/markdown'
import { buildNav, isNavActive, loadPagesForNav } from '../lib/nav'
import { isMomentsEnabled, isSearchEnabled } from '../lib/flags'
import { useCurrentUser } from '../lib/client'
import { useHeightRelay, useRouteMotion } from '../lib/motion'
import { useRouteTransition } from './RouteTransition'
import { useSiteDescription } from '../lib/site'
import { pageTitle } from '../lib/localize'
import { useI18n, useLocale } from '../lib/i18n'
import PublicSidebar from './PublicSidebar'
import Icon from '../lib/icons'

export interface PublicOutletContext {
  setToc: (items: TocItem[]) => void
}

export default function PublicShell() {
  const location = useLocation()
  const [pages, setPages] = useState<Page[]>([])
  const [toc, setToc] = useState<TocItem[]>([])
  const locale = useLocale()
  const { t } = useI18n()
  const navItems = useMemo(() => buildNav(pages, locale), [pages, locale])
  const isHome = location.pathname === '/'
  // 正文容器：路由切换时由它接力播一次入场动画（页头与侧栏保持原地不动，
  // 动的只有「这一页的内容」——与 iOS 的导航推入同一个观感）。前进 / 返回的
  // 方向与滚动位置也由同一个钩子负责；退场与高度接力由 RouteTransition 的
  // phase 驱动（老内容滑走 → 新内容带着新高度滑入）。
  const mainRef = useRef<HTMLElement | null>(null)
  const { phase } = useRouteTransition()
  useRouteMotion(mainRef, phase)
  // 页头的高度接力：首页大标题与内页精简头是两种结构，提交时会整块替换。
  // 两者的高度差（约 180px）由这个钩子连续收放，正文与页脚因此是被推过去
  // 而不是被拽过去。ref 会先后指向两个不同的元素——退场量旧头、入场量新头。
  const headRef = useRef<HTMLElement | null>(null)
  const attachHead = useCallback((el: HTMLElement | null) => {
    headRef.current = el
  }, [])
  useHeightRelay(headRef, phase)
  const setTocItems = useCallback((items: TocItem[]) => {
    setToc(items)
  }, [])

  useEffect(() => {
    let alive = true
    loadPagesForNav()
      .then((items) => {
        if (alive) setPages(items)
      })
      .catch(() => {
        // Keep the default nav if pages cannot be loaded.
      })
    return () => {
      alive = false
    }
  }, [])

  return (
    <div className="site-shell">
      {isHome ? (
        <header data-header="home" ref={attachHead}>
          <div className="wrap mast big">
            <h1>
              <span style={{ '--d': '0.05s' } as CSSProperties}>T</span>
              <span style={{ '--d': '0.15s' } as CSSProperties}>w</span>
              <span style={{ '--d': '0.25s' } as CSSProperties}>i</span>
              <span style={{ '--d': '0.35s' } as CSSProperties}>l</span>
              <span style={{ '--d': '0.45s' } as CSSProperties}>i</span>
              <span style={{ '--d': '0.55s' } as CSSProperties}>g</span>
              <span style={{ '--d': '0.65s' } as CSSProperties}>h</span>
              <span style={{ '--d': '0.75s' } as CSSProperties}>t</span>
              <span className="ink" />
            </h1>
            <div className="dbl" />
          </div>
          <div className="wrap">
            <NavRow>
              {navItems.map((item) => (
                <Link key={item.to} to={item.to} className={`nl ${isNavActive(location.pathname, item.to) ? 'on' : ''}`}>
                  {item.label}
                </Link>
              ))}
              <span className="sp" />
            </NavRow>
          </div>
        </header>
      ) : (
        <div ref={attachHead}>
          {/* 非首页的紧凑头：品牌与导航同一行，菜单紧跟在 Twilight 右侧，
              省掉原先「品牌一行 + 导航一行」的垂直空间。 */}
          <div className="wrap mast slim compact">
            <Link className="brand" to="/">
              Twilight<i className="ink" />
            </Link>
            <div className="nav-slim compact">
              <NavRow>
                {navItems.map((item) => (
                  <Link key={item.to} to={item.to} className={`nl ${isNavActive(location.pathname, item.to) ? 'on' : ''}`}>
                    {item.label}
                  </Link>
                ))}
                <span className="sp" />
              </NavRow>
            </div>
          </div>
        </div>
      )}

      <div className="public-layout">
        <main className="public-main" ref={mainRef}>
          <Outlet context={{ setToc: setTocItems } satisfies PublicOutletContext} />
        </main>
        <PublicSidebar pages={pages} toc={toc} />
      </div>

      <Footer pages={pages} />
      {/* 长页面才需要的返回入口：滚动超过一屏以后才从右下角浮出来。 */}
      <BackTop label={t('回到顶部', 'Back to top')} icon={<Icon name="up" size={16} />} />
    </div>
  )
}

function Footer({ pages }: { pages: Page[] }) {
  const year = new Date().getFullYear()
  // The footer is the only entry point to the console on the public site, so it
  // points at whatever the current session can actually open.
  const user = useCurrentUser()
  const description = useSiteDescription()
  const locale = useLocale()
  const { t } = useI18n()
  const accountTo = user ? (user.role === 'admin' ? '/admin' : '/admin/profile') : '/admin/login'
  const accountLabel = user
    ? (user.role === 'admin' ? t('控制台', 'Console') : t('我的资料', 'My profile'))
    : t('登录 / 注册', 'Sign in / Register')
  return (
    <footer className="site">
      <div className="wrap">
        <div className="cols">
          <div>
            <h6>Twilight TWILIGHT</h6>
            <p>{description}</p>
          </div>
          <div>
            <h6>{t('导航', 'Navigation')}</h6>
            <Link to="/archive">{t('归档', 'Archive')}</Link>
            <Link to="/tags">{t('标签', 'Tags')}</Link>
            {/* 搜索关闭时页脚也不再给出入口：导航、页脚、搜索页三处一致，
                否则「关了搜索」的站点还剩一条死链接。 */}
            {isSearchEnabled() && <Link to="/search">{t('搜索', 'Search')}</Link>}
            {isMomentsEnabled() && <Link to="/moments">{t('微语', 'Moments')}</Link>}
            {pages.map((page) => (
              // 与页头、侧栏同一套回退规则：英文界面取 title_en，缺失时回退中文。
              <Link key={page.id} to={`/pages/${page.id}`}>{pageTitle(page, locale)}</Link>
            ))}
          </div>
          <div>
            <h6>{t('更多', 'More')}</h6>
            <Link to="/">{t('首页', 'Home')}</Link>
            <Link to={accountTo}>{accountLabel}</Link>
          </div>
        </div>
        <div className="bar">
          <span>© 2021–{year} Twilight · {t('自托管', 'Self-hosted')}</span>
          <span>Powered by Go + React</span>
        </div>
      </div>
    </footer>
  )
}
