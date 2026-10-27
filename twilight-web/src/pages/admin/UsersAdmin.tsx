// UsersAdmin.tsx —— 访客账号管理（仅管理员可见）
// 这里只列出「注册访客」账号；管理员账号由服务端 settings.json 维护，不在此列表。
// UserRow 允许改 username（登录标识）、nickname（展示名）与 email（联系方式）
// 三个字段，密码除外——密码归账号本人所有（可在登录页重置），操作员无法改他人密码。
// 冲突（用户名/邮箱已被占用）时后端拒绝、行保持编辑态以便修正，不会清空已输入内容。

import { useCallback, useEffect, useState } from 'react'
import { Badge, Button, Input, Pagination } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtDateTime } from '../../lib/format'
import type { UserResponse } from 'twilight-blog-api'
import { useToast } from '../../lib/toast'
import { NICKNAME_MAX, USERNAME_MAX, emailError, nicknameError, usernameError } from '../../lib/validate'
import { Empty, Loading } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import { useI18n } from '../../lib/i18n'
import { usePageParam } from '../../lib/page-param'

const PAGE_SIZE = 12

/**
 * The administrator's view of visitor accounts. Every field an operator may
 * correct is editable except the password, which stays with its owner (who can
 * reset it from the sign-in page). Username and nickname are separate fields
 * here on purpose — merging them is how this project got into trouble before.
 */
export default function UsersAdmin() {
  const { t } = useI18n()
  const toast = useToast()
  const [list, setList] = useState<UserResponse[]>([])
  // 页码进 URL：浏览用户资料再后退，列表保持原来的页。
  const [page, setPage] = usePageParam()
  const [total, setTotal] = useState(0)
  const [totalPages, setTotalPages] = useState(1)
  const [loading, setLoading] = useState(true)
  // 首屏加载超过阈值才显示等待提示（见 lib/loading.ts）。
  const pending = useDelayedLoading(loading)
  const [reloadKey, setReloadKey] = useState(0)

  useEffect(() => {
    let alive = true
    setLoading(true)

    const run = async () => {
      try {
        const result = await blog.listUsers({ page, pageSize: PAGE_SIZE })
        if (!alive) return
        setList(result.items)
        setTotal(result.total)
        setTotalPages(Math.max(1, result.total_pages))
      } catch (error) {
        if (!alive) return
        setList([])
        setTotal(0)
        setTotalPages(1)
        toast(getErrorMessage(error, t('加载用户失败', 'Failed to load users')))
      } finally {
        if (alive) setLoading(false)
      }
    }

    void run()
    return () => {
      alive = false
    }
  }, [page, reloadKey, toast])

  const reload = useCallback(() => setReloadKey((value) => value + 1), [])

  return (
    <>
      <div className="ph">
        <h2>{t('用户', 'Users')}</h2>
        <span className="kicker" style={{ marginTop: 6 }}>VISITOR ACCOUNTS · {total}</span>
      </div>
      <p className="hint" style={{ marginBottom: 16 }}>
        {t('这里只列出注册的访客账号；管理员账号由配置文件维护，不在列表中。', 'Only registered visitor accounts are listed here; the administrator account is maintained separately.')}
        {t('用户名用于登录，昵称用于前台展示。', 'The username is used to sign in; the nickname is shown publicly.')}
      </p>
      {list.length === 0 ? (
        loading ? <Loading height={200} visible={pending} /> : <Empty text={t('还没有注册用户', 'No registered users yet')} />
      ) : (
        // 保存 / 翻页后的刷新：旧行原地变暗，新数据到达后替换。
        // data-busy 挂在内层 .ulist 而不是 .pcard.rv：卡片自身有一条 fill: both
        // 的入场动画，动画的终值会一直盖住 opacity，挂在它上面淡不下去。
        <div className="pcard rv">
          <div className="ulist" aria-busy={loading} data-busy={String(loading)}>
            {list.map((item) => (
              <UserRow key={item.id} user={item} onSaved={reload} />
            ))}
          </div>
        </div>
      )}
      <Pagination align="center" current={page} total={totalPages} pageSize={1} onChange={setPage} />
    </>
  )
}

function UserRow({ user, onSaved }: { user: UserResponse; onSaved: () => void }) {
  const { t } = useI18n()
  const toast = useToast()
  const [editing, setEditing] = useState(false)
  const [username, setUsername] = useState(user.username)
  const [nickname, setNickname] = useState(user.nickname)
  const [email, setEmail] = useState(user.email)
  const [busy, setBusy] = useState(false)

  const startEdit = () => {
    setUsername(user.username)
    setNickname(user.nickname)
    setEmail(user.email)
    setEditing(true)
  }

  const save = async () => {
    const trimmedUsername = username.trim()
    const trimmedNickname = nickname.trim()
    const trimmedEmail = email.trim()

    const usernameErr = usernameError(trimmedUsername)
    if (usernameErr) {
      toast(usernameErr)
      return
    }
    const nicknameErr = nicknameError(trimmedNickname)
    if (nicknameErr) {
      toast(nicknameErr)
      return
    }
    const emailErr = emailError(trimmedEmail)
    if (emailErr) {
      toast(emailErr)
      return
    }

    setBusy(true)
    try {
      await blog.updateUser(user.id, { username: trimmedUsername, nickname: trimmedNickname, email: trimmedEmail })
      toast(t('已保存', 'Saved'))
      setEditing(false)
      onSaved()
    } catch (error) {
      // The server rejects a username/address another account already uses;
      // keep the row in edit mode so the operator can correct it without retyping.
      toast(getErrorMessage(error, t('保存失败', 'Save failed')))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className={`urow${editing ? ' editing' : ''}`}>
      {editing ? (
        <div className="uedit">
          <div className="fld">
            <label>{t('用户名（登录用）', 'Username (for signing in)')}</label>
            <Input variant="login" maxLength={USERNAME_MAX} value={username} onChange={(event) => setUsername(event.target.value)} />
          </div>
          <div className="fld">
            <label>{t('昵称（对外展示）', 'Nickname (public)')}</label>
            <Input variant="login" maxLength={NICKNAME_MAX} value={nickname} onChange={(event) => setNickname(event.target.value)} />
          </div>
          <div className="fld">
            <label>{t('邮箱（联系方式）', 'Email (contact)')}</label>
            <Input variant="login" type="email" value={email} onChange={(event) => setEmail(event.target.value)} />
          </div>
          <div className="act-row">
            <span className="sp" />
            <Button ghost small disabled={busy} onClick={() => setEditing(false)}>{t('取消', 'Cancel')}</Button>
            <Button primary small disabled={busy} onClick={() => void save()}>{busy ? t('保存中…', 'Saving…') : t('保存', 'Save')}</Button>
          </div>
        </div>
      ) : (
        <>
          <span className="cav">{(user.nickname.slice(0, 1) || t('访', '?')).toUpperCase()}</span>
          <div className="uinfo">
            <div className="uline">
              <b>{user.nickname || t('访客', 'Guest')}</b>
              <span className="utag">@{user.username || '—'}</span>
              {user.has_password ? null : <Badge>{t('未设密码', 'No password')}</Badge>}
            </div>
            <span className="umeta">#{user.id} · {user.email} · {t('注册于', 'Joined')} {fmtDateTime(user.created_at)}</span>
          </div>
          <Button ghost small onClick={startEdit}>{t('编辑', 'Edit')}</Button>
        </>
      )}
    </div>
  )
}
