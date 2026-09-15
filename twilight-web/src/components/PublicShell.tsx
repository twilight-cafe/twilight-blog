// PublicShell.tsx —— 公开站点整体布局
// 根据路径区分「首页大标题」与「内页精简头」，正文通过 Outlet 渲染，并把
// setToc 通过 Outlet context 传给子页面（文章页用它把目录回传给侧栏）。
// 导航用的独立页面列表由 loadPagesForNav 拉取；拉取失败时吞掉异常、保留默认导航，
// 不让整站布局崩掉。页脚是公开站点唯一指向控制台的入口，按当前角色跳不同地址。
// 首页标语与页脚简介都来自站长设置的「网站描述」（useSiteDescription），
// 未设置或接口失败时回退到内置文案——这里是描述唯一的展示落点，
// 若写死在这里，后台改完就只是存了个没有人看得到的字符串。

import { useCallback, useEffect, useMemo, useState, type CSSProperties } from 'react'
import { Link, Outlet, useLocation } from 'react-router-dom'
import { NavRow } from 'ink-design'
import type { Page } from 'twilight-blog-api'
import type { TocItem } from '../lib/markdown'
import { buildNav, isNavActive, loadPagesForNav } from '../lib/nav'
import { useCurrentUser } from '../lib/client'
import { useSiteDescription } from '../lib/site'
import PublicSidebar from './PublicSidebar'

export interface PublicOutletContext {
  setToc: (items: TocItem[]) => void
}

export default function PublicShell() {
  const location = useLocation()
  const [pages, setPages] = useState<Page[]>([])
  const [toc, setToc] = useState<TocItem[]>([])
  const navItems = useMemo(() => buildNav(pages), [pages])
  const isHome = location.pathname === '/'
  const description = useSiteDescription()
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
        <header data-header="home">
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
            <div className="sub">
              <span className="slogan">{description}</span>
            </div>
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
        <div>
          <div className="wrap mast slim">
            <Link className="brand" to="/">
              Twilight<i className="ink" />
            </Link>
            <span className="latin">TWILIGHT BLOG — INK &amp; CODE</span>
          </div>
          <div className="wrap nav-slim">
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
      )}

      <div className="public-layout">
        <main className="public-main">
          <Outlet context={{ setToc: setTocItems } satisfies PublicOutletContext} />
        </main>
        <PublicSidebar pages={pages} toc={toc} />
      </div>

      <Footer pages={pages} />
    </div>
  )
}

function Footer({ pages }: { pages: Page[] }) {
  const year = new Date().getFullYear()
  // The footer is the only entry point to the console on the public site, so it
  // points at whatever the current session can actually open.
  const user = useCurrentUser()
  const description = useSiteDescription()
  const accountTo = user ? (user.role === 'admin' ? '/admin' : '/admin/profile') : '/admin/login'
  const accountLabel = user ? (user.role === 'admin' ? '控制台' : '我的资料') : '登录 / 注册'
  return (
    <footer className="site">
      <div className="wrap">
        <div className="cols">
          <div>
            <h6>Twilight TWILIGHT</h6>
            <p>{description}</p>
          </div>
          <div>
            <h6>导航</h6>
            <Link to="/archive">归档</Link>
            <Link to="/tags">标签</Link>
            <Link to="/search">搜索</Link>
            <Link to="/moments">微语</Link>
            {pages.map((page) => (
              <Link key={page.id} to={`/pages/${page.id}`}>{page.title}</Link>
            ))}
          </div>
          <div>
            <h6>更多</h6>
            <Link to="/">首页</Link>
            <Link to={accountTo}>{accountLabel}</Link>
          </div>
        </div>
        <div className="bar">
          <span>© 2021–{year} Twilight · 自托管</span>
          <span>Powered by Go + React</span>
        </div>
      </div>
    </footer>
  )
}
