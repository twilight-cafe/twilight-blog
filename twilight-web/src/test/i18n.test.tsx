// i18n.test.ts —— 语言状态机的全部可观察行为
//
// 这个模块是**模块级单例**，所以有几条用例必须走 `vi.resetModules()` + 动态
// import 才能观察「首次访问时从 localStorage 恢复」这一分支；普通 import 拿到的
// 实例在文件加载时就已经 hydrate 过了。两种写法混用时，每个用例结束都要把
// 那一份新实例复位，否则它会把语言泄漏给同文件后面的用例。

import { act, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  DEFAULT_LOCALE,
  LOCALE_STORAGE_KEY,
  applyLocale,
  getLocale,
  resetLocaleForTests,
  setLocale,
  subscribeLocale,
  t,
  translate,
  useI18n,
  useLocale,
} from '../lib/i18n'

function LocaleProbe() {
  const locale = useLocale()
  const { t: bound } = useI18n()
  return (
    <div>
      <span data-testid="locale">{locale}</span>
      <span data-testid="text">{bound('中文', 'English')}</span>
    </div>
  )
}

afterEach(() => {
  resetLocaleForTests()
  vi.unstubAllGlobals()
})

describe('默认语言', () => {
  it('没有存储时是中文：t 取中文、<html> 标记为 zh-CN / zh', () => {
    expect(getLocale()).toBe(DEFAULT_LOCALE)
    expect(t('你好', 'Hello')).toBe('你好')
    expect(translate('en', '你好', 'Hello')).toBe('Hello')
    expect(document.documentElement.lang).toBe('zh-CN')
    expect(document.documentElement.dataset.locale).toBe('zh')
  })

  it('applyLocale 不带参数时读当前语言', () => {
    setLocale('en')
    applyLocale()
    expect(document.documentElement.lang).toBe('en')
    expect(document.documentElement.dataset.locale).toBe('en')
  })

  it('没有 document 时 applyLocale 静默返回（SSR / 纯函数单测）', () => {
    vi.stubGlobal('document', undefined)
    expect(() => applyLocale('en')).not.toThrow()
  })
})

describe('切换语言', () => {
  it('切到英文：文案、<html> 属性与 localStorage 同步更新', () => {
    setLocale('en')
    expect(getLocale()).toBe('en')
    expect(t('你好', 'Hello')).toBe('Hello')
    expect(document.documentElement.lang).toBe('en')
    expect(document.documentElement.dataset.locale).toBe('en')
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('en')

    setLocale('zh')
    expect(t('你好', 'Hello')).toBe('你好')
    expect(document.documentElement.lang).toBe('zh-CN')
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('zh')
  })

  it('重复设置同一语言：持久化照做，但不通知订阅者（避免无意义的重渲染）', () => {
    setLocale('en')
    const listener = vi.fn()
    const unsubscribe = subscribeLocale(listener)

    setLocale('en')
    expect(listener).not.toHaveBeenCalled()

    setLocale('zh')
    expect(listener).toHaveBeenCalledTimes(1)

    // 取消订阅后不再收到通知——useSyncExternalStore 的契约就是这个返回值。
    unsubscribe()
    setLocale('en')
    expect(listener).toHaveBeenCalledTimes(1)
  })

  it('组件订阅：切换语言后 useLocale / useI18n 一起更新', () => {
    render(<LocaleProbe />)
    expect(screen.getByTestId('locale').textContent).toBe('zh')
    expect(screen.getByTestId('text').textContent).toBe('中文')

    act(() => setLocale('en'))
    expect(screen.getByTestId('locale').textContent).toBe('en')
    expect(screen.getByTestId('text').textContent).toBe('English')
  })

  it('resetLocaleForTests 回到中文并清空订阅者', () => {
    setLocale('en')
    const listener = vi.fn()
    subscribeLocale(listener)

    resetLocaleForTests()

    expect(getLocale()).toBe('zh')
    expect(document.documentElement.dataset.locale).toBe('zh')
    // 订阅者已被清空：再切换也不该回调到上一个用例的监听器。
    setLocale('en')
    expect(listener).not.toHaveBeenCalled()
  })
})

describe('localStorage 恢复与降级', () => {
  it('首次访问从存储恢复语言（刷新后保持英文）', async () => {
    window.localStorage.setItem(LOCALE_STORAGE_KEY, 'en')
    vi.resetModules()
    const fresh = await import('../lib/i18n')

    expect(fresh.getLocale()).toBe('en')
    // 恢复的同时把 <html> 对齐，读屏不会先用中文念一遍。
    expect(document.documentElement.lang).toBe('en')

    fresh.resetLocaleForTests()
  })

  it('存储里是非法值时回退中文', async () => {
    window.localStorage.setItem(LOCALE_STORAGE_KEY, 'fr')
    vi.resetModules()
    const fresh = await import('../lib/i18n')

    expect(fresh.getLocale()).toBe('zh')
    fresh.resetLocaleForTests()
  })

  it('没有 localStorage（SSR / 极旧浏览器）：读写都跳过，切换仍然生效', async () => {
    vi.stubGlobal('localStorage', undefined)
    vi.resetModules()
    const fresh = await import('../lib/i18n')

    expect(fresh.getLocale()).toBe('zh')
    expect(() => fresh.setLocale('en')).not.toThrow()
    expect(fresh.getLocale()).toBe('en')

    fresh.resetLocaleForTests()
  })

  it('localStorage 抛异常（隐私模式）：读回退中文、切换不崩', async () => {
    vi.stubGlobal('localStorage', {
      getItem: () => {
        throw new Error('SecurityError')
      },
      setItem: () => {
        throw new Error('QuotaExceededError')
      },
    })
    vi.resetModules()
    const fresh = await import('../lib/i18n')

    expect(fresh.getLocale()).toBe('zh')
    expect(() => fresh.setLocale('en')).not.toThrow()
    expect(fresh.getLocale()).toBe('en')

    fresh.resetLocaleForTests()
  })
})
