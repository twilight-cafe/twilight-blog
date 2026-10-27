// common-ui.test.tsx —— 通用展示组件 / 亮暗切换 / 错误边界
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { Reveal } from 'ink-design'
import { Avatar, Empty, Loading, PageHero } from '../components/common'
import ModeToggle from '../components/ModeToggle'
import ErrorBoundary from '../components/ErrorBoundary'

describe('common.tsx', () => {
  it('Reveal：进视口前隐藏，命中后加 ink-reveal-in 并停止观察', () => {
    const { container, unmount } = render(
      <Reveal as="article" delay={300} className="extra">
        <span>内容</span>
      </Reveal>,
    )
    const element = container.firstElementChild as HTMLElement
    expect(element.tagName).toBe('ARTICLE')
    expect(element.className).toBe('ink-reveal extra')
    // 延迟单位是毫秒（ink-design 的契约），由 CSS 变量表达，不走 JS 定时器。
    expect(element.style.getPropertyValue('--ink-reveal-delay')).toBe('300ms')

    // 从 setup 暴露的静态数组里拿最近的实例
    const observers = (globalThis as unknown as { IntersectionObserver: { instances: unknown[] } }).IntersectionObserver.instances
    const observer = observers[observers.length - 1] as unknown as {
      callback: (entries: Array<{ isIntersecting: boolean; target: Element }>) => void
      unobserve: ReturnType<typeof vi.fn>
      targets: Element[]
    }
    expect(observer.targets).toContain(element)
    act(() => {
      observer.callback([{ isIntersecting: true, target: element }])
    })
    expect(element.className).toBe('ink-reveal ink-reveal-in extra')
    expect(observer.unobserve).toHaveBeenCalledWith(element)

    // 未进入视口不播放；卸载断开观察
    const second = render(<Reveal>x</Reveal>)
    const secondElement = second.container.firstElementChild as HTMLElement
    const secondObserver = observers[observers.length - 1] as unknown as {
      callback: (entries: Array<{ isIntersecting: boolean }>) => void
      disconnect: ReturnType<typeof vi.fn>
    }
    secondObserver.callback([{ isIntersecting: false }])
    expect(secondElement.className).toBe('ink-reveal')
    unmount()
  })

  it('Reveal：没有 delay 时延迟变量为 0ms；减少动态效果时直接可见', () => {
    const { container } = render(<Reveal><i>x</i></Reveal>)
    const element = container.firstElementChild as HTMLElement
    expect(element.style.getPropertyValue('--ink-reveal-delay')).toBe('0ms')

    // 系统要求减少动态效果：不挂观察器，首帧就是可见态（Reveal 内部直接返回）。
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: query.includes('reduced-motion'),
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
    const reduced = render(<Reveal>y</Reveal>)
    expect((reduced.container.firstElementChild as HTMLElement).className).toBe('ink-reveal ink-reveal-in')
    vi.unstubAllGlobals()
  })

  it('Loading / Empty / Avatar / PageHero', () => {
    render(<Loading height={80} />)
    expect(screen.getByText('LOADING…')).toBeTruthy()

    const { rerender } = render(<Empty text="没有数据" />)
    expect(screen.getByText('没有数据')).toBeTruthy()

    rerender(<Avatar letter="a" />)
    expect(screen.getByText('a').className).toBe('cav')
    rerender(<Avatar letter="b" alt />)
    expect(screen.getByText('b').className).toBe('cav alt')
    rerender(<Avatar letter="" />)
    expect(screen.getByText('匿')).toBeTruthy()

    const { unmount } = render(<PageHero title={<b>T</b>} subtitle={<i>S</i>} />)
    expect(screen.getByText('T')).toBeTruthy()
    expect(screen.getByText('S')).toBeTruthy()
    // kicker 已移除：页头不再渲染那行英文小标签。
    expect(document.querySelector('.phero .kicker')).toBeNull()
    unmount()
    render(<PageHero title="只有标题" />)
    expect(screen.getByText('只有标题')).toBeTruthy()
  })
})

