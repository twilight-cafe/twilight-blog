// SignIn.tsx —— 登录页
//
// 只有一个「用户名 + 密码」表单：**没有**管理员 / 普通用户的身份切换。
// 身份由用户名决定（服务端比对站长的登录名，否则去账号表里找），而这个表单
// 本来就不可能比服务端更清楚该走哪一支——两个身份用的是同一种凭据，选错了
// 只会得到一个与「密码错了」无法区分的 401。少一个开关，就少一次注定要
// 失败的重试。
//
// 登录成功后落到哪里仍然看 role：站长进控制台，访客账号只有资料页可管。
// 已登录状态下打开本页会直接跳走（不是报错）。
// 底部提示的「3 次/分钟 限流」是后端行为，前端只做展示。

import { useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate } from 'react-router-dom'
import { Button, Input } from 'ink-design'
import { blog, getErrorMessage, setCurrentUser, useCurrentUser } from '../../lib/client'
import { useToast } from '../../lib/toast'
import { useI18n } from '../../lib/i18n'
import LanguageSwitcher from '../../components/LanguageSwitcher'

export default function SignIn() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const toast = useToast()
  const signedIn = useCurrentUser()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()

    if (!username.trim()) {
      toast(t('请输入用户名（或注册邮箱）', 'Enter your username (or registered email)'))
      return
    }
    if (!password) {
      toast(t('请输入密码', 'Enter your password'))
      return
    }

    setBusy(true)
    try {
      const me = await blog.login({ username: username.trim(), password })
      setCurrentUser(me)
      toast(t('登录成功', 'Signed in'))
      // The administrator gets the full console; a visitor account only owns a
      // profile, so send it straight there.
      navigate(me.role === 'admin' ? '/admin' : '/admin/profile', { replace: true })
    } catch (error) {
      toast(getErrorMessage(error, t('登录失败', 'Sign-in failed')))
    } finally {
      setBusy(false)
    }
  }

  // Opening the form while already signed in is a no-op, not an error: send the
  // identity to the console it owns.
  if (signedIn) {
    return <Navigate to={signedIn.role === 'admin' ? '/admin' : '/admin/profile'} replace />
  }

  return (
    <div className="login-bg">
      <LanguageSwitcher className="auth-lang" />
      <form className="login" onSubmit={submit}>
        <div className="lg">
          <span className="sq">T</span>
          <h1>Twilight · {t('登录', 'Sign in')}</h1>
        </div>
        <p className="sub">TWILIGHT · SIGN IN</p>

        <div className="fld">
          <label>{t('用户名', 'Username')}</label>
          <Input
            variant="login"
            autoComplete="username"
            placeholder={t('用户名或注册邮箱', 'Username or email')}
            value={username}
            onChange={(event) => setUsername(event.target.value)}
          />
        </div>

        <div className="fld">
          <label>{t('密码', 'Password')}</label>
          <Input
            variant="login"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </div>

        <Button primary type="submit" style={{ width: '100%', justifyContent: 'center' }} disabled={busy}>
          {busy ? t('登录中…', 'Signing in…') : t('登录 →', 'Sign in →')}
        </Button>

        <div className="login-links">
          <Link to="/admin/register">{t('注册新账号', 'Create an account')}</Link>
          <span>·</span>
          <Link to="/admin/forgot">{t('忘记密码', 'Forgot password')}</Link>
        </div>

        <p className="sec">{t('站长与访客共用这一个入口：填谁的账号，就以谁的身份登入。登录失败次数受限（3 次/分钟），请稍后再试。', 'The owner and visitors share this one entry point: whoever’s account you enter is who you sign in as. Failed attempts are rate-limited (3 per minute).')}</p>
      </form>
    </div>
  )
}
