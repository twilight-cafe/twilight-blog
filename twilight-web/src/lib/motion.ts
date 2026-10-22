// motion.ts —— 全站「iOS 手感」动效的底层实现
//
// 分工：本文件只管「什么时候动、动哪个元素」，曲线与时长尽量留在 app.css 的
// --ease-* / --dur-* 令牌里，避免 JS 与 CSS 各写一套时间轴。必须写在 JS 的只有
// 两处：路由入场的关键帧（WAAPI 不能把 CSS 变量当关键帧值传），以及共享元素
// 放大的位移换算（要读 rect）。
//
// 换路由（useRouteMotion）与分页翻页（usePagerMotion）共用同一段内容区动画
// （useContentMotion）：「翻页的手感就是换页的手感」只靠一份实现才守得住。
//
// 「要不要动」这件事本身（matchMedia 判断、iOS 曲线）由 ink-design 提供（0.1.3 起），
// 见下面的再导出。滚动相关的通用行为同理：阅读进度用 `useScrollProgress`、
// 回到顶部用 `BackTop`、目录高亮用 `useScrollSpy`，站点不再各自维护一份。
//
// 无障碍：系统开启「减少动态效果」时入场动画一律不播；进度条照常更新
// （它表达的是滚动位置，不是装饰动效）。CSS 侧在 app.css 末尾还有一条全局
// 兜底，两者互不替代——JS 跳过的只是 WAAPI 这一条路径。

import { useEffect, useLayoutEffect, useRef, useState, type RefObject } from 'react'
import { useLocation, useNavigationType } from 'react-router-dom'

// 减少动态效果与 iOS 曲线是**库与站点共用的同一份事实**：0.1.3 起 ink-design
// 导出了这两个符号，这里只做转发，不在站点再抄一遍 matchMedia 判断或曲线字面量。
// 保持再导出（而不是让调用方各自去 import 'ink-design'）是为了让
// `../lib/motion` 仍然是「动效基础设施」的唯一入口，调用方不必知道哪个符号
// 来自库、哪个是站点自己的路由动画。
import { IOS_EASE_OUT, prefersReducedMotion } from 'ink-design'

export { IOS_EASE_OUT, prefersReducedMotion }

/**
 * 路由入场的时长（ms）。
 * 比 --dur-slow（520ms）短一档：整页位移同时覆盖的内容更多，慢下来会像卡住。
 */
export const ROUTE_ENTER_DURATION = 420

/**
 * 退场时长（ms）。比入场短一截：老内容只需要「让开」，多留一拍会让每一次
 * 导航都显得迟钝；180ms 足够让眼睛看见它是滑走的，而不是被切掉。
 */
export const ROUTE_EXIT_DURATION = 180

/**
 * 换页时容器高度变化的时长（ms）。
 *
 * 从长文章跳到短列表（或反过来）时，页脚的位置会变。旧页已经不在 DOM 里，
 * 这一跳只能由容器自己补：把高度从旧页量到的值连续拉到新页的高度，页脚
 * 就是「被推过去」而不是「瞬移过去」。
 */
const HEIGHT_DURATION = 380

/**
 * 位移压到 14px：整页换页只需要表达「换了一层」，再多就像幻灯片。
 * 横向而不是纵向，是因为这里要表达**方向**：前进从右侧推入、返回从左侧退回。
 */
const ENTER_KEYFRAMES: Keyframe[] = [
  { opacity: 0, transform: 'translateX(14px)' },
  { opacity: 1, transform: 'none' },
]

/** 返回时的入场：与前进镜像。 */
const BACK_KEYFRAMES: Keyframe[] = [
  { opacity: 0, transform: 'translateX(-14px)' },
  { opacity: 1, transform: 'none' },
]

/** 路由入场 / 退场的三个方向：PUSH / REPLACE 是前进，POP 是返回。 */
export type EnterDirection = 'forward' | 'back'

/**
 * 退场：与入场相反方向滑走并淡出。
 *
 * 老内容往哪边退，新内容就从哪边来——两者镜像才像「一层推走另一层」。
 * fill: forwards 让它在 180ms 的等待期里停在透明状态，而不是播完又弹回可见；
 * 换页提交时这段动画会被 cleanup 取消（见下面入场 effect 的返回值）。
 */
const EXIT_KEYFRAMES: Record<EnterDirection, Keyframe[]> = {
  forward: [
    { opacity: 1, transform: 'none' },
    { opacity: 0, transform: 'translateX(-10px)' },
  ],
  back: [
    { opacity: 1, transform: 'none' },
    { opacity: 0, transform: 'translateX(10px)' },
  ],
}

