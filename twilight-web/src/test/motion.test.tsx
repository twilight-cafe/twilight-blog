// motion.test.tsx —— 动效底层（lib/motion.ts）与阅读进度条
//
// 这里守的是三种「系统级」行为：
//   ① matchMedia 缺失 / 命中减少动态效果时都不能抛错（App 外壳在调它）；
//   ② 退场与入场只在对应阶段播一次，换阶段必取消上一段，首屏不播；高度接力
//      只在两端都量得出来时才做，收尾必须把高度交还内容；
//   ③ 阅读进度、目录高亮、回到顶部的滚动订阅已由 ink-design 提供（见
//      components/common.tsx 与 PublicSidebar），这里不再重复测库的实现。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { useEffect, useRef, useState, type ReactElement } from 'react'
import { MemoryRouter, useLocation, useNavigate } from 'react-router-dom'
import {
  IOS_EASE_OUT,
  prefersReducedMotion,
  ROUTE_ENTER_DURATION,
  ROUTE_EXIT_DURATION,
  useHeightRelay,
  usePagerMotion,
  useRouteMotion,
  zoomTransform,
  type RoutePhase,
} from '../lib/motion'

// jsdom 把 scrollY / innerHeight 定义成 window 上的自有访问器；覆盖后必须按
// 原描述符还原，而不是 delete——delete 会连访问器一起删掉，后续用例里
// window.scrollY 变成 undefined，问题看起来和本文件毫无关系。
const scrollYDescriptor = Object.getOwnPropertyDescriptor(window, 'scrollY')!
const innerHeightDescriptor = Object.getOwnPropertyDescriptor(window, 'innerHeight')!
// getBoundingClientRect 同理：覆盖后要能还原，否则同一个 worker 里后续用例
// 量出来的高度全变成了这里的桩值。
const rectDescriptor = Object.getOwnPropertyDescriptor(Element.prototype, 'getBoundingClientRect')!

afterEach(() => {
  vi.unstubAllGlobals()
  delete (Element.prototype as unknown as { animate?: unknown }).animate
  Object.defineProperty(window, 'scrollY', scrollYDescriptor)
  Object.defineProperty(window, 'innerHeight', innerHeightDescriptor)
  Object.defineProperty(Element.prototype, 'getBoundingClientRect', rectDescriptor)
  delete (document.documentElement as unknown as { scrollHeight?: unknown }).scrollHeight
})

function setMatchMedia(matches: boolean): void {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches,
    media: query,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
}

interface FakeAnimation {
  cancel: ReturnType<typeof vi.fn>
  onfinish: (() => void) | null
}

/**
 * 把 Element.prototype.animate 换成一个记账桩：jsdom 没有 Web Animations，
 * 而这里要断言的是「播了什么关键帧」以及「收尾时有没有把高度交还内容」。
 * 返回的 made 数组按调用顺序保存每次返回的动画对象。
 */
function stubAnimations(): { animate: ReturnType<typeof vi.fn>; made: FakeAnimation[] } {
  const made: FakeAnimation[] = []
  const animate = vi.fn(() => {
    const animation: FakeAnimation = { cancel: vi.fn(), onfinish: null }
    made.push(animation)
    return animation
  })
  Object.defineProperty(Element.prototype, 'animate', { configurable: true, value: animate })
  return { animate, made }
}

/** 让所有元素都量出同一个高度（jsdom 默认量出 0，高度接力就无从验起）。 */
function stubHeight(height: number): void {
  Object.defineProperty(Element.prototype, 'getBoundingClientRect', {
    configurable: true,
    value: () => ({ top: 0, left: 0, right: 0, bottom: height, width: 0, height, x: 0, y: 0, toJSON: () => ({}) }),
  })
}

describe('prefersReducedMotion', () => {
  it('跟随系统偏好', () => {
    setMatchMedia(true)
    expect(prefersReducedMotion()).toBe(true)

    setMatchMedia(false)
    expect(prefersReducedMotion()).toBe(false)
  })

  it('matchMedia 不存在时按「不减少」处理，而不是抛错', () => {
    vi.stubGlobal('matchMedia', undefined)
    expect(prefersReducedMotion()).toBe(false)
  })
})

