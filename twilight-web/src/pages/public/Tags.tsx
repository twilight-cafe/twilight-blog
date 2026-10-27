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
import { fetchAllTags } from '../../lib/tags'
import { usePageParam } from '../../lib/page-param'

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
  // 页码存在 URL 里。换标签时标签链接只带 ?id=、不带 page，因此页码在导航
  // 生效的同一帧就回到第 1 页——不再需要「渲染期把旧页码打回 1」的同步逻辑。
  const [page, setPage] = usePageParam()
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
        <PageHero title={<>{t('标签', 'Tags')}<small>{t('加载失败', 'Failed to load')}</small></>} subtitle={t('无法获取标签列表。', 'The tag list could not be loaded.')} />
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
