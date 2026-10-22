// providers.test.tsx —— 全局对话框与轻提示
//
// 对话框现在直接转发 ink-design 的 DialogProvider（见 lib/dialog.tsx），
// 所以这里守的是「站点依赖的那几条语义」：
//   ① 没有 Provider 时直接报错——静默返回 false/null 会把漏包 Provider 的
//      问题藏到运行期；
//   ② 并发调用时后一次覆盖前一次，前一个 Promise 按取消结算，调用方不会永远悬着；
//   ③ prompt 的选项对象（placeholder / initialValue）与返回值约定。

import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DialogProvider, useDialog } from '../lib/dialog'
import { ToastProvider, useToast } from '../lib/toast'

function ConfirmProbe({ onResult }: { onResult: (value: boolean) => void }) {
  const { confirm } = useDialog()
  return <button type="button" onClick={() => void confirm('删除文章', '确定吗？').then(onResult)}>ask</button>
}

function PromptProbe({ onResult }: { onResult: (value: string | null) => void }) {
  const { prompt } = useDialog()
  return <button type="button" onClick={() => void prompt('重命名', { placeholder: '输入新名字' }).then(onResult)}>ask</button>
}

function ToastProbe() {
  const show = useToast()
  return <button type="button" onClick={() => show('保存成功')}>toast</button>
}

describe('DialogProvider', () => {
  it('无 Provider 时直接报错（不静默降级）', () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    function Bare() {
      useDialog()
      return null
    }
    expect(() => render(<Bare />)).toThrow('useDialog 必须在 <DialogProvider> 内使用')
    errorSpy.mockRestore()
  })

  it('confirm：确定 → true；取消 → false', async () => {
    const onResult = vi.fn()
    const first = render(
      <DialogProvider>
        <ConfirmProbe onResult={onResult} />
      </DialogProvider>,
    )
    fireEvent.click(screen.getByText('ask'))
    await waitFor(() => expect(screen.getByText('删除文章')).toBeTruthy())
    expect(screen.getByText('确定吗？')).toBeTruthy()
    fireEvent.click(screen.getByText('确定'))
    await waitFor(() => expect(onResult).toHaveBeenCalledWith(true))

    first.unmount()
    const second = render(
      <DialogProvider>
        <ConfirmProbe onResult={onResult} />
      </DialogProvider>,
    )
    fireEvent.click(screen.getByText('ask'))
    await waitFor(() => expect(screen.getByText('取消')).toBeTruthy())
    fireEvent.click(screen.getByText('取消'))
    await waitFor(() => expect(onResult).toHaveBeenCalledWith(false))
    second.unmount()
  })

  it('prompt：回车提交原文；取消 → null；提交前不替调用方 trim', async () => {
    const user = userEvent.setup()
    const onResult = vi.fn()

    const first = render(
      <DialogProvider>
        <PromptProbe onResult={onResult} />
      </DialogProvider>,
    )
    fireEvent.click(screen.getByText('ask'))
    const input = await screen.findByPlaceholderText('输入新名字')
    await user.type(input, '  新名字  ')
    await user.keyboard('{Enter}')
    // trim 是调用方的业务决定（见 MediaAdmin 的 rename），对话框只负责原样返回。
    await waitFor(() => expect(onResult).toHaveBeenCalledWith('  新名字  '))
    first.unmount()

    const second = render(
      <DialogProvider>
        <PromptProbe onResult={onResult} />
      </DialogProvider>,
    )
    fireEvent.click(screen.getByText('ask'))
    await screen.findByPlaceholderText('输入新名字')
    fireEvent.click(screen.getByText('取消'))
    await waitFor(() => expect(onResult).toHaveBeenCalledWith(null))
    second.unmount()
  })

  it('并发：后一次调用覆盖前一次，前一个 Promise 按取消结算', async () => {
    const results: boolean[] = []
    function DoubleProbe() {
      const { confirm } = useDialog()
      return (
        <button
          type="button"
          onClick={() => {
            void confirm('第一次').then((value) => results.push(value))
            void confirm('第二次').then((value) => results.push(value))
          }}
        >
          ask
        </button>
      )
    }
    render(
      <DialogProvider>
        <DoubleProbe />
      </DialogProvider>,
    )
    fireEvent.click(screen.getByText('ask'))
    // 第一个请求在第二次调用时立刻按「取消」结算，不会永远悬着。
    await waitFor(() => expect(results).toEqual([false]))
    expect(screen.getByText('第二次')).toBeTruthy()
    fireEvent.click(screen.getByText('确定'))
    await waitFor(() => expect(results).toEqual([false, true]))
  })

  it('prompt：initialValue 预填并默认全选', async () => {
    function RenameProbe() {
      const { prompt } = useDialog()
      return (
        <button type="button" onClick={() => void prompt('重命名', { initialValue: 'old.png' })}>
          ask
        </button>
      )
    }
    render(
      <DialogProvider>
        <RenameProbe />
      </DialogProvider>,
    )
    fireEvent.click(screen.getByText('ask'))
    const input = await screen.findByRole('textbox')
    expect((input as HTMLInputElement).value).toBe('old.png')
    expect((input as HTMLInputElement).selectionStart).toBe(0)
    expect((input as HTMLInputElement).selectionEnd).toBe('old.png'.length)
  })
})

describe('ToastProvider', () => {
  it('show 走 ink-design message：渲染出提示文本', async () => {
    render(
      <ToastProvider>
        <ToastProbe />
      </ToastProvider>,
    )
    fireEvent.click(screen.getByText('toast'))
    await waitFor(() => expect(screen.getAllByText('保存成功').length).toBeGreaterThan(0))
  })
})
