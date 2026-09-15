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
import { Loading, PageHero } from '../../components/common'
import { useDocumentTitle } from '../../lib/site'
import NotFound from './NotFound'
import { extractToc, type TocItem } from '../../lib/markdown'

const noopSetToc = () => {}

export default function PageDetail() {
  const { id = '' } = useParams()
  const pageId = Number(id)
  const outletContext = useOutletContext<{ setToc?: (items: TocItem[]) => void } | undefined>()
  const setToc = outletContext?.setToc ?? noopSetToc
  const [page, setPage] = useState<Page | null>(null)
  const [error, setError] = useState('')

  // 标题必须在这里设，不能指望下面 return 的 <NotFound />：子组件的 effect 先于
  // 父组件执行，父组件再设一次会把「页面不存在」覆盖回「页面」。
  useDocumentTitle(error ? '页面不存在' : (page?.title ?? '页面'))

  useEffect(() => {
    if (!Number.isFinite(pageId) || pageId <= 0) {
      setPage(null)
      setError('页面不存在')
      setToc([])
      return
    }
    setPage(null)
    setError('')
    blog.getPage(pageId)
      .then(setPage)
      .catch((reason) => setError(getErrorMessage(reason, '页面不存在')))
  }, [pageId, setToc])

  useEffect(() => {
    if (!page) {
      setToc([])
      return
    }
    setToc(extractToc(page.content))
    return () => setToc([])
  }, [page, setToc])

  if (error) return <NotFound />
  if (!page) return <Loading height={300} />

  return (
    <main className="wrap gpage page-detail">
      <PageHero kicker="PAGE" title={page.title} />
      <ContentView text={page.content} trusted />
      <div style={{ marginTop: 30 }}>
        <Comments targetType="page" targetId={page.id} enabled />
      </div>
      <div style={{ height: 70 }} />
    </main>
  )
}
