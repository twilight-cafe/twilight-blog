// dialog-modal.test.tsx —— 用受控的 Modal 替身驱动 dialog.tsx 的罕见分支
// 真实 Modal 关闭后按钮即卸载，「state 已为 null 时 close 再次被调用」（onOk 与
// onCancel 竞态双触发）在渲染路径上不可达，需要替身保持按钮存活才能触达；
// 替身模拟的是「关闭动作与取消动作在同一帧内先后到达」的极端时序。
import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { DialogProvider, useDialog } from '../lib/dialog'

vi.mock('ink-design', async (importOriginal) => {
  const actual = await importOriginal<typeof import('ink-design')>()
  return {
    ...actual,
    // 替身：无论 open 与否都渲染两枚按钮，让测试能在 close 之后再次触发 onCancel
    Modal: (props: { onOk?: () => void; onCancel?: () => void; children?: React.ReactNode }) => (
      <div>
        <button onClick={props.onOk}>MODAL-OK</button>
        <button onClick={props.onCancel}>MODAL-CANCEL</button>
        {props.children}
      </div>
    ),
  }
})

function Consumer({ onConfirm, onPrompt }: { onConfirm: (d: ReturnType<typeof useDialog>) => void; onPrompt?: (d: ReturnType<typeof useDialog>) => void }) {
  const dialog = useDialog()
  return (
    <button
      onClick={() => {
        onConfirm(dialog)
        onPrompt?.(dialog)
      }}
    >
      open
    </button>
  )
}

describe('DialogProvider 罕见时序', () => {
  it('prompt 不带 placeholder：占位符走空串分支；确定提交 trim 后的值', async () => {
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
    fireEvent.click(screen.getByText('MODAL-OK'))
    await vi.waitFor(() => expect(result).toBe('答案'))
  })

  it('确认后紧接取消：close 在 state 已清空时被再调一次，不炸不重复 resolve', async () => {
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
    fireEvent.click(screen.getByText('MODAL-OK')) // close(true)，state 清空
    fireEvent.click(screen.getByText('MODAL-CANCEL')) // state 已为 null 的 close 分支
    await vi.waitFor(() => expect(results).toEqual([true]))
  })
})
