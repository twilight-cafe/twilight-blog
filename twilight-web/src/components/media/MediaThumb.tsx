// MediaThumb.tsx —— 单个媒体缩略图
// 用 item.url 直接展示（前端不区分缩略图与原图，url 是后端给的那个地址）；
// 视频用 preload="metadata" 只取首帧、避免整段下载。onRemove 可选：传了才显示
// 删除按钮（选择器里会传，纯展示场景不传）。点击主体触发 onOpen（通常打开灯箱）。

import type { MediaItem } from 'twilight-blog-api'

function isVideoItem(item: MediaItem): boolean {
  return item.mime_type.startsWith('video/') || String(item.media_type) === 'video'
}

interface MediaThumbProps {
  item: MediaItem
  onOpen?: () => void
  onRemove?: () => void
}

export default function MediaThumb({ item, onOpen, onRemove }: MediaThumbProps) {
  const video = isVideoItem(item)

  return (
    <div className="media-thumb">
      <button
        type="button"
        className="media-thumb-open"
        onClick={onOpen}
        aria-label={`查看${video ? '视频' : '图片'}：${item.original_name}`}
      >
        {video ? (
          <video src={item.url} muted playsInline preload="metadata" />
        ) : (
          <img src={item.url} alt={item.original_name} loading="lazy" />
        )}
        {video ? <span className="media-thumb-play" aria-hidden="true" /> : null}
      </button>
      {onRemove ? (
        <button
          type="button"
          className="media-thumb-remove"
          onClick={onRemove}
          aria-label={`移除 ${item.original_name}`}
        >
          ×
        </button>
      ) : null}
    </div>
  )
}