describe('useRouteMotion（退场 / 入场 / 高度接力）', () => {
  function Motion({ phase, path, attach }: { phase: RoutePhase; path: string; attach: boolean }) {
    const ref = useRef<HTMLDivElement | null>(null)
    const navigate = useNavigate()
    const { pathname } = useLocation()
    useRouteMotion(ref, phase)
    // 方向来自 useNavigationType：首屏那条记录是 POP（= 返回方向），只有真的
    // 推入一次之后才是前进。不给它一次真实导航，就只能验到返回镜像。
    useEffect(() => {
      if (pathname !== path) navigate(path)
    }, [navigate, path, pathname])
    return attach ? <div ref={ref} /> : null
  }

  // 阶段由 RouteTransition 驱动（见 RouteTransition.tsx），这里直接给 prop：
  // 这个文件只验「拿到某个阶段时该怎么动」。
  function Harness({ phase, path = '/a', attach = true }: {
    phase: RoutePhase
    path?: string
    attach?: boolean
  }) {
    return (
      <MemoryRouter initialEntries={['/a']}>
        <Motion phase={phase} path={path} attach={attach} />
      </MemoryRouter>
    )
  }

  /** 先推入一次，把方向从首屏的 POP（返回）改成前进。 */
  function pushToB(rerender: (ui: ReactElement) => void): void {
    rerender(<Harness phase="idle" path="/b" />)
  }

  it('退场：按前进方向滑走并淡出，fill: forwards 停在透明态；阶段一换就取消', () => {
    const { animate, made } = stubAnimations()
    stubHeight(600)

    const { rerender } = render(<Harness phase="idle" />)
    // 静止阶段什么都不播：首屏已经有标题逐字入场与 .rv，不叠第二次整页位移。
    expect(animate).not.toHaveBeenCalled()

    pushToB(rerender)
    rerender(<Harness phase="exit" path="/b" />)
    expect(animate).toHaveBeenCalledTimes(1)
    expect(animate.mock.calls[0][0]).toEqual([
      { opacity: 1, transform: 'none' },
      { opacity: 0, transform: 'translateX(-10px)' },
    ])
    expect(animate.mock.calls[0][1]).toEqual({
      duration: ROUTE_EXIT_DURATION,
      easing: IOS_EASE_OUT,
      fill: 'forwards',
    })

    // 换页提交时旧页面整体消失，退场动画必须取消：留着动画对象等于让已经
    // 不在树上的元素继续占着合成层。
    rerender(<Harness phase="enter" path="/b" />)
    expect(made[0].cancel).toHaveBeenCalled()
  })

  it('入场：从来的方向推入，高度从旧页连续接到新页，收尾交还高度', () => {
    const { animate, made } = stubAnimations()
    stubHeight(600)

    const { rerender, container } = render(<Harness phase="idle" />)
    pushToB(rerender)
    rerender(<Harness phase="exit" path="/b" />)
    // 提交之后旧内容就没了，旧高度只能在退场那一刻先量下来。
    stubHeight(260)
    rerender(<Harness phase="enter" path="/b" />)

    expect(animate.mock.calls[1][0]).toEqual([
      { opacity: 0, transform: 'translateX(14px)' },
      { opacity: 1, transform: 'none' },
    ])
    expect(animate.mock.calls[1][1]).toEqual({ duration: ROUTE_ENTER_DURATION, easing: IOS_EASE_OUT })
    expect(animate.mock.calls[2][0]).toEqual([{ height: '600px' }, { height: '260px' }])
    expect(animate.mock.calls[2][1]).toEqual({ duration: 380, easing: IOS_EASE_OUT })

    const el = container.querySelector('div') as HTMLElement
    // 第一帧就是「旧高度 + 新内容起点」，不会先看到新页面完整地闪一下。
    expect(el.style.height).toBe('600px')
    expect(el.style.overflow).toBe('hidden')

    // 收尾必须把高度交还内容：钉死在 px 上之后评论展开、新数据都会被视为溢出。
    made[2].onfinish?.()
    expect(el.style.height).toBe('')
    expect(el.style.overflow).toBe('')

    // 再切到静止：cleanup 取消两段动画，也不会把高度写回去。
    rerender(<Harness phase="idle" />)
    expect(made[1].cancel).toHaveBeenCalled()
    expect(made[2].cancel).toHaveBeenCalled()
    expect(el.style.height).toBe('')
  })

  it('返回（POP）镜像：老内容向右退，新内容从左侧回来', () => {
    const { animate } = stubAnimations()
    stubHeight(600)

    function Back() {
      const [phase, setPhase] = useState<RoutePhase>('idle')
      const ref = useRef<HTMLDivElement | null>(null)
      useRouteMotion(ref, phase)
      const navigate = useNavigate()
      return (
        <>
          <div ref={ref} />
          <button type="button" onClick={() => { navigate(-1); setPhase('exit') }}>返回</button>
        </>
      )
    }

    render(
      <MemoryRouter initialEntries={['/a', '/b']} initialIndex={1}>
        <Back />
      </MemoryRouter>,
    )
    fireEvent.click(screen.getByText('返回'))
    expect(animate.mock.calls[0][0]).toEqual([
      { opacity: 1, transform: 'none' },
      { opacity: 0, transform: 'translateX(10px)' },
    ])
  })

  it('系统要求减少动态效果：退场与入场都不播，高度也不接力', () => {
    const { animate } = stubAnimations()
    stubHeight(600)
    setMatchMedia(true)

    const { rerender, container } = render(<Harness phase="idle" />)
    expect(() => {
      rerender(<Harness phase="exit" />)
      rerender(<Harness phase="enter" />)
    }).not.toThrow()
    expect(animate).not.toHaveBeenCalled()
    expect((container.querySelector('div') as HTMLElement).style.height).toBe('')
  })

  it('容器还没挂载（ref 为空）时两段都跳过', () => {
    const { animate } = stubAnimations()
    stubHeight(600)

    const { rerender } = render(<Harness phase="idle" attach={false} />)
    rerender(<Harness phase="exit" attach={false} />)
    rerender(<Harness phase="enter" attach={false} />)
    expect(animate).not.toHaveBeenCalled()
  })

  it('浏览器不支持 Web Animations 时静默跳过（jsdom 就是这种情况）', () => {
    stubHeight(600)

    const { rerender, container } = render(<Harness phase="idle" />)
    expect(() => {
      rerender(<Harness phase="exit" />)
      rerender(<Harness phase="enter" />)
    }).not.toThrow()
    // 不支持 WAAPI 时也不能留下高度或裁切的痕迹。
    expect((container.querySelector('div') as HTMLElement).style.height).toBe('')
    expect((container.querySelector('div') as HTMLElement).style.overflow).toBe('')
  })

  it('量不出旧高度（首帧、display: none）时只播位移，不碰高度', () => {
    const { animate } = stubAnimations()
    stubHeight(0)

    const { rerender, container } = render(<Harness phase="idle" />)
    rerender(<Harness phase="enter" />)
    // 只有位移那一段：0 → 0 的高度动画没有意义，还会把 overflow: hidden 留下。
    expect(animate).toHaveBeenCalledTimes(1)
    expect((container.querySelector('div') as HTMLElement).style.height).toBe('')
  })

  it('新页面量不出高度时同样只播位移，不留 overflow: hidden', () => {
    const { animate } = stubAnimations()
    stubHeight(600)

    const { rerender, container } = render(<Harness phase="idle" />)
    rerender(<Harness phase="exit" />)
    stubHeight(0)
    rerender(<Harness phase="enter" />)
    // 退场 1 段 + 入场位移 1 段，没有高度那一段。
    expect(animate).toHaveBeenCalledTimes(2)
    expect((container.querySelector('div') as HTMLElement).style.height).toBe('')
    expect((container.querySelector('div') as HTMLElement).style.overflow).toBe('')
  })
})

