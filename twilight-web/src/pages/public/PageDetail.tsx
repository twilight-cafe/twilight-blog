// PageDetail.tsx —— 单个独立页面（公开）
// 与 Post 结构一致：id 解析、TOC 经 Outlet context 注入侧栏、内容用 ContentView 渲染。
// 注意：加载失败/不存在时直接 return <NotFound /> 组件（就地显示 404 文案），
// 而非路由跳转，所以浏览器地址栏仍停留在 /pages/<id>。

import { useEffect, useState } from 'react'
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
import type { TocItem } from '../../lib/markdown'
import { useI18n } from '../../lib/i18n'

const noopSetToc = () => {}

export default function PageDetail() {
  const { t } = useI18n()
  const { id = '' } = useParams()
  const pageId = Number(id)
  const outletContext = useOutletContext<{ setToc?: (items: TocItem[]) => void } | undefined>()
  const setToc = outletContext?.setToc ?? noopSetToc
  const [page, setPage] = useState<Page | null>(null)
  const [error, setError] = useState('')

  // 标题必须在这里设，不能指望下面 return 的 <NotFound />：子组件的 effect 先于
  // 父组件执行，父组件再设一次会把「页面不存在」覆盖回「页面」。
  const pageTitle = error ? t('页面不存在', 'Page not found') : (page?.title ?? t('页面', 'Page'))
  useDocumentTitle(pageTitle)

  // 独立页面与文章同样按「文章性内容」输出 head；取不到页面时 noindex（同上理由）。
  const canonicalPath = useCanonicalPath()
  usePageHead(
    page
      ? articlePageHead({
        path: canonicalPath,
        title: page.title,
        content: page.content,
        publishedAt: page.created_at,
      })
      : { path: canonicalPath, title: pageTitle, robots: 'noindex, nofollow' },
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
    setPage(null)
    setError('')
    blog.getPage(pageId)
      .then(setPage)
      .catch((reason) => setError(getErrorMessage(reason, t('页面不存在', 'Page not found'))))
  }, [pageId, setToc])

  useEffect(() => {
    if (!page) {
      setToc([])
      return
    }
    let alive = true
    // 失败已在 loader 里收敛成空目录（见 Post.tsx 的同一段说明）。
    loadToc(page.content).then((items) => {
      if (alive) setToc(items)
    })
    return () => {
      alive = false
      setToc([])
    }
  }, [page, setToc])

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
      <PageHero kicker="PAGE" title={page.title} />
      <ContentView text={page.content} trusted />
      <div style={{ marginTop: 30 }}>
        <Comments targetType="page" targetId={page.id} enabled />
      </div>
      <div style={{ height: 70 }} />
    </main>
    </ImageGroup>
  )
}
