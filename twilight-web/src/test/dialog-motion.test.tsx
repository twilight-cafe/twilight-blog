// dialog-motion.test.tsx —— 对话框的退场
//
// 退场由 ink-design 的 Modal `closeDuration` 接管（DialogProvider 默认 140ms）：
// promise 先兑现，浮层再留一段时间播完退场。这里守两件事：
//   ① 那段时间里浮层还在、已从可访问性树移出（aria-hidden），之后才卸载；
//   ② 系统要求减少动态效果时不等，关闭后立即卸载（否则会剩一段看不见的遮罩）。

import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { DialogProvider, useDialog } from '../lib/dialog'

afterEach(() => {
  vi.unstubAllGlobals()
})

function setMatchMedia(matches: boolean): void {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches,
    media: query,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
}

function Probe({ onResult }: { onResult: (value: boolean) => void }) {
  const { confirm } = useDialog()
  return (
    <button type="button" onClick={() => void confirm('删除文章', '确定吗？').then(onResult)}>
      打开
    </button>
  )
}

describe('DialogProvider 退场', () => {
  it('resolve 后浮层留到退场结束：aria-hidden、重复关闭不再结算，之后卸载', async () => {
    setMatchMedia(false)
    let result: boolean | undefined
    render(
      <DialogProvider>
        <Probe onResult={(value) => { result = value }} />
      </DialogProvider>,
    )

    fireEvent.click(screen.getByText('打开'))
    fireEvent.click(screen.getByText('确定'))
    await waitFor(() => expect(result).toBe(true))

    const modal = document.querySelector('.modal') as HTMLElement
    // 此刻浮层还在：promise 已经兑现，但卸载被推迟到退场动画之后。
    expect(modal).toBeTruthy()
    expect(modal.getAttribute('aria-hidden')).toBe('true')

    // 退场中重复点取消：closingRef 守卫让同一个 promise 不会被再 resolve 一次。
    fireEvent.click(screen.getByText('取消'))
    expect(result).toBe(true)

    // 退场结束后整棵浮层卸载，不会留在 DOM 里。
    await waitFor(() => expect(document.querySelector('.modal')).toBeNull(), { timeout: 1500 })
  })

  it('减少动态效果：不等退场，关闭后立即卸载', async () => {
    setMatchMedia(true)
    let result: boolean | undefined
    render(
      <DialogProvider>
        <Probe onResult={(value) => { result = value }} />
      </DialogProvider>,
    )

    fireEvent.click(screen.getByText('打开'))
    fireEvent.click(screen.getByText('确定'))
    await waitFor(() => expect(result).toBe(true))
    await waitFor(() => expect(document.querySelector('.modal')).toBeNull(), { timeout: 1500 })
  })
})
