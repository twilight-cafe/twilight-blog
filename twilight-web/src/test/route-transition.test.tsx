// route-transition.test.tsx —— 换页的三段编排（RouteTransition.tsx）
//
// 这里守的是五件事：
//   ① 点击后**第一帧**就是 exit，且交给 <Routes> 的仍是旧 location——旧页面
//      因此留在台上；它不会在退场途中自己变成新页面；
//   ② exit 到点才提交新地址，随后进入 enter，最后回到 idle；
//   ③ 退场途中又点一次（连点导航）：最终落在**最新**那条地址上；
//      入场途中又点一次：立刻改判为退场，不用等入场播完；
//   ④ 减少动态效果时不延迟、不冻结，直接换页；方向 POP 是 back；
//   ⑤ 卸载时计时器必须清干净，不能在已卸载的树上 setState。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { Link, MemoryRouter, Outlet, Route, useLocation, useNavigate, useNavigationType } from 'react-router-dom'
import RouteTransition, { useRouteTransition } from '../components/RouteTransition'
import { ROUTE_ENTER_DURATION, ROUTE_EXIT_DURATION, type RoutePhase } from '../lib/motion'

/**
 * 壳的替身：把当前阶段、方向、以及 <Routes> 实际拿到的地址都画成 data 属性，
 * 这样「编排走到哪一步」可以从 DOM 上直接读。
 */
function Probe() {
  const { phase, direction } = useRouteTransition()
  const location = useLocation()
  const navigationType = useNavigationType()
  return (
    <div
      data-testid="probe"
      data-phase={phase}
      data-direction={direction}
      data-path={location.pathname}
      data-hash={location.hash}
      data-navigation={navigationType}
    >
      <Outlet />
    </div>
  )
}

function Tree() {
  return (
    <RouteTransition>
      <Route element={<Probe />}>
        <Route path="/a" element={<div data-testid="page">A</div>} />
        <Route path="/b" element={<div data-testid="page">B</div>} />
        <Route path="/c" element={<div data-testid="page">C</div>} />
      </Route>
    </RouteTransition>
  )
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Tree />
      <Link to="/b">到 B</Link>
      <Link to="/c">到 C</Link>
    </MemoryRouter>,
  )
}

function state() {
  const probe = screen.getByTestId('probe')
  return {
    phase: probe.dataset.phase as RoutePhase,
    direction: probe.dataset.direction,
    path: probe.dataset.path,
    hash: probe.dataset.hash,
    navigation: probe.dataset.navigation,
  }
}

function pageName(): string | null {
  return screen.queryByTestId('page')?.textContent ?? null
}

