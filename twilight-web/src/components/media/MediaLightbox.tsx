// MediaLightbox.tsx —— 媒体灯箱（全屏预览 + 左右切换 + 手势 + 共享元素）
//
// 通过 createPortal 挂到 document.body，打开时锁住 body 滚动并在关闭时还原
// 原先的 overflow；Esc 关闭、点背景关闭（舞台区域 stopPropagation 阻止误关）。
// index 为 null 或越界时表示关闭。
//
// 「现在看第几张」由灯箱自己持有：props.index 只是**打开请求**（点哪张缩略图就从
// 哪张开始），打开之后的左右切换不再回调调用方——否则 MediaGallery 与 MediaPicker
// 要各自再实现一份 prev/next，两边迟早漂移。props.index 真的变了（含关闭时的
// null）才把内部下标对齐回请求值；这一步写在**渲染期**而不是 useEffect 里：
// effect 会先按旧下标渲染一帧，表现为「点第 3 张却先闪一下第 1 张」。
//
// 四种换图方式共用同一套边界（第一张没有「上一张」，最后一张没有「下一张」）：
// ① 左右按钮；② 键盘 ← / →；③ 在图上横向拖动（见 beginGesture / moveGesture /
// endGesture）；④ 缩略图上的「2 / 5」计数只负责说明位置，不参与交互。
// 切换动画是一条等宽幻灯片组成的轨道整体平移（transform + transition，见
// app.css 的 .media-lightbox-track）：当前张滑出、下一张滑入。
//
// 手势用 Pointer Events 而不是 touch + mouse 两套：一套代码同时覆盖手指、触控笔
// 与鼠标，pointer capture 还能让手指划出图片区域之后继续收到 move/up。纵向只认
// **向下**，超过阈值就关闭（iOS 照片的下拉返回）；向上不做任何事——这里没有
// 「下拉刷新」的语义，跟着手指走反而会让人以为还有别的功能。
//
// 共享元素：origin 是调用方点开灯箱时那一块缩略图的 rect。打开时当前图从它
// 「长」到最终位置，关闭时（没换过图的话）再缩回去——iOS 照片里从网格进全屏
// 就是这一下。换过图就没有「来时的缩略图」了，此时关闭直接淡出：把这张塞回
// 第一张的位置，那一下会被读成「关闭时跳了一张」。系统要求减少动态效果时
// 整段跳过（包括退场计时器）。
//
// 轨道里挂的是**当前张与左右相邻的两张**：拖动时下一张已经在轨道上（缩略图用的
// 是同一个 url，浏览器已解码，拖过去不会看到空白），又不会一开灯箱就把整本图册
// 连视频一起下载。视频只有当前张会 autoPlay，相邻张只取元数据。

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from 'react'
import { createPortal } from 'react-dom'
import type { MediaItem } from 'twilight-blog-api'
import { IOS_EASE_OUT, prefersReducedMotion, zoomTransform, type ZoomRect } from '../../lib/motion'
import { useI18n } from '../../lib/i18n'

/** 超过这个位移才认为「这是一次拖动」，而不是点击时手指的抖动（px）。 */
const DRAG_SLOP = 8

/** 松手时横向位移超过它才换图（px）；再小就弹回原处。 */
const SWITCH_THRESHOLD = 60

/** 下拉超过它就关闭（px）。 */
const DISMISS_THRESHOLD = 120

/** 到边界后继续拖的阻尼：0.35 是「有反馈、但看得出那头没有了」的手感。 */
const EDGE_RESISTANCE = 0.35

/** 下拉关闭的退场时长（ms），必须与 app.css 的 .media-lightbox.closing 对齐。 */
const EXIT_DURATION = 260

/**
 * 共享元素放大（缩略图 → 全屏）的时长，与 app.css 的 lb-pop（380ms）同一档：
 * 两段动作在同一个瞬间发生，时长差得多了会像是各走各的。
 */
const ZOOM_IN_DURATION = 380

