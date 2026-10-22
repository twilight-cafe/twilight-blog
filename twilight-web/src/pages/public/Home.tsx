// Home.tsx —— 公开首页：文章列表 + 分页
// 每页篇数由站点设置决定（外壳属性 data-home-page-size 下发，见 lib/flags.ts），
// 翻页用 Pager 换页（非无限滚动）：旧列表先滑走，新页再按与换路由同一段动画入场
// （见 lib/motion.ts 的 usePagerMotion）。
// 「阅读时长」是字符数 / 600 的粗略估算，不是真实阅读速度；摘要用 excerpt 截 160 字。

import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Reveal } from 'ink-design'
import { Chip, Pagination } from 'ink-design'
import { mediaUrl } from 'twilight-blog-api'
import { blog } from '../../lib/client'
import { excerpt, fmtMonthDay } from '../../lib/format'
import type { Article } from 'twilight-blog-api'
import { useHomeDocumentTitle, useSiteDescription } from '../../lib/site'
import { useCanonicalPath, usePageHead, websiteJsonLd } from '../../lib/head'
import { Empty, SkeletonCards } from '../../components/common'
import ProgressiveImage from '../../components/ProgressiveImage'
import { ImageGroup } from '../../lib/imageGroup'
import { toAbsoluteUrl } from '../../lib/url'
import { homePageSize } from '../../lib/flags'
import { usePagerMotion } from '../../lib/motion'
import { useDelayedLoading } from '../../lib/loading'
import { useI18n, useLocale } from '../../lib/i18n'
import { articleContent, articleTags, articleTitle } from '../../lib/localize'

