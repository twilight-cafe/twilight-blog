// imageGroup.tsx —— 「一批图一起出现」的同步闸门
//
// 需求：一屏里的图片只有**全部**下载并解码完成之后才一起显示，在那之前整块
// 区域只画加载动画。屏幕上因此不会出现「先到的先亮、晚到的还在转圈」的参差，
// 更不会看到只画了一半的图片。已经在页面上、这一轮没变化的图片不参与等待：
// 闸门是一次性的，开过就不再关，之后才登记进来的图自己到齐就显示。
//
// 用法：把这一批图包进 <ImageGroup>，组内的 ProgressiveImage 与 ContentView
// 注入的正文图会在挂载时各登记一次等待，拿到结果（load + decode 完成，或
// error）时销账；组里最后一个销账时整组一起开闸。
//
// 三条容易踩的边界：
//   ① **一次性**：open 只会从 false 变 true。翻页 / 切换文章时用 key 换一个
//      新组（Home 的 key={shownPage}、文章页的 key={article.id}），这样新一批
//      图重新「等齐」；而同一批数据刷新（key 不变）不会让已经在屏幕上的图
//      回到加载态。
//   ② **组内一律 eager 加载**：懒加载的图在视口外根本不会开始下载，组就永远
//      等不到「全部到齐」——这与整组同步的前提直接冲突。见 ProgressiveImage
//      里对 loading 属性的覆盖。
//   ③ **兜底**：等满 IMAGE_GROUP_TIMEOUT 还没齐就开闸。一次永远挂住的请求
//      不该让整页停在骨架屏上；没就绪的图开闸后各自按单张的规则显示。
//
// 图片与闸门之间只有两个动作：登记（track）与销账（release）。销账必须幂等
// 且必须覆盖卸载——一张图在加载途中被卸载（React 重设 innerHTML、翻页）时
// 若不销账，整组就再也开不了。

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'

/**
 * 等满多久还没齐就放行（ms）。取值比「一个慢请求」长得多，正常网络下永远
 * 走不到；它只负责从「某张图永远不回来」里把整页救出来。
 */
export const IMAGE_GROUP_TIMEOUT = 15000

export interface ImageGroupHandle {
  /** 本组是否已经放行（一次性：放过就不再收回）。 */
  open: boolean
  /** 登记一次等待；返回的回调表示「这张已有结果」，幂等。 */
  track: () => () => void
}

const ImageGroupContext = createContext<ImageGroupHandle | null>(null)

/**
 * 取当前的图片组。不在组里时返回 null——此时组件按单张图的规则工作
 * （加载完就显示），不会因为「等别人」而停在骨架上。
 */
export function useImageGroup(): ImageGroupHandle | null {
  return useContext(ImageGroupContext)
}

export function ImageGroup({ children }: { children: ReactNode }) {
  const [waits, setWaits] = useState(0)
  // children 的 layout effect 先于父组件执行：等本组自己的 layout effect 跑完，
  // 这一批图就都登记完了——在那之前「0 张在等」只代表还没登记，不代表到齐。
  const [registered, setRegistered] = useState(false)
  // open 的**锁存**：开过就不再关。用它而不是直接看 waits===0，是为了让
  // 开闸后新登记的图（正文渲染器晚到、追加一条）不把已经显示的图重新盖住。
  const [latched, setLatched] = useState(false)

  const track = useCallback(() => {
    let released = false
    setWaits((count) => count + 1)
    return () => {
      if (released) return
      released = true
      setWaits((count) => Math.max(0, count - 1))
    }
  }, [])

  useLayoutEffect(() => {
    setRegistered(true)
  }, [])

  const open = latched || (registered && waits === 0)

  // 派生值一旦为真就锁存。放在 layout effect 里：多出来的这一次渲染发生在
  // 绘制之前，不会先闪一帧「已经开闸但还按骨架渲染」的画面。
  useLayoutEffect(() => {
    if (open) setLatched(true)
  }, [open])

  useEffect(() => {
    if (latched) return
    const timer = window.setTimeout(() => setLatched(true), IMAGE_GROUP_TIMEOUT)
    return () => window.clearTimeout(timer)
  }, [latched])

  const value = useMemo<ImageGroupHandle>(() => ({ open, track }), [open, track])
  return <ImageGroupContext.Provider value={value}>{children}</ImageGroupContext.Provider>
}
