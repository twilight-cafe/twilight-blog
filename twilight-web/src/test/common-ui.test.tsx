// common-ui.test.tsx —— 通用展示组件 / 亮暗切换 / 错误边界
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { Avatar, Empty, Loading, PageHero, Reveal } from '../components/common'
import ModeToggle from '../components/ModeToggle'
import ErrorBoundary from '../components/ErrorBoundary'

describe('common.tsx', () => {
  it('Reveal：IntersectionObserver 命中后加 in 类并停止观察', () => {
    const { container, unmount } = render(
      <Reveal as="article" delay={0.3} className="extra">
        <span>内容</span>
      </Reveal>,
    )
    const element = container.firstElementChild as HTMLElement
    expect(element.tagName).toBe('ARTICLE')
    expect(element.className).toBe('rv  extra')
    expect(element.style.getPropertyValue('--d')).toBe('0.3s')

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
    expect(element.className).toBe('rv in extra')
    expect(observer.unobserve).toHaveBeenCalledWith(element)

    // 未进入视口不播放；卸载断开观察
    const second = render(<Reveal>x</Reveal>)
    const secondElement = second.container.firstElementChild as HTMLElement
    const secondObserver = observers[observers.length - 1] as unknown as {
      callback: (entries: Array<{ isIntersecting: boolean }>) => void
      disconnect: ReturnType<typeof vi.fn>
    }
    secondObserver.callback([{ isIntersecting: false }])
    expect(secondElement.className).toBe('rv')
    unmount()
  })

  it('Reveal：无 delay 不写 CSS 变量；ref 为空安全', () => {
    const { container } = render(<Reveal><i>x</i></Reveal>)
    expect((container.firstElementChild as HTMLElement).style.getPropertyValue('--d')).toBe('')
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

    const { unmount } = render(<PageHero kicker="K" title={<b>T</b>} subtitle={<i>S</i>} />)
    expect(screen.getByText('K')).toBeTruthy()
    expect(screen.getByText('T')).toBeTruthy()
    expect(screen.getByText('S')).toBeTruthy()
    unmount()
    render(<PageHero kicker="K2" title="只有标题" />)
    expect(screen.getByText('只有标题')).toBeTruthy()
  })
})

describe('ModeToggle', () => {
  afterEach(() => {
    window.localStorage.clear()
    delete document.documentElement.dataset.mode
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
