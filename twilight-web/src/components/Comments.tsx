// Comments.tsx —— 评论区（公开页面用）
//
// 提交的前置条件：
//   - 必须登录（visitor 账号或 admin）。未登录时表单位显示一张**内嵌登录卡片**
//     （InlineAuth，与登录页同一个「用户名 + 密码」表单，没有身份切换），
//     登录成功后就地变成评论框，不跳转、不丢上下文。
//   - 内容必填、≤2000 字。表单里没有昵称字段：署名由服务端取自作者身份
//     （访客账号的 nickname / 站长的展示名），客户端无权替作者自取署名。
//     表单里也没有邮箱字段——「邮箱」仅出现在 admin 鉴权后的响应里。
//   - 鉴权靠 httpOnly cookie 会话，前端不持有 token。
//
// 分页与嵌套：
//   - listComments 每页返回的是「若干根评论 + 它们的完整子孙」，所以每页天然是
//     若干棵完整子树；前端把多页摊平成扁平列表，再用 buildTree 按 parent_id 重组。
//   - 嵌套层级本身没有在前端做深度限制，深度由后端决定。
//
// 更新策略（非乐观更新）：
//   - 点赞/取消点赞：等服务器返回被更新的那条评论，再精准替换列表里对应项，
//     不整页重拉。
//   - 发帖/回复成功：刷新第 1 页（reloadFirstPage），不是乐观插入。

import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { Badge, Button, Input } from 'ink-design'
import { blog, getErrorMessage, setCurrentUser, useCurrentUser } from '../lib/client'
import { fmtRelative } from '../lib/format'
import type { Comment, TargetType } from 'twilight-blog-api'
import { useToast } from '../lib/toast'
import { Avatar, Empty, Loading } from './common'
import ContentView from './ContentView'

interface CommentsProps {
  targetType: TargetType
  targetId: number
  enabled?: boolean
}

type CommentNode = Comment & { children: CommentNode[] }

type LoadMode = 'replace' | 'append'

const PAGE_SIZE = 12
const CONTENT_MAX = 2000

function authorLabel(comment: Comment): string {
  if (comment.author.type === 'admin') return comment.author.name || '管理员'
  // `email` is only present on admin-authenticated responses, so the display
  // name can never be derived from it.
  const nickname = (comment.author.nickname || '').trim()
  return nickname || '访客'
}

function authorLetter(comment: Comment): string {
  // authorLabel 恒非空（后备「管理员」/「访客」），首字母不会是空串。
  return authorLabel(comment).slice(0, 1).toUpperCase()
}

/**
 * The list endpoint returns one page of root comments together with all of
 * their descendants, so every page holds complete subtrees and the accumulated
 * flat list can be reassembled as a tree.
 */
function buildTree(comments: Comment[]): CommentNode[] {
  const map = new Map<number, CommentNode>()
  const roots: CommentNode[] = []

  const nodes = comments.map((item) => {
    const node: CommentNode = { ...item, children: [] }
    map.set(item.id, node)
    return node
  })

  // 同一次遍历建出的节点必然在 map 里，无需再查空。
  comments.forEach((item, index) => {
    const node = nodes[index]
    if (item.parent_id && map.has(item.parent_id)) {
      map.get(item.parent_id)?.children.push(node)
    } else {
      roots.push(node)
    }
  })

  const sortByCreated = (nodes: CommentNode[]) => {
    nodes.sort((a, b) => a.created_at - b.created_at)
    nodes.forEach((node) => sortByCreated(node.children))
  }
  sortByCreated(roots)

  return roots
}

