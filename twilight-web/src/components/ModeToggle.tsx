// ModeToggle.tsx —— 亮 / 暗模式切换
//
// 状态与持久化整块交给 ink-design 的 `useColorMode`：它读 localStorage 的
// 'tw_mode'、写 `<html data-mode>`、没有存储值时跟随系统偏好，并在切换时用
// View Transitions 做整页交叉淡入（不支持该 API 或系统要求减少动态效果时直接切）。
//
// 这正是 iOS「外观」切换的那一下：换的是一整套配色，逐条给颜色加 transition
// 又贵又容易漏，交给浏览器截两帧交叉淡入最干净。
//
// 本组件只负责外观：一个 IconButton + 一枚随模式重挂的图标（key 让 CSS 的
// 旋转 + 缩放 + 淡入有东西可触发，否则同一棵 <svg> 只换 path，什么都不会动）。

import { IconButton, useColorMode } from 'ink-design'
import Icon from '../lib/icons'
import { useI18n } from '../lib/i18n'

export default function ModeToggle() {
  const { t } = useI18n()
  const { mode, toggle } = useColorMode({ storageKey: 'tw_mode' })

  return (
    <IconButton onClick={toggle} title={t('亮 / 暗模式', 'Light / dark mode')}>
      <span className="mode-icon" key={mode}>
        {mode === 'dark' ? <Icon name="sun" size={15} /> : <Icon name="moon" size={15} />}
      </span>
    </IconButton>
  )
}
