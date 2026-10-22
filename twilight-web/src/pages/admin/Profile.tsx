// Profile.tsx —— 账号页（所有登录身份都能打开）
// 按角色分流，但两种身份都遵循同一条铁律：**登录名与显示名是两个独立字段**，
// 界面上必须分成两个输入框，不得互相回填——历史版本曾把它们合成一个字段，
// 导致「改个显示名」连带改掉了登录方式。
//   - 管理员：登录名 + 显示名可改，还可以设置自己的头像（复用媒体库，见
//     media/AvatarPicker）；改密码走**与访客同一条** PUT /api/me/password
//     （两个角色共用下面的 PasswordCard）。这条路径曾经对管理员返回 403，
//     于是这个卡片永远显示「修改失败」——那是后端的 bug，不是本页的问题。
//   - 访客：用户名 + 昵称可改，邮箱只读（已验证的联系方式，仅管理员可改派）。
//     头像只有站长有：它是侧栏作者卡上的那一张，而作者卡属于「站长」这个身份，
//     不属于某个访客账号。
// 每次保存后 refreshAuth() 重新拉取身份：昵称被评论表单与侧栏引用，
// 改用户名/密码会让服务端轮换会话，本地缓存必须及时刷新。

import { useState, type FormEvent } from 'react'
import { Button, Input } from 'ink-design'
import { mediaUrl, type MeResponse, type UserResponse } from 'twilight-blog-api'
import { blog, getErrorMessage, refreshAuth, setCurrentUser, useCurrentUser } from '../../lib/client'
import { passwordRuleText, validatePassword } from '../../lib/password'
import { refreshSiteMeta, useSiteAvatar } from '../../lib/site'
import { NICKNAME_MAX, USERNAME_MAX, nicknameError, usernameError } from '../../lib/validate'
import { useToast } from '../../lib/toast'
import { t, useI18n } from '../../lib/i18n'
import AvatarPicker from '../../components/media/AvatarPicker'

/**
 * 头像字段的三种状态。与 viewmodel.OptionalID 一一对应，但这里必须自己表达：
 *   - keep   —— 没碰。字段**不进请求体**，服务端才知道「这次不动头像」；
 *   - set    —— 换了新图，提交它的媒体 id；
 *   - remove —— 明确要摘掉，提交显式的 null。
 *
 * 塌缩成 `number | null` 会同时丢掉「没碰」与「摘掉」的区别——那意味着一次
 * 只改显示名的保存会静默删掉头像，或者用户永远摘不掉头像。
 */
type AvatarEdit =
  | { kind: 'keep' }
  | { kind: 'set'; id: number }
  | { kind: 'remove' }

/**
 * The one page every signed-in identity can open. Both roles get the same
 * two-field treatment (login name vs display name); the differences are only
 * where the values live and which sub-cards appear.
 */
export default function Profile() {
  const { t } = useI18n()
  const user = useCurrentUser()

  if (!user) return null

  return (
    <>
      <div className="ph">
        <h2>{user.role === 'admin' ? t('管理员账号', 'Administrator account') : t('我的资料', 'My profile')}</h2>
        <span className="kicker" style={{ marginTop: 6 }}>ACCOUNT · {user.role === 'admin' ? 'ADMIN' : 'USER'}</span>
      </div>
      {user.role === 'admin'
        ? <AdminProfile name={user.name || 'admin'} username={user.username || ''} />
        : <UserProfile />}
    </>
  )
}

