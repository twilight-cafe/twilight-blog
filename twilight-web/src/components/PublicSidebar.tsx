// PublicSidebar.tsx —— 公开站点右栏
// 上半部分是文章目录（TOC）：由文章页经 PublicShell 的 Outlet context 注入，
// 按标题层级 (level-1)*14px 缩进；没有目录（如首页）时不渲染。下面固定展示
// 作者卡与导航，并内嵌 ModeToggle。注意 buildNav 在此直接调用、未 useMemo，
// 每次渲染都会重建数组（页面规模小，可忽略）。
//
// 作者卡里的那行小字就是站点描述（站长在「站点设置」里改的那段话），与首页标语、
// 页脚简介同一来源。它曾经是写死在这里的常量：后台改完描述，首页与页脚都变了，
// 只有侧栏还挂着旧文案——一处硬编码就足以让「可配置」这个承诺看起来没兑现。

import { Link, useLocation } from 'react-router-dom'
import type { Page } from 'twilight-blog-api'
import type { TocItem } from '../lib/markdown'
import { buildNav, isNavActive } from '../lib/nav'
import { useSiteDescription } from '../lib/site'
import ModeToggle from './ModeToggle'

export default function PublicSidebar({ pages, toc = [] }: { pages: Page[]; toc?: TocItem[] }) {
  const location = useLocation()
  const navItems = buildNav(pages)
  const description = useSiteDescription()

  return (
    <aside className="public-sidebar" aria-label="侧边栏">
      {toc.length > 0 ? (
        <div className="box toc-box">
          <div className="toc-head">
            <b>目录</b>
            <span className="kicker">TOC</span>
          </div>
          <nav className="toc-list" aria-label="文章目录">
            {toc.map((item) => (
              <a
                key={item.id}
                href={`#${item.id}`}
                style={{ paddingLeft: `${(item.level - 1) * 14}px` }}
              >
                {item.text}
              </a>
            ))}
          </nav>
        </div>
      ) : null}
      <div className="box home-all">
        <div className="author">
          <span className="avatar">T</span>
          <div>
            <b>Twilight</b>
            <p>{description}</p>
          </div>
        </div>
        <nav className="homenav-row" aria-label="侧边栏导航">
          {navItems.map((item) => (
            <Link key={item.to} to={item.to} className={isNavActive(location.pathname, item.to) ? 'on' : ''}>
              {item.label}
            </Link>
          ))}
        </nav>
        <div className="tools">
          <span className="mono">TWILIGHT BLOG</span>
          <span className="sp" />
          <ModeToggle />
        </div>
      </div>
    </aside>
  )
}
