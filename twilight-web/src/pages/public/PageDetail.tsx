// PageDetail.tsx —— 单个独立页面（公开）
// 与 Post 结构一致：id 解析、TOC 经 Outlet context 注入侧栏、内容用 ContentView 渲染。
// 注意：加载失败/不存在时直接 return <NotFound /> 组件（就地显示 404 文案），
// 而非路由跳转，所以浏览器地址栏仍停留在 /pages/<id>。

import { useEffect, useRef, useState } from 'react'
import { useOutletContext, useParams } from 'react-router-dom'
import { blog, getErrorMessage } from '../../lib/client'
import type { Page } from 'twilight-blog-api'
import Comments from '../../components/Comments'
import ContentView from '../../components/ContentView'
import { ImageGroup } from '../../lib/imageGroup'
import { ScrollProgress } from 'ink-design'
import { PageHero, SkeletonArticle } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import { useDocumentTitle } from '../../lib/site'
import { articlePageHead, useCanonicalPath, usePageHead } from '../../lib/head'
import { loadToc, prefetchMarkdown } from '../../lib/markdown-loader'
import NotFound from './NotFound'
import { NOOP_SET_TOC, type DocOutletContext } from '../../lib/doc-comments'
import { useI18n, useLocale } from '../../lib/i18n'
import { pageContent, pageTitle } from '../../lib/localize'
import { applyIfLatest } from '../../lib/latest'

export default function PageDetail() {
  const { t } = useI18n()
  const locale = useLocale()
  const { id = '' } = useParams()
  const pageId = Number(id)
  const outletContext = useOutletContext<DocOutletContext | undefined>()
  const setToc = outletContext?.setToc ?? NOOP_SET_TOC
  const commentsHosted = outletContext?.commentsHosted === true
  const setCommentUnavailable = outletContext?.setCommentUnavailable
  const [page, setPage] = useState<Page | null>(null)
  const [error, setError] = useState('')
  // 与 Post 同一条竞态：/pages/1 → /pages/2 时组件实例复用，
  // 旧页面的响应不能覆盖新页面。
  const requestRef = useRef(0)

  // 标题必须在这里设，不能指望下面 return 的 <NotFound />：子组件的 effect 先于
  // 父组件执行，父组件再设一次会把「页面不存在」覆盖回「页面」。
  // 标题 / 正文都按当前语言取，英文缺失时逐字段回退中文（见 lib/localize.ts）。
  const docTitle = error ? t('页面不存在', 'Page not found') : (page ? pageTitle(page, locale) : t('页面', 'Page'))
  useDocumentTitle(docTitle)

  // 独立页面与文章同样按「文章性内容」输出 head；取不到页面时 noindex（同上理由）。
  const canonicalPath = useCanonicalPath()
  usePageHead(
    page
      ? articlePageHead({
        path: canonicalPath,
        title: pageTitle(page, locale),
        content: pageContent(page, locale),
        publishedAt: page.created_at,
      })
      : { path: canonicalPath, title: docTitle, robots: 'noindex, nofollow' },
  )

  // 与下面的页面请求并行开始下载渲染器（理由见 Post.tsx）。
  useEffect(() => {
    prefetchMarkdown()
  }, [])

  useEffect(() => {
    if (!Number.isFinite(pageId) || pageId <= 0) {
      setPage(null)
      setError(t('页面不存在', 'Page not found'))
      setToc([])
      return
    }
    const requestId = ++requestRef.current
    setPage(null)
    setError('')
    blog.getPage(pageId)
      .then((value) => applyIfLatest(requestRef, requestId, () => setPage(value)))
      .catch((reason) => applyIfLatest(
        requestRef,
        requestId,
        () => setError(getErrorMessage(reason, t('页面不存在', 'Page not found'))),
      ))
    // 卸载或换页面时让这一轮请求作废，避免迟到的响应覆盖新页面。
    return () => {
      requestRef.current += 1
    }
  }, [pageId, setToc])

  // 与 Post 一致：评论宿主由布局常驻，页面不存在时通知它把评论区收掉。
  useEffect(() => {
    setCommentUnavailable?.(error !== '')
  }, [error, setCommentUnavailable])

  useEffect(() => {
    if (!page) {
      setToc([])
      return
    }
    let alive = true
    // 目录从**当前语言**的正文里抽，并把 locale 列为依赖：切换语言时正文变了，
    // 目录必须跟着重算，否则侧栏会停在另一种语言的标题上（与 Post.tsx 一致）。
    // 失败已在 loader 里收敛成空目录（见 Post.tsx 的同一段说明）。
    loadToc(pageContent(page, locale)).then((items) => {
      if (alive) setToc(items)
    })
    return () => {
      alive = false
      setToc([])
    }
  }, [page, locale, setToc])

  const pending = useDelayedLoading(!page)

  if (error) return <NotFound />
  if (!page) {
    return (
      <main className="wrap gpage page-detail">
        <SkeletonArticle visible={pending} />
      </main>
    )
  }

  return (
    // 与文章页同一口径：本页的图下载完再一起出现，切页面时换一个组。
    <ImageGroup key={page.id}>
    <main className="wrap gpage page-detail">
      {/* 与文章页同一根进度条：独立页面同样是「读长文」的场景。 */}
      <ScrollProgress />
      <PageHero title={docTitle} />
      <ContentView text={pageContent(page, locale)} trusted />
      {/* 生产布局里评论区由 DocCommentsLayout 统一渲染；单独渲染时回退到本页。 */}
      {commentsHosted ? null : (
        <>
          <div style={{ marginTop: 30 }}>
            <Comments targetType="page" targetId={page.id} enabled />
          </div>
          <div style={{ height: 70 }} />
        </>
      )}
    </main>
    </ImageGroup>
  )
}