function AdminProfile({ name, username }: { name: string; username: string }) {
  const { t } = useI18n()
  const toast = useToast()
  const [displayName, setDisplayName] = useState(name)
  const [loginName, setLoginName] = useState(username)
  const [avatar, setAvatar] = useState<AvatarEdit>({ kind: 'keep' })
  const [busy, setBusy] = useState(false)
  const [uploading, setUploading] = useState(false)
  // 已保存的那张头像来自站点设置（公开的 GET /api/site），面向前台里所有读者；
  // 它与当前正在编辑的草稿是两回事，所以分开存。
  const savedAvatarUrl = useSiteAvatar()

  // 预览显示哪一个：优先看待保存的改动，没动过才显示服务端上那一张。
  // mediaUrl 与头像在其他地方的地址来自同一个函数，因此这里预演的就是保存后
  // 侧栏会显示的那张图，不会出现「预览里是这张、存下去变成另一张」。
  const displayedAvatarUrl = avatar.kind === 'set'
    ? mediaUrl(avatar.id)
    : avatar.kind === 'remove' ? '' : savedAvatarUrl

  const avatarChanged = avatar.kind !== 'keep'

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const trimmedName = displayName.trim()
    const trimmedLogin = loginName.trim()
    const nameErr = nicknameError(displayName, '显示名称', 'display name')
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
        // 只有真的动了头像才带上这个键：它的缺席就是「不改」的意思。
        ...(avatar.kind === 'keep' ? {} : { avatarMediaId: avatar.kind === 'set' ? avatar.id : null }),
      })
      setCurrentUser(me)
      setAvatar({ kind: 'keep' })
      if (avatarChanged) {
        // 侧栏作者卡（以及站点设置页的预览）订阅的是站点信息那份缓存，
        // 而这条接口回的是会话身份——不主动刷新的话，用户会看到
        // 「保存成功但头像没变」，直到下一次整页刷新。
        await refreshSiteMeta()
      }
      toast(changingLogin
        ? t('已更新，下次登录请使用新的登录名', 'Updated — use the new login name next time')
        : avatarChanged ? t('头像已更新', 'Avatar updated') : t('显示名称已更新', 'Display name updated'))
    } catch (error) {
      toast(getErrorMessage(error, t('保存失败', 'Save failed')))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <form className="pcard rv" onSubmit={submit}>
        <div className="ph2"><b>{t('站长资料', 'Owner profile')}</b></div>
        <div className="bd2">
          <p className="hint" style={{ marginBottom: 14 }}>
            {t('显示名称用于前台署名与后台侧栏；登录名只用于登录。两者互相独立，改一个不会影响另一个。', 'The display name is used for public bylines and the console sidebar; the login name is only for signing in. They are independent.')}
          </p>
          <div className="fld">
            {/* 头像与显示名/登录名在业务上是三件独立的事，但它们在同一个表单里、
                共用同一个「保存」——分开保存会让「我只想换张图」变成两次请求，
                而其中一次的结果无法回滚另一次。 */}
            <label>{t('头像', 'Avatar')}</label>
            <AvatarPicker
              url={displayedAvatarUrl}
              onChange={(id) => setAvatar(id === null ? { kind: 'remove' } : { kind: 'set', id })}
              onBusyChange={setUploading}
              disabled={busy}
            />
            <p className="hint" style={{ marginTop: 8 }}>
              {t('显示在前台侧栏的作者卡上。未设置时用首字母占位；换图或移除都只是解除引用，图片仍留在媒体库。', 'Shown on the author card. Without one, the first letter is used; changing or removing only drops the reference, the file stays in the library.')}
            </p>
          </div>
          <div className="fld" style={{ marginTop: 12 }}>
            <label>{t('显示名称', 'Display name')}</label>
            <Input variant="login" maxLength={NICKNAME_MAX} value={displayName} onChange={(event) => setDisplayName(event.target.value)} />
          </div>
          <div className="fld" style={{ marginTop: 12 }}>
            <label>{t('登录名（留空表示不修改）', 'Login name (leave empty to keep it)')}</label>
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
              {changingLoginHint(displayName, name, loginName, username, avatarChanged)}
            </span>
            {/* 上传没结束就提交会存下一张还没拿到 id 的图，所以两个状态都要挡住。 */}
            <Button primary small type="submit" disabled={busy || uploading}>
              {busy ? t('保存中…', 'Saving…') : t('保存', 'Save')}
            </Button>
          </div>
        </div>
      </form>
      <div className="pcard rv" style={{ marginTop: 16 }}>
        <div className="ph2"><b>{t('密码', 'Password')}</b></div>
        <div className="bd2">
          <p className="hint">
            {t('管理员密码在安装时设置。如需更换，请在服务器上运行 admin set-password 子命令。', 'The administrator password is set during installation. To change it, run the admin set-password command on the server.')}
          </p>
        </div>
      </div>
    </>
  )
}

/**
 * 保存按钮左侧的状态提示。改登录名是一件「下次登录会不一样」的事，值得在按钮
 * 旁边先说清楚，而不是等按下去才从 toast 里知道。头像改动同样列在这里——它是三
 * 个字段里唯一「改了但不改就看不出来」的那个：新图要等到保存后才会上屏。
 */
