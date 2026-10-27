// CommentsAdmin.tsx —— 评论管理（全部 / 回收站）
// 与文章/页面一致的软删模型：all 下的「删除」是移入回收站（deleteComment），
// trash 下才可永久删（permanentlyDeleteComment）。切 tab 时同步把 page 归 1，
// 避免用上一 tab 的页码去请求新 tab。邮箱只展示 user 类型评论（admin 调用才回传），
// 且绝不由邮箱派生显示名，公开名以 nickname 为准。

import { useCallback, useEffect, useState } from 'react'
import { Button, Chip, Pagination } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtRelative } from '../../lib/format'
import type { Comment } from 'twilight-blog-api'
import { useDialog } from '../../lib/dialog'
import { useToast } from '../../lib/toast'
import { Avatar, Empty, Loading } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import { t, useI18n } from '../../lib/i18n'
import { usePageParam } from '../../lib/page-param'

type Tab = 'all' | 'trash'

function authorLabel(comment: Comment): string {
  if (comment.author.type === 'admin') return comment.author.name || t('管理员', 'Admin')
  // Never derive the display name from the address; `nickname` is the public
  // name and `email` is only sent to admin-authenticated callers.
  const nickname = (comment.author.nickname || '').trim()
  return nickname || t('访客', 'Guest')
}

function authorEmail(comment: Comment): string {
  return comment.author.type === 'user' ? (comment.author.email || '') : ''
}

export default function CommentsAdmin() {
  const { t } = useI18n()
  const toast = useToast()
  const { confirm } = useDialog()
  const [tab, setTab] = useState<Tab>('all')
  const [list, setList] = useState<Comment[]>([])
  // 页码进 URL：切到前台文章再后退，评论管理仍停在原来的页。
  const [page, setPage] = usePageParam()
  const [totalPages, setTotalPages] = useState(1)
  const [loading, setLoading] = useState(true)
  // 首屏加载超过阈值才显示等待提示（见 lib/loading.ts）。
  const pending = useDelayedLoading(loading)
  const [reloadKey, setReloadKey] = useState(0)

  // One effect owns the fetch: changing the tab resets the page in the click
  // handler, so the new tab is never requested with the previous page number.
  useEffect(() => {
    let alive = true
    setLoading(true)

    const run = async () => {
      try {
        const result = tab === 'all'
          ? await blog.listAllComments({ page, pageSize: 12 })
          : await blog.listTrashComments({ page, pageSize: 12 })
        if (!alive) return
        setList(result.items)
        setTotalPages(result.total_pages)
      } catch (error) {
        if (!alive) return
        setList([])
        setTotalPages(1)
        toast(getErrorMessage(error, t('加载评论失败', 'Failed to load comments')))
      } finally {
        if (alive) setLoading(false)
      }
    }

    void run()
    return () => {
      alive = false
    }
  }, [page, reloadKey, tab, toast])

  const reload = useCallback(() => setReloadKey((value) => value + 1), [])

  const changeTab = (next: Tab) => {
    if (next === tab) return
    setPage(1)
    setTab(next)
  }

  const remove = async (comment: Comment) => {
    if (!(await confirm(t('删除评论', 'Delete comment'), t('确定将这条评论移入回收站？', 'Move this comment to the recycle bin?')))) return
    try {
      await blog.deleteComment(comment.id)
      toast(t('已移入回收站', 'Moved to the recycle bin'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const restore = async (comment: Comment) => {
    try {
      await blog.restoreComment(comment.id)
      toast(t('已恢复', 'Restored'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const permanent = async (comment: Comment) => {
    if (!(await confirm(t('彻底删除评论', 'Delete comment permanently'), t('确定彻底删除这条评论？此操作不可撤销。', 'Permanently delete this comment? This cannot be undone.')))) return
    try {
      await blog.permanentlyDeleteComment(comment.id)
      toast(t('已彻底删除', 'Permanently deleted'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const targetLabel = (comment: Comment) => {
    const map = { article: t('文章', 'Post'), moment: t('微语', 'Moment'), page: t('页面', 'Page') } as const
    return `${map[comment.target_type]} #${comment.target_id}`
  }

  return (
    <>
      <div className="ph">
        <h2>{t('评论', 'Comments')}</h2>
        <span className="kicker" style={{ marginTop: 6 }}>COMMENTS · {list.length}</span>
      </div>
      <div className="atabs">
        <Chip solid={tab === 'all'} onClick={() => changeTab('all')}>{t('全部', 'All')}</Chip>
        <Chip solid={tab === 'trash'} onClick={() => changeTab('trash')}>{t('回收站', 'Recycle bin')}</Chip>
      </div>
      {list.length === 0 ? (
        loading ? <Loading height={160} visible={pending} /> : <Empty text={t('当前列表没有评论', 'No comments in this list')} />
      ) : (
        // 刷新时旧列表原地变暗，新数据到达后替换（见 app.css 的 [data-busy]）。
        <div aria-busy={loading} data-busy={String(loading)}>
          {list.map((comment) => (
            <div key={comment.id} className="pcard rv" style={{ marginBottom: 14 }}>
              <div className="ph2">
                <Avatar letter={authorLabel(comment).slice(0, 1).toUpperCase()} alt={comment.author.type !== 'admin'} />
                <div>
                  <b>{authorLabel(comment)}</b>
                  <br />
                  <span className="mono" style={{ fontSize: 10.5, color: 'var(--faint)' }}>
                    {authorEmail(comment) ? `${authorEmail(comment)} · ` : ''}{targetLabel(comment)} · {fmtRelative(comment.created_at)}
                  </span>
                </div>
                <span style={{ flex: 1 }} />
                {comment.parent_id ? <span className="chip">{t('回复', 'Reply')}</span> : null}
              </div>
              <div className="bd2" style={{ paddingTop: 4 }}>
                <div style={{ fontSize: 14, whiteSpace: 'pre-wrap' }}>{comment.content}</div>
                <div style={{ display: 'flex', gap: 8, marginTop: 12, flexWrap: 'wrap' }}>
                  {tab === 'all'
                    ? <Button ghost small onClick={() => void remove(comment)}>{t('删除', 'Delete')}</Button>
                    : (
                      <>
                        <Button ghost small onClick={() => void restore(comment)}>{t('恢复', 'Restore')}</Button>
                        <Button ghost small onClick={() => void permanent(comment)}>{t('彻底删除', 'Delete permanently')}</Button>
                      </>
                    )}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
      <Pagination align="center" current={page} total={totalPages} pageSize={1} onChange={setPage} />
    </>
  )
}