/** 让定时器走完，并让 React 提交随之而来的重渲染。 */
function advance(ms: number): void {
  act(() => {
    vi.advanceTimersByTime(ms)
  })
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('RouteTransition', () => {
  it('空闲态停在首屏地址上，没有任何计时器', () => {
    renderAt('/a')
    // 首屏那条 history 记录是 POP——按返回方向记，但阶段是 idle，所以不播
    // 任何东西；第一次推入之后才是前进方向。
    expect(state()).toEqual({ phase: 'idle', direction: 'back', path: '/a', hash: '', navigation: 'POP' })
    expect(pageName()).toBe('A')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('点击后立刻退场（地址与内容仍是旧的），到点提交新页并进入入场，最后回到静止', () => {
    renderAt('/a')

    fireEvent.click(screen.getByText('到 B'))
    // 第一帧：阶段已经是 exit，但 Routes 拿到的还是 /a —— 旧页面还在台上，
    // 由各壳播退场动画。这正是「先退场再挂载」的全部意义。
    expect(state()).toMatchObject({ phase: 'exit', path: '/a' })
    expect(pageName()).toBe('A')

    // 差一毫秒都不提交：退场动画要播满。
    advance(ROUTE_EXIT_DURATION - 1)
    expect(state()).toMatchObject({ phase: 'exit', path: '/a' })

    advance(1)
    expect(state()).toMatchObject({ phase: 'enter', path: '/b' })
    expect(pageName()).toBe('B')
    expect(state().direction).toBe('forward')

    // 入场还没播完就仍然是 enter；播完才回 idle。
    advance(ROUTE_ENTER_DURATION - 1)
    expect(state().phase).toBe('enter')
    advance(1)
    expect(state().phase).toBe('idle')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('退场途中再点一次：落在最新地址上，而不是中间那次', () => {
    renderAt('/a')

    fireEvent.click(screen.getByText('到 B'))
    advance(ROUTE_EXIT_DURATION - 40)
    // 第二次点击把退场计时器重置：旧页面还在，地址仍未提交。
    fireEvent.click(screen.getByText('到 C'))
    expect(state()).toMatchObject({ phase: 'exit', path: '/a' })

    advance(ROUTE_EXIT_DURATION)
    expect(state()).toMatchObject({ phase: 'enter', path: '/c' })
    expect(pageName()).toBe('C')
  })

  it('入场途中再点一次：立刻改判为退场，不用等入场播完', () => {
    renderAt('/a')

    fireEvent.click(screen.getByText('到 B'))
    advance(ROUTE_EXIT_DURATION)
    expect(state()).toMatchObject({ phase: 'enter', path: '/b' })

    fireEvent.click(screen.getByText('到 C'))
    // 已经提交的 /b 是「现在正在台上的内容」，它得先退场。
    expect(state()).toMatchObject({ phase: 'exit', path: '/b' })
    expect(pageName()).toBe('B')

    advance(ROUTE_EXIT_DURATION)
    expect(state()).toMatchObject({ phase: 'enter', path: '/c' })
    expect(pageName()).toBe('C')
  })

  it('返回（POP）时方向是 back，供壳把老内容向右退、新内容从左回来', () => {
    function Back() {
      const navigate = useNavigate()
      return <button type="button" onClick={() => navigate(-1)}>后退</button>
    }

    // 两条记录、停在第 2 条：后退即 POP，而不是一次新的 PUSH。
    render(
      <MemoryRouter initialEntries={['/a', '/b']} initialIndex={1}>
        <Tree />
        <Back />
      </MemoryRouter>,
    )

    fireEvent.click(screen.getByText('后退'))
    expect(state()).toMatchObject({ phase: 'exit', path: '/b' })
    advance(ROUTE_EXIT_DURATION)
    expect(state()).toMatchObject({ phase: 'enter', path: '/a', direction: 'back' })
    expect(pageName()).toBe('A')
  })

  it('减少动态效果时不延迟、不冻结：直接换页', () => {
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: true,
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
    renderAt('/a')

    fireEvent.click(screen.getByText('到 B'))
    // 没有 180ms 的等待：这一帧就已经在新地址上，阶段也直接回 idle。
    expect(state()).toMatchObject({ phase: 'idle', path: '/b' })
    expect(pageName()).toBe('B')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('同一页内只换 key（点当前导航项 / 只换 hash）不换页：地址同步，但没有动画', () => {
    render(
      <MemoryRouter initialEntries={['/a']}>
        <Tree />
        <Link to="/a#top">回本页顶部</Link>
      </MemoryRouter>,
    )

    fireEvent.click(screen.getByText('回本页顶部'))
    // 内容一个字都没变：阶段仍是 idle，也没有任何计时器在跑。
    expect(state()).toMatchObject({ phase: 'idle', path: '/a', hash: '#top' })
    expect(pageName()).toBe('A')
    expect(vi.getTimerCount()).toBe(0)

    // 地址真的同步了（否则 hash / 查询串会永远停在旧值上）。
    advance(ROUTE_ENTER_DURATION + ROUTE_EXIT_DURATION)
    expect(state()).toMatchObject({ phase: 'idle', hash: '#top' })
  })

  it('卸载时清掉还没到点的计时器', () => {
    const { unmount } = renderAt('/a')
    fireEvent.click(screen.getByText('到 B'))
    expect(state().phase).toBe('exit')
    // 正好一个：退场那 180ms。它必须在卸载时被清掉，否则回调会在已经
    // 卸载的树上 setState。
    expect(vi.getTimerCount()).toBe(1)

    unmount()
    expect(vi.getTimerCount()).toBe(0)
    expect(() => advance(ROUTE_EXIT_DURATION * 4)).not.toThrow()
  })
})
