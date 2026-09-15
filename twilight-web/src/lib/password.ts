// password.ts —— 密码强度校验（前端镜像）
// 这里的规则是服务端 config.ValidatePasswordStrength 的镜像，仅用于即时反馈、
// 省一次往返；服务端才是权威，弱密码最终仍会在注册/改密接口被后端拦下。
// 72 字节上限源于 bcrypt 会静默截断超出部分（见下方英文注释）。

/**
 * Mirrors the server-side policy in `config.ValidatePasswordStrength`, which is
 * authoritative: this only saves a round trip and explains the rule up front.
 */
export const PASSWORD_MIN_LENGTH = 8
export const PASSWORD_MAX_BYTES = 72

/** Shown next to every password field. */
export const PASSWORD_RULE_TEXT = `至少 ${PASSWORD_MIN_LENGTH} 个字符，最多 ${PASSWORD_MAX_BYTES} 字节，且同时包含字母和数字`

/** Returns a human-readable reason, or null when the password is acceptable. */
export function validatePassword(password: string): string | null {
  const value = password.trim()
  if (!value) return '请输入密码'

  // bcrypt silently ignores everything past 72 bytes.
  if (new TextEncoder().encode(value).length > PASSWORD_MAX_BYTES) {
    return `密码不能超过 ${PASSWORD_MAX_BYTES} 字节`
  }
  // Count code points, matching the server's rune count.
  if ([...value].length < PASSWORD_MIN_LENGTH) {
    return `密码至少 ${PASSWORD_MIN_LENGTH} 个字符`
  }
  if (!/\p{L}/u.test(value)) return '密码需包含至少一个字母'
  if (!/\p{Nd}/u.test(value)) return '密码需包含至少一个数字'
  return null
}