/**
 * 缩回缩略图的时长（ms）。比放大快一档：关闭要「立刻」，等 380ms 才消失
 * 会让人觉得灯箱卡了一下。CSS 里 .media-lightbox.zoom-out 的淡出与之对齐。
 */
const ZOOM_EXIT_DURATION = 240

function isVideoItem(item: MediaItem): boolean {
  return item.mime_type.startsWith('video/') || String(item.media_type) === 'video'
}

/**
 * 灯箱里的一张图：下载并解码完成之前只画骨架。
 *
 * 不做 .img-frame 那层 overflow: hidden 外框——共享元素缩回缩略图时图片要飞出
 * 舞台，裁掉就看不见了；骨架改成绝对定位盖在整张幻灯片上（尺寸未知时也能占
 * 住一块地方）。
 *
 * `decode()` 之后才显示：与 ProgressiveImage 同一口径，避免渐进式 JPEG 在
 * 第一帧画出半成品。解码失败也照常显示——字节已经完整，这里没有第二套兜底。
 */
function LightboxImage({
  entry,
  active,
  zoomImg,
  onReady,
}: {
  entry: MediaItem
  active: boolean
  zoomImg: RefObject<HTMLImageElement | null>
  onReady: (id: number) => void
}) {
  const reveal = useCallback((node: HTMLImageElement) => {
    const show = () => onReady(entry.id)
    const decoded = typeof node.decode === 'function' ? node.decode() : null
    if (!decoded) {
      show()
      return
    }
    decoded.then(show, show)
  }, [entry.id, onReady])

  const attach = useCallback(
    (node: HTMLImageElement | null) => {
      if (!node) return
      // 只有当前张挂到父级的 ref 上：共享元素放大 / 缩回量的是「眼前这张」。
      if (active) zoomImg.current = node
      if (node.complete && node.naturalWidth > 0) reveal(node)
    },
    [active, reveal, zoomImg],
  )

  return (
    <>
      <span className="img-skeleton" aria-hidden="true" />
      <img ref={attach} src={entry.url} alt={entry.original_name} draggable={false}
        onLoad={(event) => reveal(event.currentTarget)} />
    </>
  )
}

/** 到边界之后继续拖，位移打三折；否则 1:1 跟手。 */
function resist(dx: number, at: number, count: number): number {
  const atEdge = dx > 0 ? at === 0 : at === count - 1
  return atEdge ? dx * EDGE_RESISTANCE : dx
}

interface DragState {
  /** 横向位移（已按边界阻尼）。 */
  dx: number
  /** 纵向位移，只可能是正数（向上没有语义）。 */
  dy: number
  /** 整次手势锁定的方向。 */
  axis: 'x' | 'y'
}

interface MediaLightboxProps {
  items: MediaItem[]
  /** 打开请求：null / 越界 = 关闭；打开之后当前下标由灯箱内部维护。 */
  index: number | null
  /**
   * 打开它的那块缩略图的 rect（共享元素放大的起点）。可选：没有来源（例如
   * 直接由路由打开）时退化成原来的淡入 / 淡出。
   */
  origin?: ZoomRect | null
  onClose: () => void
}

