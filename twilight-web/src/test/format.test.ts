// format.test.ts —— 展示格式化纯函数的全分支覆盖
import { describe, expect, it } from 'vitest'
import { excerpt, fmtDate, fmtDateTime, fmtMonthDay, fmtNum, fmtRelative, fmtSize } from '../lib/format'

const FIXED = new Date(2026, 8, 13, 12, 34) // 2026-09-13 12:34 本地时区

describe('toDate 兼容（经由公开函数验证）', () => {
  it('秒级与毫秒级时间戳、字符串、Date、空值与非法值', () => {
    const seconds = Math.floor(FIXED.getTime() / 1000)
    expect(fmtDate(seconds)).toBe('2026-09-13')
    expect(fmtDate(FIXED.getTime())).toBe('2026-09-13')
    expect(fmtDate('2026-09-13')).toBe('2026-09-13')
    expect(fmtDate(FIXED)).toBe('2026-09-13')
    expect(fmtDate(0)).toBe('—')
    expect(fmtDate(null)).toBe('—')
    expect(fmtDate(undefined)).toBe('—')
    expect(fmtDate('not-a-date')).toBe('—')
  })

  it('fmtDateTime / fmtMonthDay', () => {
    expect(fmtDateTime(FIXED)).toBe('2026-09-13 12:34')
    expect(fmtDateTime(null)).toBe('—')
    expect(fmtMonthDay(FIXED)).toBe('09-13')
    expect(fmtMonthDay('bad')).toBe('')
  })
})

describe('fmtRelative', () => {
  it('刚刚 / 分钟 / 小时 / 天 / 超过一周回退到日期', () => {
    const now = Date.now()
    expect(fmtRelative(now)).toBe('刚刚')
    expect(fmtRelative(now - 5 * 60_000)).toBe('5 分钟前')
    expect(fmtRelative(now - 3 * 3_600_000)).toBe('3 小时前')
    expect(fmtRelative(now - 2 * 86_400_000)).toBe('2 天前')
    expect(fmtRelative(now - 9 * 86_400_000)).toBe(fmtDate(new Date(now - 9 * 86_400_000)))
    expect(fmtRelative('bad')).toBe('')
  })
})

describe('fmtNum / fmtSize', () => {
  it('数字缩写与空值', () => {
    expect(fmtNum(undefined)).toBe('0')
    expect(fmtNum(9999)).toBe('9,999')
    expect(fmtNum(12345)).toBe('1.2w')
  })

  it('文件大小各档位与非法输入', () => {
    expect(fmtSize(0)).toBe('0 B')
    expect(fmtSize(-5)).toBe('0 B')
    expect(fmtSize(undefined)).toBe('0 B')
    expect(fmtSize(512)).toBe('512 B')
    expect(fmtSize(2048)).toBe('2.0 KB')
    expect(fmtSize(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(fmtSize(3 * 1024 * 1024 * 1024)).toBe('3.00 GB')
  })
})

describe('excerpt', () => {
  it('压缩空白、截断加省略号、超长默认 140', () => {
    expect(excerpt('  a\n\n b  ')).toBe('a b')
    const long = 'x'.repeat(200)
    expect(excerpt(long).length).toBe(141)
    expect(excerpt(long).endsWith('…')).toBe(true)
    expect(excerpt('abcdef', 3)).toBe('abc…')
  })
})
