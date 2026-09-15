// ModeToggle.tsx —— 亮 / 暗模式切换
// 模式持久化在 localStorage 的 'tw_mode'；首次访问读不到时用系统
// prefers-color-scheme 兜底。切换只改 <html data-mode> 这一个属性，真正的配色
// 由 CSS 变量按该属性分支，所以这里不涉及任何样式逻辑。

import { useEffect, useState } from 'react'
import { IconButton } from 'ink-design'
import Icon from '../lib/icons'

const MODE_KEY = 'tw_mode'

function currentMode(): 'light' | 'dark' {
  const stored = localStorage.getItem(MODE_KEY)
  if (stored === 'light' || stored === 'dark') return stored
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function applyMode(mode: 'light' | 'dark') {
  document.documentElement.dataset.mode = mode
}

export default function ModeToggle() {
  const [dark, setDark] = useState(() => currentMode() === 'dark')

  useEffect(() => {
    applyMode(dark ? 'dark' : 'light')
    localStorage.setItem(MODE_KEY, dark ? 'dark' : 'light')
  }, [dark])

  return (
    <IconButton onClick={() => setDark((value) => !value)} title="亮 / 暗模式">
      {dark ? <Icon name="sun" size={15} /> : <Icon name="moon" size={15} />}
    </IconButton>
  )
}
