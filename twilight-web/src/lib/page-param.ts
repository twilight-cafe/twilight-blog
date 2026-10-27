// page-param.ts —— 把「当前第几页」持久化到 URL 的 ?page=
//
// 整页只有一个分页器的列表（首页、微语、标签、搜索、后台文章/评论/媒体/用户）
// 都用它。页码进 URL 带来的直接好处是浏览器前进/后退能回到同一页：从列表第 3 页
// 点进一篇文章再按后退，回来仍是第 3 页，而不是从头开始。
//
// 两条约定：
//   - 第 1 页不写进 URL。首页就是 `/`，不会变成 `/?page=1`；从第 2 页翻回第 1 页
//     时删除该参数，地址栏回到干净形态。
//   - setPage 只改 page，保留 q / id 等其他参数。搜索页的 `?q=`、标签页的 `?id=`
//     都靠这一点在翻页时不被冲掉。

import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'

/** URL 里的页码参数名。 */
export const PAGE_QUERY_KEY = 'page'

/**
 * 解析页码：缺失、小数、0、负数、非数字一律回第 1 页。
 *
 * 不抛错是刻意的——页码是表现层状态，用户手改 URL 或旧书签里的垃圾值不该得到
 * 一页错误提示，回第 1 页即可。
 */
export function parsePageParam(raw: string | null): number {
  if (raw === null || raw.trim() === '') return 1
  const value = Number(raw)
  return Number.isInteger(value) && value > 0 ? value : 1
}

/**
 * 页码与 `?page=` 双向绑定。返回的 setPage 走浏览器历史的 push，因此翻页本身
 * 也能被后退键还原；但它不改变路由，列表组件不会重新挂载。
 */
export function usePageParam(): [number, (page: number) => void] {
  const [params, setParams] = useSearchParams()
  const page = parsePageParam(params.get(PAGE_QUERY_KEY))

  const setPage = useCallback((next: number) => {
    // 页码没变就什么都不做：切 tab 时常会顺手 setPage(1)，在已经处于第 1 页的
    // 情况下若照常写 URL，会在历史里压进一条一模一样的记录（后退要按两次）。
    if (next === page) return
    setParams((previous) => {
      const updated = new URLSearchParams(previous)
      if (next <= 1) {
        updated.delete(PAGE_QUERY_KEY)
      } else {
        updated.set(PAGE_QUERY_KEY, String(next))
      }
      return updated
    })
  }, [page, setParams])

  return [page, setPage]
}