export default function Comments({ targetType, targetId, enabled = true }: CommentsProps) {
  const toast = useToast()
  const [items, setItems] = useState<Comment[]>([])
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(1)
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)

  // Incremented for every request; a response whose id no longer matches is
  // stale (target changed, or the component unmounted) and is dropped.
  const requestRef = useRef(0)

  const loadPage = useCallback(
    async (nextPage: number, mode: LoadMode) => {
      const requestId = requestRef.current + 1
      requestRef.current = requestId

      if (mode === 'replace') setLoading(true)
      else setLoadingMore(true)

      try {
        const result = await blog.listComments({ targetType, targetId, page: nextPage, pageSize: PAGE_SIZE })
        if (requestRef.current !== requestId) return

        setItems((current) => {
          if (mode === 'replace') return result.items
          const seen = new Set(current.map((item) => item.id))
          return [...current, ...result.items.filter((item) => !seen.has(item.id))]
        })
        setPage(result.page || nextPage)
        setTotal(result.total)
        setTotalPages(Math.max(1, result.total_pages))
      } catch (error) {
        if (requestRef.current !== requestId) return
        if (mode === 'replace') {
          setItems([])
          setTotal(0)
          setTotalPages(1)
        } else {
          toast(getErrorMessage(error, '加载更多评论失败'))
        }
      } finally {
        if (requestRef.current === requestId) {
          setLoading(false)
          setLoadingMore(false)
        }
      }
    },
    [targetId, targetType, toast],
  )

  useEffect(() => {
    void loadPage(1, 'replace')
    return () => {
      // Invalidate any in-flight response for this target.
      requestRef.current += 1
    }
  }, [loadPage])

  const nodes = useMemo(() => buildTree(items), [items])
  const hasMore = page < totalPages

  // A like/unlike only rewrites the one comment the server sent back.
  const applyUpdated = useCallback((updated: Comment) => {
    setItems((current) => current.map((item) => (item.id === updated.id ? updated : item)))
  }, [])

  const reloadFirstPage = useCallback(() => {
    void loadPage(1, 'replace')
  }, [loadPage])

  return (
    <section className="cmt-sec">
      <h3>评论 <span>· {total} 条</span></h3>
      {enabled ? (
        <CommentForm targetType={targetType} targetId={targetId} onPosted={reloadFirstPage} />
      ) : (
        <div className="cform" style={{ textAlign: 'center', color: 'var(--faint)' }}>评论已关闭</div>
      )}
      {loading ? <Loading height={100} /> : nodes.length === 0 ? <Empty text="还没有评论" /> : (
        <>
          <div className="cmt-list">
            {nodes.map((comment) => (
              <CommentItemView
                key={comment.id}
                comment={comment}
                targetType={targetType}
                targetId={targetId}
                onUpdated={applyUpdated}
                onReplyPosted={reloadFirstPage}
              />
            ))}
          </div>
          {hasMore ? (
            <div style={{ display: 'flex', justifyContent: 'center', marginTop: 14 }}>
              <Button ghost small disabled={loadingMore} onClick={() => void loadPage(page + 1, 'append')}>
                {loadingMore ? '加载中…' : '加载更多'}
              </Button>
            </div>
          ) : null}
        </>
      )}
    </section>
  )
}

/**
 * The embedded sign-in card shown where the comment box would be. It mirrors
 * the real sign-in page (one username/password pair, no identity switch) but
 * signs in in place: after success the caller's `useCurrentUser()` re-renders
 * the comment form without any navigation, so reading position is preserved.
 */
function InlineAuth() {
  const toast = useToast()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!username.trim()) {
      toast('请输入用户名（或注册邮箱）')
      return
    }
    if (!password) {
      toast('请输入密码')
      return
    }

    setBusy(true)
    try {
      // One endpoint for both identities: the server decides who this is from the
      // username, so the card needs no "administrator / visitor" switch.
      const me = await blog.login({ username: username.trim(), password })
      setCurrentUser(me)
      toast('登录成功，欢迎参与讨论')
    } catch (error) {
      toast(getErrorMessage(error, '登录失败'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className="cform" onSubmit={submit}>
      <p className="hint" style={{ marginBottom: 10 }}>
        登录后即可发言、点赞与回复。你的昵称取自账号资料，评论署名与账号保持一致。
      </p>
      <div className="fld">
        <Input
          variant="login"
          autoComplete="username"
          placeholder="用户名或注册邮箱"
          value={username}
          onChange={(event) => setUsername(event.target.value)}
        />
      </div>
      <div className="fld" style={{ marginTop: 10 }}>
        <Input
          variant="login"
          type="password"
          autoComplete="current-password"
          placeholder="密码"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
        />
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 12 }}>
        <Button primary small type="submit" disabled={busy}>{busy ? '登录中…' : '登录'}</Button>
        <span style={{ flex: 1 }} />
        <Link to="/admin/register">注册新账号</Link>
        <span>·</span>
        <Link to="/admin/forgot">忘记密码</Link>
      </div>
    </form>
  )
}

