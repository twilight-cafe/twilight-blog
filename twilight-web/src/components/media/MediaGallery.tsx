// MediaGallery.tsx —— 媒体缩略图行 + 全屏灯箱（后台微语卡与前台微语流共用）
//
// 为什么单独成件：微语的配图行有两个消费方——后台微语管理卡（MomentsAdmin）
// 与公开微语流（pages/public/Moments）。两边曾被写成两份实现，而公开那份
// **根本没写**（微语页因此只显示文字、图片凭空消失）。把展示逻辑收在一处之后，
// 「前台与后台一致」不再依赖谁记得同步；以后首页要展示微语也直接复用它。
//
// 交互：缩略图行里的每一张都可点，点开 MediaLightbox 全屏预览；previewIndex
// 由本组件持有（null = 关闭），因此不需要调用方维护任何预览状态。全屏之后的
// 换图（左右按钮 / ←→ 键 / 横向拖动）与下拉关闭都由灯箱自己管，本组件不参与
// （见 MediaLightbox 文件头）。
//
// items 必须非空：空数组会渲染出一个空的缩略图占位行，语义上也不该被调用
// （调用方用 `items.length > 0` 守卫，见 MomentsAdmin / Moments 的渲染处）。
// 这里不再重复守卫——那会是一个永远为假的死分支。

import { useState } from 'react'
import type { MediaItem } from 'twilight-blog-api'
import type { ZoomRect } from '../../lib/motion'
import MediaLightbox from './MediaLightbox'
import MediaThumb from './MediaThumb'

interface MediaGalleryProps {
  items: MediaItem[]
}

export default function MediaGallery({ items }: MediaGalleryProps) {
  const [previewIndex, setPreviewIndex] = useState<number | null>(null)
  // 被点中的那一块在屏幕上的位置：灯箱打开时从它「长」出来、关闭时缩回去。
  // 与 previewIndex 在同一次点击里一起写入，两者永远指向同一张。
  const [origin, setOrigin] = useState<ZoomRect | null>(null)

  return (
    <div className="media-gallery sm">
      <div className="media-picker-row">
        {items.map((item, index) => (
          <MediaThumb
            key={item.id}
            item={item}
            onOpen={(event) => {
              setOrigin(event.currentTarget.getBoundingClientRect())
              setPreviewIndex(index)
            }}
          />
        ))}
      </div>
      <MediaLightbox
        items={items}
        index={previewIndex}
        origin={origin}
        onClose={() => setPreviewIndex(null)}
      />
    </div>
  )
}
