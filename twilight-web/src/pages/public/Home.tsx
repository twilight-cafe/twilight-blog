// Home.tsx —— 公开首页：文章列表 + 分页
// 每页 12 篇，翻页用 Pager 替换整个 articles 状态（非无限滚动）。
// 「阅读时长」是字符数 / 600 的粗略估算，不是真实阅读速度；摘要用 excerpt 截 160 字。

import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Chip, Pagination } from 'ink-design'
import { mediaUrl } from 'twilight-blog-api'
import { blog } from '../../lib/client'
import { excerpt, fmtMonthDay } from '../../lib/format'
import type { Article } from 'twilight-blog-api'
import { useHomeDocumentTitle } from '../../lib/site'
import { Empty, Loading, Reveal } from '../../components/common'

export default function Home() {
  // 首页标题用「Twilight · <站点描述>」，不同于内页的「<页面名> · Twilight」。
  useHomeDocumentTitle()
  const [articles, setArticles] = useState<Article[]>([])
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(1)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let alive = true
    setLoading(true)
    blog.listArticles({ page, pageSize: 12 })
      .then((result) => {
        if (!alive) return
        setArticles(result.items)
        setTotalPages(result.total_pages)
      })
      .catch(() => {
        if (!alive) return
        setArticles([])
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
    <div className="wrap">
      <div className="filterbar rv">
        <Chip solid>全部文章</Chip>
        <span className="sp" />
        <span className="mono" style={{ fontSize: 11, color: 'var(--faint)' }}>INK & CODE</span>
      </div>
      <div className="acards rowmode" style={{ marginTop: 12 }}>
        {loading ? <Loading height={260} /> : articles.length === 0 ? <Empty text="还没有文章" /> : articles.map((article, index) => (
          // has-cover 才切成两栏网格：没有头图时保持原来的单栏排版，
          // 否则每张卡片左侧都会留出一条空白。
          <Reveal key={article.id} as="article" delay={Math.min(index * 0.05, 0.2)} className={`acard${article.cover_media_id === null ? '' : ' has-cover'}`}>
            <Link to={`/post/${article.id}`} style={{ display: 'contents' }}>
              {article.cover_media_id === null ? null : (
                // alt 留空：这张图是文章的装饰，标题已经在旁边以 h3 呈现，
                // 给它写 alt 会让读屏把同一件事念两遍。
                <div className="cover">
                  <img src={mediaUrl(article.cover_media_id)} alt="" loading="lazy" />
                </div>
              )}
              <div className="body">
                <div className="top">
                  <span className="cat">文章</span>
                  {article.tags.slice(0, 1).map((tag) => <span key={tag}>· {tag}</span>)}
                  <span style={{ marginLeft: 'auto' }}>{fmtMonthDay(article.created_at)}</span>
                </div>
                <h3>{article.title}</h3>
                <p className="ex">{excerpt(article.content, 160)}</p>
                <div className="tags">
                  {article.tags.slice(0, 4).map((tag) => <Chip key={tag}># {tag}</Chip>)}
                </div>
                <div className="ft">
                  <span>{Math.max(1, Math.ceil(article.content.length / 600))} 分钟阅读</span>
                  <span>{article.content.length} 字符</span>
                </div>
              </div>
            </Link>
          </Reveal>
        ))}
      </div>
      <Pagination current={page} total={totalPages} pageSize={1} onChange={setPage} />
    </div>
  )
}
