// Posts.tsx —— 文章管理（active / 回收站 双标签）
// 删除分两级：active 标签下的「删除」其实是软删（blog.deleteArticle 移入回收站，
// 可恢复）；trash 标签下才有「彻底删除」（permanentlyDeleteArticle，不可撤销）。
// 批量操作同理：batchDeleteArticles 软删、batchPermanentlyDeleteArticles 永久删。
// 切换标签 / 翻页都会清空 selected，且 selection 只在当前页可见项内保留
// （第二个 effect 按 list 过滤），避免对已不在屏幕上的 id 误操作。
// 所有写操作后统一 reload()（reloadKey +1）整页刷新，简单而一致。

import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { Button, Checkbox, Chip, Pagination } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtDateTime } from '../../lib/format'
import type { Article } from 'twilight-blog-api'
import { useDialog } from '../../lib/dialog'
import { useToast } from '../../lib/toast'
import { Empty, Loading } from '../../components/common'

type Tab = 'active' | 'trash'

export default function Posts() {
  const toast = useToast()
  const { confirm } = useDialog()
  const [tab, setTab] = useState<Tab>('active')
  const [list, setList] = useState<Article[]>([])
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(1)
  const [selected, setSelected] = useState<number[]>([])
  const [loading, setLoading] = useState(true)
  const [reloadKey, setReloadKey] = useState(0)

  // Single source of truth for fetching: `tab` and `page` are only ever changed
  // together with `selected` being cleared, so no stale page can be requested
  // and no stale selection can be acted on.
  useEffect(() => {
    let alive = true
    setLoading(true)

    const run = async () => {
      try {
        const result = tab === 'active'
          ? await blog.listArticles({ page, pageSize: 12 })
          : await blog.listTrashArticles({ page, pageSize: 12 })
        if (!alive) return
        setList(result.items)
        setTotalPages(result.total_pages)
      } catch (error) {
        if (!alive) return
        setList([])
        setTotalPages(1)
        toast(getErrorMessage(error, '加载文章失败'))
      } finally {
        if (alive) setLoading(false)
      }
    }

    void run()
    return () => {
      alive = false
    }
  }, [page, reloadKey, tab, toast])

  // The loaded page may have changed under the selection (deletion, refresh):
  // keep only ids that are still on screen.
  useEffect(() => {
    setSelected((current) => {
      if (current.length === 0) return current
      const visible = new Set(list.map((item) => item.id))
      const next = current.filter((id) => visible.has(id))
      return next.length === current.length ? current : next
    })
  }, [list])

  const reload = useCallback(() => setReloadKey((value) => value + 1), [])

  const changeTab = (next: Tab) => {
    if (next === tab) return
    setSelected([])
    setPage(1)
    setTab(next)
  }

  const changePage = (next: number) => {
    setSelected([])
    setPage(next)
  }

  const toggleAll = (checked: boolean) => setSelected(checked ? list.map((item) => item.id) : [])

  const trash = async (item: Article) => {
    if (!(await confirm('删除文章', `确定将「${item.title}」移入回收站？`))) return
    try {
      await blog.deleteArticle(item.id)
      toast('已移入回收站')
      setSelected([])
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const batchTrash = async () => {
    // 入口只有批量条上的按钮（selected 非空才渲染），无需再查空。
    if (!(await confirm('移入回收站', `确定将选中的 ${selected.length} 篇文章移入回收站？`))) return
    try {
      await blog.batchDeleteArticles({ ids: selected })
      toast(`已删除 ${selected.length} 篇`)
      setSelected([])
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const restore = async (item: Article) => {
    try {
      await blog.restoreArticle(item.id)
      toast('已恢复')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const batchRestore = async () => {
    try {
      await blog.batchRestoreArticles({ ids: selected })
      toast('已批量恢复')
      setSelected([])
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const permanent = async (item: Article) => {
    if (!(await confirm('彻底删除', `确定彻底删除「${item.title}」？此操作不可撤销。`))) return
    try {
      await blog.permanentlyDeleteArticle(item.id)
      toast('已彻底删除')
      setSelected([])
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const batchPermanent = async () => {
    if (!(await confirm('彻底删除', `确定彻底删除选中的 ${selected.length} 篇文章？此操作不可撤销。`))) return
    try {
      await blog.batchPermanentlyDeleteArticles({ ids: selected })
      toast('已批量彻底删除')
      setSelected([])
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  return (
    <>
      <div className="ph">
        <h2>文章</h2>
        <span className="kicker" style={{ marginTop: 6 }}>POSTS · {list.length}</span>
        <span className="sp" />
        <Link className="btn pri sm" to="/admin/editor">写文章</Link>
      </div>
      <div className="atabs">
        <Chip solid={tab === 'active'} onClick={() => changeTab('active')}>文章</Chip>
        <Chip solid={tab === 'trash'} onClick={() => changeTab('trash')}>回收站</Chip>
      </div>
      {selected.length > 0 ? (
        <div className="bulkbar">
          已选 {selected.length} 篇
          <Button ghost small onClick={() => setSelected([])}>取消</Button>
          <span style={{ flex: 1 }} />
          {tab === 'active'
            ? <Button ghost small onClick={() => void batchTrash()}>移入回收站</Button>
            : (
              <>
                <Button ghost small onClick={() => void batchRestore()}>恢复</Button>
                <Button ghost small onClick={() => void batchPermanent()}>彻底删除</Button>
              </>
            )}
        </div>
      ) : null}
      <div className="pcard rv">
        <div className="bd2" style={{ padding: '6px 12px', overflowX: 'auto' }}>
          <table className="tb" style={{ minWidth: 760 }}>
            <thead>
              <tr>
                <th style={{ width: 32 }}>
                  <Checkbox checked={selected.length === list.length && list.length > 0} onChange={(event) => toggleAll(event.target.checked)} />
                </th>
                <th>标题</th>
                <th>标签</th>
                <th>创建时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {loading ? null : list.map((item) => (
                <tr key={item.id}>
                  <td>
                    <Checkbox checked={selected.includes(item.id)} onChange={(event) => setSelected(event.target.checked ? [...selected, item.id] : selected.filter((id) => id !== item.id))} />
                  </td>
                  <td>
                    <Link to={tab === 'active' ? `/admin/editor/${item.id}` : '#'}>
                      <b style={{ fontFamily: 'var(--serif)', fontSize: 14.5 }}>{item.title}</b>
                    </Link>
                  </td>
                  <td><div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>{item.tags.slice(0, 3).map((tag) => <span key={tag} className="chip">{tag}</span>)}</div></td>
                  <td className="mono" style={{ fontSize: 11 }}>{fmtDateTime(item.created_at)}</td>
                  <td style={{ whiteSpace: 'nowrap' }}>
                    {tab === 'active' ? (
                      <>
                        <Link className="miniact" to={`/admin/editor/${item.id}`}>编辑</Link>
                        <Button ghost small onClick={() => void trash(item)}>删除</Button>
                      </>
                    ) : (
                      <>
                        <Button ghost small onClick={() => void restore(item)}>恢复</Button>
                        <Button ghost small onClick={() => void permanent(item)}>彻底删除</Button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {!loading && list.length === 0 ? <Empty text="没有符合条件的文章" /> : null}
        </div>
      </div>
      {loading ? <Loading height={80} /> : null}
      <Pagination current={page} total={totalPages} pageSize={1} onChange={changePage} />
    </>
  )
}