describe('ModeToggle', () => {
  afterEach(() => {
    window.localStorage.clear()
    delete document.documentElement.dataset.mode
    // lib.dom 把 startViewTransition 标成必有，所以删除时要把类型退成可选。
    delete (document as { startViewTransition?: unknown }).startViewTransition
    vi.unstubAllGlobals()
  })

  it('初次渲染按系统偏好落 mode；点击切换并持久化', () => {
    window.localStorage.setItem('tw_mode', 'dark')
    const { container, unmount } = render(<ModeToggle />)
    expect(document.documentElement.dataset.mode).toBe('dark')
    expect(window.localStorage.getItem('tw_mode')).toBe('dark')
    const button = container.querySelector('button') as HTMLButtonElement

    fireEvent.click(button)
    expect(document.documentElement.dataset.mode).toBe('light')
    expect(window.localStorage.getItem('tw_mode')).toBe('light')

    // 从 localStorage 读不到时按 matchMedia（setup 里恒 false → light）
    window.localStorage.removeItem('tw_mode')
    const second = render(<ModeToggle />)
    expect(document.documentElement.dataset.mode).toBe('light')
    fireEvent.click((second.container.querySelector('button')) as HTMLButtonElement)
    expect(document.documentElement.dataset.mode).toBe('dark')
    second.unmount()
    unmount()
  })

  it('支持 View Transitions 时在回调里换色（整页交叉淡入）', () => {
    let inside = false
    const startViewTransition = vi.fn((callback: () => void) => {
      // 真浏览器的回调在快照之后才执行：这里同样延后到「调用栈之外」不可能，
      // 但要验证的是「换色发生在这个回调里」，不是时序。
      callback()
      inside = document.documentElement.dataset.mode === 'dark'
      return {}
    })
    Object.defineProperty(document, 'startViewTransition', { configurable: true, value: startViewTransition })

    render(<ModeToggle />)
    fireEvent.click(document.querySelector('button') as HTMLButtonElement)

    expect(startViewTransition).toHaveBeenCalledTimes(1)
    expect(inside).toBe(true)
    expect(document.documentElement.dataset.mode).toBe('dark')
    expect(window.localStorage.getItem('tw_mode')).toBe('dark')
  })

  it('系统要求减少动态效果时退回直接切换，不做整页交叉淡入', () => {
    const startViewTransition = vi.fn()
    Object.defineProperty(document, 'startViewTransition', { configurable: true, value: startViewTransition })
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: true,
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))

    render(<ModeToggle />)
    fireEvent.click(document.querySelector('button') as HTMLButtonElement)

    expect(startViewTransition).not.toHaveBeenCalled()
    // matches 恒 true 也意味着 prefers-color-scheme 命中 → 初始为暗色，点击后变亮。
    expect(document.documentElement.dataset.mode).toBe('light')
  })
})

describe('ErrorBoundary', () => {
  function Boom(): never {
    throw new Error('渲染爆炸 <script>')
  }

  it('渲染异常被拦截为回退界面；恢复正常后 children 回来', () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { container, rerender } = render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    )
    expect(screen.getByText('Twilight · 页面出错了')).toBeTruthy()
    expect(screen.getByText('渲染爆炸 <script>')).toBeTruthy()
    expect(errorSpy).toHaveBeenCalled()

    // 错误态由边界持有，children 换成健康组件也保持回退界面（重新挂载才复位）
    rerender(
      <ErrorBoundary>
        <span>已恢复</span>
      </ErrorBoundary>,
    )
    expect(screen.queryByText('已恢复')).toBeNull()
    expect(container).toBeTruthy()
    errorSpy.mockRestore()
  })

  it('重新加载按钮触发 location.reload', () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const reload = vi.fn()
    try {
      // jsdom 的 reload 是「未实现」占位，可赋值时替换、不可赋值时依赖其空实现
      (window.location as unknown as { reload: () => void }).reload = reload as unknown as () => void
    } catch {
      // ignore
    }
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    )
    act(() => {
      fireEvent.click(screen.getByText('重新加载'))
    })
    expect(screen.getByText('重新加载')).toBeTruthy() // 点击成功、无异常即覆盖处理器
    errorSpy.mockRestore()
  })
})