describe('useRouteMotion（滚动位置）', () => {
  function Harness() {
    const ref = useRef<HTMLDivElement | null>(null)
    // 只验滚动：阶段固定 idle，动画那部分由上一组用例负责。
    useRouteMotion(ref, 'idle')
    const navigate = useNavigate()
    return (
      <>
        <div ref={ref} />
        <button type="button" onClick={() => navigate('/b')}>到 B</button>
        <button type="button" onClick={() => navigate(-1)}>返回</button>
      </>
    )
  }

  function setScrollY(value: number): void {
    Object.defineProperty(window, 'scrollY', { configurable: true, value })
    act(() => {
      window.dispatchEvent(new Event('scroll'))
    })
  }

  it('前进一律到顶；返回回到离开那条记录时的位置', () => {
    const scrollTo = vi.fn()
    vi.stubGlobal('scrollTo', scrollTo)

    render(<MemoryRouter initialEntries={['/a']}><Harness /></MemoryRouter>)
    // 在 /a 上滚到 300：监听器把这个位置记在 /a 那条 history 记录上。
    setScrollY(300)

    fireEvent.click(screen.getByText('到 B'))
    expect(scrollTo).toHaveBeenLastCalledWith(0, 0)

    setScrollY(0)
    fireEvent.click(screen.getByText('返回'))
    // 回到 /a 时恢复刚才记下的 300，而不是留在 /b 的顶端。
    expect(scrollTo).toHaveBeenLastCalledWith(0, 300)
  })

  it('返回一条从没停留过的记录时按 0 处理', () => {
    const scrollTo = vi.fn()
    vi.stubGlobal('scrollTo', scrollTo)

    render(
      <MemoryRouter initialEntries={['/a', '/b']} initialIndex={1}>
        <Harness />
      </MemoryRouter>,
    )
    fireEvent.click(screen.getByText('返回'))
    expect(scrollTo).toHaveBeenLastCalledWith(0, 0)
  })
})