/** 换页的三个阶段：静止、老内容退场、新内容入场。 */
export type RoutePhase = 'idle' | 'exit' | 'enter'

/**
 * 全站导航的动效与滚动策略（公开站与后台共用）。
 *
 * 三个阶段由 RouteTransition 驱动（见 components/RouteTransition.tsx）：
 *   idle  → exit：旧页面还在 DOM 里，把它滑走并淡出，同时记下它的高度；
 *   exit  → enter：换页提交，新页面挂载；从旧高度连续长/缩到新高度，
 *                  同时从来的方向滑入；
 *   enter → idle：动画收尾。
 *
 * 为什么是「先退场再挂载」而不是同时：新页面与旧页面分属两次路由提交，
 * 同时显示需要渲染两棵路由树（两套壳、两套请求）。串行只多花 180ms，
 * 换来的是老内容真的能退出，而不是被下一次提交直接抹掉。
 *
 * phase 由调用方（壳）从 useRouteTransition() 取来并原样传入，不给默认值：
 * 少传一次就等于整站换页不再有动画，而那种漏传不会报错，只会安静地不动。
 *
 * 方向：PUSH / REPLACE 是新内容从右侧推入，POP 是返回（从左侧退回）。
 * 滚动：向前一律到顶；向后回到离开那条 history 记录时的位置。
 * 位置按 location.key 记而不是 pathname——同一篇文章会被进入两次，
 * history entry 的 key 才是「那一次停留」的身份。
 */
export function useRouteMotion(ref: RefObject<HTMLElement | null>, phase: RoutePhase): void {
  const location = useLocation()
  const navigationType = useNavigationType()
  const direction: EnterDirection = navigationType === 'POP' ? 'back' : 'forward'
  const key = location.key

  const positions = useRef(new Map<string, number>())
  const previousKey = useRef(key)
  useContentMotion(ref, phase, direction)

  // 持续记录当前位置，而不是在「离开的那一刻」读 scrollY：那时新页面已经挂载，
  // 文档可能变短，浏览器会把 scrollY 夹到新高度——记下来的是被压过的值。
  useEffect(() => {
    const record = () => {
      positions.current.set(key, window.scrollY)
    }
    window.addEventListener('scroll', record, { passive: true })
    return () => window.removeEventListener('scroll', record)
  }, [key])

  useLayoutEffect(() => {
    if (previousKey.current === key) return
    previousKey.current = key
    // 没记录过（第一次来、或从没滚动过）就按 0：返回一条没停留过的记录时，
    // 「从头看」总比留在上一页的偏移处更接近预期。
    const target = direction === 'back' ? positions.current.get(key) ?? 0 : 0
    // html 上有 scroll-behavior: smooth（目录锚点要它），但换页要的是**瞬间**
    // 到位：看着新页面平滑滚过一整篇文章的高度，比直接跳过去更像卡了。临时
    // 压成 auto 再还原，不依赖 behavior: 'instant'（老 Safari 不认）。
    const root = document.documentElement
    const behavior = root.style.scrollBehavior
    root.style.scrollBehavior = 'auto'
    window.scrollTo(0, target)
    root.style.scrollBehavior = behavior
  }, [key, direction])
}

/**
 * 换页时的「空间接力」：把某个容器的占位高度从旧页连续拉到新页。
 *
 * 存在理由是公开站的两种页头：首页是整幅大标题（约 300px），内页是精简头
 * （约 120px）。两者不是同一个元素、结构也不同，换页提交时后者直接把前者
 * 替换掉——正文与页脚会被瞬间拽动 180px，那正是「退出引起的布局变化」里最
 * 明显的一处（内容区的位移有 useRouteMotion 处理，页头这一段没有）。
 *
 * 做法与内容区的高度接力同一套：退场时量下旧高度，提交后把新元素钉在旧高度
 * 上再收到新高度。差别只有两点：
 *   ① 动的是页头自己的盒子，所以 **不能**跟着做位移——iOS 换页时导航栏是
 *      不动的，动的只有它下面的内容；
 *   ② 展开方向（120 → 300）不需要额外补空间：盒子比内容矮时 overflow: hidden
 *      会把大标题的下半截暂时裁掉，而缓动曲线起步极快，收放都在一两帧内完成，
 *      读起来就是页头「展开」而不是「被切」。
 *
 * ref 可以先后指向两个不同的元素（两种页头），因为退场与入场是两次提交，
 * 每次测量的都是当时挂在树上的那一个。
 */
