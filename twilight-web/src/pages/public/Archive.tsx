// Archive.tsx —— 归档页：按年份分组展示全部文章
// 注意 fetchAllArticles 一次性拉取所有文章（翻完所有分页），不做分页/虚拟滚动，
// 文章量很大时这一页会一次性请求很多接口并持有全部数据——属于已知瓶颈。
// 年份计算复用「时间戳 >1e12 视为毫秒否则视为秒」的隐式约定（见 lib/format）。
// 列表按创建时间倒序、再按年聚合；标签为空时用空 <span /> 占位以保持布局对齐。

import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { blog } from '../../lib/client'
import { fmtMonthDay } from '../../lib/format'
import type { Article } from 'twilight-blog-api'
import { useDocumentTitle } from '../../lib/site'
import { Loading, PageHero, Reveal } from '../../components/common'

async function fetchAllArticles(): Promise<Article[]> {
  const first = await blog.listArticles({ page: 1, pageSize: 12 })
  if (first.total_pages <= 1) return first.items
  const rest = await Promise.all(
    Array.from({ length: first.total_pages - 1 }, (_, index) =>
      blog.listArticles({ page: index + 2, pageSize: 12 }),
    ),
  )
  return [...first.items, ...rest.flatMap((page) => page.items)]
}

export default function Archive() {
  useDocumentTitle('归档')
  const [articles, setArticles] = useState<Article[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    setLoading(true)
    fetchAllArticles()
      .then(setArticles)
      .catch(() => setArticles([]))
      .finally(() => setLoading(false))
  }, [])

  const groups = useMemo(() => {
    const map = new Map<number, Article[]>()
    for (const article of [...articles].sort((a, b) => b.created_at - a.created_at)) {
      const year = new Date(article.created_at > 1e12 ? article.created_at : article.created_at * 1000).getFullYear()
      const list = map.get(year) ?? []
      list.push(article)
      map.set(year, list)
    }
    return [...map.entries()]
  }, [articles])

  if (loading) return <Loading height={300} />
  const total = articles.length

  return (
    <main className="wrap page-list">
      <PageHero kicker="ARCHIVE" title={<>归档<small>共 {total} 篇 · {groups.length} 年</small></>} subtitle="所有文字按时间封存，随时可以回头。" />
      {groups.map(([year, items]) => (
        <div key={year}>
          <div className="tl-y rv">{year}<small>{items.length} 篇</small></div>
          {items.map((article, index) => (
            <Reveal key={article.id} className="tl-row" delay={Math.min(index * 0.04, 0.2)}>
              <Link to={`/post/${article.id}`} style={{ display: 'contents' }}>
                <time>{fmtMonthDay(article.created_at)}</time>
                <b>{article.title}</b>
                {article.tags[0] ? <span className="chip">{article.tags[0]}</span> : <span />}
              </Link>
            </Reveal>
          ))}
        </div>
      ))}
      <div style={{ height: 70 }} />
    </main>
  )
}