describe('zoomTransform（共享元素起点）', () => {
  const image = { left: 200, top: 150, width: 400, height: 300 }
  const thumb = { left: 0, top: 0, width: 100, height: 100 }

  it('换算成以图片中心为基准的位移与等比缩放', () => {
    // 中心点从 (200, 150) 移到 (50, 50)；宽度 400 → 100 即 0.25 倍。
    expect(zoomTransform(thumb, image)).toBe('translate(-350px, -250px) scale(0.25)')
  })

  it('任何一边量不出尺寸都返回 null（图片还没布局、元素已隐藏）', () => {
    expect(zoomTransform({ ...thumb, width: 0 }, image)).toBeNull()
    expect(zoomTransform({ ...thumb, height: 0 }, image)).toBeNull()
    expect(zoomTransform(thumb, { ...image, width: 0 })).toBeNull()
    expect(zoomTransform(thumb, { ...image, height: 0 })).toBeNull()
  })
})

describe('useHeightRelay（页头的空间接力）', () => {
  // 这个钩子不碰路由与滚动，只认阶段与 ref，所以不需要 MemoryRouter。
  function Harness({ phase, attach = true }: { phase: RoutePhase; attach?: boolean }) {
    const ref = useRef<HTMLDivElement | null>(null)
    useHeightRelay(ref, phase)
    return attach ? <div ref={ref} /> : null
  }

  it('首页大标题 → 内页精简头：高度从旧值连续收到新值，收尾交还高度', () => {
    const { animate, made } = stubAnimations()
    stubHeight(300) // 退场时量到的旧页头

    const { rerender, container } = render(<Harness phase="idle" />)
    rerender(<Harness phase="exit" />)
    stubHeight(120) // 提交后换成精简头
    rerender(<Harness phase="enter" />)

    expect(animate).toHaveBeenCalledTimes(1)
    expect(animate.mock.calls[0][0]).toEqual([{ height: '300px' }, { height: '120px' }])
    expect(animate.mock.calls[0][1]).toEqual({ duration: 380, easing: IOS_EASE_OUT })

    const el = container.querySelector('div') as HTMLElement
    // 第一帧就把新页头钉在旧高度上：正文从原来的位置开始，再被连续推上去，
    // 而不是在提交的那一帧被拽动 180px。
    expect(el.style.height).toBe('300px')
    expect(el.style.overflow).toBe('hidden')

    made[0].onfinish?.()
    expect(el.style.height).toBe('')
    expect(el.style.overflow).toBe('')

    // 阶段回到静止：cleanup 取消动画，也不会把高度写回去。
    rerender(<Harness phase="idle" />)
    expect(made[0].cancel).toHaveBeenCalled()
    expect(el.style.height).toBe('')
  })

  it('展开方向（精简头 → 大标题）同样接力', () => {
    const { animate } = stubAnimations()
    stubHeight(120)

    const { rerender } = render(<Harness phase="idle" />)
    rerender(<Harness phase="exit" />)
    stubHeight(300)
    rerender(<Harness phase="enter" />)

    expect(animate.mock.calls[0][0]).toEqual([{ height: '120px' }, { height: '300px' }])
  })

  it('两种页头一样高（内页之间互跳）时不动，也不留多余的 overflow: hidden', () => {
    const { animate } = stubAnimations()
    stubHeight(120)

    const { rerender, container } = render(<Harness phase="idle" />)
    rerender(<Harness phase="exit" />)
    rerender(<Harness phase="enter" />)

    expect(animate).not.toHaveBeenCalled()
    expect((container.querySelector('div') as HTMLElement).style.height).toBe('')
  })

  it('没有退场测量（首屏直接进入入场）时不做接力', () => {
    const { animate } = stubAnimations()
    stubHeight(120)

    const { rerender } = render(<Harness phase="idle" />)
    rerender(<Harness phase="enter" />)
    expect(animate).not.toHaveBeenCalled()
  })

  it('新页头量出 0 高（隐藏 / 还没布局）时不做接力', () => {
    const { animate } = stubAnimations()
    stubHeight(300)

    const { rerender } = render(<Harness phase="idle" />)
    rerender(<Harness phase="exit" />)
    stubHeight(0)
    rerender(<Harness phase="enter" />)
    expect(animate).not.toHaveBeenCalled()
  })

  it('系统要求减少动态效果时不接力', () => {
    const { animate } = stubAnimations()
    stubHeight(300)
    setMatchMedia(true)

    const { rerender, container } = render(<Harness phase="idle" />)
    rerender(<Harness phase="exit" />)
    stubHeight(120)
    rerender(<Harness phase="enter" />)

    expect(animate).not.toHaveBeenCalled()
    expect((container.querySelector('div') as HTMLElement).style.height).toBe('')
  })

  it('浏览器不支持 Web Animations 时静默跳过（jsdom 就是这种情况）', () => {
    stubHeight(300)

    const { rerender, container } = render(<Harness phase="idle" />)
    rerender(<Harness phase="exit" />)
    stubHeight(120)
    expect(() => rerender(<Harness phase="enter" />)).not.toThrow()
    expect((container.querySelector('div') as HTMLElement).style.height).toBe('')
  })

  it('容器还没挂载（ref 为空）时两段都跳过', () => {
    const { animate } = stubAnimations()
    stubHeight(300)

    const { rerender } = render(<Harness phase="idle" attach={false} />)
    rerender(<Harness phase="exit" attach={false} />)
    stubHeight(120)
    rerender(<Harness phase="enter" attach={false} />)
    expect(animate).not.toHaveBeenCalled()
  })
})

