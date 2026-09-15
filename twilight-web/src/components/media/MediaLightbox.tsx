// MediaLightbox.tsx —— 媒体灯箱（全屏预览）
// 通过 createPortal 挂到 document.body，打开时锁住 body 滚动并在关闭时还原
// 原先的 overflow；Esc 关闭、点背景关闭（舞台区域 stopPropagation 阻止误关）。
// index 为 null 或越界时表示关闭。注意：灯箱本身只支持「开/关」，没有在
// items 之间左右切换的键盘/按钮导航，要看下一张得先关再点别的缩略图。

import { useEffect } from 'react'
import { createPortal } from 'react-dom'
import type { MediaItem } from 'twilight-blog-api'

function isVideoItem(item: MediaItem): boolean {
  return item.mime_type.startsWith('video/') || String(item.media_type) === 'video'
}

interface MediaLightboxProps {
  items: MediaItem[]
  index: number | null
  onClose: () => void
}

export default function MediaLightbox({ items, index, onClose }: MediaLightboxProps) {
  const item = index === null || index < 0 || index >= items.length ? null : items[index]

  useEffect(() => {
    if (!item) return

    const handleKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    const previousOverflow = document.body.style.overflow

    window.addEventListener('keydown', handleKey)
    document.body.style.overflow = 'hidden'

    return function (): void {
      window.removeEventListener('keydown', handleKey)
      document.body.style.overflow = previousOverflow
    }
  }, [item, onClose])

  if (!item) return null

  const video = isVideoItem(item)

  return createPortal(
    <div className="media-lightbox" role="presentation" onClick={onClose}>
      <button
        type="button"
        className="media-lightbox-close"
        aria-label="关闭"
        onClick={(event) => {
          event.stopPropagation()
          onClose()
        }}
      >
        ×
      </button>
      <div className="media-lightbox-stage" onClick={(event) => event.stopPropagation()}>
        {video ? (
          <video src={item.url} controls autoPlay playsInline />
        ) : (
          <img src={item.url} alt={item.original_name} />
        )}
        <div className="media-lightbox-meta">
          <span>{item.original_name}</span>
          <span className="mono">{video ? 'VIDEO' : 'IMAGE'}</span>
        </div>
      </div>
    </div>
    ,
    document.body,


  )
}
