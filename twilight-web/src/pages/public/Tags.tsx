// Tags.tsx —— 标签云 + 按标签聚合文章
// 标签一次性全量拉取（fetchAllTags），字号按 article_count 相对最大值分四级。
// 选中的标签从 URL 的 ?id= 读取；点已选标签回到 /tags。两个加载 effect 都依赖
// reloadKey（重试按钮 +1）以强制重跑。
// 页码由 selected 变化时重置（见下面的 effect）：两个标签各自有自己的第 1 页，
// 带着上一页的 page 换标签会从第 2 页开始取，静默漏掉新标签的第一页文章。
// 翻页时列表按与换路由同一段动画退出/入场（见 lib/motion.ts 的 usePagerMotion）。

import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Button, Pagination, Reveal } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtDate } from '../../lib/format'
import type { Article, Tag } from 'twilight-blog-api'
import { useDocumentTitle } from '../../lib/site'
import { useCanonicalPath, usePageHead } from '../../lib/head'
import { PageHero, SkeletonRows } from '../../components/common'
import { usePagerMotion } from '../../lib/motion'
import { useDelayedLoading } from '../../lib/loading'
import { useI18n, useLocale } from '../../lib/i18n'
import { articleContent, articleTitle, tagName } from '../../lib/localize'

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
  const { t } = useI18n()
  const locale = useLocale()
  const [params] = useSearchParams()
  const selected = Number(params.get('id')) || 0
  const [tags, setTags] = useState<Tag[]>([])
  const [page, setPage] = useState(1)
  // 屏幕上真正显示的那一页：翻页时旧列表先退场，180ms 后才换页（请求不等它）。
  const { page: shownPage, ref: listRef } = usePagerMotion(page)
  // 页码 → 那一页的文章：新页数据常在退场动画播完前就回来，那时它还不该上屏，
  // 先存在映射里，提交到新页码的那一帧直接取用；取不到就渲染骨架。
  const [listed, setListed] = useState<Map<number, Article[]>>(() => new Map())
  const [totalPages, setTotalPages] = useState(1)
  const [loadingTags, setLoadingTags] = useState(true)
  // 请求在途（含换标签、重试）：翻页之外，它也是「旧列表先变暗」的依据。
  const [loadingArticles, setLoadingArticles] = useState(false)
  const [tagsError, setTagsError] = useState('')
  const [articlesError, setArticlesError] = useState('')
  const [reloadKey, setReloadKey] = useState(0)
  const articles = listed.get(shownPage) ?? []
  // 标签云与文章列表各有一个加载态；两者都要等超过阈值才显示骨架。
  const pendingTags = useDelayedLoading(loadingTags)
  const pendingArticles = useDelayedLoading(loadingArticles)

  // 选中某个标签时标题跟着走：从搜索结果点回来的一堆标签页里，
  // 「标签 # Go」比五个一模一样的「标签」有用得多。标签还没加载完时退回「标签」。
  const selectedTag = tags.find((tag) => tag.id === selected)
  const selectedLabel = selectedTag ? tagName(selectedTag, locale) : ''
  const pageTitle = selectedTag ? t(`标签 ${selectedLabel}`, `Tag: ${selectedLabel}`) : t('标签', 'Tags')
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
        setTagsError(getErrorMessage(error, t('标签加载失败', 'Failed to load tags')))
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
      setListed(new Map())
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
        setListed((previous) => new Map(previous).set(page, result.items))
        setTotalPages(result.total_pages)
      })
      .catch((error) => {
        if (!alive) return
        // 失败也记成「这一页有数据（空）」：否则会一直停在骨架上。
        setListed((previous) => new Map(previous).set(page, []))
        setTotalPages(1)
        setArticlesError(getErrorMessage(error, t('文章加载失败', 'Failed to load posts')))
      })
      .finally(() => {
        if (alive) setLoadingArticles(false)
      })

    return () => {
      alive = false
    }
  }, [reloadKey, page, selected])

  if (tagsError) {
    return (
      <main className="wrap page-list">
        <PageHero kicker="TAGS" title={<>{t('标签', 'Tags')}<small>{t('加载失败', 'Failed to load')}</small></>} subtitle={t('无法获取标签列表。', 'The tag list could not be loaded.')} />
        <div style={{ padding: '30px 4px', color: 'var(--faint)' }}>
          <p>{tagsError}</p>
          <Button small onClick={() => setReloadKey((value) => value + 1)}>{t('重试', 'Retry')}</Button>
        </div>
      </main>
    )
  }

  const max = Math.max(1, ...tags.map((tag) => tag.article_count))

  return (
    <main className="wrap page-list">
      {/* 页头始终在：篇数只是 h1 里的行内 small，数据到达时不会改变页头高度。
          标签云用等高的胶囊骨架占位——加载前后都是同一块 flex 区域。 */}
      <PageHero
        kicker="TAGS"
        title={<>{t('标签', 'Tags')}{loadingTags ? null : <small>{t(`${tags.length} 个标签`, `${tags.length} tags`)}</small>}</>}
        subtitle={t('字号越大，文章越多。点击标签聚合相关文章。', 'Bigger means more posts. Click a tag to gather everything under it.')}
      />
      {loadingTags ? (
        <div style={{ marginTop: 28 }}>
          <SkeletonRows variant="cloud" count={10} visible={pendingTags} />
        </div>
      ) : (
        <div className="cloud big rv" style={{ marginTop: 28, justifyContent: 'center' }}>
          {tags.map((tag) => (
            <Link key={tag.id} to={selected === tag.id ? '/tags' : `?id=${tag.id}`} className={`chip ${tagSize(tag.article_count, max)} ${selected === tag.id ? 'solid' : ''}`}>
              {tagName(tag, locale)} <b>{tag.article_count}</b>
            </Link>
          ))}
        </div>
      )}

      {selected && !loadingTags ? (
        <>
          <div className="sec-h rv" style={{ marginTop: 60 }}>
            <h2 style={{ fontSize: 20 }}>{t('相关文章', 'Related posts')}</h2>
            <span className="ln" />
          </div>
          {/* 列表是翻页动画作用的那一块（listRef），分页器原地不动。
              切到另一个标签时旧文章先变暗留在原位（[data-busy]），新数据到达后
              原地替换；翻页则整块滑走再滑入，与换路由同一段动画。新页数据没到
              时显示骨架，位置先占住。 */}
          <div aria-busy={loadingArticles} data-busy={String(pendingArticles)} ref={listRef}>
            {articles.length === 0 ? (
              loadingArticles ? <SkeletonRows variant="results" count={3} visible={pendingArticles} /> : articlesError ? (
                <div style={{ padding: 30, color: 'var(--faint)' }}>
                  <p>{articlesError}</p>
                  <Button small onClick={() => setReloadKey((value) => value + 1)}>{t('重试', 'Retry')}</Button>
                </div>
              ) : <div style={{ padding: 30, color: 'var(--faint)' }}>{t('该标签下暂无文章', 'No posts under this tag yet')}</div>
            ) : (
              articles.map((article) => (
                <Reveal key={article.id} className="sres">
                  <Link to={`/post/${article.id}`} style={{ display: 'block' }}>
                    <span className="where">{t('文章', 'Post')} · {fmtDate(article.created_at)}</span>
                    <h3>{articleTitle(article, locale)}</h3>
                    <p>{articleContent(article, locale).slice(0, 140)}</p>
                  </Link>
                </Reveal>
              ))
            )}
          </div>
          {/* 翻页提交后到新文章到达之间也要留着分页器：它消失再出现，
              等于把读者刚点的东西从手底下抽走。没有任何文章且加载结束时隐藏。 */}
          {loadingArticles || articles.length > 0 ? (
            <Pagination align="center" current={page} total={totalPages} pageSize={1} onChange={setPage} />
          ) : null}
        </>
      ) : null}
      <div style={{ height: 70 }} />
    </main>
  )
}