export default function MediaLightbox({ items, index, origin, onClose }: MediaLightboxProps) {
  const { t } = useI18n()
  const [current, setCurrent] = useState(index)
  const [openedAt, setOpenedAt] = useState(index)
  const [drag, setDrag] = useState<DragState | null>(null)
  // 退场方式：下拉是往下沉，共享元素是缩回缩略图。两种都要等一段动画再卸载，
  // 但 CSS 类与时长不同，所以不能再用一个 boolean。
  const [closing, setClosing] = useState<'dismiss' | 'zoom' | null>(null)
  // 正在做共享元素放大：这段时间要关掉舞台自己的入场动画（见下面的 effect）。
  const [zooming, setZooming] = useState(false)
  // 已经下载并解码完成的图。按 id 记而不是塞进 LightboxImage 的局部 state：
  // 幻灯片盒子必须始终存在（轨道的位移按子元素个数算），图片的终态要能跨
  // 左右切换保留下来。
  const [readyIds, setReadyIds] = useState<ReadonlySet<number>>(() => new Set())
  const markReady = useCallback((id: number) => {
    setReadyIds((previous) => (previous.has(id) ? previous : new Set(previous).add(id)))
  }, [])

  // 手势起点与已锁定的方向只活在一次手势里：放 ref 而不是 state，pointermove
  // 记录起点时就不会额外触发一次渲染。
  const gesture = useRef<{ x: number; y: number; axis: 'x' | 'y' | null } | null>(null)
  // 退场计时器：外部先关掉灯箱时必须能取消，否则 onClose 会在卸载后再响一次
  // （父组件里是一次多余的状态写入）。
  const exitTimer = useRef<number | undefined>(undefined)
  // 当前张的图片元素：共享元素放大 / 缩回都要直接量它的 rect。
  const zoomImg = useRef<HTMLImageElement | null>(null)

  // 渲染期同步 props → state：只在「打开请求」真的变了时对齐，父组件的无关重渲染
  // 不会把用户滑到的第 5 张打回第 1 张。（React 官方推荐的 props 派生 state 写法。）
  if (openedAt !== index) {
    setOpenedAt(index)
    setCurrent(index)
    // 一次新的打开请求意味着上一轮退场已经结束，状态必须复位，否则再次打开
    // 会顶着 zoom-out / closing 的类名进来（背景是透明的、控件是隐形的）。
    setClosing(null)
  }

  const at = current !== null && current >= 0 && current < items.length ? current : null

  /**
   * 请求关闭：能缩回缩略图就缩回，否则（换过图 / 没有来源 / 系统要求减少
   * 动态效果 / 浏览器没有 Web Animations）立刻关闭。
   */
  const requestClose = useCallback(() => {
    // 已经在退场里了：再点一次（双击关闭按钮、Esc 连按）不能排第二个计时器。
    if (closing) return
    const img = zoomImg.current
    if (
      !origin || !img || at !== openedAt
      || prefersReducedMotion() || typeof img.animate !== 'function'
    ) {
      onClose()
      return
    }
    const transform = zoomTransform(origin, img.getBoundingClientRect())
    if (!transform) {
      onClose()
      return
    }
    setClosing('zoom')
    // fill: forwards —— 灯箱到点就卸载，这段动画没有机会自然结束；
    // 不留住最终帧的话，图片会在卸载前弹回全屏尺寸，看起来像闪了一下。
    img.animate([{ transform: 'none' }, { transform }], {
      duration: ZOOM_EXIT_DURATION,
      easing: IOS_EASE_OUT,
      fill: 'forwards',
    })
    exitTimer.current = window.setTimeout(onClose, ZOOM_EXIT_DURATION)
  }, [at, closing, onClose, openedAt, origin])

  // 打开时从缩略图长到最终位置。放在 layout effect 里而不是 CSS：起点要读 rect，
  // 而 rect 只有元素挂上去之后才量得到。fill 保持 none —— 结束就交还给布局。
  useLayoutEffect(() => {
    const img = zoomImg.current
    if (!origin || !img || prefersReducedMotion() || typeof img.animate !== 'function') return
    const transform = zoomTransform(origin, img.getBoundingClientRect())
    if (!transform) return
    // 舞台上原本的 lb-pop 也会淡入：照片正从缩略图飞过来，再让它自己淡入一次
    // 就成了「先隐形、后出现」，把这段共享元素读没了。类名在布局阶段就生效，
    // 所以不会出现「播了一帧才关掉」的闪动。
    setZooming(true)
    const animation = img.animate([{ transform }, { transform: 'none' }], {
      duration: ZOOM_IN_DURATION,
      easing: IOS_EASE_OUT,
    })
    return () => {
      animation.cancel()
      setZooming(false)
    }
  }, [origin, openedAt])

  useEffect(() => {
    if (at === null) return

    const handleKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') requestClose()
      // 与按钮同一套边界：没有上一张/下一张时按了也不动。
      if (event.key === 'ArrowLeft' && at > 0) setCurrent(at - 1)
      if (event.key === 'ArrowRight' && at < items.length - 1) setCurrent(at + 1)
    }
    const previousOverflow = document.body.style.overflow

    window.addEventListener('keydown', handleKey)
    document.body.style.overflow = 'hidden'

    return function (): void {
      window.removeEventListener('keydown', handleKey)
      document.body.style.overflow = previousOverflow
    }
  }, [at, items.length, requestClose])

  // 卸载时清掉未触发的退场计时器（包括父组件自己先关掉的那种情况）。
  useEffect(() => () => window.clearTimeout(exitTimer.current), [])

  if (at === null) return null

  const beginGesture = (event: ReactPointerEvent<HTMLDivElement>) => {
    // 右键/中键不是拖拽。
    if (event.button !== 0) return
    // 按钮与视频控制条自己要用指针事件：从它们身上起手不算拖拽，否则
    // 「点下一张」会先被当成一次 0px 的拖动。
    if ((event.target as Element).closest('button, video')) return
    gesture.current = { x: event.clientX, y: event.clientY, axis: null }
    // 指针捕获：手指/鼠标划出视口之后，move/up 仍然送回这个元素。
    if (typeof event.currentTarget.setPointerCapture === 'function') {
      event.currentTarget.setPointerCapture(event.pointerId)
    }
  }

  const moveGesture = (event: ReactPointerEvent<HTMLDivElement>) => {
    const start = gesture.current
    if (!start) return
    const dx = event.clientX - start.x
    const dy = event.clientY - start.y
    if (!start.axis) {
      // 还没过阈值：可能是一次点击，也可能只是手指抖了一下。
      if (Math.abs(dx) < DRAG_SLOP && Math.abs(dy) < DRAG_SLOP) return
      // 先动得更多的那个轴说了算，整次手势都锁在它上面——否则斜着拖会在
      // 「换图」与「关闭」之间来回跳。
      start.axis = Math.abs(dx) >= Math.abs(dy) ? 'x' : 'y'
    }
    if (start.axis === 'x') {
      setDrag({ dx: resist(dx, at, items.length), dy: 0, axis: 'x' })
    } else {
      // 向上没有语义，直接按 0 处理（手指往上划，图片纹丝不动）。
      setDrag({ dx: 0, dy: Math.max(0, dy), axis: 'y' })
    }
  }

  const endGesture = (event: ReactPointerEvent<HTMLDivElement>) => {
    const start = gesture.current
    gesture.current = null
    // 没有起手的 up（例如从按钮上抬起）什么都不做。
    if (!start) return
    const dx = event.clientX - start.x
    const dy = event.clientY - start.y
    setDrag(null)
    if (start.axis === 'x' && Math.abs(dx) > SWITCH_THRESHOLD) {
      // 往左拖 = 看下一张；到边界时 next 会越界，直接丢弃（与按钮一致）。
      const next = dx < 0 ? at + 1 : at - 1
      if (next >= 0 && next < items.length) setCurrent(next)
    } else if (start.axis === 'y' && dy > DISMISS_THRESHOLD) {
      // 先播完退场再真正卸载：松手就消失会让这次手势看起来「没被接住」。
      // 开了「减少动态效果」就不等：CSS 已把动画压到 0.001ms，再等 260ms
      // 只会留下一层点不动的透明遮罩。
      setClosing('dismiss')
      exitTimer.current = window.setTimeout(onClose, prefersReducedMotion() ? 0 : EXIT_DURATION)
    }
  }

  const cancelGesture = () => {
    // 系统抢走了指针（来电、手势返回等）：回到原位，不做任何切换。
    gesture.current = null
    setDrag(null)
  }

  const item = items[at]
  const video = isVideoItem(item)
  // 位移写 `at * -100` 而不是 `-${at * 100}`：后者在第 1 张会得到 "-0%"，
  // 虽然浏览器认，但读起来像笔误，测试断言也得跟着将就。
  const percent = at * -100

  // 纵向拖动时把位移交给 CSS 变量：舞台平移 + 背景按拖动比例变淡。
  // 0.35 是背景的最低不透明度，再淡就看不见「这是同一个灯箱」了。
  const shellStyle = drag?.axis === 'y'
    ? ({
      '--lb-shift': `${drag.dy}px`,
      '--lb-fade': `${Math.max(0.35, 1 - drag.dy / 600)}`,
    } as CSSProperties)
    : undefined

  // 拖动中把手指位移加到「当前张」的百分比位移上；静止时回到纯百分比，
  // 这样 transition 才能把「松手弹回 / 换到下一张」都做成一段动画。
  const trackStyle = drag?.axis === 'x'
    ? { transform: `translateX(calc(${percent}% + ${drag.dx}px))` }
    : { transform: `translateX(${percent}%)` }

  return createPortal(
    <div
      className={`media-lightbox${drag ? ' dragging' : ''}${closing === 'dismiss' ? ' closing' : ''}${closing === 'zoom' ? ' zoom-out' : ''}`}
      role="presentation"
      style={shellStyle}
      onClick={requestClose}
    >
      <button
        type="button"
        className="media-lightbox-close"
        aria-label={t('关闭', 'Close')}
        onClick={(event) => {
          event.stopPropagation()
          requestClose()
        }}
      >
        ×
      </button>
      <div
        className={`media-lightbox-stage${zooming ? ' zooming' : ''}`}
        onClick={(event) => event.stopPropagation()}
      >
        <div
          className="media-lightbox-viewport"
          onPointerDown={beginGesture}
          onPointerMove={moveGesture}
          onPointerUp={endGesture}
          onPointerCancel={cancelGesture}
        >
          <div className="media-lightbox-track" style={trackStyle}>
            {items.map((entry, position) => (
              // 每张都有一格：轨道的位移按 `at * -100%` 算，少一个子元素当前位置
              // 就整体错位。内容只挂当前张与相邻两张（缩略图与灯箱用的是同一个
              // url，相邻张已经解码过，拖过去不会空白）。
              <div className="media-lightbox-slide" key={entry.id} data-loaded={readyIds.has(entry.id)}>
                {Math.abs(position - at) > 1 ? null : isVideoItem(entry) ? (
                  <video
                    src={entry.url}
                    controls={position === at}
                    autoPlay={position === at}
                    preload={position === at ? 'auto' : 'metadata'}
                    playsInline
                  />
                ) : (
                  <LightboxImage
                    entry={entry}
                    active={position === at}
                    zoomImg={zoomImg}
                    onReady={markReady}
                  />
                )}
              </div>
            ))}
          </div>
          {at > 0 ? (
            <button
              type="button"
              className="media-lightbox-nav prev"
              aria-label={t('上一张', 'Previous')}
              onClick={() => setCurrent(at - 1)}
            >
              ‹
            </button>
          ) : null}
          {at < items.length - 1 ? (
            <button
              type="button"
              className="media-lightbox-nav next"
              aria-label={t('下一张', 'Next')}
              onClick={() => setCurrent(at + 1)}
            >
              ›
            </button>
          ) : null}
        </div>
        <div className="media-lightbox-meta">
          <span>{item.original_name}</span>
          {/* 单张图不显示计数：没有「第几张」可言，写个 1 / 1 只是噪音。 */}
          {items.length > 1 ? (
            // key 让计数在换图时重新挂载，CSS 的入场动画才会重播。
            <span className="media-lightbox-count" key={at}>{`${at + 1} / ${items.length}`}</span>
          ) : null}
          <span className="mono">{video ? 'VIDEO' : 'IMAGE'}</span>
        </div>
      </div>
    </div>,
    document.body,
  )
}
