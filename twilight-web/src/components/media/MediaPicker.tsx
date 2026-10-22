// MediaPicker.tsx —— 媒体选择器（受控）
// 状态全部在父组件：本组件通过 items/onChange 实现「受控」。选择文件后调
// blog.uploadMedia 上传，成功则把返回项追加到列表末尾（onChange 上抛、由父决定
// 最终顺序）；onBusyChange 用于让外层表单在上传期间禁用提交。
// 大小 / 类型限制由后端在上传接口强制，前端只在 <input accept> 上做软约束，
// 不能绕过。每次上传后重置 input.value，否则同文件名无法再次选中。

import { useRef, useState, type ChangeEvent } from 'react'
import type { MediaItem } from 'twilight-blog-api'
import { blog, getErrorMessage } from '../../lib/client'
import type { ZoomRect } from '../../lib/motion'
import { useToast } from '../../lib/toast'
import { useI18n } from '../../lib/i18n'
import MediaLightbox from './MediaLightbox'
import MediaThumb from './MediaThumb'

interface MediaPickerProps {
  items: MediaItem[]
  onChange: (items: MediaItem[]) => void
  onBusyChange?: (busy: boolean) => void
  disabled?: boolean
}

export default function MediaPicker({
  items,
  onChange,
  onBusyChange,
  disabled = false,
}: MediaPickerProps) {
  const { t } = useI18n()
  const toast = useToast()
  const inputRef = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)
  const [previewIndex, setPreviewIndex] = useState<number | null>(null)
  // 与 MediaGallery 同一套：点中的缩略图位置（灯箱共享元素放大的起点）。
  const [origin, setOrigin] = useState<ZoomRect | null>(null)

  const setBusy = (busy: boolean) => {
    setUploading(busy)
    onBusyChange?.(busy)
  }

  const handleFiles = async (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files ?? [])
    if (files.length === 0) return

    setBusy(true)
    try {
      const uploaded = await blog.uploadMedia(files)
      onChange([...items, ...uploaded])
    } catch (error) {
      toast(getErrorMessage(error, '上传失败'))
    } finally {
      setBusy(false)
      if (inputRef.current) inputRef.current.value = ''
    }
  }

  const removeItem = (id: number) => {
    onChange(items.filter((item) => item.id !== id))
  }

  const canAdd = !disabled && !uploading

  return (
    <div className="media-picker">
      <input
        ref={inputRef}
        type="file"
        accept="image/*,video/*"
        multiple
        hidden
        onChange={(event) => void handleFiles(event)}
      />
      <div className="media-picker-row">
        {items.map((item, index) => (
          <MediaThumb
            key={item.id}
            item={item}
            onOpen={(event) => {
              setOrigin(event.currentTarget.getBoundingClientRect())
              setPreviewIndex(index)
            }}
            onRemove={() => removeItem(item.id)}
          />
        ))}
        <button
          type="button"
          className="media-picker-add"
          onClick={() => inputRef.current?.click()}
          disabled={!canAdd}
          title={t('添加图片或视频', 'Add images or videos')}
          aria-label={t('添加图片或视频', 'Add images or videos')}
        >
          {uploading ? (
            <span className="media-picker-loading" aria-hidden="true" />
          ) : (
            <span aria-hidden="true">+</span>
          )}
        </button>
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
