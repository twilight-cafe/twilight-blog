// doc-comments.ts —— 文章页与独立页面共用的评论宿主契约
//
// /post/:id 与 /pages/:id 的内容不同，但评论区的行为完全一致。让两者共用同一个
// 布局路由（见 components/DocCommentsLayout.tsx），评论区在两种页面之间切换时
// 就不会被卸载重挂：评论框留在原地，只是把 targetType / targetId 换成新页面。
//
// 这里只放「从地址解析评论对象」和页面 ↔ 布局之间的最小上下文。页面单独渲染
// （测试、Storybook）时没有布局上下文，仍然回退到页面内的评论区。

import type { TocItem } from './markdown'

export interface DocCommentTarget {
  type: 'article' | 'page'
  id: number
  /** 稳定的对象标识；布局用它判断失败状态属于哪一个页面。 */
  key: string
}

/** 外壳 → 布局 → 正文页逐层透传的 Outlet context。 */
export interface DocOutletContext {
  setToc?: (items: TocItem[]) => void
  /** 为 true 时评论区由布局统一渲染，页面自身不再重复挂一份。 */
  commentsHosted?: boolean
  /** 正文加载失败时告诉布局：这个评论对象不存在，别把评论区留在 404 下面。 */
  setCommentUnavailable?: (unavailable: boolean) => void
}

export const NOOP_SET_TOC = () => {}

/**
 * 从 pathname 解析评论对象。
 *
 * 不使用 useParams：布局路由没有自己的路径段，参数来自子路由；直接从地址取
 * 更直观，也让「非法 id」在渲染前就得到 null。Number 的语义与页面的 id 解析
 * 保持一致（'1e3' 与 '0x10' 都和正文页得到同一个数字）。
 */
export function parseDocCommentTarget(pathname: string): DocCommentTarget | null {
  const [section, rawId] = pathname.split('/').filter(Boolean)
  const type = section === 'post' ? 'article' : section === 'pages' ? 'page' : null
  const id = Number(rawId)
  if (type === null || !Number.isFinite(id) || id <= 0) return null
  return { type, id, key: `${type}:${id}` }
}
