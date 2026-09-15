// Dashboard.tsx —— 后台仪表盘（纯展示，从简）
// 五个计数都只用 listXxx({pageSize:1}) 取 total 字段，不拉内容；用 allSettled
// 保证某个接口（如评论）失败时其余计数仍能显示。

import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { blog } from '../../lib/client'
import { Loading } from '../../components/common'

interface Overview {
  posts: number
  pages: number
  tags: number
  moments: number
  comments: number
}

export default function Dashboard() {
  const [overview, setOverview] = useState<Overview | null>(null)

  useEffect(() => {
    void Promise.allSettled([
      blog.listArticles({ page: 1, pageSize: 1 }),
      blog.listPages({ page: 1, pageSize: 1 }),
      blog.listTags({ page: 1, pageSize: 1 }),
      blog.listMoments({ page: 1, pageSize: 1 }),
      blog.listAllComments({ page: 1, pageSize: 1 }),
    ]).then((results) => {
      const totals = results.map((result) => (result.status === 'fulfilled' ? result.value.total : 0))
      setOverview({
        posts: totals[0],
        pages: totals[1],
        tags: totals[2],
        moments: totals[3],
        comments: totals[4],
      })
    })
  }, [])

  if (!overview) return <Loading height={400} />

  const cards = [
    { label: '文章', value: overview.posts, to: '/admin/posts' },
    { label: '页面', value: overview.pages, to: '/admin/pages' },
    { label: '标签', value: overview.tags, to: '/admin/tags' },
    { label: '微语', value: overview.moments, to: '/admin/moments' },
    { label: '评论', value: overview.comments, to: '/admin/comments' },
  ]

  return (
    <>
      <div className="ph">
        <h2>仪表盘</h2>
        <span className="kicker" style={{ marginTop: 6 }}>OVERVIEW</span>
      </div>
      <div className="stat-grid">
        {cards.map((card) => (
          <Link key={card.label} className="stat" to={card.to}>
            <div className="stat-body">
              <b>{card.value}</b>
              <span>{card.label}</span>
            </div>
          </Link>
        ))}
      </div>
      <div className="pcard rv" style={{ marginTop: 20 }}>
        <div className="ph2"><b>快捷入口</b></div>
        <div className="quick-grid">
          <Link className="btn pri sm" to="/admin/editor">写文章</Link>
          <Link className="btn gho sm" to="/admin/pages">管理页面</Link>
          <Link className="btn gho sm" to="/admin/moments">发布微语</Link>
        </div>
      </div>
    </>
  )
}
