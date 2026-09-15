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

type Tab = 'all' | 'trash'

function authorLabel(comment: Comment): string {
  if (comment.author.type === 'admin') return comment.author.name || '管理员'
  // Never derive the display name from the address; `nickname` is the public
  // name and `email` is only sent to admin-authenticated callers.
  const nickname = (comment.author.nickname || '').trim()
  return nickname || '访客'
}

function authorEmail(comment: Comment): string {
  return comment.author.type === 'user' ? (comment.author.email || '') : ''
}

export default function CommentsAdmin() {
  const toast = useToast()
  const { confirm } = useDialog()
  const [tab, setTab] = useState<Tab>('all')
  const [list, setList] = useState<Comment[]>([])
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(1)
  const [loading, setLoading] = useState(true)
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
        toast(getErrorMessage(error, '加载评论失败'))
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
    if (!(await confirm('删除评论', '确定将这条评论移入回收站？'))) return
    try {
      await blog.deleteComment(comment.id)
      toast('已移入回收站')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const restore = async (comment: Comment) => {
    try {
      await blog.restoreComment(comment.id)
      toast('已恢复')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const permanent = async (comment: Comment) => {
    if (!(await confirm('彻底删除评论', '确定彻底删除这条评论？此操作不可撤销。'))) return
    try {
      await blog.permanentlyDeleteComment(comment.id)
      toast('已彻底删除')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const targetLabel = (comment: Comment) => {
    const map = { article: '文章', moment: '微语', page: '页面' } as const
    return `${map[comment.target_type]} #${comment.target_id}`
  }

  return (
    <>
      <div className="ph">
        <h2>评论</h2>
        <span className="kicker" style={{ marginTop: 6 }}>COMMENTS · {list.length}</span>
      </div>
      <div className="atabs">
        <Chip solid={tab === 'all'} onClick={() => changeTab('all')}>全部</Chip>
        <Chip solid={tab === 'trash'} onClick={() => changeTab('trash')}>回收站</Chip>
      </div>
      {loading ? <Loading height={160} /> : list.length === 0 ? <Empty text="当前列表没有评论" /> : list.map((comment) => (
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
            {comment.parent_id ? <span className="chip">回复</span> : null}
          </div>
          <div className="bd2" style={{ paddingTop: 4 }}>
            <div style={{ fontSize: 14, whiteSpace: 'pre-wrap' }}>{comment.content}</div>
            <div style={{ display: 'flex', gap: 8, marginTop: 12, flexWrap: 'wrap' }}>
              {tab === 'all'
                ? <Button ghost small onClick={() => void remove(comment)}>删除</Button>
                : (
                  <>
                    <Button ghost small onClick={() => void restore(comment)}>恢复</Button>
                    <Button ghost small onClick={() => void permanent(comment)}>彻底删除</Button>
                  </>
                )}
            </div>
          </div>
        </div>
      ))}
      <Pagination current={page} total={totalPages} pageSize={1} onChange={setPage} />
    </>
  )
}
