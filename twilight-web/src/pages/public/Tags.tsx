// Tags.tsx —— 标签云 + 按标签聚合文章
// 标签一次性全量拉取（fetchAllTags），字号按 article_count 相对最大值分四级。
// 选中的标签从 URL 的 ?id= 读取；点已选标签回到 /tags。两个加载 effect 都依赖
// reloadKey（重试按钮 +1）以强制重跑。
// 页码由 selected 变化时重置（见下面的 effect）：两个标签各自有自己的第 1 页，
// 带着上一页的 page 换标签会从第 2 页开始取，静默漏掉新标签的第一页文章。

import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Button, Pagination } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtDate } from '../../lib/format'
import type { Article, Tag } from 'twilight-blog-api'
import { useDocumentTitle } from '../../lib/site'
import { useCanonicalPath, usePageHead } from '../../lib/head'
import { Loading, PageHero, Reveal } from '../../components/common'

async function fetchAllTags(): Promise<Tag[]> {
  const first = await blog.listTags({ page: 1, pageSize: 12 })
  if (first.total_pages <= 1) return first.items
  const rest = await Promise.all(
    Array.from({ length: first.total_pages - 1 }, (_, index) =>
      blog.listTags({ page: index + 2, pageSize: 12 }),
    ),
  )
  return [...first.items, ...rest.flatMap((page) => page.items)]
}

function tagSize(count: number, max: number): 't1' | 't2' | 't3' | 't4' {
  // max 由 Math.max(1, ...) 得出，恒 >= 1，无需除零守卫。
  const ratio = count / max
  if (ratio > 0.75) return 't4'
  if (ratio > 0.5) return 't3'
  if (ratio > 0.3) return 't2'
  return 't1'
}

export default function Tags() {
  const [params] = useSearchParams()
  const selected = Number(params.get('id')) || 0
  const [tags, setTags] = useState<Tag[]>([])
  const [articles, setArticles] = useState<Article[]>([])
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(1)
  const [loadingTags, setLoadingTags] = useState(true)
  const [loadingArticles, setLoadingArticles] = useState(false)
  const [tagsError, setTagsError] = useState('')
  const [articlesError, setArticlesError] = useState('')
  const [reloadKey, setReloadKey] = useState(0)

  // 选中某个标签时标题跟着走：从搜索结果点回来的一堆标签页里，
  // 「标签 # Go」比五个一模一样的「标签」有用得多。标签还没加载完时退回「标签」。
  const selectedTag = tags.find((tag) => tag.id === selected)
  const pageTitle = selectedTag ? `标签 ${selectedTag.name}` : '标签'
  useDocumentTitle(pageTitle)
  // `?id=N` 是**各自独立的内容**（某标签下的文章列表），所以 canonical 指向自身
  // 而不是收敛到 /tags——收敛会让第二、三个标签页几乎不可能被收录。
  usePageHead({ path: useCanonicalPath(), title: pageTitle })

  useEffect(() => {
    let alive = true
    setLoadingTags(true)
    setTagsError('')

    fetchAllTags()
      .then((result) => {
        if (!alive) return
        setTags(result)
      })
      .catch((error) => {
        if (!alive) return
        setTags([])
        setTagsError(getErrorMessage(error, '标签加载失败'))
      })
      .finally(() => {
        if (alive) setLoadingTags(false)
      })

    return () => {
      alive = false
    }
  }, [reloadKey])

  // 换标签时页码必须归 1。放一个 effect 里 setPage(1) 是不够的：两个 effect 在**同一次
  // 提交**里依次执行，请求 effect 读到的是本次渲染闭包里的旧 page，会先带着旧页码
  // （比如上一个标签的第 2 页）发一次请求，下一轮才用 page=1 再发一次。后果不只是多一个
  // 请求——若那个失效请求的响应后到，它会把列表盖成新标签第 2 页的内容（可能是空的），
  // 用户看到「该标签下暂无文章」。
  //
  // 所以这里在渲染期就把 page 归位，让本次渲染的闭包直接拿到 1。
  // React 官方推荐「渲染期 adjust state」这一写法：组件随后立刻重渲染、丢弃这次输出，
  // 代价比 effect 里的往返小，也不会漏出中间态。依赖里不放 page，
  // 否则用户手动翻页会被立刻打回第 1 页。
  const [pagedTag, setPagedTag] = useState(selected)
  if (pagedTag !== selected) {
    setPagedTag(selected)
    setPage(1)
  }

  useEffect(() => {
    if (!selected) {
      setArticles([])
      setTotalPages(1)
      setArticlesError('')
      return
    }

    let alive = true
    setLoadingArticles(true)
    setArticlesError('')

    blog.getTagArticles(selected, { page, pageSize: 12 })
      .then((result) => {
        if (!alive) return
        setArticles(result.items)
        setTotalPages(result.total_pages)
      })
      .catch((error) => {
        if (!alive) return
        setArticles([])
        setTotalPages(1)
        setArticlesError(getErrorMessage(error, '文章加载失败'))
      })
      .finally(() => {
        if (alive) setLoadingArticles(false)
      })

    return () => {
      alive = false
    }
  }, [reloadKey, page, selected])

  if (loadingTags) return <Loading height={300} />

  if (tagsError) {
    return (
      <main className="wrap page-list">
        <PageHero kicker="TAGS" title={<>标签<small>加载失败</small></>} subtitle="无法获取标签列表。" />
        <div style={{ padding: '30px 4px', color: 'var(--faint)' }}>
          <p>{tagsError}</p>
          <Button small onClick={() => setReloadKey((value) => value + 1)}>重试</Button>
        </div>
      </main>
    )
  }

  const max = Math.max(1, ...tags.map((tag) => tag.article_count))

  return (
    <main className="wrap page-list">
      <PageHero kicker="TAGS" title={<>标签<small>{tags.length} 个标签</small></>} subtitle="字号越大，文章越多。点击标签聚合相关文章。" />
      <div className="cloud big rv" style={{ marginTop: 28, justifyContent: 'center' }}>
        {tags.map((tag) => (
          <Link key={tag.id} to={selected === tag.id ? '/tags' : `?id=${tag.id}`} className={`chip ${tagSize(tag.article_count, max)} ${selected === tag.id ? 'solid' : ''}`}>
            {tag.name} <b>{tag.article_count}</b>
          </Link>
        ))}
      </div>

      {selected ? (
        <>
          <div className="sec-h rv" style={{ marginTop: 60 }}>
            <h2 style={{ fontSize: 20 }}>相关文章</h2>
            <span className="ln" />
          </div>
          {loadingArticles ? <Loading height={160} /> : articlesError ? (
            <div style={{ padding: 30, color: 'var(--faint)' }}>
              <p>{articlesError}</p>
              <Button small onClick={() => setReloadKey((value) => value + 1)}>重试</Button>
            </div>
          ) : articles.length === 0 ? <div style={{ padding: 30, color: 'var(--faint)' }}>该标签下暂无文章</div> : articles.map((article) => (
            <Reveal key={article.id} className="sres">
              <Link to={`/post/${article.id}`} style={{ display: 'block' }}>
                <span className="where">文章 · {fmtDate(article.created_at)}</span>
                <h3>{article.title}</h3>
                <p>{article.content.slice(0, 140)}</p>
              </Link>
            </Reveal>
          ))}
          <Pagination align="center" current={page} total={totalPages} pageSize={1} onChange={setPage} />
        </>
      ) : null}
      <div style={{ height: 70 }} />
    </main>
  )
}
