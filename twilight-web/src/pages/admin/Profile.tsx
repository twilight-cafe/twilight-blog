// Profile.tsx —— 账号页（所有登录身份都能打开）
// 按角色分流，但两种身份都遵循同一条铁律：**登录名与显示名是两个独立字段**，
// 界面上必须分成两个输入框，不得互相回填——历史版本曾把它们合成一个字段，
// 导致「改个显示名」连带改掉了登录方式。
//   - 管理员：登录名 + 显示名可改；改密码走**与访客同一条** PUT /api/me/password
//     （两个角色共用下面的 PasswordCard）。这条路径曾经对管理员返回 403，
//     于是这个卡片永远显示「修改失败」——那是后端的 bug，不是本页的问题。
//   - 访客：用户名 + 昵称可改，邮箱只读（已验证的联系方式，仅管理员可改派）。
// 每次保存后 refreshAuth() 重新拉取身份：昵称被评论表单与侧栏引用，
// 改用户名/密码会让服务端轮换会话，本地缓存必须及时刷新。

import { useState, type FormEvent } from 'react'
import { Button, Input } from 'ink-design'
import type { MeResponse, UserResponse } from 'twilight-blog-api'
import { blog, getErrorMessage, refreshAuth, setCurrentUser, useCurrentUser } from '../../lib/client'
import { PASSWORD_RULE_TEXT, validatePassword } from '../../lib/password'
import { NICKNAME_MAX, USERNAME_MAX, nicknameError, usernameError } from '../../lib/validate'
import { useToast } from '../../lib/toast'

/**
 * The one page every signed-in identity can open. Both roles get the same
 * two-field treatment (login name vs display name); the differences are only
 * where the values live and which sub-cards appear.
 */
export default function Profile() {
  const user = useCurrentUser()

  if (!user) return null

  return (
    <>
      <div className="ph">
        <h2>{user.role === 'admin' ? '管理员账号' : '我的资料'}</h2>
        <span className="kicker" style={{ marginTop: 6 }}>ACCOUNT · {user.role === 'admin' ? 'ADMIN' : 'USER'}</span>
      </div>
      {user.role === 'admin'
        ? <AdminProfile name={user.name || 'admin'} username={user.username || ''} />
        : <UserProfile />}
    </>
  )
}