function CommentForm({
  targetType,
  targetId,
  onPosted,
}: {
  targetType: TargetType
  targetId: number
  onPosted: () => void
}) {
  const toast = useToast()
  const user = useCurrentUser()
  const [content, setContent] = useState('')
  const [busy, setBusy] = useState(false)

  // The signature comes from the identity, so there is nothing to collect:
  // admins sign with the configured display name, visitors with the account
  // nickname.
  const isAdmin = user?.role === 'admin'

  if (!user) return <InlineAuth />

  const submit = async () => {
    const trimmed = content.trim()
    if (!trimmed) {
      toast('内容不能为空')
      return
    }
    if (trimmed.length > CONTENT_MAX) {
      toast(`评论不能超过 ${CONTENT_MAX} 个字符`)
      return
    }

    setBusy(true)
    try {
      await blog.createComment({ targetType, targetId, content: trimmed })
      toast('评论已发布')
      setContent('')
      onPosted()
    } catch (error) {
      toast(getErrorMessage(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="cform">
      <Input
        variant="box"
        multiline
        maxLength={CONTENT_MAX}
        placeholder="写下你的想法…"
        value={content}
        onChange={(event) => setContent(event.target.value)}
      />
      <div className="foot">
        <span className="cmt-authed">
          {isAdmin ? '管理员评论' : `以 ${user.user?.nickname || '访客'} 的身份评论`}
        </span>
        <span style={{ flex: 1 }} />
        <Button primary small disabled={busy} onClick={() => void submit()}>发布评论</Button>
      </div>
    </div>
  )
}

function CommentItemView({
  comment,
  targetType,
  targetId,
  onUpdated,
  onReplyPosted,
}: {
  comment: CommentNode
  targetType: TargetType
  targetId: number
  onUpdated: (updated: Comment) => void
  onReplyPosted: () => void
}) {
  const toast = useToast()
  const user = useCurrentUser()
  const [replying, setReplying] = useState(false)
  const [liking, setLiking] = useState(false)

  // Only a visitor account owns a like: the administrator can see the count but
  // the server rejects the action, so it is not offered.
  const canLike = user?.role === 'user'
  const isAdmin = user?.role === 'admin'

  const toggleLike = async () => {
    // 点赞按钮只在 canLike 时渲染 onClick，无需再守 !canLike；这里只防连点重入。
    if (liking) return
    setLiking(true)
    try {
      const updated = comment.liked_by_me
        ? await blog.unlikeComment(comment.id)
        : await blog.likeComment(comment.id)
      // 接口返回空（无变化）时直接退出；只有本条评论变化：不做整页重取。
      // 不写成「if (updated) onUpdated(updated)」——它是 try 块末句时隐式 else
      // 区域为零长，v8 覆盖率会给出一个永远无法覆盖的幽灵分支。
      if (!updated) return
      onUpdated(updated)
    } catch (error) {
      toast(getErrorMessage(error, '操作失败'))
    } finally {
      setLiking(false)
    }
  }

  const likeTip = !canLike
    ? (isAdmin ? '管理员仅可查看点赞数，不能点赞' : '登录后可点赞')
    : undefined

  return (
    <div className="cmt">
      <Avatar letter={authorLetter(comment)} alt={comment.author.type !== 'admin'} />
      <div style={{ flex: 1 }}>
        <div className="nm">
          <b>{authorLabel(comment)}</b>
          {comment.author.type === 'admin' ? <Badge>作者</Badge> : null}
          <time>{fmtRelative(comment.created_at)}</time>
        </div>
        <div className="bd"><ContentView text={comment.content} /></div>
        <div className="ops">
          <button
            type="button"
            className={`like-btn ${comment.liked_by_me ? 'on' : ''}${canLike ? '' : ' admin-view'}${liking ? ' busy' : ''}`}
            data-tip={likeTip}
            onClick={canLike ? () => void toggleLike() : undefined}
            aria-disabled={!canLike || liking || undefined}
          >
            {comment.liked_by_me ? '♥' : '♡'} {comment.like_count}
          </button>
          {user ? (
            <button type="button" className="reply-btn" onClick={() => setReplying((value) => !value)}>回复</button>
          ) : null}
        </div>
        {replying ? (
          <div style={{ marginTop: 10 }}>
            <ReplyForm
              parentId={comment.id}
              targetType={targetType}
              targetId={targetId}
              onDone={() => setReplying(false)}
              onPosted={() => { setReplying(false); onReplyPosted() }}
            />
          </div>
        ) : null}
        {comment.children.length > 0 ? (
          <div className="cmt-list" style={{ marginLeft: 16, marginTop: 8 }}>
            {comment.children.map((child) => (
              <CommentItemView
                key={child.id}
                comment={child}
                targetType={targetType}
                targetId={targetId}
                onUpdated={onUpdated}
                onReplyPosted={onReplyPosted}
              />
            ))}
          </div>
        ) : null}
      </div>
    </div>
  )
}

function ReplyForm({
  parentId,
  targetType,
  targetId,
  onDone,
  onPosted,
}: {
  parentId: number
  targetType: TargetType
  targetId: number
  onDone: () => void
  onPosted: () => void
}) {
  const toast = useToast()
  const [content, setContent] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    const trimmed = content.trim()
    if (!trimmed) {
      toast('内容不能为空')
      return
    }
    if (trimmed.length > CONTENT_MAX) {
      toast(`回复不能超过 ${CONTENT_MAX} 个字符`)
      return
    }

    setBusy(true)
    try {
      await blog.createComment({ targetType, targetId, parentId, content: trimmed })
      toast('回复已发布')
      setContent('')
      onPosted()
    } catch (error) {
      toast(getErrorMessage(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="cform" style={{ padding: 12 }}>
      <Input
        variant="box"
        multiline
        maxLength={CONTENT_MAX}
        placeholder="写下回复…"
        value={content}
        onChange={(event) => setContent(event.target.value)}
      />
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 10 }}>
        <Button ghost small onClick={onDone}>取消</Button>
        <Button primary small disabled={busy} onClick={() => void submit()}>回复</Button>
      </div>
    </div>
  )
}
