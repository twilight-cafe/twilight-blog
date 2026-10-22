// language-switcher.test.tsx —— 语言切换按钮
//
// 断言打在**可访问性状态**（aria-pressed）与真实副作用（<html lang>、
// localStorage）上，而不是只看 class：`.on` 是样式钩子，读屏并不认识它。

import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import LanguageSwitcher from '../components/LanguageSwitcher'
import { LOCALE_STORAGE_KEY } from '../lib/i18n'

describe('LanguageSwitcher', () => {
  it('默认中文：中 按下、EN 未按下', () => {
    render(<LanguageSwitcher />)

    expect(screen.getByRole('group', { name: '语言' })).toBeTruthy()
    const zh = screen.getByRole('button', { name: '中' })
    const en = screen.getByRole('button', { name: 'EN' })
    expect(zh.getAttribute('aria-pressed')).toBe('true')
    expect(en.getAttribute('aria-pressed')).toBe('false')
    expect(zh.className).toBe('on')
    expect(en.className).toBe('')
  })

  it('点击 EN：切语言、写存储、更新 <html>，按钮状态互换', () => {
    render(<LanguageSwitcher />)

    fireEvent.click(screen.getByRole('button', { name: 'EN' }))

    expect(document.documentElement.lang).toBe('en')
    expect(document.documentElement.dataset.locale).toBe('en')
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('en')
    expect(screen.getByRole('button', { name: 'EN' }).getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByRole('button', { name: '中' }).getAttribute('aria-pressed')).toBe('false')
    // 切换后读屏标签也跟着换语言。
    expect(screen.getByRole('group', { name: 'Language' })).toBeTruthy()
  })

  it('点击当前语言：不产生任何可见变化，也不清掉存储', () => {
    render(<LanguageSwitcher />)

    fireEvent.click(screen.getByRole('button', { name: '中' }))

    expect(document.documentElement.lang).toBe('zh-CN')
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('zh')
    expect(screen.getByRole('button', { name: '中' }).getAttribute('aria-pressed')).toBe('true')
  })

  it('className 追加到根元素（登录页的绝对定位），且不留下多余空格', () => {
    const { container } = render(<LanguageSwitcher className="auth-lang" />)
    const root = container.firstElementChild as HTMLElement
    expect(root.className).toBe('lang-switch auth-lang')

    const plain = render(<LanguageSwitcher />)
    expect((plain.container.firstElementChild as HTMLElement).className).toBe('lang-switch')
  })

  it('每个按钮带上语言与完整名称，便于读屏与悬浮提示', () => {
    render(<LanguageSwitcher />)
    const zh = screen.getByRole('button', { name: '中' })
    const en = screen.getByRole('button', { name: 'EN' })
    expect(zh.getAttribute('lang')).toBe('zh-CN')
    expect(zh.getAttribute('title')).toBe('中文')
    expect(en.getAttribute('lang')).toBe('en')
    expect(en.getAttribute('title')).toBe('English')
  })
})
