// ForgotPassword.tsx —— 重置密码（邮箱 + 验证码）
// 流程与注册类似：发码 → 带 code 调 resetPassword。后端会吊销该账号全部会话、
// 清掉 cookie，所以前端在成功后 setCurrentUser(null) 并跳回登录页，强制重新登录。

import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button, Input } from 'ink-design'
import { blog, getErrorMessage, setCurrentUser } from '../../lib/client'
import { PASSWORD_RULE_TEXT, validatePassword } from '../../lib/password'
import { emailError } from '../../lib/validate'
import { useToast } from '../../lib/toast'

export default function ForgotPassword() {
  const navigate = useNavigate()
  const toast = useToast()
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [codeSent, setCodeSent] = useState(false)
  const [sending, setSending] = useState(false)
  const [busy, setBusy] = useState(false)

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
      toast('验证码已发送，请查收邮箱')
    } catch (error) {
      toast(getErrorMessage(error, '发送验证码失败'))
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
      toast('请输入验证码')
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
      toast('密码已重置，请使用新密码登录')
      navigate('/admin/login', { replace: true })
    } catch (error) {
      toast(getErrorMessage(error, '重置失败'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-bg">
      <form className="login" onSubmit={submit}>
        <div className="lg">
          <span className="sq">T</span>
          <h1>Twilight · 重置密码</h1>
        </div>
        <p className="sub">RESET PASSWORD</p>

        <div className="fld">
          <label>邮箱 Email</label>
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
              <Input variant="login" placeholder="验证码" value={code} onChange={(event) => setCode(event.target.value)} />
              <Button small ghost type="button" disabled={sending} onClick={() => void requestCode()}>
                重新发送
              </Button>
            </>
          ) : (
            <Button small type="button" disabled={sending} onClick={() => void requestCode()}>
              {sending ? '发送中…' : '发送验证码'}
            </Button>
          )}
        </div>

        <div className="fld">
          <label>新密码 New password</label>
          <Input
            variant="login"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </div>

        <p className="policy">密码要求：{PASSWORD_RULE_TEXT}。</p>

        <Button primary type="submit" style={{ width: '100%', justifyContent: 'center' }} disabled={busy}>
          {busy ? '提交中…' : '重置密码 →'}
        </Button>

        <div className="login-links">
          <Link to="/admin/login">返回登录</Link>
          <span>·</span>
          <Link to="/admin/register">注册新账号</Link>
        </div>
      </form>
    </div>
  )
}
