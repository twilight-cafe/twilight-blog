// MediaThumb.tsx —— 单个媒体缩略图
// 用 item.url 直接展示（前端不区分缩略图与原图，url 是后端给的那个地址）；
// 视频用 preload="metadata" 只取首帧、避免整段下载。onRemove 可选：传了才显示
// 删除按钮（选择器里会传，纯展示场景不传）。点击主体触发 onOpen（通常打开灯箱）。
//
// 图片走 ProgressiveImage：72px 的小方块没有尺寸问题，但「空白 → 图片」那一下
// 仍然是最显眼的跳变，骨架把等待画出来。视频的等待更长（元数据 → 首帧），而
// video 在首帧到达前是一片纯黑——单独盖一层同样的骨架，loadeddata 后淡出；
// 播放三角在骨架之上，等待期间就能看出「这是一段视频」。

import { useState, type MouseEvent as ReactMouseEvent } from 'react'
import type { MediaItem } from 'twilight-blog-api'
import ProgressiveImage from '../ProgressiveImage'
import { useI18n } from '../../lib/i18n'

function isVideoItem(item: MediaItem): boolean {
  return item.mime_type.startsWith('video/') || String(item.media_type) === 'video'
}

interface MediaThumbProps {
  item: MediaItem
  /**
   * 点击缩略图。带上事件是为了让调用方能取到这一块的 rect：灯箱要用它做
   * 「从缩略图长出来」的共享元素放大（见 MediaLightbox）。不需要 rect 的
   * 调用方照旧传一个无参函数即可。
   */
  onOpen?: (event: ReactMouseEvent<HTMLButtonElement>) => void
  onRemove?: () => void
}

export default function MediaThumb({ item, onOpen, onRemove }: MediaThumbProps) {
  const { t } = useI18n()
  const video = isVideoItem(item)
  // 首帧到达（或加载失败）之前一直显示骨架。失败也收起：一块永远在扫光的
  // 方块会让人以为它还在加载，而这其实是一条再也播不出来的素材。
  const [videoReady, setVideoReady] = useState(false)
  const markReady = () => setVideoReady(true)

  return (
    <div className="media-thumb">
      <button
        type="button"
        className="media-thumb-open"
        onClick={onOpen}
        data-video-ready={videoReady ? 'true' : undefined}
        aria-label={t(`查看${video ? '视频' : '图片'}：${item.original_name}`, `View ${video ? 'video' : 'image'}: ${item.original_name}`)}
      >
        {video ? (
          <>
            <video src={item.url} muted playsInline preload="metadata" onLoadedData={markReady} onError={markReady} />
            <span className="img-skeleton" aria-hidden="true" />
            <span className="media-thumb-play" aria-hidden="true" />
          </>
        ) : (
          <ProgressiveImage src={item.url} alt={item.original_name} loading="lazy" />
        )}
      </button>
      {onRemove ? (
        <button
          type="button"
          className="media-thumb-remove"
          onClick={onRemove}
          aria-label={t(`移除 ${item.original_name}`, `Remove ${item.original_name}`)}
        >
          ×
        </button>
      ) : null}
    </div>
  )
}