export function useHeightRelay(ref: RefObject<HTMLElement | null>, phase: RoutePhase): void {
  const measured = useRef(0)

  useLayoutEffect(() => {
    if (phase !== 'exit') return
    const el = ref.current
    if (!el) return
    // 提交之后旧页头就没了，它的高度只能在退场这一刻量。
    measured.current = el.getBoundingClientRect().height
  }, [phase, ref])

  useLayoutEffect(() => {
    if (phase !== 'enter') return
    const el = ref.current
    if (!el) return
    if (prefersReducedMotion() || typeof el.animate !== 'function') {
      measured.current = 0
      return
    }

    const from = measured.current
    measured.current = 0
    const to = el.getBoundingClientRect().height
    // 两种页头高度相同（内页之间互跳）就什么都不做：给一个 380ms 的
    // overflow: hidden 只会白白多出一个合成层。
    if (from <= 0 || to <= 0 || from === to) return

    el.style.height = `${from}px`
    el.style.overflow = 'hidden'
    const animation = el.animate(
      [{ height: `${from}px` }, { height: `${to}px` }],
      { duration: HEIGHT_DURATION, easing: IOS_EASE_OUT },
    )
    animation.onfinish = () => {
      el.style.height = ''
      el.style.overflow = ''
    }

    return () => {
      animation.onfinish = null
      animation.cancel()
      el.style.height = ''
      el.style.overflow = ''
    }
  }, [phase, ref])
}

/** 一个矩形：共享元素放大只需要这四项（DOMRect 结构上兼容）。 */
export interface ZoomRect {
  left: number
  top: number
  width: number
  height: number
}

function round(value: number, digits: number): number {
  const factor = 10 ** digits
  return Math.round(value * factor) / factor
}

/**
 * 从 `from`（缩略图）长到 `to`（灯箱里那张图）的**起始**变换。
 *
 * 用 scale + translate 而不是改宽高：只走合成层，且灯箱里的图不必为了这段
 * 动画放弃 object-fit。等比缩放（只取宽度比）——非等比会让照片在飞行途中被
 * 拉扁，那比「起点略微对不齐」难看得多；正常结束态是 identity，所以最终
 * 位置与尺寸永远精确。
 *
 * 返回 null 表示这段动画不该播：任何一边量不出尺寸（图片还没解码、元素
 * display:none、jsdom）时，位移只会得到 NaN/Infinity。
 */
export function zoomTransform(from: ZoomRect, to: ZoomRect): string | null {
  if (from.width <= 0 || from.height <= 0 || to.width <= 0 || to.height <= 0) return null
  const scale = from.width / to.width
  const dx = from.left + from.width / 2 - (to.left + to.width / 2)
  const dy = from.top + from.height / 2 - (to.top + to.height / 2)
  return `translate(${round(dx, 2)}px, ${round(dy, 2)}px) scale(${round(scale, 4)})`
}

/**
 * 内容区的「退场 → 入场 → 高度接力」本体。
 *
 * 换路由（useRouteMotion）与翻页（usePagerMotion）共用这一段：两边是同一件
 * 事——一块内容滑走、换成新的、从旧高度长到新高度——区别只在谁驱动 phase。
 * 各写一份的话，两条动画一定会慢慢走偏（少一个位移、差几十毫秒），而
 * 「翻页的手感就是换页的手感」正是这里唯一值得保证的东西。
 */
function useContentMotion(
  ref: RefObject<HTMLElement | null>,
  phase: RoutePhase,
  direction: EnterDirection,
): void {
  const oldHeight = useRef(0)

  // 退场：旧页面还在树上时把它滑走。高度先量下来——换页提交之后旧内容就
  // 没了，那一刻再想量「之前多高」已经来不及。
  useLayoutEffect(() => {
    if (phase !== 'exit') return
    const el = ref.current
    if (!el) return

    oldHeight.current = el.getBoundingClientRect().height
    if (prefersReducedMotion() || typeof el.animate !== 'function') return

    const animation = el.animate(EXIT_KEYFRAMES[direction], {
      duration: ROUTE_EXIT_DURATION,
      easing: IOS_EASE_OUT,
      fill: 'forwards',
    })
    return () => animation.cancel()
  }, [phase, direction, ref])

  // 入场 + 高度过渡。两者在同一个布局 effect 里开始，所以第一帧就是
  // 「旧高度 + 新内容的起始位移」，不会先看到新页面完整地闪一下。
  useLayoutEffect(() => {
    if (phase !== 'enter') return
    const el = ref.current
    if (!el) return
    if (prefersReducedMotion() || typeof el.animate !== 'function') return

    const enter = el.animate(direction === 'back' ? BACK_KEYFRAMES : ENTER_KEYFRAMES, {
      duration: ROUTE_ENTER_DURATION,
      easing: IOS_EASE_OUT,
    })

    const from = oldHeight.current
    oldHeight.current = 0
    const to = el.getBoundingClientRect().height
    // 量不出高度（首帧为 0、display: none、jsdom）时只播位移，不碰高度：
    // 0 → 0 的动画没有意义，还会把 overflow: hidden 留在元素上。
    if (from <= 0 || to <= 0) {
      return () => enter.cancel()
    }

    el.style.height = `${from}px`
    el.style.overflow = 'hidden'
    const height = el.animate(
      [{ height: `${from}px` }, { height: `${to}px` }],
      { duration: HEIGHT_DURATION, easing: IOS_EASE_OUT },
    )
    height.onfinish = () => {
      // 交还高度给内容决定：留在固定 px 上，之后的展开（评论、新数据）会被裁掉。
      el.style.height = ''
      el.style.overflow = ''
    }

    return () => {
      enter.cancel()
      height.onfinish = null
      height.cancel()
      el.style.height = ''
      el.style.overflow = ''
    }
  }, [phase, direction, ref])
}

