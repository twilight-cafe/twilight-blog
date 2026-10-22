// loading.ts —— 加载指示器的「延迟出现」
//
// 为什么需要它：接口通常在 20–80ms 内返回（本地、keep-alive、缓存命中），而
// 骨架/LOADING 在第 0 帧就画出来、几十毫秒后又消失——每一次点击、翻页、切标签
// 都会闪一下灰条。那种闪烁比「什么都没有」更像故障：它让每个操作都显得卡过。
//
// 规则只有一条：**加载超过阈值才显示等待动画**。阈值以内的请求，用户看到的是
// 「内容直接出现」，而不是「骨架闪了一下」。
//
// 为什么是 200ms：低于 100ms 的反馈会被感知成「即时」，没必要画等待；而超过
// 300ms 的静默会让人怀疑点击没生效。200ms 正好在两者之间——快请求完全静默，
// 慢请求的等待从「将要卡住」的那一刻开始画。
//
// 骨架的**占位**不延迟（见 components/common.tsx 的 data-visible）：骨架始终
// 挂在树上占住内容将要出现的位置，只是不可见。否则「先空白、200ms 后骨架」
// 本身又是一次布局跳动。
//
// 为什么不做「最短显示时长」：那会让一个 210ms 就返回的请求把内容压到 500ms
// 才出现——用真实的等待换取「闪得没那么明显」，是把快页面变慢。这里宁可让
// 阈值附近的极少数请求闪一帧，也不拖慢绝大多数请求。

import { useEffect, useState } from 'react'

/** 加载指示器的出现阈值（ms）。全站共用一个值，改这里就全站生效。 */
export const LOADING_DELAY = 200

/**
 * 加载是否已经久到该显示等待动画。
 *
 * 返回 true 的条件是「loading 连续为真超过 delay」。中途 loading 变假会把
 * 定时器取消掉，所以快速请求（包括两次 state 更新之间只隔一帧的那种）永远
 * 不会让指示器亮起来。
 */
export function useDelayedLoading(loading: boolean, delay: number = LOADING_DELAY): boolean {
  const [visible, setVisible] = useState(false)

  useEffect(() => {
    if (!loading) {
      // 请求结束就立刻收起：收起不需要延迟，否则会在内容到达之后继续闪一格。
      // （可见的骨架此时已被内容替换，这里管的是 data-busy 这类「还在忙」的标记。）
      setVisible(false)
      return
    }
    const timer = window.setTimeout(() => setVisible(true), delay)
    return () => window.clearTimeout(timer)
  }, [loading, delay])

  return visible
}
