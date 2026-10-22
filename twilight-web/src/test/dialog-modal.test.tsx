// dialog-modal.test.tsx —— 对话框在「关闭动作与取消动作撞在同一帧」时的时序
//
// 真实 Modal 在关闭后会留 140ms 播退场（DialogProvider 的 closeDuration），
// 这段窗口里按钮仍在 DOM 中——第二次点击由 closingRef 守卫挡住（ink-design
// 的 DialogProvider），同一个 Promise 不会被结算两次。这里直接驱动真实 Modal，
// 不再 mock：库内部的 Dialog 走 `@/Modal` 别名，mock 包名拦不住它。
import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { DialogProvider, useDialog } from '../lib/dialog'

function Consumer({ onConfirm }: { onConfirm: (dialog: ReturnType<typeof useDialog>) => void }) {
  const dialog = useDialog()
  return <button onClick={() => onConfirm(dialog)}>open</button>
}

describe('DialogProvider 罕见时序', () => {
  it('prompt 不带 placeholder：占位符走空串分支；确定原样提交', async () => {
    let result: string | null | undefined
    render(
      <DialogProvider>
        <Consumer
          onConfirm={(dialog) => {
            void dialog.prompt('标题').then((value) => {
              result = value
            })
          }}
        />
      </DialogProvider>,
    )
    fireEvent.click(screen.getByText('open'))
    const input = screen.getByRole('textbox') as HTMLInputElement
    expect(input.placeholder).toBe('')
    fireEvent.change(input, { target: { value: '  答案  ' } })
    fireEvent.click(screen.getByText('确定'))
    // 对话框只负责原样返回；trim 是调用方的业务决定（MediaAdmin 的 rename）。
    await vi.waitFor(() => expect(result).toBe('  答案  '))
  })

  it('prompt 带初值：预填旧名字并全选，直接打字即覆盖', () => {
    render(
      <DialogProvider>
        <Consumer
          onConfirm={(dialog) => {
            void dialog.prompt('重命名', { placeholder: '新的文件名', initialValue: 'old.png' })
          }}
        />
      </DialogProvider>,
    )
    fireEvent.click(screen.getByText('open'))

    const input = screen.getByRole('textbox') as HTMLInputElement
    // placeholder 只是提示，初值才是「当前值」——改名场景要能就地改。
    expect(input.placeholder).toBe('新的文件名')
    expect(input.value).toBe('old.png')
    // 打开即全选：用户直接打字就是覆盖，不必先自己删一遍。
    expect(input.selectionStart).toBe(0)
    expect(input.selectionEnd).toBe('old.png'.length)
  })

  it('确认后紧接取消：退场窗口内再次 close，不炸不重复 resolve', async () => {
    const results: boolean[] = []
    render(
      <DialogProvider>
        <Consumer
          onConfirm={(dialog) => {
            void dialog.confirm('标题').then((value) => {
              results.push(value)
            })
          }}
        />
      </DialogProvider>,
    )
    fireEvent.click(screen.getByText('open'))
    fireEvent.click(screen.getByText('确定')) // close(true)
    // 退场窗口内按钮仍在：再一次「取消」不得结算同一个 Promise。
    fireEvent.click(screen.getByText('取消'))
    await vi.waitFor(() => expect(results).toEqual([true]))
  })
})
