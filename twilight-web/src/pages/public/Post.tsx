// Post.tsx —— 单篇文章页（公开）
// id 从路由参数解析成数字，非法（NaN / ≤0）直接判「文章不存在」，不设 loading。
// 文章加载后，用 extractToc 抽取目录并通过 Outlet context 的 setToc 交给侧栏渲染；
// 卸载或文章变化时清空 TOC，避免残留上一篇文章的目录。
// 评论区此处恒为 enabled（公开文章默认可评）；标题按「文章标题 · Twilight」写入
// document.title，利于 SEO/分享。
//
// 渲染器（KaTeX / highlight.js）是懒加载块，见 lib/markdown-loader.ts：
// prefetch 与文章数据**并行**发出，等到数据回来再开始下载就等于把下载时间整个
// 加到首屏上；目录（extractToc）与正文用的是同一块，所以也走同一个 loader。

import { useEffect, useState } from 'react'
import { Link, useOutletContext, useParams } from 'react-router-dom'
import { Chip, Status } from 'ink-design'
import { mediaUrl } from 'twilight-blog-api'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtDate, fmtNum } from '../../lib/format'
import type { Article } from 'twilight-blog-api'
import { useDocumentTitle } from '../../lib/site'
import { articlePageHead, useCanonicalPath, usePageHead } from '../../lib/head'
import { loadToc, prefetchMarkdown } from '../../lib/markdown-loader'
import Comments from '../../components/Comments'
import ContentView from '../../components/ContentView'
import { Loading } from '../../components/common'
import Icon from '../../lib/icons'
import type { TocItem } from '../../lib/markdown'

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
  const pageTitle = error ? '文章加载失败' : (article?.title ?? '文章')
  useDocumentTitle(pageTitle)

  // head 与标题同源（同一个 pageTitle 也是 og:title 的来源，见 head.ts 契约 4）。
  // 文章还没到手、或 id 根本不存在时整页 noindex：一个指向「不存在」的地址没有
  // 被收录的理由，而它此刻的 canonical 只能指向这个地址本身。
  const canonicalPath = useCanonicalPath()
  usePageHead(
    article
      ? articlePageHead({
        path: canonicalPath,
        title: article.title,
        content: article.content,
        publishedAt: article.created_at,
        image: article.cover_media_id === null ? undefined : mediaUrl(article.cover_media_id),
      })
      : { path: canonicalPath, title: pageTitle, robots: 'noindex, nofollow' },
  )

  // 与下面的文章请求并行开始下载渲染器；失败不在这里处置（正文那边有降级）。
  useEffect(() => {
    prefetchMarkdown()
  }, [])

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
    let alive = true
    // 目录拿不到只是侧栏空着（失败已在 loader 里收敛成空目录），这里只守一件事：
    // 组件已经卸载/换了文章之后，别把上一篇文章的目录写进侧栏。
    loadToc(article.content).then((items) => {
      if (alive) setToc(items)
    })
    return () => {
      alive = false
      setToc([])
    }
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
