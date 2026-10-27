// Moments.tsx —— 微语流（公开）
// 每页 12 条、Pager 翻页替换整页数据。每条微语用 ContentView 渲染（作者可信，
// 传 trusted 走文章管线、不套评论白名单），下面挂**配图行**与**评论按钮**：
//
//   - 配图行走 MediaGallery，与后台微语管理卡（MomentsAdmin）是同一个组件。
//     这里曾经完全没有渲染 item.media_items —— 接口一直返回它（见
//     viewmodel.NewMomentResponseList），只是前台没读，于是带图微语在微语页
//     只剩文字。两边共用一个组件之后，「前台与后台一致」不再靠人记得同步。
//   - 评论区不再随卡片一起挂载：Comments 一挂载就请求 GET /api/comments，
//     12 条微语 = 12 个请求，而多数评论区根本没人展开。现在由 MomentComments
//     的「评论」按钮触发（见该组件文件头）。
//   - 发布时间（精确到分钟）在卡片右上角（.mom-meta）。微语没有标题，时间就是
//     这条内容的时间锚点；放在正文之前而不是页脚，读者先看到「什么时候写的」。
//
// 每条仍是独立的 reveal 动画（延迟按序号错峰，最多 0.2s），整块内容区在翻页时
// 再按与换路由同一段动画换页（见 lib/motion.ts 的 usePagerMotion）。

import { useEffect, useState } from 'react'
import { blog } from '../../lib/client'
import type { Moment } from 'twilight-blog-api'
import ContentView from '../../components/ContentView'
import MomentComments from '../../components/MomentComments'
import MediaGallery from '../../components/media/MediaGallery'
import { Pagination, Reveal } from 'ink-design'
import { useDocumentTitle } from '../../lib/site'
import { useCanonicalPath, usePageHead } from '../../lib/head'
import { fmtDateTime } from '../../lib/format'
import { prefetchMarkdown } from '../../lib/markdown-loader'
import { Empty, PageHero, SkeletonRows } from '../../components/common'
import { usePagerMotion } from '../../lib/motion'
import { ImageGroup } from '../../lib/imageGroup'
import { useDelayedLoading } from '../../lib/loading'
import { isMomentsEnabled } from '../../lib/flags'
import { useI18n } from '../../lib/i18n'
import { usePageParam } from '../../lib/page-param'

export default function Moments() {
  const { t } = useI18n()
  useDocumentTitle(t('微语', 'Moments'))
  usePageHead({ path: useCanonicalPath(), title: t('微语', 'Moments') })
  // 页码存在 URL 里，浏览器后退回到微语页时保留翻页位置。
  const [page, setPage] = usePageParam()
  // 屏幕上真正显示的那一页：旧列表先退场，180ms 后才换页（请求不等这 180ms）。
  const { page: shownPage, ref: listRef } = usePagerMotion(page)
  // 页码 → 那一页的微语：新页数据常在退场动画播完前就回来，那时它还不该上屏，
  // 先存在映射里，提交到新页码的那一帧直接取用；取不到就渲染骨架。
  const [listed, setListed] = useState<Map<number, Moment[]>>(() => new Map())
  const [totalPages, setTotalPages] = useState(1)
  const items = listed.get(shownPage) ?? []
  const loading = !listed.has(shownPage)
  // 骨架只在加载超过阈值后可见，但始终占位：微语卡的高度在加载前后不变。
  const pending = useDelayedLoading(loading)
  // 开关在首屏就确定（外壳属性），关闭时页面直接给出说明，不发那次注定 404 的请求。
  const enabled = isMomentsEnabled()

  // 与下面的列表请求并行开始下载渲染器（每条微语都由 ContentView 渲染，理由见 Post.tsx）。
  useEffect(() => {
    prefetchMarkdown()
  }, [])

  useEffect(() => {
    if (!enabled) return
    let alive = true
    blog.listMoments({ page, pageSize: 12 })
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
  }, [page, enabled])

  if (!enabled) {
    return (
      <main className="wrap">
        <PageHero title={<>{t('碎碎念', 'Notes')}<small>{t('微语', 'Moments')}</small></>} subtitle={t('比文章短，比日常长。', 'Shorter than a post, longer than a day.')} />
        <Empty text={t('本站未启用微语功能', 'Moments are disabled on this site')} />
      </main>
    )
  }

  return (
    <main className="wrap">
      <PageHero title={<>{t('碎碎念', 'Notes')}<small>{t('微语', 'Moments')}</small></>} subtitle={t('比文章短，比日常长。', 'Shorter than a post, longer than a day.')} />
      {/* 列表是翻页动画作用的那一块（listRef），分页器留在它外面原地不动。
          新页数据没到时这块换成骨架（位置先占住），加载超过阈值才显形。 */}
      <div className="momfeed" aria-busy={loading} ref={listRef}>
        {items.length === 0
          ? (loading ? <SkeletonRows variant="moments" count={3} visible={pending} /> : <Empty text={t('还没有微语', 'No moments yet')} />)
          : <ImageGroup key={shownPage}>{items.map((item, index) => (
          <Reveal key={item.id} className="mom" delay={Math.min(index * 50, 200)}>
            <div className="mom-meta">
              <time className="mom-time">{fmtDateTime(item.created_at)}</time>
            </div>
            <ContentView text={item.content} className="mom-content" trusted />
            {/* media_items 的类型是必填数组，但混用更旧的后端时字段可能缺席，
                所以与后台卡片保持同一个守卫写法。 */}
            {item.media_items && item.media_items.length > 0 ? <MediaGallery items={item.media_items} /> : null}
            <MomentComments momentId={item.id} />
          </Reveal>
        ))}</ImageGroup>}
      </div>
      <Pagination align="center" current={page} total={totalPages} pageSize={1} onChange={setPage} />
    </main>
  )
}
