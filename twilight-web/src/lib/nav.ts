// nav.ts —— 公开站点的导航项构建
// 基础导航是固定的，独立页面（Page）通过接口拉取后追加到末尾。
// 注意分页拉取：listPages 默认 pageSize=12，若总页数 >1 会并发补齐剩余页，
// 但这里的上限是接口返回的总页数（first.total_pages），不会无限翻页。

import type { Page } from 'twilight-blog-api'
import { blog } from './client'
import { isSearchEnabled } from './flags'

export interface NavItem {
  to: string
  label: string
}

const BASE_NAV: NavItem[] = [
  { to: '/', label: '首页' },
  { to: '/archive', label: '归档' },
  { to: '/tags', label: '标签' },
  { to: '/search', label: '搜索' },
  { to: '/moments', label: '微语' },
]

export function buildNav(pages: Page[]): NavItem[] {
  const nav = BASE_NAV.filter((item) => item.to !== '/search' || isSearchEnabled())
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
