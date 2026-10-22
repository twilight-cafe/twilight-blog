// i18n.ts —— 全站中英文状态
//
// 三条设计约束（都来自实际踩过的坑）：
//
//  1. **词典内联在调用点**：t('中文', 'English')，两个参数都是必填的字符串字面量。
//     新增一句文案却忘了写另一半，TypeScript 当场报错——不存在「漏翻的键」这种
//     运行期才会发现的状态。代价是没法集中改文案，但这个站点的文案总量不大。
//
//  2. **状态极小且可订阅**：只有当前语言一个值，住在模块级变量里，组件通过
//     useSyncExternalStore 订阅。不用 Context：语言切换要影响页头、页脚、侧栏、
//     单个按钮，Provider 包住整棵树等于让每次切换都重渲染整棵树；而且非组件代码
//     （format / validate / password 这些纯函数）读不到 Context，只能各自接参数。
//
//  3. **切换语言要同步到 <html>**：lang 属性是读屏与浏览器翻译的判据，data-locale
//     供样式（例如某些只在中文下成立的字体回退）使用。二者都写在 applyLocale 里，
//     切换时立刻生效——不能等下一次挂载。
//
// 持久化键刻意用 `tw_locale`（与 `tw_mode` / `tw_csrf` 同一前缀）。localStorage
// 在隐私模式 / 被策略禁写时会抛异常，读写都要包 try/catch：读失败退默认中文，
// 写失败只意味着下次打开回到中文，不应该让切换按钮本身炸掉。

import { useMemo, useSyncExternalStore } from 'react'

/** 站点支持的两种语言。默认中文。 */
export type Locale = 'zh' | 'en'

/** 默认语言：属性缺失、存储不可用、值非法时都用它。 */
export const DEFAULT_LOCALE: Locale = 'zh'

/** localStorage 里的键名。 */
export const LOCALE_STORAGE_KEY = 'tw_locale'

/** <html lang> 的取值：zh 要带地区（中文有简繁之分，这里只有简体）。 */
const HTML_LANG: Record<Locale, string> = {
  zh: 'zh-CN',
  en: 'en',
}

/** 模块级状态。它是唯一事实来源，DOM 属性与存储都是它的投影。 */
let currentLocale: Locale = DEFAULT_LOCALE

/**
 * 是否已经读过一次存储。
 *
 * 不在模块顶层读：模块可能在「没有 localStorage」的环境里被 import（SSR、
 * 纯函数单测），一读就会抛。改成第一次访问时懒加载，既避免了顶层副作用，
 * 也让「先写存储再 import」这种测试写法成立。
 */
let hydrated = false

const listeners = new Set<() => void>()

function isLocale(value: unknown): value is Locale {
  return value === 'zh' || value === 'en'
}

/** 读存储；不可用或值非法时回退默认中文。 */
function readStoredLocale(): Locale {
  if (typeof localStorage === 'undefined') return DEFAULT_LOCALE
  try {
    const stored = localStorage.getItem(LOCALE_STORAGE_KEY)
    return isLocale(stored) ? stored : DEFAULT_LOCALE
  } catch {
    // 隐私模式（Safari 的旧实现会直接抛 SecurityError）与禁用 cookie 的浏览器。
    return DEFAULT_LOCALE
  }
}

/** 写存储；失败只影响「下次打开是否记得」，不影响本次切换。 */
function persistLocale(locale: Locale): void {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(LOCALE_STORAGE_KEY, locale)
  } catch {
    // 写不进去就算了——本次会话的语言仍然是 locale，界面上看不出差别。
  }
}

function hydrate(): void {
  if (hydrated) return
  hydrated = true
  currentLocale = readStoredLocale()
  // 首次访问就把 <html> 对齐到存储值：刷新后首帧的语言标记必须是对的，
  // 否则读屏会先用默认语言念一遍再切。
  applyLocale(currentLocale)
}

/**
 * 把语言写到 <html> 上：lang 给读屏 / 浏览器翻译，data-locale 给样式。
 *
 * 无 document 时静默返回（SSR / 纯函数单测）。这里不抛错是刻意的：
 * 语言是表现层的事，不该让「没有 DOM」的调用方因为读一次文案而崩掉。
 */
export function applyLocale(locale: Locale = getLocale()): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  root.lang = HTML_LANG[locale]
  root.dataset.locale = locale
}

/** 当前语言。首次调用会从 localStorage 恢复。 */
export function getLocale(): Locale {
  hydrate()
  return currentLocale
}

/** 订阅语言变化；返回取消订阅的函数（useSyncExternalStore 的契约）。 */
export function subscribeLocale(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/**
 * 切换语言并持久化。
 *
 * 同一个语言重复设置时仍然写一次存储（幂等），但**不通知订阅者**：
 * 重渲染是纯浪费，而且会让「点当前语言」产生一次可见的闪动。
 */
export function setLocale(locale: Locale): void {
  hydrate()
  const changed = locale !== currentLocale
  currentLocale = locale
  persistLocale(locale)
  applyLocale(locale)
  if (!changed) return
  for (const listener of listeners) listener()
}

/** 纯函数版本：给定语言取对应文案，不读模块状态。 */
export function translate(locale: Locale, zh: string, en: string): string {
  return locale === 'en' ? en : zh
}

/**
 * 非组件代码用的翻译（format / validate / password 等纯函数）。
 *
 * 它读的是调用瞬间的模块状态，不会订阅变化：这些函数的返回值都被组件在
 * 渲染期使用，组件自己会因为 useI18n / useLocale 重渲染，自然就重新取到了。
 */
export function t(zh: string, en: string): string {
  return translate(getLocale(), zh, en)
}

/** React 侧的语言订阅。没有 Provider，任何组件都能直接调用。 */
export function useLocale(): Locale {
  return useSyncExternalStore(subscribeLocale, getLocale, getLocale)
}

export interface I18n {
  locale: Locale
  setLocale: (locale: Locale) => void
  /** 当前语言的取词函数；签名与模块级 t 相同，漏翻同样是编译错误。 */
  t: (zh: string, en: string) => string
}

/**
 * 组件里最常用的入口：拿到当前语言、切换函数和一个绑定好语言的 t。
 *
 * t 绑定到本次渲染的 locale 而不是每次读模块状态：同一次渲染里的所有文案
 * 必须来自同一个语言，否则切换的瞬间会出现「标题是英文、正文是中文」。
 */
export function useI18n(): I18n {
  const locale = useLocale()
  return useMemo(
    () => ({
      locale,
      setLocale,
      t: (zh: string, en: string) => translate(locale, zh, en),
    }),
    [locale],
  )
}

/**
 * 测试专用：回到默认语言并清空订阅者。
 *
 * 语言是模块级状态，不清理的话上一个用例切到英文，下一个用例会莫名其妙
 * 拿到英文界面——那种失败看起来与它自己的断言毫无关系。订阅者一并清空：
 * Testing Library 的 cleanup 已经卸载了组件，残留订阅只会在下个用例里
 * 触发一次对已卸载组件的 setState。
 */
export function resetLocaleForTests(): void {
  currentLocale = DEFAULT_LOCALE
  hydrated = true
  listeners.clear()
  applyLocale(DEFAULT_LOCALE)
}
