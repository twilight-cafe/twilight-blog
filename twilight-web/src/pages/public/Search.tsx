// Search.tsx —— 全文搜索（公开）
// 关键词走 URL 的 ?q=，便于分享/前进后退；draft 只是输入框的本地态，只有提交
// 时才写回 URL、触发真正的搜索 effect（依赖 [query, page]）。空关键词不发起请求，
// 直接显示空状态。文章与页面结果混排，按 type 决定跳转地址。
// 翻页时结果列表按与换路由同一段动画退出/入场（见 lib/motion.ts 的 usePagerMotion）；
// 换关键词仍沿用「旧结果变暗、原地替换」——那不是翻页，整块滑走只会让人以为搜索被重置。

import { useEffect, useState, type FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Button, Input, Pagination, Reveal } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { excerpt, fmtDate } from '../../lib/format'
import type { SearchResult } from 'twilight-blog-api'
import { isSearchEnabled } from '../../lib/flags'
import { useDocumentTitle } from '../../lib/site'
import { useCanonicalPath, usePageHead } from '../../lib/head'
import { Empty, PageHero, SkeletonRows } from '../../components/common'
import { usePagerMotion } from '../../lib/motion'
import { useDelayedLoading } from '../../lib/loading'
import { useI18n, useLocale } from '../../lib/i18n'
import { articleContent, articleTags, articleTitle } from '../../lib/localize'