function changingLoginHint(
  displayName: string,
  currentName: string,
  loginName: string,
  currentLogin: string,
  avatarChanged: boolean,
): string {
  const parts: string[] = []
  if (displayName.trim() !== currentName) parts.push(t('显示名称有改动', 'display name changed'))
  const nextLogin = loginName.trim()
  if (nextLogin !== '' && nextLogin !== currentLogin) parts.push(t('登录名有改动', 'login name changed'))
  if (avatarChanged) parts.push(t('头像有改动', 'avatar changed'))
  return parts.length > 0 ? parts.join(' · ') : t('尚未修改', 'No changes yet')
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
  const { t } = useI18n()
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
      toast(changingUsername ? t('已更新，下次登录请使用新的用户名', 'Updated — use the new username next time') : t('昵称已更新', 'Nickname updated'))
    } catch (error) {
      toast(getErrorMessage(error, t('保存失败', 'Save failed')))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className="pcard rv" onSubmit={submit}>
      <div className="ph2"><b>{t('资料', 'Profile')}</b></div>
      <div className="bd2">
        <p className="hint" style={{ marginBottom: 14 }}>
          {t('用户名用于登录，昵称用于前台展示（评论署名等）；两者独立，改其一不影响另一个。', 'The username is for signing in; the nickname is shown publicly (e.g. comment bylines). They are independent.')}
        </p>
        <div className="fld">
          <label>{t('用户名（登录用）', 'Username (for signing in)')}</label>
          <Input
            variant="login"
            maxLength={USERNAME_MAX}
            autoComplete="username"
            value={usernameValue}
            onChange={(event) => setUsernameValue(event.target.value)}
          />
        </div>
        <div className="fld" style={{ marginTop: 12 }}>
          <label>{t('昵称（对外展示）', 'Nickname (public)')}</label>
          <Input variant="login" maxLength={NICKNAME_MAX} value={nicknameValue} onChange={(event) => setNicknameValue(event.target.value)} />
        </div>
        <div className="fld" style={{ marginTop: 12 }}>
          <label>{t('邮箱（已验证的联系方式，不可自行修改）', 'Email (verified; cannot be changed here)')}</label>
          <Input variant="login" value={account.email} disabled readOnly />
        </div>
        <div className="act-row">
          <span className="sp" />
          <span className="set-state">
            {changingUsername ? t('用户名有改动，下次登录需用新用户名', 'Username changed — sign in with it next time') : (nicknameChanged ? t('昵称有改动', 'Nickname changed') : t('尚未修改', 'No changes yet'))}
          </span>
          <Button primary small type="submit" disabled={busy}>{busy ? t('保存中…', 'Saving…') : t('保存', 'Save')}</Button>
        </div>
      </div>
    </form>
  )
}

function PasswordCard() {
  const { t } = useI18n()
  const toast = useToast()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!current) {
      toast(t('请输入当前密码', 'Enter your current password'))
      return
    }
    const passwordError = validatePassword(next)
    if (passwordError) {
      toast(passwordError)
      return
    }
    if (next.trim() !== confirm.trim()) {
      toast(t('两次输入的新密码不一致', 'The new passwords do not match'))
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
      toast(t('密码已更新，其他设备需要重新登录', 'Password updated — other devices must sign in again'))
    } catch (error) {
      toast(getErrorMessage(error, t('修改失败', 'Change failed')))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className="pcard rv" style={{ marginTop: 16 }} onSubmit={submit}>
      <div className="ph2"><b>{t('修改密码', 'Change password')}</b></div>
      <div className="bd2">
        <div className="fld">
          <label>{t('当前密码', 'Current password')}</label>
          <Input variant="login" type="password" autoComplete="current-password" value={current} onChange={(event) => setCurrent(event.target.value)} />
        </div>
        <div className="fld" style={{ marginTop: 12 }}>
          <label>{t('新密码', 'New password')}</label>
          <Input variant="login" type="password" autoComplete="new-password" value={next} onChange={(event) => setNext(event.target.value)} />
        </div>
        <div className="fld" style={{ marginTop: 12 }}>
          <label>{t('确认新密码', 'Confirm new password')}</label>
          <Input variant="login" type="password" autoComplete="new-password" value={confirm} onChange={(event) => setConfirm(event.target.value)} />
        </div>
        <p className="hint" style={{ marginTop: 10 }}>{t('密码要求：', 'Password requirements: ')}{passwordRuleText()}{t('。', '.')}</p>
        <div className="act-row">
          <span className="sp" />
          <Button primary small type="submit" disabled={busy}>{busy ? t('提交中…', 'Submitting…') : t('修改密码', 'Change password')}</Button>
        </div>
      </div>
    </form>
  )
}
