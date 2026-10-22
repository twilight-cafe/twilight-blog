// RouteTransition.tsx —— 路由切换的「退场 → 换页 → 入场」编排
//
// 为什么需要这一层：React Router 的换页是一次提交，旧页面在新页面挂载的瞬间
// 就被卸载了。只靠页面自己的入场动画，用户看到的是「新内容淡入、旧内容凭空
// 消失」——退场从来没有发生过，而且两次提交之间页面高度会硬跳（长文章 →
// 短列表时页脚瞬移）。
//
// 这里把换页拆成三段：
//   idle → exit：地址已经变了，但交给 <Routes> 的仍是**旧 location**。旧页面
//                 原样留在树上，由各壳用自己的容器播退场动画；
//   exit → enter：180ms 后把 location 换成新的。新页面挂载，壳从旧高度连续
//                 过渡到新高度，同时从来的方向滑入；
//   enter → idle：入场动画结束后恢复静止。
//
// 代价与新页面的数据请求晚 180ms 开始。这是刻意的取舍：两棵树同时挂载（旧
// 页面退场、新页面入场）能省掉这 180ms，但会同时存在两套壳、两套请求与两套
// 滚动位置——对这样一个内容站，不值得。
//
// 减少动态效果：不延迟、不冻结，直接换页（视觉上就是以前的行为）。
//
// 不换页的地址变化：路径与查询串都没变、只是 key 变了（点当前导航项、只换
// #hash）时不同步走这三段——内容一个字都不变，播一遍只会像闪了一下。

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { Routes, useLocation, useNavigationType, type Location } from 'react-router-dom'
import {
  prefersReducedMotion,
  ROUTE_ENTER_DURATION,
  ROUTE_EXIT_DURATION,
  type EnterDirection,
  type RoutePhase,
} from '../lib/motion'

export interface RouteTransitionState {
  phase: RoutePhase
  direction: EnterDirection
}

const RouteTransitionContext = createContext<RouteTransitionState>({
  phase: 'idle',
  direction: 'forward',
})

/** 壳组件（PublicShell / AdminLayout）用它拿到当前阶段，驱动自己那块内容区。 */
export function useRouteTransition(): RouteTransitionState {
  return useContext(RouteTransitionContext)
}

export default function RouteTransition({ children }: { children: ReactNode }) {
  const location = useLocation()
  const navigationType = useNavigationType()
  const direction: EnterDirection = navigationType === 'POP' ? 'back' : 'forward'
  const [display, setDisplay] = useState<Location>(location)
  const [phase, setPhase] = useState<RoutePhase>('idle')

  // 渲染期就切到 exit：点击后的第一帧就是「老内容开始退场」，不用等一次
  // effect。与 Tags.tsx 里「渲染期调整 state」同一写法——React 会立刻重渲染
  // 并丢弃本次输出，不会把 phase=idle 的中间态画到屏幕上。
  //
  // 条件只排除 exit 自己：入场途中又点了一下（连点两次导航）也要立刻改成退场，
  // 否则第二段地址会等第一段入场播完才被处理，看起来像「点空了」。
  //
  // 同一页内的 key 变化（点当前导航项、只换了 #hash）不换页：内容一个字都不会
  // 变，播一遍退场再回来只会像闪了一下。这类变化直接把地址同步过去，滚动位置
  // 仍由 useRouteMotion 按 key 处理。
  const samePage = location.pathname === display.pathname && location.search === display.search
  if (phase !== 'exit' && location.key !== display.key) {
    if (samePage) {
      setDisplay(location)
    } else {
      setPhase('exit')
    }
  }

  useEffect(() => {
    if (phase !== 'exit') return
    // 减少动态效果：不延迟挂载，直接换页。
    if (prefersReducedMotion()) {
      setDisplay(location)
      setPhase('idle')
      return
    }
    const timer = window.setTimeout(() => {
      setDisplay(location)
      setPhase('enter')
    }, ROUTE_EXIT_DURATION)
    return () => window.clearTimeout(timer)
  }, [phase, location])

  useEffect(() => {
    if (phase !== 'enter') return
    const timer = window.setTimeout(() => setPhase('idle'), ROUTE_ENTER_DURATION)
    return () => window.clearTimeout(timer)
  }, [phase])

  return (
    <RouteTransitionContext.Provider value={{ phase, direction }}>
      {/* 关键：交给 Routes 的是**冻结的** location。在换页提交之前，所有
          useLocation / useParams / useSearchParams 读到的仍是旧地址——旧页面
          因此不会在退场途中自己变成新页面。 */}
      <Routes location={display}>{children}</Routes>
    </RouteTransitionContext.Provider>
  )
}
