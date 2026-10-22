// loading.test.tsx —— 延迟加载指示器（lib/loading.ts）
//
// 守的是三条「静默失效」的行为：
//   ① 阈值内的请求从不显示指示器（点击、翻页不该闪一下灰条）；
//   ② 请求结束时立刻收起，且下一次加载重新计时（不会沿用上一轮的可见状态）；
//   ③ 卸载时清掉定时器（否则会在已卸载的树里 setState）。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { LOADING_DELAY, useDelayedLoading } from '../lib/loading'

afterEach(() => {
  vi.useRealTimers()
})

describe('useDelayedLoading', () => {
  it('加载超过阈值才显示：阈值前一毫秒仍不可见', () => {
    vi.useFakeTimers()
    const { result } = renderHook(() => useDelayedLoading(true))

    expect(result.current).toBe(false)
    act(() => { vi.advanceTimersByTime(LOADING_DELAY - 1) })
    expect(result.current).toBe(false)
    act(() => { vi.advanceTimersByTime(1) })
    expect(result.current).toBe(true)
  })

  it('阈值内结束的请求从不显示', () => {
    vi.useFakeTimers()
    const { result, rerender } = renderHook(
      ({ loading }: { loading: boolean }) => useDelayedLoading(loading),
      { initialProps: { loading: true } },
    )

    act(() => { vi.advanceTimersByTime(LOADING_DELAY - 1) })
    rerender({ loading: false })
    // 请求已经结束，定时器被取消：再怎么推进时间也不该亮起来。
    act(() => { vi.advanceTimersByTime(LOADING_DELAY * 5) })
    expect(result.current).toBe(false)
  })

  it('显示后请求结束立刻收起；再次加载重新计时', () => {
    vi.useFakeTimers()
    const { result, rerender } = renderHook(
      ({ loading }: { loading: boolean }) => useDelayedLoading(loading),
      { initialProps: { loading: true } },
    )

    act(() => { vi.advanceTimersByTime(LOADING_DELAY) })
    expect(result.current).toBe(true)

    rerender({ loading: false })
    expect(result.current).toBe(false)

    // 第二轮：从头计时，而不是沿用上一轮的「已显示」。
    rerender({ loading: true })
    act(() => { vi.advanceTimersByTime(LOADING_DELAY - 1) })
    expect(result.current).toBe(false)
    act(() => { vi.advanceTimersByTime(1) })
    expect(result.current).toBe(true)
  })

  it('支持自定义阈值；卸载时清理定时器', () => {
    vi.useFakeTimers()
    const clear = vi.spyOn(window, 'clearTimeout')
    const { result, unmount } = renderHook(() => useDelayedLoading(true, 50))

    act(() => { vi.advanceTimersByTime(50) })
    expect(result.current).toBe(true)

    unmount()
    // 清理函数必须把「到点显示」的定时器撤掉，否则会在已卸载的树里 setState。
    expect(clear).toHaveBeenCalled()
    clear.mockRestore()
  })
})