export default function Home() {
  const { t } = useI18n()
  const locale = useLocale()
  // 首页标题用「Twilight · <站点描述>」，不同于内页的「<页面名> · Twilight」。
  useHomeDocumentTitle()
  // og:title 用品牌名而不是上面那个「品牌 · 描述」：分享卡片已经有描述那一行，
  // 标题再把它重复一遍只会被截断。「Twilight」在这里是既有常量（外壳品牌名、
  // 页脚、侧栏都用它），不是新引入的一处来源。
  const description = useSiteDescription()
  const canonicalPath = useCanonicalPath()
  usePageHead({
    path: canonicalPath,
    title: 'Twilight',
    description,
    jsonLd: websiteJsonLd('Twilight', description, toAbsoluteUrl(canonicalPath)),
  })
  const [page, setPage] = useState(1)
  // 屏幕上真正显示的那一页。点击页码后旧列表先退场，180ms 后才换成新页；
  // 请求不等这 180ms（下面仍是点到的 page 一变就发），先到的数据在这里等着。
  const { page: shownPage, ref: listRef } = usePagerMotion(page)
  // 页码 → 那一页的列表。用映射而不是「一份当前列表」，是因为新页的数据常常在
  // 退场动画播完之前就回来了：那时它还不该上屏（旧卡片正滑到一半），先存着，
  // 提交到新页码的那一帧直接取用。取不到（数据还没到）就渲染骨架——入场动画
  // 不该播在上一页的旧卡片上。
  const [listed, setListed] = useState<Map<number, Article[]>>(() => new Map())
  const [totalPages, setTotalPages] = useState(1)
  const articles = listed.get(shownPage) ?? []
  // 「这一页还没有数据」就是加载中：不再单独维护 loading，少一处会与列表脱节的状态。
  const loading = !listed.has(shownPage)
  // 每页篇数在渲染期读一次即可：外壳属性在整个会话期间不会变（改站点设置要
  // 重新加载页面才会下发新值），放进 state 或 effect 只会多一层无意义的同步。
  const pageSize = homePageSize()
  // 加载超过阈值才显示骨架（见 lib/loading.ts）：翻页 / 首屏只要在 200ms 内
  // 返回，用户看到的就是内容直接出现，而不是灰条闪一下。
  const pending = useDelayedLoading(loading)

  useEffect(() => {
    let alive = true
    blog.listArticles({ page, pageSize })
      .then((result) => {
        if (!alive) return
        setListed((previous) => new Map(previous).set(page, result.items))
        setTotalPages(result.total_pages)
      })
      .catch(() => {
        if (!alive) return
        // 失败也记成「这一页有数据（空）」：否则会一直停在骨架上。
        setListed((previous) => new Map(previous).set(page, []))
        setTotalPages(1)
      })

    return () => {
      alive = false
    }
    // pageSize 进入依赖：它是本页唯一的请求参数来源，漏掉会让人以为
    // 「改了站点设置但首页没变」是后端没生效。
  }, [page, pageSize])

  return (
    <div className="wrap">
      <div className="filterbar rv">
        <Chip solid>{t('全部文章', 'All posts')}</Chip>
        <span className="sp" />
        <span className="mono" style={{ fontSize: 11, color: 'var(--faint)' }}>INK & CODE</span>
      </div>
      {/* 列表是翻页动画作用的那一块（listRef）：旧卡片滑走、新卡片滑入，
          分页器本身原地不动。数据没跟上时这块换成骨架，位置由骨架先占住
          （数量 = 每页篇数），加载超过阈值才显形——快请求不会闪出灰条，
          也不会在出现时改变页面高度。 */}
      <div className="acards rowmode" style={{ marginTop: 12 }} aria-busy={loading} ref={listRef}>
        {articles.length === 0
          ? (loading ? <SkeletonCards count={pageSize} visible={pending} /> : <Empty text={t('还没有文章', 'No posts yet')} />)
          : <ImageGroup key={shownPage}>{articles.map((article, index) => (
          // has-cover 才切成两栏网格：没有头图时保持原来的单栏排版，
          // 否则每张卡片左侧都会留出一条空白。
          <Reveal key={article.id} as="article" delay={Math.min(index * 50, 200)} className={`acard${article.cover_media_id === null ? '' : ' has-cover'}`}>
            <Link to={`/post/${article.id}`} style={{ display: 'contents' }}>
              {article.cover_media_id === null ? null : (
                // alt 留空：这张图是文章的装饰，标题已经在旁边以 h3 呈现，
                // 给它写 alt 会让读屏把同一件事念两遍。
                <div className="cover">
                  {/* 卡片头图的外框已经定尺寸（.cover 撑满），骨架不需要宽高比。
                      不写 loading="lazy"：组内图片一律 eager 加载，否则视口外的
                      头图不会开始下载，整组就永远等不齐（见 lib/imageGroup.tsx）。 */}
                  <ProgressiveImage src={mediaUrl(article.cover_media_id)} alt="" />
                </div>
              )}
              <div className="body">
                <div className="top">
                  <span className="cat">{t('文章', 'Post')}</span>
                  {articleTags(article, locale).slice(0, 1).map((tag) => <span key={tag}>· {tag}</span>)}
                  <span style={{ marginLeft: 'auto' }}>{fmtMonthDay(article.created_at)}</span>
                </div>
                <h3>{articleTitle(article, locale)}</h3>
                <p className="ex">{excerpt(articleContent(article, locale), 160)}</p>
                <div className="tags">
                  {articleTags(article, locale).slice(0, 4).map((tag) => <Chip key={tag}># {tag}</Chip>)}
                </div>
                <div className="ft">
                  <span>{t(`${Math.max(1, Math.ceil(articleContent(article, locale).length / 600))} 分钟阅读`, `${Math.max(1, Math.ceil(articleContent(article, locale).length / 600))} min read`)}</span>
                  <span>{t(`${articleContent(article, locale).length} 字符`, `${articleContent(article, locale).length} characters`)}</span>
                </div>
              </div>
            </Link>
          </Reveal>
        ))}</ImageGroup>}
      </div>
      <Pagination align="center" current={page} total={totalPages} pageSize={1} onChange={setPage} />
    </div>
  )
}