export default function Search() {
  const { t } = useI18n()
  const locale = useLocale()
  const [params, setParams] = useSearchParams()
  const query = params.get('q') ?? ''
  const [draft, setDraft] = useState(query)
  const [page, setPage] = useState(1)
  // 屏幕上真正显示的那一页：翻页时旧结果先退场，180ms 后才换页（请求不等它）。
  const { page: shownPage, ref: listRef } = usePagerMotion(page)
  // 页码 → 那一页的结果：新页数据常在退场动画播完前就回来，那时它还不该上屏，
  // 先存在映射里，提交到新页码的那一帧直接取用；取不到就渲染骨架。换关键词时
  // 页码归 1、与 shownPage 相同，旧结果照常留在原位变暗。
  const [listed, setListed] = useState<Map<number, SearchResult[]>>(() => new Map())
  const [totalPages, setTotalPages] = useState(1)
  const results = listed.get(shownPage) ?? []
  const [loading, setLoading] = useState(false)
  // 搜索结果骨架同样延迟出现：输入即返回的搜索不该先闪一屏灰条。
  const pending = useDelayedLoading(loading)
  const [error, setError] = useState('')
  const searchEnabled = isSearchEnabled()

  // 标题刻意**不包含关键词**。
  //
  // 原先这里写的是 `搜索 ${query}`，好处是多标签与历史记录里能分辨是哪一次搜索。
  // 代价是 ?q= 完全由链接提供：任何人都能构造
  //   /search?q=会话已过期，请重新登录
  // 让标签页在真实域名与真实站点名旁边显示一句伪造的站内提示——一种成本极低的
  // 标签页伪装/钓鱼手法（第三轮渗透测试 R3-16d）。
  //
  // 关键词仍然出现在页面正文与 URL 里，多标签的区分能力并未完全失去；
  // 而「不要反射访客可控文本到标签页标题」这条更值钱。若要恢复，请连同
  // 一条长度上限与清洗一起加，并明确接受上述取舍。
  useDocumentTitle(t('搜索', 'Search'))
  // 搜索结果页整页 noindex：它是「访客现拼出来的组合」，没有稳定内容，而且会把
  // 同一篇文章以无数条带 q= 的地址重复送进索引。follow 保留（结果里的文章链接
  // 本身值得被爬）。
  usePageHead({ path: useCanonicalPath(), title: t('搜索', 'Search'), robots: 'noindex, follow' })

  useEffect(() => {
    setDraft(query)
  }, [query])

  useEffect(() => {
    setPage(1)
  }, [query])

  useEffect(() => {
    // 禁用时 effects 仍会被 React 提交执行（提前返回只影响 JSX），
    // 所以这里必须再挡一次：后端路由已不注册，请求只会得到 404。
    if (!searchEnabled) {
      setListed(new Map())
      setTotalPages(1)
      setError('')
      setLoading(false)
      return
    }
    const keyword = query.trim()
    if (!keyword) {
      setListed(new Map())
      setTotalPages(1)
      setError('')
      setLoading(false)
      return
    }

    let alive = true
    setLoading(true)
    setError('')
    blog.search({ q: keyword, page, pageSize: 12 })
      .then((result) => {
        if (!alive) return
        setListed((previous) => new Map(previous).set(page, result.items))
        setTotalPages(result.total_pages)
      })
      .catch((reason) => {
        if (!alive) return
        // 失败也记成「这一页有数据（空）」：否则会一直停在骨架上。
        setListed((previous) => new Map(previous).set(page, []))
        setTotalPages(1)
        setError(getErrorMessage(reason, t('搜索失败', 'Search failed')))
      })
      .finally(() => {
        if (alive) setLoading(false)
      })

    return () => {
      alive = false
    }
  }, [query, page, searchEnabled])

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const keyword = draft.trim()
    setPage(1)
    if (!keyword) {
      setParams({})
      return
    }
    setParams({ q: keyword })
  }

  // 搜索关闭时的兜底页：正常导航根本不会到这（导航与页脚都隐藏了入口），
  // 能到达的只剩手输 URL 与收藏夹。页面保留但明确说「未启用」，并且
  // 不发起任何搜索请求——后端的路由已经不注册，请求只会得到 404。
  // 提前返回放在全部 hooks 之后：组件的 hook 顺序不能因 flag 而分支。
  if (!searchEnabled) {
    return (
      <main className="wrap" style={{ maxWidth: 880 }}>
        <PageHero kicker="SEARCH" title={<>{t('搜索', 'Search')}<small>{t('全文检索', 'Full-text search')}</small></>} subtitle={t('搜索本站的文章与页面。', 'Search every post and page on this site.')} />
        <Empty text={t('本站未启用搜索功能', 'Search is disabled on this site')} />
      </main>
    )
  }

  return (
    <main className="wrap" style={{ maxWidth: 880 }}>
      <PageHero kicker="SEARCH" title={<>{t('搜索', 'Search')}<small>{t('全文检索', 'Full-text search')}</small></>} subtitle={t('搜索本站的文章与页面。', 'Search every post and page on this site.')} />

      <form className="sbar" style={{ display: 'flex', gap: 10, marginTop: 24 }} onSubmit={submit}>
        <Input
          variant="box"
          className="sbar-input"
          placeholder={t('输入关键词搜索…', 'Type a keyword and press search…')}
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          style={{ flex: 1, minWidth: 0 }}
        />
        <Button primary type="submit">{t('搜索', 'Search')}</Button>
      </form>

      {error ? (
        <div style={{ padding: '40px 4px', color: 'var(--faint)' }}>{error}</div>
      ) : !query.trim() ? (
        <Empty text={t('输入关键词开始搜索', 'Type a keyword to start searching')} />
      ) : (
        <>
          {/* 结果列表是翻页动画作用的那一块（listRef），分页器留在外面原地不动。
              换关键词时列表不清空，只按 [data-busy] 变暗到新结果到达；翻页时新页
              数据没到则显示骨架，位置先占住。 */}
          <div ref={listRef} style={{ marginTop: 22 }} aria-busy={loading} data-busy={String(pending)}>
            {results.length === 0 ? (
              loading ? <SkeletonRows variant="results" count={3} visible={pending} /> : <Empty text={t(`没有找到与「${query}」相关的内容`, `Nothing found for “${query}”`)} />
            ) : results.map((result, index) => (
            <Reveal key={`${result.type}-${result.id}`} className="sres" delay={Math.min(index * 50, 200)}>
              <Link to={result.type === 'article' ? `/post/${result.id}` : `/pages/${result.id}`} style={{ display: 'block' }}>
                <span className="where">
                  {result.type === 'article' ? t('文章', 'Post') : t('页面', 'Page')} · {fmtDate(result.created_at)}
                  {result.type === 'article' && result.tags.length > 0 ? ` · ${articleTags(result, locale).slice(0, 3).join(' / ')}` : ''}
                </span>
                <h3>{result.type === 'article' ? articleTitle(result, locale) : result.title}</h3>
                <p>{excerpt(result.type === 'article' ? articleContent(result, locale) : result.content, 160)}</p>
              </Link>
            </Reveal>
            ))}
          </div>
          {/* 翻页提交后到新结果到达之间也要留着分页器：它消失再出现，
              等于把读者刚点的东西从手底下抽走。 */}
          {loading || results.length > 0 ? (
            <Pagination align="center" current={page} total={totalPages} pageSize={1} onChange={setPage} />
          ) : null}
        </>
      )}
    </main>
  )
}
