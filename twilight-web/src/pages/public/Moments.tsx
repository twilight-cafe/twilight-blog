// Moments.tsx —— 微语流（公开）
// 每页 12 条、Pager 翻页替换整页数据。每条微语用 ContentView 渲染（作者可信，
// 传 trusted 走文章管线、不套评论白名单），下面挂**配图行**与**评论按钮**：
//
//   - 配图行走 MediaGallery，与后台微语管理卡（MomentsAdmin）是同一个组件。
//     这里曾经完全没有渲染 item.media_items —— 接口一直返回它（见
//     viewmodel.NewMomentResponseList），只是前台没读，于是带图微语在微语页
//     只剩文字。两边共用一个组件之后，「前台与后台一致」不再靠人记得同步。
//   - 评论区不再随卡片一起挂载：Comments 一挂载就请求 GET /api/comments，
//     12 条微语 = 12 个请求，而多数评论区根本没人展开。现在由 MomentComments
//     的「评论」按钮触发（见该组件文件头）。
//
// 每条仍是独立的 reveal 动画（延迟按序号错峰，最多 0.2s）。

import { useEffect, useState } from 'react'
import { blog } from '../../lib/client'
import type { Moment } from 'twilight-blog-api'
import ContentView from '../../components/ContentView'
import MomentComments from '../../components/MomentComments'
import MediaGallery from '../../components/media/MediaGallery'
import { Pagination } from 'ink-design'
import { useDocumentTitle } from '../../lib/site'
import { useCanonicalPath, usePageHead } from '../../lib/head'
import { prefetchMarkdown } from '../../lib/markdown-loader'
import { Empty, Loading, PageHero, Reveal } from '../../components/common'

export default function Moments() {
  useDocumentTitle('微语')
  usePageHead({ path: useCanonicalPath(), title: '微语' })
  const [items, setItems] = useState<Moment[]>([])
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(1)
  const [loading, setLoading] = useState(true)

  // 与下面的列表请求并行开始下载渲染器（每条微语都由 ContentView 渲染，理由见 Post.tsx）。
  useEffect(() => {
    prefetchMarkdown()
  }, [])

  useEffect(() => {
    let alive = true
    setLoading(true)
    blog.listMoments({ page, pageSize: 12 })
      .then((result) => {
        if (!alive) return
        setItems(result.items)
        setTotalPages(result.total_pages)
      })
      .catch(() => {
        if (!alive) return
        setItems([])
        setTotalPages(1)
      })
      .finally(() => {
        if (alive) setLoading(false)
      })

    return () => {
      alive = false
    }
  }, [page])

  return (
    <main className="wrap">
      <PageHero kicker="MOMENTS / 微语" title={<>碎碎念<small>微语</small></>} subtitle="比文章短，比日常长。" />
      <div className="momfeed">
        {loading ? <Loading height={260} /> : items.length === 0 ? <Empty text="还没有微语" /> : items.map((item, index) => (
          <Reveal key={item.id} className="mom" delay={Math.min(index * 0.05, 0.2)}>
            <ContentView text={item.content} className="mom-content" trusted />
            {/* media_items 的类型是必填数组，但混用更旧的后端时字段可能缺席，
                所以与后台卡片保持同一个守卫写法。 */}
            {item.media_items && item.media_items.length > 0 ? <MediaGallery items={item.media_items} /> : null}
            <MomentComments momentId={item.id} />
          </Reveal>
        ))}
        <Pagination align="center" current={page} total={totalPages} pageSize={1} onChange={setPage} />
      </div>
    </main>
  )
}
