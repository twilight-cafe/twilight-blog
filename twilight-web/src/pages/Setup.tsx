// Setup.tsx —— 首次安装向导
// 首次运行（后端未安装）时由 App 重定向到此。安装只做一件事：设置管理员密码，
// 登录用户名固定为 'admin'（之后可在后台改名）。install 与 login 是两步：
// 先存密码、再单独登录，使控制台走与日后相同的 cookie 会话。
// 关键点：若登录这一步失败，密码其实已经存好了，所以不算「安装失败」——操作员
// 直接去登录页手动登即可，错误提示不会误导成需要重装。

import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Input } from 'ink-design'
import { blog, getErrorMessage, setCurrentUser } from '../lib/client'
import { PASSWORD_RULE_TEXT, validatePassword } from '../lib/password'
import { useToast } from '../lib/toast'
import { useCanonicalPath, usePageHead } from '../lib/head'

interface SetupProps {
  onInstalled?: () => void
}

/** The login name the installation flow writes; it is renameable afterwards. */
const INITIAL_ADMIN_NAME = 'admin'

export default function Setup({ onInstalled }: SetupProps) {
  const navigate = useNavigate()
  const toast = useToast()
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  // 安装页是「站点还不存在」时的入口，同样不该被收录。
  usePageHead({ path: useCanonicalPath(), title: '安装', robots: 'noindex, nofollow' })

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const passwordError = validatePassword(password)
    if (passwordError) {
      toast(passwordError)
      return
    }

    setBusy(true)
    try {
      const result = await blog.install({ password: password.trim() })
      if (!result.installed) {
        toast('安装未完成，请稍后重试')
        return
      }

      // Installing only stores the password. Signing in is a separate call so the
      // console is reached through the same cookie session as any later visit;
      // there is no token to keep and nothing to persist locally. The unified
      // endpoint resolves the name "admin" to the administrator on its own.
      const me = await blog.login({ username: INITIAL_ADMIN_NAME, password: password.trim() })
      setCurrentUser(me)
      onInstalled?.()
      toast('安装完成，已自动登录')
      navigate('/admin', { replace: true })
    } catch (error) {
      // The password is already saved at this point, so a failed sign-in must not
      // read as a failed install: the operator can simply log in manually.
      toast(getErrorMessage(error, '安装失败'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-bg">
      <form className="login" style={{ width: 460 }} onSubmit={submit}>
        <div className="lg">
          <span className="sq">T</span>
          <h1>安装 Twilight</h1>
        </div>
        <p className="sub">FIRST RUN · SET ADMIN PASSWORD</p>
        <div className="fld">
          <label>管理员密码 *</label>
          <Input variant="login" type="password" autoComplete="new-password" value={password} onChange={(event) => setPassword(event.target.value)} />
        </div>
        <p className="policy">密码要求：{PASSWORD_RULE_TEXT}。</p>
        <Button primary type="submit" style={{ width: '100%', justifyContent: 'center' }} disabled={busy}>
          {busy ? '正在安装…' : '设置密码并进入后台 →'}
        </Button>
        <p className="sec">登录用户名默认为 <b>{INITIAL_ADMIN_NAME}</b>，配置写入服务端后即可使用。</p>
      </form>
    </div>
  )
}