function AdminProfile({ name, username }: { name: string; username: string }) {
  const toast = useToast()
  const [displayName, setDisplayName] = useState(name)
  const [loginName, setLoginName] = useState(username)
  const [busy, setBusy] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const trimmedName = displayName.trim()
    const trimmedLogin = loginName.trim()
    const nameErr = nicknameError(displayName, '显示名称')
    if (nameErr) {
      toast(nameErr)
      return
    }
    // 登录名留空表示「这次不改」——改登录名影响下次怎么登录，不该被误触发。
    const changingLogin = trimmedLogin !== '' && trimmedLogin !== username
    if (changingLogin) {
      const error = usernameError(trimmedLogin)
      if (error) {
        toast(error)
        return
      }
    }

    setBusy(true)
    try {
      const me = await blog.updateAdminProfile({
        name: trimmedName,
        ...(changingLogin ? { username: trimmedLogin } : {}),
      })
      setCurrentUser(me)
      toast(changingLogin ? '已更新，下次登录请使用新的登录名' : '显示名称已更新')
    } catch (error) {
      toast(getErrorMessage(error, '保存失败'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <form className="pcard rv" onSubmit={submit}>
        <div className="ph2"><b>站长资料</b></div>
        <div className="bd2">
          <p className="hint" style={{ marginBottom: 14 }}>
            显示名称用于前台署名与后台侧栏；登录名只用于登录。两者互相独立，改一个不会影响另一个。
          </p>
          <div className="fld">
            <label>显示名称 Display name</label>
            <Input variant="login" maxLength={NICKNAME_MAX} value={displayName} onChange={(event) => setDisplayName(event.target.value)} />
          </div>
          <div className="fld" style={{ marginTop: 12 }}>
            <label>登录名 Login name（留空表示不修改）</label>
            <Input
              variant="login"
              maxLength={USERNAME_MAX}
              placeholder={username}
              value={loginName}
              onChange={(event) => setLoginName(event.target.value)}
            />
          </div>
          <div className="act-row">
            <span className="sp" />
            <span className="set-state">
              {changingLoginHint(displayName, name, loginName, username)}
            </span>
            <Button primary small type="submit" disabled={busy}>{busy ? '保存中…' : '保存'}</Button>
          </div>
        </div>
      </form>
      <div className="pcard rv" style={{ marginTop: 16 }}>
        <div className="ph2"><b>密码</b></div>
        <div className="bd2">
          <p className="hint">
            管理员密码在安装时设置，目前没有通过控制台修改的入口。如需更换，请修改服务端配置后重新安装或直接更新
            settings.json 中的 password_hash。
          </p>
        </div>
      </div>
    </>
  )
}

/**
 * 保存按钮左侧的状态提示。改登录名是一件「下次登录会不一样」的事，值得在按钮
 * 旁边先说清楚，而不是等按下去才从 toast 里知道。
 */
function changingLoginHint(displayName: string, currentName: string, loginName: string, currentLogin: string): string {
  const parts: string[] = []
  if (displayName.trim() !== currentName) parts.push('显示名称有改动')
  const nextLogin = loginName.trim()
  if (nextLogin !== '' && nextLogin !== currentLogin) parts.push('登录名有改动')
  return parts.length > 0 ? parts.join(' · ') : '尚未修改'
}

function UserProfile() {
  // Profile 只在 role === 'user' 时渲染本组件，身份形状由上游守卫保证。
  // MeResponse 是单一 interface（非可辨识联合），Extract 会得到 never，
  // 这里用交集断言表达「上游已保证 role==='user' 且 user 必在」。
  const user = useCurrentUser() as MeResponse & { role: 'user'; user: UserResponse }
  const account = user.user

  return (
    <>
      <IdentityCard account={account} />
      <PasswordCard />
    </>
  )
}

function IdentityCard({ account }: { account: UserResponse }) {
  const toast = useToast()
  const [nicknameValue, setNicknameValue] = useState(account.nickname)
  const [usernameValue, setUsernameValue] = useState(account.username)
  const [busy, setBusy] = useState(false)

  // 在渲染期就算出来：既要给提交逻辑用，也要给按钮旁边的状态提示用。
  // 同一份判断只写一次，提示与真正发出去的内容才不会各说各话。
  const trimmedNickname = nicknameValue.trim()
  const trimmedUsername = usernameValue.trim()
  const changingUsername = trimmedUsername !== account.username
  const nicknameChanged = trimmedNickname !== account.nickname

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const nicknameErr = nicknameError(trimmedNickname)
    if (nicknameErr) {
      toast(nicknameErr)
      return
    }
    if (changingUsername) {
      const error = usernameError(trimmedUsername)
      if (error) {
        toast(error)
        return
      }
    }

    setBusy(true)
    try {
      // 只把真正变化的字段发出去：服务端靠「键是否存在」区分「不改」与「清空」，
      // 与其让服务端猜，不如这里就别发。
      await blog.updateProfile({
        ...(changingUsername ? { username: trimmedUsername } : {}),
        nickname: trimmedNickname,
      })
      // The identity cache holds the old values; re-read it so the comment form
      // and the sidebar agree with the server.
      await refreshAuth()
      toast(changingUsername ? '已更新，下次登录请使用新的用户名' : '昵称已更新')
    } catch (error) {
      toast(getErrorMessage(error, '保存失败'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className="pcard rv" onSubmit={submit}>
      <div className="ph2"><b>资料</b></div>
      <div className="bd2">
        <p className="hint" style={{ marginBottom: 14 }}>
          用户名用于登录，昵称用于前台展示（评论署名等）；两者独立，改其一不影响另一个。
        </p>
        <div className="fld">
          <label>用户名 Username（登录用）</label>
          <Input
            variant="login"
            maxLength={USERNAME_MAX}
            autoComplete="username"
            value={usernameValue}
            onChange={(event) => setUsernameValue(event.target.value)}
          />
        </div>
        <div className="fld" style={{ marginTop: 12 }}>
          <label>昵称 Nickname（对外展示）</label>
          <Input variant="login" maxLength={NICKNAME_MAX} value={nicknameValue} onChange={(event) => setNicknameValue(event.target.value)} />
        </div>
        <div className="fld" style={{ marginTop: 12 }}>
          <label>邮箱 Email（已验证的联系方式，不可自行修改）</label>
          <Input variant="login" value={account.email} disabled readOnly />
        </div>
        <div className="act-row">
          <span className="sp" />
          <span className="set-state">
            {changingUsername ? '用户名有改动，下次登录需用新用户名' : (nicknameChanged ? '昵称有改动' : '尚未修改')}
          </span>
          <Button primary small type="submit" disabled={busy}>{busy ? '保存中…' : '保存'}</Button>
        </div>
      </div>
    </form>
  )
}

function PasswordCard() {
  const toast = useToast()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!current) {
      toast('请输入当前密码')
      return
    }
    const passwordError = validatePassword(next)
    if (passwordError) {
      toast(passwordError)
      return
    }
    if (next.trim() !== confirm.trim()) {
      toast('两次输入的新密码不一致')
      return
    }

    setBusy(true)
    try {
      await blog.changePassword({ currentPassword: current.trim(), newPassword: next.trim() })
      setCurrent('')
      setNext('')
      setConfirm('')
      // The server rotates the session: every other device is signed out and this
      // one receives fresh cookies, so the cached identity must be re-read.
      await refreshAuth()
      toast('密码已更新，其他设备需要重新登录')
    } catch (error) {
      toast(getErrorMessage(error, '修改失败'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className="pcard rv" style={{ marginTop: 16 }} onSubmit={submit}>
      <div className="ph2"><b>修改密码</b></div>
      <div className="bd2">
        <div className="fld">
          <label>当前密码 Current password</label>
          <Input variant="login" type="password" autoComplete="current-password" value={current} onChange={(event) => setCurrent(event.target.value)} />
        </div>
        <div className="fld" style={{ marginTop: 12 }}>
          <label>新密码 New password</label>
          <Input variant="login" type="password" autoComplete="new-password" value={next} onChange={(event) => setNext(event.target.value)} />
        </div>
        <div className="fld" style={{ marginTop: 12 }}>
          <label>确认新密码 Confirm</label>
          <Input variant="login" type="password" autoComplete="new-password" value={confirm} onChange={(event) => setConfirm(event.target.value)} />
        </div>
        <p className="hint" style={{ marginTop: 10 }}>密码要求：{PASSWORD_RULE_TEXT}。</p>
        <div className="act-row">
          <span className="sp" />
          <Button primary small type="submit" disabled={busy}>{busy ? '提交中…' : '修改密码'}</Button>
        </div>
      </div>
    </form>
  )
}
