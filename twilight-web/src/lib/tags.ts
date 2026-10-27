import type { Tag } from 'twilight-blog-api'
import { blog } from './client'

/**
 * 拉取全量标签。
 *
 * 列表接口的单页上限是 12（见 twilight-blog-api 的 pagination 校验），所以标签
 * 超过一页时要翻完剩余页。标签云与文章编辑器的输入建议都需要「全部」，而不是
 * 首页那 12 个。失败由调用方处理：两处的共同点是标签只是增强内容，拿不到不应
 * 当阻断页面主体。
 */
export async function fetchAllTags(): Promise<Tag[]> {
  const first = await blog.listTags({ page: 1, pageSize: 12 })
  if (first.total_pages <= 1) return first.items
  const rest = await Promise.all(
    Array.from({ length: first.total_pages - 1 }, (_, index) =>
      blog.listTags({ page: index + 2, pageSize: 12 }),
    ),
  )
  return [...first.items, ...rest.flatMap((page) => page.items)]
}
