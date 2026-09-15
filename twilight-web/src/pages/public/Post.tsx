// Post.tsx —— 单篇文章页（公开）
// id 从路由参数解析成数字，非法（NaN / ≤0）直接判「文章不存在」，不设 loading。
// 文章加载后，用 extractToc 抽取目录并通过 Outlet context 的 setToc 交给侧栏渲染；
// 卸载或文章变化时清空 TOC，避免残留上一篇文章的目录。
// 评论区此处恒为 enabled（公开文章默认可评）；标题按「文章标题 · Twilight」写入
// document.title，利于 SEO/分享。

import { useEffect, useState } from 'react'
import { Link, useOutletContext, useParams } from 'react-router-dom'
import { Chip, Status } from 'ink-design'
import { mediaUrl } from 'twilight-blog-api'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtDate, fmtNum } from '../../lib/format'
import type { Article } from 'twilight-blog-api'
import { useDocumentTitle } from '../../lib/site'
import Comments from '../../components/Comments'
import ContentView from '../../components/ContentView'
import { Loading } from '../../components/common'
import Icon from '../../lib/icons'
import { extractToc, type TocItem } from '../../lib/markdown'

const noopSetToc = () => {}

export default function Post() {
  const { id = '' } = useParams()
  const articleId = Number(id)
  const outletContext = useOutletContext<{ setToc?: (items: TocItem[]) => void } | undefined>()
  const setToc = outletContext?.setToc ?? noopSetToc
  const [article, setArticle] = useState<Article | null>(null)
  const [error, setError] = useState('')

  // 标题在这里统一派生，而不是在 load 成功的分支里直接写 document.title：
  // 后者会让「打开一个不存在的 id」把上一条文章的标题一直挂在标签页上。
  useDocumentTitle(error ? '文章加载失败' : (article?.title ?? '文章'))

  useEffect(() => {
    if (!Number.isFinite(articleId) || articleId <= 0) {
      setArticle(null)
      setError('文章不存在')
      setToc([])
      return
    }
    setArticle(null)
    setError('')
    blog.getArticle(articleId)
      .then(setArticle)
      .catch((reason) => setError(getErrorMessage(reason, '文章不存在')))
  }, [articleId, setToc])

  useEffect(() => {
    if (!article) {
      setToc([])
      return
    }
    setToc(extractToc(article.content))
    return () => setToc([])
  }, [article, setToc])

  if (error) {
    return (
      <div className="wrap">
        <div className="e404">
          <div><p>{error}</p><Link className="btn pri" to="/">回到首页</Link></div>
        </div>
      </div>
    )
  }

  if (!article) return <Loading height={300} />

  return (
    <div className="wrap post-wrap page-article">
      {/* 头图放在 .phead 之前：于是它落在顶栏之下、标签之上，宽度与正文栏一致，
          侧边栏仍在整栏的右侧（不是被顶到图下面）。 */}
      {article.cover_media_id === null ? null : (
        <div className="post-cover">
          <img src={mediaUrl(article.cover_media_id)} alt="" />
        </div>
      )}
      <div className="phead">
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
          {article.tags.map((tag) => <Chip key={tag}># {tag}</Chip>)}
          <Status variant="published">已发布</Status>
        </div>
        <h1>{article.title}</h1>
        <div className="meta">
          <span><Icon name="clock" size={13} /> {fmtDate(article.created_at)}</span>
          <span><Icon name="doc" size={13} /> {fmtNum(article.content.length)} 字符</span>
        </div>
      </div>
      <ContentView text={article.content} trusted />
      <div style={{ marginTop: 34 }}>
        <Comments targetType="article" targetId={article.id} enabled />
      </div>
    </div>
  )
}
