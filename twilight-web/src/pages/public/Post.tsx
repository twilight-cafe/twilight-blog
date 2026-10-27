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

import { useEffect, useRef, useState } from 'react'
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
import { ImageGroup } from '../../lib/imageGroup'
import { ScrollProgress } from 'ink-design'
import { SkeletonArticle } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import ProgressiveImage from '../../components/ProgressiveImage'
import Icon from '../../lib/icons'
import { NOOP_SET_TOC, type DocOutletContext } from '../../lib/doc-comments'
import { useI18n, useLocale } from '../../lib/i18n'
import { articleAIGenerated, articleContent, articleTags, articleTitle } from '../../lib/localize'
import { applyIfLatest } from '../../lib/latest'

export default function Post() {
  const { t } = useI18n()
  const locale = useLocale()
  const { id = '' } = useParams()
  const articleId = Number(id)
  const outletContext = useOutletContext<DocOutletContext | undefined>()
  const setToc = outletContext?.setToc ?? NOOP_SET_TOC
  const commentsHosted = outletContext?.commentsHosted === true
  const setCommentUnavailable = outletContext?.setCommentUnavailable
  const [article, setArticle] = useState<Article | null>(null)
  const [error, setError] = useState('')
  // 路由参数变化时组件会被复用：旧文章的响应可能晚于新文章返回。
  // 每次请求递增，响应回来时只允许最新一轮写入状态。
  const requestRef = useRef(0)

  // 标题在这里统一派生，而不是在 load 成功的分支里直接写 document.title：
  // 后者会让「打开一个不存在的 id」把上一条文章的标题一直挂在标签页上。
  const pageTitle = error
    ? t('文章加载失败', 'Failed to load the post')
    : (article ? articleTitle(article, locale) : t('文章', 'Post'))
  useDocumentTitle(pageTitle)

  // head 与标题同源（同一个 pageTitle 也是 og:title 的来源，见 head.ts 契约 4）。
  // 文章还没到手、或 id 根本不存在时整页 noindex：一个指向「不存在」的地址没有
  // 被收录的理由，而它此刻的 canonical 只能指向这个地址本身。
  const canonicalPath = useCanonicalPath()
  usePageHead(
    article
      ? articlePageHead({
        path: canonicalPath,
        title: articleTitle(article, locale),
        content: articleContent(article, locale),
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
      setError(t('文章不存在', 'Post not found'))
      setToc([])
      return
    }
    const requestId = ++requestRef.current
    setArticle(null)
    setError('')
    blog.getArticle(articleId)
      .then((value) => applyIfLatest(requestRef, requestId, () => setArticle(value)))
      .catch((reason) => applyIfLatest(
        requestRef,
        requestId,
        () => setError(getErrorMessage(reason, t('文章不存在', 'Post not found'))),
      ))
    // 卸载或换文章时让这一轮请求作废：迟到的响应不能再覆盖新地址的状态。
    return () => {
      requestRef.current += 1
    }
  }, [articleId, setToc])

  // 评论区由 DocCommentsLayout 常驻渲染；正文 404/加载失败时通知它收掉，
  // 否则错误页下面会挂着一条不存在的评论对象。成功（error 为空）时恢复。
  useEffect(() => {
    setCommentUnavailable?.(error !== '')
  }, [error, setCommentUnavailable])

  useEffect(() => {
    if (!article) {
      setToc([])
      return
    }
    let alive = true
    // 目录必须从**当前语言**的正文里抽：正文按 locale 在 zh/en 之间切换，
    // 只读 article.content 会让英文界面下的目录仍然停在中文，与正文对不上。
    // locale 因此是依赖项——切换语言时重算一次。
    // 目录拿不到只是侧栏空着（失败已在 loader 里收敛成空目录），这里只守一件事：
    // 组件已经卸载/换了文章之后，别把上一篇文章的目录写进侧栏。
    loadToc(articleContent(article, locale)).then((items) => {
      if (alive) setToc(items)
    })
    return () => {
      alive = false
      setToc([])
    }
  }, [article, locale, setToc])

  // 文章骨架要等加载超过阈值才可见（见 lib/loading.ts）；在此之前它已经占住
  // 标题区与正文的位置，所以「内容到达」不会把页面往下推。
  const pending = useDelayedLoading(!article)

  if (error) {
    return (
      <div className="wrap">
        <div className="e404">
          <div><p>{error}</p><Link className="btn pri" to="/">{t('回到首页', 'Back to home')}</Link></div>
        </div>
      </div>
    )
  }

  // 还在取文章：正文的位置先按标题 + 段落的骨架占住，数据到达时不跳版。
  if (!article) {
    return (
      <div className="wrap post-wrap">
        <SkeletonArticle visible={pending} />
      </div>
    )
  }

  return (
    // 一篇 = 一组图：头图与正文图下载并解码完成之后一起出现。key 用文章 id，
    // 切文章时换一个组重新等齐；同一篇文章重渲染不会让已显示的图回到加载态。
    <ImageGroup key={article.id}>
    <div className="wrap post-wrap page-article">
      {/* 阅读进度条固定在视口最上沿，与文章内容是否滚动无关地跟着读。 */}
      <ScrollProgress />
      {/* 头图放在 .phead 之前：于是它落在顶栏之下、标签之上，宽度与正文栏一致，
          侧边栏仍在整栏的右侧（不是被顶到图下面）。 */}
      {article.cover_media_id === null ? null : (
        <div className="post-cover">
          {/* 外框高度固定为站点设置里的 --post-banner-height：顶部与窗口平齐，
              底部在高度末端渐隐（见 app.css 的 .post-cover）。骨架与图片共用
              同一个高度，所以加载态不会改变排版。 */}
          <ProgressiveImage src={mediaUrl(article.cover_media_id)} alt="" />
        </div>
      )}
      {articleAIGenerated(article, locale) ? (
        <div className="ai-notice" role="note">
          <Icon name="zap" size={14} />
          <span>{t('本文由 AI 生成，请自行甄别内容。', 'This post was generated by AI. Please check the facts yourself.')}</span>
        </div>
      ) : null}
      <div className="phead">
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
          {articleTags(article, locale).map((tag) => <Chip key={tag}># {tag}</Chip>)}
          <Status variant="published">{t('已发布', 'Published')}</Status>
        </div>
        <h1>{articleTitle(article, locale)}</h1>
        <div className="meta">
          <span><Icon name="clock" size={13} /> {fmtDate(article.created_at)}</span>
          <span><Icon name="doc" size={13} /> {t(`${fmtNum(articleContent(article, locale).length)} 字符`, `${fmtNum(article.content.length)} characters`)}</span>
        </div>
      </div>
      <ContentView text={articleContent(article, locale)} trusted />
      {/* 在生产布局里评论区由 DocCommentsLayout 渲染，页面不重复挂一份；
          单独渲染本页（测试等）时仍回退到页面内评论区。 */}
      {commentsHosted ? null : (
        <div style={{ marginTop: 34 }}>
          <Comments targetType="article" targetId={article.id} enabled />
        </div>
      )}
    </div>
    </ImageGroup>
  )
}
