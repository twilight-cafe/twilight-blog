// validate.test.ts —— 账号字段与密码强度的镜像规则
import { describe, expect, it } from 'vitest'
import { NICKNAME_MAX, USERNAME_MAX, USERNAME_MIN, emailError, nicknameError, usernameError } from '../lib/validate'
import { PASSWORD_MAX_BYTES, PASSWORD_MIN_LENGTH, PASSWORD_RULE_TEXT, validatePassword } from '../lib/password'

describe('emailError', () => {
  it('合法与非法', () => {
    expect(emailError('a@b.co')).toBeNull()
    expect(emailError(' a@b.co ')).toBeNull()
    expect(emailError('')).toBe('请输入正确的邮箱')
    expect(emailError('no-at-sign')).toBe('请输入正确的邮箱')
    expect(emailError('a@b')).toBe('请输入正确的邮箱')
  })
})

describe('usernameError', () => {
  it('长度边界', () => {
    expect(USERNAME_MIN).toBe(3)
    expect(USERNAME_MAX).toBe(24)
    expect(usernameError('ab')).toBe('用户名需 3–24 个字符')
    expect(usernameError('a'.repeat(25))).toBe('用户名需 3–24 个字符')
  })

  it('字符集：必须字母开头，仅字母数字下划线连字符', () => {
    expect(usernameError('1abc')).toBe('用户名只能包含字母、数字、下划线和连字符，且以字母开头')
    expect(usernameError('ab c')).toBe('用户名只能包含字母、数字、下划线和连字符，且以字母开头')
    expect(usernameError('ab-c_1')).toBeNull()
  })
})

describe('nicknameError', () => {
  it('空值、超长、自定义 label', () => {
    expect(nicknameError('')).toBe('请输入昵称')
    expect(nicknameError('   ')).toBe('请输入昵称')
    expect(nicknameError('x'.repeat(25))).toBe('昵称不能超过 24 个字符')
    expect(nicknameError('')).toBe('请输入昵称')
    expect(nicknameError('', '显示名称')).toBe('请输入显示名称')
    expect(nicknameError('x'.repeat(NICKNAME_MAX + 1), '显示名称')).toBe('显示名称不能超过 24 个字符')
    expect(nicknameError('好名字')).toBeNull()
  })
})

describe('validatePassword', () => {
  it('规则文案与各失败分支', () => {
    expect(PASSWORD_MIN_LENGTH).toBe(8)
    expect(PASSWORD_MAX_BYTES).toBe(72)
    expect(PASSWORD_RULE_TEXT).toContain('至少 8 个字符')
    expect(validatePassword('  ')).toBe('请输入密码')
    expect(validatePassword('abc1')).toBe('密码至少 8 个字符')
    // 7 个中文字符 = 21 字节，码点 7 < 8：先撞字节上限不成立，撞的是长度
    expect(validatePassword('中文密码七八个')).toBe('密码至少 8 个字符')
    // 超过 72 字节：25 个中文 = 75 字节
    expect(validatePassword('中'.repeat(25) + 'a1')).toBe('密码不能超过 72 字节')
    expect(validatePassword('12345678')).toBe('密码需包含至少一个字母')
    expect(validatePassword('abcdefgh')).toBe('密码需包含至少一个数字')
    expect(validatePassword('abcd1234')).toBeNull()
  })
})
