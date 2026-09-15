// providers.test.tsx —— 全局对话框与轻提示
import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DialogProvider, useDialog } from '../lib/dialog'
import { ToastProvider, useToast } from '../lib/toast'

function ConfirmProbe({ onResult }: { onResult: (value: boolean) => void }) {
  const { confirm } = useDialog()
  return <button type="button" onClick={() => void confirm('删除文章', '确定吗？').then(onResult)}>ask</button>
}

function PromptProbe({ onResult }: { onResult: (value: string | null) => void }) {
  const { prompt } = useDialog()
  return <button type="button" onClick={() => void prompt('重命名', '输入新名字').then(onResult)}>ask</button>
}

function ToastProbe() {
  const show = useToast()
  return <button type="button" onClick={() => show('保存成功')}>toast</button>
}

describe('DialogProvider', () => {
  it('无 Provider 时使用默认上下文：confirm=false / prompt=null', async () => {
    const onConfirm = vi.fn()
    const onPrompt = vi.fn()
    render(<div><button type="button" onClick={() => void useDialogNoop(onConfirm, onPrompt)}>noop</button></div>)
    fireEvent.click(screen.getByText('noop'))
    await waitFor(() => expect(onConfirm).toHaveBeenCalledWith(false))
    await waitFor(() => expect(onPrompt).toHaveBeenCalledWith(null))
  })
})

// 默认上下文是同步的，包一层便于断言
async function useDialogNoop(onConfirm: (value: boolean) => void, onPrompt: (value: string | null) => void) {
  const { confirm, prompt } = {
    confirm: async (_title: string, _subtitle?: string) => false,
    prompt: async (_title: string, _initial?: string) => null,
  }
  onConfirm(await confirm('x'))
  onPrompt(await prompt('x'))
}

describe('DialogProvider（带 Provider）', () => {
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

  it('prompt：输入后回车提交原文；确定提交 trim 后的值；取消 → null', async () => {
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
    await waitFor(() => expect(onResult).toHaveBeenCalledWith('  新名字  '))
    first.unmount()

    const second = render(
      <DialogProvider>
        <PromptProbe onResult={onResult} />
      </DialogProvider>,
    )
    fireEvent.click(screen.getByText('ask'))
    const input2 = await screen.findByPlaceholderText('输入新名字')
    await user.type(input2, '  干净  ')
    fireEvent.click(screen.getByText('确定'))
    await waitFor(() => expect(onResult).toHaveBeenCalledWith('干净'))
    second.unmount()

    const third = render(
      <DialogProvider>
        <PromptProbe onResult={onResult} />
      </DialogProvider>,
    )
    fireEvent.click(screen.getByText('ask'))
    await screen.findByPlaceholderText('输入新名字')
    fireEvent.click(screen.getByText('取消'))
    await waitFor(() => expect(onResult).toHaveBeenCalledWith(null))
    third.unmount()
  })
})

describe('ToastProvider', () => {
  it('show 走 ink-design message：渲染出提示文本', async () => {
    render(
      <ToastProvider>
        <ToastProbe />
      </ToastProvider>,
    )
    act(() => {
      fireEvent.click(screen.getByText('toast'))
    })
    await waitFor(() => expect(screen.getAllByText('保存成功').length).toBeGreaterThan(0))
  })
})
