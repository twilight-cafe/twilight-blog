// validate.ts —— 账号字段形状校验（前端镜像）
//
// 与 lib/password.ts 同一套路：规则本体是服务端
// model.NormalizeAndValidateUsername 与邮箱校验的镜像，只用于即时反馈、
// 省一次往返；服务端才是权威。
//
// 之所以收敛成一份：这三个规则原本在 Register / Profile / UsersAdmin 各写了一遍，
// 连错误文案都是手抄的（`用户名需 3–24 个字符` 之类出现在三个文件里）。
// 任何一处放宽或收紧，另外两个界面就会给出与后端不一致的提示——
// 那种「前后端各说各话」的 bug 只能靠肉眼比对发现。
//
// 约定：返回 null 表示合法，否则返回可直接展示给用户的文案。

import { t } from './i18n'

/** 用户名与昵称的长度上限，同时用作输入框的 maxLength。 */
export const USERNAME_MIN = 3
export const USERNAME_MAX = 24
export const NICKNAME_MAX = 24

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
const USERNAME_RE = /^[A-Za-z][A-Za-z0-9_-]*$/

/** 校验邮箱形状。只挡明显写错的输入，不试图实现 RFC 5322。 */
export function emailError(value: string): string | null {
  return EMAIL_RE.test(value.trim()) ? null : t('请输入正确的邮箱', 'Enter a valid email address')
}

/** 校验登录名的形状（长度 + 字符集），与后端 service 层规则镜像。 */
export function usernameError(value: string): string | null {
  if (value.length < USERNAME_MIN || value.length > USERNAME_MAX) {
    return t(`用户名需 ${USERNAME_MIN}–${USERNAME_MAX} 个字符`, `The username must be ${USERNAME_MIN}–${USERNAME_MAX} characters long`)
  }
  if (!USERNAME_RE.test(value)) {
    return t('用户名只能包含字母、数字、下划线和连字符，且以字母开头', 'The username may only contain letters, digits, underscores and hyphens, and must start with a letter')
  }
  return null
}

/**
 * 校验展示名。label 必须可换：同一个长度上限既用在访客的「昵称」上，
 * 也用在站长的「显示名称」上，文案里的称呼得跟着走——否则管理员会看到
 * 一句让他去改「昵称」的提示，而页面上根本没有叫昵称的输入框。
 */
export function nicknameError(value: string, label = '昵称', labelEn = 'nickname'): string | null {
  const trimmed = value.trim()
  if (!trimmed) return t(`请输入${label}`, `Please enter your ${labelEn}`)
  if (trimmed.length > NICKNAME_MAX) {
    return t(`${label}不能超过 ${NICKNAME_MAX} 个字符`, `The ${labelEn} must be at most ${NICKNAME_MAX} characters long`)
  }
  return null
}