/**
 * 分页翻页的动效：点一次页码之后的三个阶段，与换路由**完全同一段动画**。
 *
 * 页面把「点到的页码」传进来，拿回「已经切上屏幕的页码」：
 *   page 变化        → exit：旧列表滑走（180ms），页面可以同时去取新数据；
 *   180ms 后         → 提交：回传的 page 换成新页，旧列表从树上撤掉（数据没到
 *                      就上骨架），然后播与换路由同一段入场 + 高度接力；
 *   ROUTE_ENTER_DURATION 后 → idle。
 *
 * 为什么也要等这 180ms 才提交：换路由的这 180ms 是留给旧内容真正退出的，
 * 翻页如果立刻换掉，旧列表就只是「被切掉」，两处手感会明显不同。数据请求
 * 不必跟着等——页面仍然在点到的瞬间就发请求，先到就先存着，提交那一帧直接
 * 上新内容，不会因为动画多等一次往返。
 *
 * 不滚动页面：翻页只换这一块内容，读者还停在分页器附近，把他甩回页首不是
 * 这里该做的事（换路由的「前进回到页首」是一次真正的换页，另当别论）。
 */
export interface PagerMotion {
  /** 已经切上屏幕的页码：渲染要以它为准，而不是点到的那个。 */
  page: number
  /** 当前阶段，与换路由共用 RoutePhase。 */
  phase: RoutePhase
  /** 内容区容器：退场、入场与高度接力都作用在它上面。 */
  ref: RefObject<HTMLDivElement | null>
}

/**
 * 分页翻页的「退场 → 提交 → 入场」，见上面 PagerMotion 的说明。
 *
 * 调用方只需要做两件事：
 *   ① 请求照旧在点到的页码变化时发出，并把结果连同页码一起存；
 *   ② 渲染时只显示「数据所属页码 === 返回值 page」的那一份，其余显示骨架。
 * 第 ② 条是关键：少了它，提交后到数据到达之间会显示上一页的列表，入场动画
 * 就播在了一张注定要被替换的旧图上。
 */
export function usePagerMotion(page: number): PagerMotion {
  const ref = useRef<HTMLDivElement | null>(null)
  const [shown, setShown] = useState(page)
  const [phase, setPhase] = useState<RoutePhase>('idle')

  // 与 RouteTransition 同一写法：渲染期就切到退场，点击后的第一帧旧列表
  // 已经在滑走，不用等一次 effect。
  if (phase !== 'exit' && page !== shown) setPhase('exit')

  useEffect(() => {
    if (phase !== 'exit') return
    // 退场途中页码又被改回当前页（换标签 / 换关键词把 page 归 1）：没有内容
    // 要换，直接收摊——否则会白播一段退场再原样入场，看起来像闪了一下。
    if (page === shown) {
      setPhase('idle')
      return
    }
    // 减少动态效果：不延迟、不冻结，直接换页。
    if (prefersReducedMotion()) {
      setShown(page)
      setPhase('idle')
      return
    }
    const timer = window.setTimeout(() => {
      setShown(page)
      setPhase('enter')
    }, ROUTE_EXIT_DURATION)
    return () => window.clearTimeout(timer)
  }, [phase, page, shown])

  useEffect(() => {
    if (phase !== 'enter') return
    const timer = window.setTimeout(() => setPhase('idle'), ROUTE_ENTER_DURATION)
    return () => window.clearTimeout(timer)
  }, [phase])

  // 方向永远是「前进」：页码只会往后翻，没有返回手势。
  useContentMotion(ref, phase, 'forward')

  return { page: shown, phase, ref }
}
