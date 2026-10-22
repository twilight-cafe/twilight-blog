// nav.ts —— 公开站点的导航项构建
// 基础导航是固定的，独立页面（Page）通过接口拉取后追加到末尾。
// 注意分页拉取：listPages 默认 pageSize=12，若总页数 >1 会并发补齐剩余页，
// 但这里的上限是接口返回的总页数（first.total_pages），不会无限翻页。

import type { Page } from 'twilight-blog-api'
import { blog } from './client'
import { isMomentsEnabled, isSearchEnabled } from './flags'
import { getLocale, translate, type Locale } from './i18n'

export interface NavItem {
  to: string
  label: string
}

/** 每条导航同时给出中英文；locale 由调用方传入，纯函数本身不订阅状态。 */
const BASE_NAV: Array<{ to: string; zh: string; en: string }> = [
  { to: '/', zh: '首页', en: 'Home' },
  { to: '/archive', zh: '归档', en: 'Archive' },
  { to: '/tags', zh: '标签', en: 'Tags' },
  { to: '/search', zh: '搜索', en: 'Search' },
  { to: '/moments', zh: '微语', en: 'Moments' },
]

export function buildNav(pages: Page[], locale: Locale = getLocale()): NavItem[] {
  const nav = BASE_NAV.filter((item) => {
    if (item.to === '/search') return isSearchEnabled()
    // 微语关闭时导航里不再留一条点了就 404 的链接。页脚与侧栏都复用
    // buildNav / isMomentsEnabled，三处保持一致。
    if (item.to === '/moments') return isMomentsEnabled()
    return true
  }).map((item) => ({ to: item.to, label: translate(locale, item.zh, item.en) }))
  return [
    ...nav,
    ...pages.map((page) => ({
      to: `/pages/${page.id}`,
      label: page.title,
    })),
  ]
}

export function isNavActive(pathname: string, to: string): boolean {
  return to === '/' ? pathname === '/' : pathname === to
}

export async function loadPagesForNav(): Promise<Page[]> {
  const first = await blog.listPages({ page: 1, pageSize: 12 })
  if (first.total_pages <= 1) return first.items
  const rest = await Promise.all(
    Array.from({ length: first.total_pages - 1 }, (_, index) =>
      blog.listPages({ page: index + 2, pageSize: 12 }),
    ),
  )
  return [...first.items, ...rest.flatMap((page) => page.items)]
}
