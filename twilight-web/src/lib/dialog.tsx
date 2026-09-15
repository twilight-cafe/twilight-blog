// dialog.tsx —— 全局 confirm / prompt 对话框
// 用 Promise 把命令式的「询问用户」变成可 await 的调用。注意 state 只保存
// 当前「一个」对话框，所以若同时发起多个 confirm/prompt，后者会覆盖前者、
// 先前的 Promise 不会被 resolve（会一直挂起）—— 调用方应避免并发弹窗。
// close 依赖 [state]，是为了在 resolve 时拿到正确的那个 resolve 函数。
//
// 2026-09：InkUI 的 Dialog（博客遗留一次性组件）已删除，改用 antd 风格的
// Modal：onOk/onCancel 接管底部按钮，Esc/遮罩关闭统一走 onCancel。

import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'
import { Modal } from 'ink-design'

interface DialogContextValue {
  confirm: (title: string, message?: string) => Promise<boolean>
  prompt: (title: string, placeholder?: string) => Promise<string | null>
}

const Ctx = createContext<DialogContextValue>({
  confirm: async () => false,
  prompt: async () => null,
})

type DialogState =
  | { kind: 'confirm'; title: string; message?: string; resolve: (value: boolean) => void }
  | { kind: 'prompt'; title: string; placeholder?: string; resolve: (value: string | null) => void }
  | null

export function DialogProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<DialogState>(null)
  const [value, setValue] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  const confirm = useCallback((title: string, message?: string) => {
    return new Promise<boolean>((resolve) => {
      setState({ kind: 'confirm', title, message, resolve })
    })
  }, [])

  const prompt = useCallback((title: string, placeholder?: string) => {
    return new Promise<string | null>((resolve) => {
      setValue('')
      setState({ kind: 'prompt', title, placeholder, resolve })
    })
  }, [])

  const close = useCallback(
    (result: boolean | string | null) => {
      if (state) state.resolve(result as never)
      setState(null)
    },
    [state],
  )

  return (
    <Ctx.Provider value={{ confirm, prompt }}>
      {children}
      <Modal
        open={Boolean(state)}
        title={state?.title}
        okText="确定"
        cancelText="取消"
        onOk={() => (state?.kind === 'confirm' ? close(true) : close(value.trim()))}
        onCancel={() => close(state?.kind === 'prompt' ? null : false)}
      >
        {state?.kind === 'confirm' && state.message ? <p>{state.message}</p> : null}
        {state?.kind === 'prompt' ? (
          <input
            ref={inputRef}
            className="linp mono"
            placeholder={state.placeholder || ''}
            value={value}
            onChange={(event) => setValue(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') close(value)
            }}
            autoFocus
          />
        ) : null}
      </Modal>
    </Ctx.Provider>
  )
}

export function useDialog() {
  return useContext(Ctx)
}
