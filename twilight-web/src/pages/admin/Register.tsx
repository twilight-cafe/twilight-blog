// Register.tsx —— 访客账号注册（邮箱 + 验证码 + 用户名 + 昵称）
// 先 requestEmailCode 发码，再带 code 注册；注册接口会直接返回会话（自动登录），
// 所以成功即 setCurrentUser 并跳到 /admin/profile。密码强度用 validatePassword
// 本地校验（与服务端规则镜像）。用户名与昵称是两个独立字段：username 用于登录
// （ASCII、3–24 位、字母开头，规则与服务端镜像），nickname 用于前台展示（≤24 字）。
// 验证码按钮受 60 秒冷却约束（见 lib/email-code-cooldown），与 ForgotPassword 共用。

import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button, Input } from 'ink-design'
import { blog, getErrorMessage, setCurrentUser } from '../../lib/client'
import { passwordRuleText, validatePassword } from '../../lib/password'
import { NICKNAME_MAX, USERNAME_MAX, emailError, nicknameError, usernameError } from '../../lib/validate'
import { isRateLimited, startEmailCodeCooldown, useEmailCodeCooldown } from '../../lib/email-code-cooldown'
import { useToast } from '../../lib/toast'
import { useI18n } from '../../lib/i18n'
import LanguageSwitcher from '../../components/LanguageSwitcher'

export default function Register() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const toast = useToast()
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [username, setUsername] = useState('')
  const [nickname, setNickname] = useState('')
  const [codeSent, setCodeSent] = useState(false)
  const [sending, setSending] = useState(false)
  const [busy, setBusy] = useState(false)
  const cooldown = useEmailCodeCooldown(email)

  const requestCode = async () => {
    const emailErr = emailError(email)
    if (emailErr) {
      toast(emailErr)
      return
    }
    setSending(true)
    try {
      await blog.requestEmailCode({ email: email.trim() })
      setCodeSent(true)
      // 冷却以**服务端确认成功**为准，不是以点击为准。
      startEmailCodeCooldown(email)
      toast(t('验证码已发送，请查收邮箱', 'Verification code sent — check your inbox'))
    } catch (error) {
      // 服务端说「太频繁」时也要把按钮锁住：否则用户会继续点，继续被拒。
      // 这里不区分 429 的具体文案——只要被拒就等一个冷却周期，
      // 让前端的节奏与服务端的记录重新对齐。
      if (isRateLimited(error)) startEmailCodeCooldown(email)
      toast(getErrorMessage(error, t('发送验证码失败', 'Failed to send the code')))
    } finally {
      setSending(false)
    }
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()

    const emailErr = emailError(email)
    if (emailErr) {
      toast(emailErr)
      return
    }
    if (!code.trim()) {
      toast(t('请输入验证码', 'Enter the verification code'))
      return
    }
    const passwordError = validatePassword(password)
    if (passwordError) {
      toast(passwordError)
      return
    }
    const nameErr = usernameError(username.trim())
    if (nameErr) {
      toast(nameErr)
      return
    }
    const nicknameErr = nicknameError(nickname)
    if (nicknameErr) {
      toast(nicknameErr)
      return
    }

    setBusy(true)
    try {
      // Registration signs the new account in, so the response is the session.
      const me = await blog.register({
        email: email.trim(),
        code: code.trim(),
        username: username.trim(),
        password: password.trim(),
        nickname: nickname.trim(),
      })
      setCurrentUser(me)
      toast(t('注册成功，已自动登录', 'Account created — you are signed in'))
      navigate('/admin/profile', { replace: true })
    } catch (error) {
      toast(getErrorMessage(error, t('注册失败', 'Registration failed')))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-bg">
      <LanguageSwitcher className="auth-lang" />
      <form className="login" onSubmit={submit}>
        <div className="lg">
          <span className="sq">T</span>
          <h1>Twilight · {t('注册', 'Register')}</h1>
        </div>
        <p className="sub">CREATE ACCOUNT</p>

        <div className="fld">
          <label>{t('邮箱', 'Email')}</label>
          <Input
            variant="login"
            type="email"
            autoComplete="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
        </div>

        <div className="cmt-code-row">
          {codeSent ? (
            <>
              <Input variant="login" placeholder={t('验证码', 'Verification code')} value={code} onChange={(event) => setCode(event.target.value)} />
              <Button
                small
                ghost
                type="button"
                disabled={sending || cooldown > 0}
                onClick={() => void requestCode()}
              >
                {cooldown > 0 ? t(`${cooldown} 秒后可重发`, `Resend in ${cooldown}s`) : t('重新发送', 'Resend')}
              </Button>
            </>
          ) : (
            <Button
              small
              type="button"
              disabled={sending || cooldown > 0}
              onClick={() => void requestCode()}
            >
              {sending ? t('发送中…', 'Sending…') : cooldown > 0 ? t(`${cooldown} 秒后可重发`, `Resend in ${cooldown}s`) : t('发送验证码', 'Send code')}
            </Button>
          )}
        </div>

        <div className="fld">
          <label>{t('密码', 'Password')}</label>
          <Input
            variant="login"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </div>

        <div className="fld">
          <label>{t('用户名', 'Username')}</label>
          <Input
            variant="login"
            autoComplete="username"
            maxLength={USERNAME_MAX}
            placeholder={t('今后登录用，不可与昵称混用', 'Used to sign in, separate from your display name')}
            value={username}
            onChange={(event) => setUsername(event.target.value)}
          />
        </div>

        <div className="fld">
          <label>{t('昵称', 'Nickname')}</label>
          <Input
            variant="login"
            maxLength={NICKNAME_MAX}
            placeholder={t('评论等场合对外展示', 'Shown publicly, e.g. on comments')}
            value={nickname}
            onChange={(event) => setNickname(event.target.value)}
          />
        </div>

        <p className="policy">{t('密码要求：', 'Password requirements: ')}{passwordRuleText()}{t('。', '.')}</p>

        <Button primary type="submit" style={{ width: '100%', justifyContent: 'center' }} disabled={busy}>
          {busy ? t('注册中…', 'Registering…') : t('注册并登录 →', 'Register and sign in →')}
        </Button>

        <div className="login-links">
          <Link to="/admin/login">{t('已有账号？去登录', 'Already have an account? Sign in')}</Link>
          <span>·</span>
          <Link to="/admin/forgot">{t('忘记密码', 'Forgot password')}</Link>
        </div>
      </form>
    </div>
  )
}
