// ForgotPassword.tsx —— 重置密码（邮箱 + 验证码）
// 流程与注册类似：发码 → 带 code 调 resetPassword。后端会吊销该账号全部会话、
// 清掉 cookie，所以前端在成功后 setCurrentUser(null) 并跳回登录页，强制重新登录。
// 验证码按钮受 60 秒冷却约束（见 lib/email-code-cooldown），与 Register 共用同一条
// 时间线——用户在注册页发过码再过来，倒计时是接着走的，与服务端判定一致。

import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button, Input } from 'ink-design'
import { blog, getErrorMessage, setCurrentUser } from '../../lib/client'
import { passwordRuleText, validatePassword } from '../../lib/password'
import { emailError } from '../../lib/validate'
import { isRateLimited, startEmailCodeCooldown, useEmailCodeCooldown } from '../../lib/email-code-cooldown'
import { useToast } from '../../lib/toast'
import { useI18n } from '../../lib/i18n'
import LanguageSwitcher from '../../components/LanguageSwitcher'

export default function ForgotPassword() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const toast = useToast()
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
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
      startEmailCodeCooldown(email)
      toast(t('验证码已发送，请查收邮箱', 'Verification code sent — check your inbox'))
    } catch (error) {
      // 被限流（429）时锁住按钮：让前端的节奏与服务端的记录重新对齐，
      // 否则用户会继续点、继续被拒。
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

    setBusy(true)
    try {
      await blog.resetPassword({
        email: email.trim(),
        code: code.trim(),
        password: password.trim(),
      })
      // The server revokes every session for the account and clears the cookies,
      // so the only thing left to do is sign in again.
      setCurrentUser(null)
      toast(t('密码已重置，请使用新密码登录', 'Password reset — sign in with the new one'))
      navigate('/admin/login', { replace: true })
    } catch (error) {
      toast(getErrorMessage(error, t('重置失败', 'Reset failed')))
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
          <h1>Twilight · {t('重置密码', 'Reset password')}</h1>
        </div>
        <p className="sub">RESET PASSWORD</p>

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
          <label>{t('新密码', 'New password')}</label>
          <Input
            variant="login"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </div>

        <p className="policy">{t('密码要求：', 'Password requirements: ')}{passwordRuleText()}{t('。', '.')}</p>

        <Button primary type="submit" style={{ width: '100%', justifyContent: 'center' }} disabled={busy}>
          {busy ? t('提交中…', 'Submitting…') : t('重置密码 →', 'Reset password →')}
        </Button>

        <div className="login-links">
          <Link to="/admin/login">{t('返回登录', 'Back to sign in')}</Link>
          <span>·</span>
          <Link to="/admin/register">{t('注册新账号', 'Create an account')}</Link>
        </div>
      </form>
    </div>
  )
}
