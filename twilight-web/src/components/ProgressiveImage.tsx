// ProgressiveImage.tsx —— 带占位动画的图片
//
// 图片是页面上最晚到达的东西：接口 200ms 就返回，正文已经排好版，头图 / 缩略图 /
// 头像还要再等一次（图床、弱网、大图）。什么都不画的代价是「一块空白突然变成
// 一张图」：高度一跳，正在读的那一行也跟着移动。这里把等待本身画出来：
//
//   loading → 一层扫光的骨架，图片 opacity 0；
//   loaded  → 骨架淡出、图片淡入（两个 opacity 叠在一起就是交叉溶解）；
//   failed  → 骨架换成斜纹底的 IMG 标记，破图图标不再露出来。
//
// 两个「到齐」的口径（缺一不可）：
//   ① **字节到齐**（load）之后还要 **decode() 完成**：渐进式 JPEG / 大图在
//      load 那一刻仍可能先画出一张半成品（上半有内容、下半空白，或先糊后清），
//      那正是「加载了一半的图片」。decode 之后交 loaded，第一帧就是完整的。
//   ② 在 <ImageGroup> 里时还要等**同组其它图**（见 lib/imageGroup.tsx）：
//      组没开闸，这张就算已经就绪也只画骨架，整组一起出现。
//
// 为什么在 <img> 外面包一层 <span>：<img> 是替换元素，`::before` / `::after`
// 不参与渲染，骨架只能作为兄弟节点存在。这层外框随后也承担了「给骨架定位」
// 与「按容器裁切」两件事（见 app.css 的 .img-frame 一节）。
//
// 缓存命中不能先闪一下骨架：ref 回调在浏览器绘制之前就能读到 `img.complete`，
// 在那里直接判定终态；onLoad / onError 只负责真正异步的那一次。ref 的依赖里
// 带上 src，于是换图（选择器里换一张预览）时回调身份变化，React 会用新节点
// 重新调用一次——一张已经缓存的新图同样不会闪。
//
// `ratio` 只在没有加载完时生效：容器本身已经有尺寸（缩略图方块、卡片头图）时
// 不必传；图片高度由自身决定的地方（文章头图、Markdown 正文图）传一个近似
// 宽高比，让骨架先占住一块地方，图到了再把高度交还给图片。

import { useCallback, useLayoutEffect, useRef, useState, type ImgHTMLAttributes } from 'react'
import { useImageGroup } from '../lib/imageGroup'

type ImageStatus = 'loading' | 'loaded' | 'failed'

export interface ProgressiveImageProps
  extends Omit<ImgHTMLAttributes<HTMLImageElement>, 'src' | 'alt' | 'className'> {
  src: string
  alt?: string
  /** 加载中骨架的宽高比，如 '16 / 9'。容器已定尺寸时不必传。 */
  ratio?: string
  /** 叠加在 <img> 上的类名——保持既有选择器（.avatar / .cover img 等）不变。 */
  className?: string
  /** 外框的附加类名（例如头像的 round）。 */
  frameClassName?: string
}

export default function ProgressiveImage({
  src,
  alt = '',
  ratio,
  className = '',
  frameClassName = '',
  loading,
  ...rest
}: ProgressiveImageProps) {
  const group = useImageGroup()
  // 把 src 一起存进状态，而不是只存 status：src 变化时（同一位置换了另一张图）
  // 必须在**渲染期**回到骨架态，否则新图会先以旧图的 loaded 态露一帧空白。
  // 这是 React 官方推荐的「渲染期 adjust state」写法：本次输出被丢弃、立刻重渲染。
  const [state, setState] = useState<{ src: string; status: ImageStatus }>({ src, status: 'loading' })
  if (state.src !== src) setState({ src, status: 'loading' })
  const status = state.status

  // 只认当前 src 的结果：换图之后旧请求的 load / decode 才落地，不能把新图
  // 标成已加载。
  const settle = useCallback((next: ImageStatus) => {
    setState((prev) => (prev.src === src && prev.status !== next ? { src, status: next } : prev))
  }, [src])

  const reveal = useCallback((node: HTMLImageElement) => {
    // 没有 decode 的环境（jsdom、很老的浏览器）退化成原来的 load 即显示：
    // 这类环境没有「解码一半」可观察，硬等只会让图永远不出现。
    const decoded = typeof node.decode === 'function' ? node.decode() : null
    if (!decoded) {
      // 没有 decode 的环境（jsdom、很老的浏览器）退化成 load 即显示：这类环境
      // 没有「解码一半」可观察，硬等只会让图永远不出现。坏图由调用方先按
      // naturalWidth 拦下（见下面的 attach）。
      settle('loaded')
      return
    }
    // decode 失败说明字节在、但解不出来（坏图）：按失败处理，露出斜纹占位，
    // 而不是让浏览器画一个破图图标。
    decoded.then(() => settle('loaded'), () => settle('failed'))
  }, [settle])

  const attach = useCallback((node: HTMLImageElement | null) => {
    // 组件卸载时 ref 会以 null 回调一次，那不是「图取到了」。
    if (!node || !node.complete) return
    if (node.naturalWidth === 0) {
      settle('failed')
      return
    }
    reveal(node)
  }, [reveal, settle])

  // 组内登记：换图（src 变化）要重新等一次，卸载时销账——漏了销账整组就
  // 永远开不了闸。track 的身份只随 Provider 变，所以组开闸不会重新登记。
  const track = group?.track
  const release = useRef<(() => void) | null>(null)
  useLayoutEffect(() => {
    if (!track) return
    const done = track()
    release.current = done
    return () => {
      release.current = null
      done()
    }
  }, [track, src])

  useLayoutEffect(() => {
    if (status !== 'loading') release.current?.()
  }, [status])

  // 组没开闸时，无论这张到没到都按骨架渲染：一批图要一起出现。
  const shown: ImageStatus = group && !group.open ? 'loading' : status

  return (
    <span
      className={`img-frame ${shown}${frameClassName ? ` ${frameClassName}` : ''}`}
      style={shown === 'loaded' ? undefined : { aspectRatio: ratio }}
    >
      <span className="img-skeleton" aria-hidden="true" />
      <img
        {...rest}
        ref={attach}
        className={className}
        src={src}
        alt={alt}
        // 组内强制 eager：懒加载的图在视口外根本不开始下载，整组就永远等不齐
        // （理由见 lib/imageGroup.tsx 的文件头）。
        loading={group ? 'eager' : loading}
        onLoad={(event) => reveal(event.currentTarget)}
        onError={() => settle('failed')}
      />
    </span>
  )
}
