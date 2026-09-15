// toast.tsx —— 全局轻提示
// 通过 Context 暴露一个 `show(message)` 函数。2026-09 起 InkUI 的 Toast（博客
// 遗留一次性组件）已删除，改用 antd 风格的命令式 message 单例：每条消息自带
// 2.2s 自动关闭，连续调用各自独立，不再需要手动管理计时器。

import { createContext, useCallback, useContext, type ReactNode } from 'react'
import { message } from 'ink-design'

const ToastCtx = createContext<(message: string) => void>(() => {})

export function ToastProvider({ children }: { children: ReactNode }) {
  const show = useCallback((next: string) => {
    message.info(next, 2.2)
  }, [])

  return (
    <ToastCtx.Provider value={show}>
      {children}
    </ToastCtx.Provider>
  )
}

export function useToast() {
  return useContext(ToastCtx)
}