describe('usePagerMotion（分页翻页：与换路由同一段动画）', () => {
  // 翻页动画由页码驱动（不是路由），这里直接把 usePagerMotion 挂在一个可点的
  // 数字上：点一下 = 点了一个页码。断言的是「第几帧换页、播了哪一组关键帧」，
  // 这正是「和切换路由时一样」这句话里唯一可测的部分。
  function Harness({ pages = 3 }: { pages?: number }) {
    const [page, setPage] = useState(1)
    const { page: shown, phase, ref } = usePagerMotion(page)
    return (
      <div>
        {Array.from({ length: pages }, (_, index) => (
          <button key={index} type="button" onClick={() => setPage(index + 1)}>
            {index + 1}
          </button>
        ))}
        <div ref={ref} data-shown={shown} data-phase={phase} />
      </div>
    )
  }

  const content = () => document.querySelector('[data-shown]') as HTMLElement

  it('退场 180ms 后才提交新页，再播与换路由相同的入场与高度接力', () => {
    vi.useFakeTimers()
    try {
      setMatchMedia(false)
      stubHeight(240)
      const { animate, made } = stubAnimations()
      render(<Harness />)

      expect(content().dataset.shown).toBe('1')
      expect(content().dataset.phase).toBe('idle')
      expect(animate).not.toHaveBeenCalled()

      fireEvent.click(screen.getByRole('button', { name: '2' }))
      // 渲染期就进入退场：点击后的第一帧旧内容已经在滑走（与 RouteTransition 同一写法）。
      expect(content().dataset.phase).toBe('exit')
      expect(content().dataset.shown).toBe('1')
      const exit = animate.mock.calls[0][0] as Keyframe[]
      expect(exit[0]).toMatchObject({ opacity: 1 })
      expect(exit[1]).toMatchObject({ opacity: 0, transform: 'translateX(-10px)' })

      // 差 1ms 都不提交：这 180ms 是旧内容真正退出的时间，与换路由完全一致。
      act(() => { vi.advanceTimersByTime(ROUTE_EXIT_DURATION - 1) })
      expect(content().dataset.shown).toBe('1')
      expect(content().dataset.phase).toBe('exit')

      act(() => { vi.advanceTimersByTime(1) })
      expect(content().dataset.shown).toBe('2')
      expect(content().dataset.phase).toBe('enter')
      // 退场交给入场接管：上一段的 cleanup 必须取消它，否则两段动画叠在一起。
      expect(made[0].cancel).toHaveBeenCalled()
      // 入场与换路由的前进方向是同一组关键帧：从右侧 14px 推入。
      const enter = animate.mock.calls[1][0] as Keyframe[]
      expect(enter[0]).toMatchObject({ opacity: 0, transform: 'translateX(14px)' })
      expect(enter[1]).toMatchObject({ opacity: 1, transform: 'none' })
      // 高度接力：新内容的高度从旧高度连续长过去，页脚不会瞬移。
      expect((animate.mock.calls[2][0] as Keyframe[])[0]).toMatchObject({ height: '240px' })
      expect(content().style.height).toBe('240px')

      act(() => { vi.advanceTimersByTime(ROUTE_ENTER_DURATION) })
      expect(content().dataset.phase).toBe('idle')
    } finally {
      vi.useRealTimers()
    }
  })

  it('连点两次：以最后一次点到的页码为准，只提交一次', () => {
    vi.useFakeTimers()
    try {
      setMatchMedia(false)
      stubHeight(0)
      stubAnimations()
      render(<Harness />)

      fireEvent.click(screen.getByRole('button', { name: '2' }))
      act(() => { vi.advanceTimersByTime(100) })
      fireEvent.click(screen.getByRole('button', { name: '3' }))
      act(() => { vi.advanceTimersByTime(ROUTE_EXIT_DURATION - 1) })
      // 第二次点击重置了退场计时：第一次的 180ms 不能把第 2 页提交上去。
      expect(content().dataset.shown).toBe('1')

      act(() => { vi.advanceTimersByTime(1) })
      expect(content().dataset.shown).toBe('3')
      expect(content().dataset.phase).toBe('enter')
    } finally {
      vi.useRealTimers()
    }
  })

  it('退场途中页码被改回当前页：撤掉退场，不播入场', () => {
    vi.useFakeTimers()
    try {
      setMatchMedia(false)
      stubHeight(240)
      const { animate, made } = stubAnimations()
      render(<Harness />)

      fireEvent.click(screen.getByRole('button', { name: '2' }))
      expect(content().dataset.phase).toBe('exit')
      // 换标签 / 换关键词会把 page 归 1：没有内容要换，白播一段只会像闪了一下。
      fireEvent.click(screen.getByRole('button', { name: '1' }))
      expect(content().dataset.phase).toBe('idle')
      expect(made[0].cancel).toHaveBeenCalled()
      act(() => { vi.advanceTimersByTime(ROUTE_EXIT_DURATION * 2) })
      expect(content().dataset.phase).toBe('idle')
      expect(content().dataset.shown).toBe('1')
      expect(animate).toHaveBeenCalledTimes(1) // 只有那段被撤掉的退场
    } finally {
      vi.useRealTimers()
    }
  })

  it('减少动态效果：不延迟、不播动画，直接换页', () => {
    vi.useFakeTimers()
    try {
      setMatchMedia(true)
      stubHeight(240)
      const { animate } = stubAnimations()
      render(<Harness />)

      fireEvent.click(screen.getByRole('button', { name: '2' }))
      // 没有 exit → enter 的两段，页码立即提交，阶段直接回 idle。
      expect(content().dataset.shown).toBe('2')
      expect(content().dataset.phase).toBe('idle')
      expect(animate).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })
})
