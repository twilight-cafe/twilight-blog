// Moments.tsx —— 微语流（公开）
// 每页 12 条、Pager 翻页替换整页数据。每条微语用 ContentView 渲染（作者可信，
// 传 trusted 走文章管线、不套评论白名单），下面挂可评论区（targetType="moment"）。

import { useEffect, useState } from 'react'
import { blog } from '../../lib/client'
import type { Moment } from 'twilight-blog-api'
import Comments from '../../components/Comments'
import ContentView from '../../components/ContentView'
import { Pagination } from 'ink-design'
import { useDocumentTitle } from '../../lib/site'
import { Empty, Loading, PageHero, Reveal } from '../../components/common'

export default function Moments() {
  useDocumentTitle('微语')
  const [items, setItems] = useState<Moment[]>([])
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(1)
  const [loading, setLoading] = useState(true)

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
            <Comments targetType="moment" targetId={item.id} enabled />
          </Reveal>
        ))}
        <Pagination current={page} total={totalPages} pageSize={1} onChange={setPage} />
      </div>
    </main>
  )
}
