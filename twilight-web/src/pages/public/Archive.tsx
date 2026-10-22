// Archive.tsx —— 归档页：按年份分组展示全部文章
// 注意 fetchAllArticles 一次性拉取所有文章（翻完所有分页），不做分页/虚拟滚动，
// 文章量很大时这一页会一次性请求很多接口并持有全部数据——属于已知瓶颈。
// 年份计算复用「时间戳 >1e12 视为毫秒否则视为秒」的隐式约定（见 lib/format）。
// 列表按创建时间倒序、再按年聚合；标签为空时用空 <span /> 占位以保持布局对齐。

import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Reveal } from 'ink-design'
import { blog } from '../../lib/client'
import { fmtMonthDay } from '../../lib/format'
import type { Article } from 'twilight-blog-api'
import { useDocumentTitle } from '../../lib/site'
import { useCanonicalPath, usePageHead } from '../../lib/head'
import { PageHero, SkeletonRows } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import { useI18n, useLocale } from '../../lib/i18n'
import { articleTitle } from '../../lib/localize'

async function fetchAllArticles(): Promise<Article[]> {
  const first = await blog.listArticles({ page: 1, pageSize: 12 })
  if (first.total_pages <= 1) return first.items
  const rest = await Promise.all(
    Array.from({ length: first.total_pages - 1 }, (_, index) =>
      blog.listArticles({ page: index + 2, pageSize: 12 }),
    ),
  )
  return [...first.items, ...rest.flatMap((page) => page.items)]
}

export default function Archive() {
  const { t } = useI18n()
  const locale = useLocale()
  useDocumentTitle(t('归档', 'Archive'))
  // 列表页不自定义描述，交给站点描述兜底（head.ts 会回落）——「归档」两个字
  // 作为描述对搜索结果毫无信息量。
  usePageHead({ path: useCanonicalPath(), title: t('归档', 'Archive') })
  const [articles, setArticles] = useState<Article[]>([])
  const [loading, setLoading] = useState(true)
  // 页头在加载前后都保留：否则「先光秃秃一行骨架、再整页下移」是最明显的跳版。
  const pending = useDelayedLoading(loading)

  useEffect(() => {
    setLoading(true)
    fetchAllArticles()
      .then(setArticles)
      .catch(() => setArticles([]))
      .finally(() => setLoading(false))
  }, [])

  const groups = useMemo(() => {
    const map = new Map<number, Article[]>()
    for (const article of [...articles].sort((a, b) => b.created_at - a.created_at)) {
      const year = new Date(article.created_at > 1e12 ? article.created_at : article.created_at * 1000).getFullYear()
      const list = map.get(year) ?? []
      list.push(article)
      map.set(year, list)
    }
    return [...map.entries()]
  }, [articles])

  const total = articles.length

  return (
    <main className="wrap page-list">
      {/* 篇数/年数要到数据到达才有，但它们是 h1 里的行内 small：出现时只改这
          一行里的内容，不改变页头高度。 */}
      <PageHero
        kicker="ARCHIVE"
        title={<>{t('归档', 'Archive')}{loading ? null : <small>{t(`共 ${total} 篇 · ${groups.length} 年`, `${total} posts · ${groups.length} years`)}</small>}</>}
        subtitle={t('所有文字按时间封存，随时可以回头。', 'Everything written, sealed by date and ready to revisit.')}
      />
      {loading ? <SkeletonRows variant="archive" count={6} visible={pending} /> : null}
      {!loading && groups.map(([year, items]) => (
        <div key={year}>
          <div className="tl-y rv">{year}<small>{t(`${items.length} 篇`, `${items.length} posts`)}</small></div>
          {items.map((article, index) => (
            <Reveal key={article.id} className="tl-row" delay={Math.min(index * 40, 200)}>
              <Link to={`/post/${article.id}`} style={{ display: 'contents' }}>
                <time>{fmtMonthDay(article.created_at)}</time>
                <b>{articleTitle(article, locale)}</b>
                {article.tags[0] ? <span className="chip">{article.tags[0]}</span> : <span />}
              </Link>
            </Reveal>
          ))}
        </div>
      ))}
      <div style={{ height: 70 }} />
    </main>
  )
}
