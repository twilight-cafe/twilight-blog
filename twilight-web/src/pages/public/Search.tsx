// Search.tsx —— 全文搜索（公开）
// 关键词走 URL 的 ?q=，便于分享/前进后退；draft 只是输入框的本地态，只有提交
// 时才写回 URL、触发真正的搜索 effect（依赖 [query, page]）。空关键词不发起请求，
// 直接显示空状态。文章与页面结果混排，按 type 决定跳转地址。

import { useEffect, useState, type FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Button, Input, Pagination } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { excerpt, fmtDate } from '../../lib/format'
import type { SearchResult } from 'twilight-blog-api'
import { useDocumentTitle } from '../../lib/site'
import { Empty, Loading, PageHero, Reveal } from '../../components/common'

export default function Search() {
  const [params, setParams] = useSearchParams()
  const query = params.get('q') ?? ''
  const [draft, setDraft] = useState(query)
  const [results, setResults] = useState<SearchResult[]>([])
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(1)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

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
  useDocumentTitle('搜索')

  useEffect(() => {
    setDraft(query)
  }, [query])

  useEffect(() => {
    setPage(1)
  }, [query])

  useEffect(() => {
    const keyword = query.trim()
    if (!keyword) {
      setResults([])
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
        setResults(result.items)
        setTotalPages(result.total_pages)
      })
      .catch((reason) => {
        if (!alive) return
        setResults([])
        setTotalPages(1)
        setError(getErrorMessage(reason, '搜索失败'))
      })
      .finally(() => {
        if (alive) setLoading(false)
      })

    return () => {
      alive = false
    }
  }, [query, page])

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

  return (
    <main className="wrap" style={{ maxWidth: 880 }}>
      <PageHero kicker="SEARCH" title={<>搜索<small>全文检索</small></>} subtitle="搜索本站的文章与页面。" />

      <form className="sbar" style={{ display: 'flex', gap: 10, marginTop: 24 }} onSubmit={submit}>
        <Input
          variant="box"
          className="sbar-input"
          placeholder="输入关键词搜索…"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          style={{ flex: 1, minWidth: 0 }}
        />
        <Button primary type="submit">搜索</Button>
      </form>

      {error ? (
        <div style={{ padding: '40px 4px', color: 'var(--faint)' }}>{error}</div>
      ) : loading ? (
        <Loading height={200} />
      ) : !query.trim() ? (
        <Empty text="输入关键词开始搜索" />
      ) : results.length === 0 ? (
        <Empty text={`没有找到与「${query}」相关的内容`} />
      ) : (
        <div style={{ marginTop: 22 }}>
          {results.map((result, index) => (
            <Reveal key={`${result.type}-${result.id}`} className="sres" delay={Math.min(index * 0.05, 0.2)}>
              <Link to={result.type === 'article' ? `/post/${result.id}` : `/pages/${result.id}`} style={{ display: 'block' }}>
                <span className="where">
                  {result.type === 'article' ? '文章' : '页面'} · {fmtDate(result.created_at)}
                  {result.type === 'article' && result.tags.length > 0 ? ` · ${result.tags.slice(0, 3).join(' / ')}` : ''}
                </span>
                <h3>{result.title}</h3>
                <p>{excerpt(result.content, 160)}</p>
              </Link>
            </Reveal>
          ))}
          <Pagination current={page} total={totalPages} pageSize={1} onChange={setPage} />
        </div>
      )}
    </main>
  )
}
