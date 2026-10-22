// format.ts —— 日期 / 数字 / 文件大小 / 摘要的展示格式化
//
// 关键约定：后端传来的时间可能是「秒级」也可能是「毫秒级」时间戳。toDate 用
// 1e12 做分界——大于它的当作毫秒（new Date 直接吃），否则当作秒并 ×1000。
// 这个启发式依赖「秒级时间戳在 1e12 之前（约 2001 年之前才会越界）」这一假设，
// 属于隐式约定，改后端时间格式时这里要同步。
// fmtNum 在 >=10000 时用「w」（万）缩写；fmtSize 用 1024 进制（KiB/MiB…）。
// 相对时间通过 lib/i18n 的 t() 跟随当前语言；它是事件/渲染期读取，不需要订阅。

import { t } from './i18n'

/**
 * 把后端传来的时间戳转成 Date：大于 1e12 的当作毫秒，否则当作秒并 ×1000。
 * 无法解析（空值、NaN）时返回 null，调用方据此回退占位文案。
 *
 * 导出是为了让**回写**也能用同一份判据。编辑器把 created_at 读进 DatePicker、
 * 改完再换回时间戳送回去；两边各写一份 1e12 的判断，改了一处就会得到一个
 * 「打开是 2020 年、保存变 55835 年」的表单——而且不会有任何报错。
 */
export function toDate(value?: number | string | Date | null): Date | null {
  if (!value) return null
  const date = typeof value === 'number'
    ? new Date(value > 1e12 ? value : value * 1000)
    : new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

/**
 * 把 Date 转回**秒级** Unix 时间戳。
 *
 * 一律向下取整到秒：服务端把 `created_at` 当秒用（列表按它排序），
 * 而 DatePicker 的显隐只到秒，所以这里不会丢有效精度。
 */
export function toUnixSeconds(date: Date): number {
  return Math.floor(date.getTime() / 1000)
}

function pad(value: number): string {
  return String(value).padStart(2, '0')
}

export function fmtDate(value?: number | string | Date | null): string {
  const d = toDate(value)
  if (!d) return '—'
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

export function fmtDateTime(value?: number | string | Date | null): string {
  const d = toDate(value)
  if (!d) return '—'
  return `${fmtDate(d)} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function fmtMonthDay(value?: number | string | Date | null): string {
  const d = toDate(value)
  if (!d) return ''
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

export function fmtRelative(value?: number | string | Date | null): string {
  const d = toDate(value)
  if (!d) return ''
  const diff = Date.now() - d.getTime()
  const minutes = Math.floor(diff / 60000)
  if (minutes < 1) return t('刚刚', 'just now')
  if (minutes < 60) return t(`${minutes} 分钟前`, `${minutes} min ago`)
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return t(`${hours} 小时前`, `${hours} h ago`)
  const days = Math.floor(hours / 24)
  if (days < 7) return t(`${days} 天前`, `${days} d ago`)
  return fmtDate(d)
}

export function fmtNum(value?: number): string {
  if (value === undefined || value === null) return '0'
  if (value >= 10000) return `${(value / 10000).toFixed(1)}w`
  return value.toLocaleString('en-US')
}

export function fmtSize(bytes?: number): string {
  if (!bytes || bytes < 0) return '0 B'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`
}

export function excerpt(content: string, max = 140): string {
  const text = (content || '')
    .replace(/\s+/g, ' ')
    .trim()
  return text.length > max ? `${text.slice(0, max)}…` : text
}

/**
 * 把一段文本转成能安全放进 HTML 的字符串。
 *
 * 两个调用方，都是**承重**的：markdown 渲染失败时把原文包成 `<p>` 的兜底，
 * 以及 ContentView 在懒加载块取不到时的降级显示。后者尤其关键——评论是访客
 * 提交的内容，那条降级路径绝不能把原文当 HTML 注进去。
 *
 * 放在 format.ts 而不是各写一份：它是纯函数、零依赖，因此能留在首屏包里边
 * （markdown 渲染器本身已经是懒加载的了）。
 */
export function escapeHtml(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
