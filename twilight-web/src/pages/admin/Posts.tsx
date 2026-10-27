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
import { useDelayedLoading } from '../../lib/loading'
import { useI18n, useLocale } from '../../lib/i18n'
import { articleAIGenerated, articleTags, articleTitle } from '../../lib/localize'
import { usePageParam } from '../../lib/page-param'

type Tab = 'active' | 'trash'

export default function Posts() {
  const { t } = useI18n()
  const locale = useLocale()
  const toast = useToast()
  const { confirm } = useDialog()
  const [tab, setTab] = useState<Tab>('active')
  const [list, setList] = useState<Article[]>([])
  // 页码进 URL：从列表翻到第 2 页、进文章编辑再后退，回来仍是第 2 页。
  const [page, setPage] = usePageParam()
  const [totalPages, setTotalPages] = useState(1)
  const [selected, setSelected] = useState<number[]>([])
  const [loading, setLoading] = useState(true)
  // 首屏加载超过阈值才显示等待提示（见 lib/loading.ts）。
  const pending = useDelayedLoading(loading)
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
        toast(getErrorMessage(error, t('加载文章失败', 'Failed to load posts')))
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
  // keep only ids that are still on screen. Dropping the old array identity when
  // nothing was removed keeps this from re-rendering on every list refresh.
  useEffect(() => {
    const visible = new Set(list.map((item) => item.id))
    setSelected((current) => {
      if (current.every((id) => visible.has(id))) return current
      return current.filter((id) => visible.has(id))
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
    if (!(await confirm(t('删除文章', 'Delete post'), t(`确定将「${item.title}」移入回收站？`, `Move “${item.title}” to the recycle bin?`)))) return
    try {
      await blog.deleteArticle(item.id)
      toast(t('已移入回收站', 'Moved to the recycle bin'))
      setSelected([])
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const batchTrash = async () => {
    // 入口只有批量条上的按钮（selected 非空才渲染），无需再查空。
    if (!(await confirm(t('移入回收站', 'Move to recycle bin'), t(`确定将选中的 ${selected.length} 篇文章移入回收站？`, `Move the ${selected.length} selected posts to the recycle bin?`)))) return
    try {
      await blog.batchDeleteArticles({ ids: selected })
      toast(t(`已删除 ${selected.length} 篇`, `${selected.length} posts moved`))
      setSelected([])
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const restore = async (item: Article) => {
    try {
      await blog.restoreArticle(item.id)
      toast(t('已恢复', 'Restored'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const batchRestore = async () => {
    try {
      await blog.batchRestoreArticles({ ids: selected })
      toast(t('已批量恢复', 'Posts restored'))
      setSelected([])
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const permanent = async (item: Article) => {
    if (!(await confirm(t('彻底删除', 'Delete permanently'), t(`确定彻底删除「${item.title}」？此操作不可撤销。`, `Permanently delete “${item.title}”? This cannot be undone.`)))) return
    try {
      await blog.permanentlyDeleteArticle(item.id)
      toast(t('已彻底删除', 'Permanently deleted'))
      setSelected([])
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const batchPermanent = async () => {
    if (!(await confirm(t('彻底删除', 'Delete permanently'), t(`确定彻底删除选中的 ${selected.length} 篇文章？此操作不可撤销。`, `Permanently delete the ${selected.length} selected posts? This cannot be undone.`)))) return
    try {
      await blog.batchPermanentlyDeleteArticles({ ids: selected })
      toast(t('已批量彻底删除', 'Posts permanently deleted'))
      setSelected([])
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  return (
    <>
      <div className="ph">
        <h2>{t('文章', 'Posts')}</h2>
        <span className="kicker" style={{ marginTop: 6 }}>POSTS · {list.length}</span>
        <span className="sp" />
        <Link className="btn pri sm" to="/admin/editor">{t('写文章', 'Write a post')}</Link>
      </div>
      <div className="atabs">
        <Chip solid={tab === 'active'} onClick={() => changeTab('active')}>{t('文章', 'Posts')}</Chip>
        <Chip solid={tab === 'trash'} onClick={() => changeTab('trash')}>{t('回收站', 'Recycle bin')}</Chip>
      </div>
      {selected.length > 0 ? (
        <div className="bulkbar">
          {t('已选', 'Selected')} {selected.length} {t('篇', 'posts')}
          <Button ghost small onClick={() => setSelected([])}>{t('取消', 'Cancel')}</Button>
          <span style={{ flex: 1 }} />
          {tab === 'active'
            ? <Button ghost small onClick={() => void batchTrash()}>{t('移入回收站', 'Move to recycle bin')}</Button>
            : (
              <>
                <Button ghost small onClick={() => void batchRestore()}>{t('恢复', 'Restore')}</Button>
                <Button ghost small onClick={() => void batchPermanent()}>{t('彻底删除', 'Delete permanently')}</Button>
              </>
            )}
        </div>
      ) : null}
      <div className="pcard rv">
        {/* 刷新（翻页 / 切标签 / 删除 / 恢复）时不清空表格：旧行留在原位、
            整块变暗，新数据到达后原地替换。只有第一次加载（还没有任何行）
            才在表格下方显示等待提示。 */}
        <div className="bd2" style={{ padding: '6px 12px', overflowX: 'auto' }} aria-busy={loading} data-busy={String(loading)}>
          <table className="tb" style={{ minWidth: 760 }}>
            <thead>
              <tr>
                <th style={{ width: 32 }}>
                  <Checkbox checked={selected.length === list.length && list.length > 0} onChange={(event) => toggleAll(event.target.checked)} />
                </th>
                <th>{t('标题', 'Title')}</th>
                <th>{t('标签', 'Tags')}</th>
                <th>{t('创建时间', 'Created')}</th>
                <th>{t('操作', 'Actions')}</th>
              </tr>
            </thead>
            <tbody>
              {list.map((item) => (
                <tr key={item.id}>
                  <td>
                    <Checkbox checked={selected.includes(item.id)} onChange={(event) => setSelected(event.target.checked ? [...selected, item.id] : selected.filter((id) => id !== item.id))} />
                  </td>
                  <td>
                    <Link to={tab === 'active' ? `/admin/editor/${item.id}` : '#'}>
                      <b style={{ fontFamily: 'var(--serif)', fontSize: 14.5 }}>{articleTitle(item, locale)}</b>
                      {articleAIGenerated(item, locale) ? <span className="chip" style={{ marginLeft: 6 }}>{t('AI 生成', 'AI')}</span> : null}
                    </Link>
                  </td>
                  <td><div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>{articleTags(item, locale).slice(0, 3).map((tag) => <span key={tag} className="chip">{tag}</span>)}</div></td>
                  <td className="mono" style={{ fontSize: 11 }}>{fmtDateTime(item.created_at)}</td>
                  <td style={{ whiteSpace: 'nowrap' }}>
                    {tab === 'active' ? (
                      <>
                        <Link className="miniact" to={`/admin/editor/${item.id}`}>{t('编辑', 'Edit')}</Link>
                        <button className="miniact danger" type="button" onClick={() => void trash(item)}>{t('删除', 'Delete')}</button>
                      </>
                    ) : (
                      <>
                        <button className="miniact" type="button" onClick={() => void restore(item)}>{t('恢复', 'Restore')}</button>
                        <button className="miniact danger" type="button" onClick={() => void permanent(item)}>{t('彻底删除', 'Delete permanently')}</button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {!loading && list.length === 0 ? <Empty text={t('没有符合条件的文章', 'No posts match this view')} /> : null}
        </div>
      </div>
      {loading && list.length === 0 ? <Loading height={80} visible={pending} /> : null}
      <Pagination align="center" current={page} total={totalPages} pageSize={1} onChange={changePage} />
    </>
  )
}
