// CoverPicker.tsx —— 文章头图选择器（受控，最多一张）
//
// 与 MediaPicker 的差别就是「最多一张」：这里没有列表，只有一个可选中的位置。
// 上传成功后立刻用新图替换旧图（旧图**不删**——它还在媒体库里，用户可能只是
// 换张试试，而删掉是不可逆的）；「移除」只解除引用，同样不删文件。
//
// 为什么要在前端再挡一次非图片：`<input accept="image/*">` 只是文件对话框的
// 过滤条件，拖拽与「所有文件」都能绕过它，而服务端对**媒体上传**是照单全收的
// （视频/音频都是合法媒体），只有「把它当文章头图」才会被拒。所以这里拿到
// 上传结果后自检一次：不是图片就顺手把它删掉再提示，避免在媒体库里留下
// 一份谁也不会引用、用户也不知道从哪来的文件。

import { useRef, useState, type ChangeEvent } from 'react'
import { mediaUrl } from 'twilight-blog-api'
import { Button } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import ProgressiveImage from '../ProgressiveImage'
import { useToast } from '../../lib/toast'
import { useI18n } from '../../lib/i18n'

interface CoverPickerProps {
  /** 当前头图媒体 id；null 表示没有头图。 */
  value: number | null
  onChange: (id: number | null) => void
  /** 上传期间为 true，供外层表单禁用提交（上传没结束就保存会存下空头图）。 */
  onBusyChange?: (busy: boolean) => void
  disabled?: boolean
}

export default function CoverPicker({ value, onChange, onBusyChange, disabled = false }: CoverPickerProps) {
  const { t } = useI18n()
  const toast = useToast()
  const inputRef = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)

  const setBusy = (busy: boolean) => {
    setUploading(busy)
    onBusyChange?.(busy)
  }

  const handleFile = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    if (!file) return

    setBusy(true)
    try {
      const [uploaded] = await blog.uploadMedia([file])
      if (!uploaded) {
        toast('上传失败')
        return
      }
      if (uploaded.media_type !== 'image') {
        // 删掉刚上传的那一份，否则媒体库里会多出一个没人引用的文件。
        // 删除失败也不改变「不能当头图」这个结论，所以只吞掉错误。
        try {
          await blog.deleteMedia(uploaded.id)
        } catch {
          // 忽略：清理失败不影响这里的用户提示。
        }
        toast('头图必须是图片')
        return
      }
      onChange(uploaded.id)
    } catch (error) {
      toast(getErrorMessage(error, t('上传失败', 'Upload failed')))
    } finally {
      setBusy(false)
      // 重置 value，否则同一个文件第二次选中不会触发 change。
      if (inputRef.current) inputRef.current.value = ''
    }
  }

  const busy = uploading || disabled

  return (
    <div className="cover-picker">
      <input
        ref={inputRef}
        type="file"
        accept="image/*"
        hidden
        onChange={(event) => void handleFile(event)}
        aria-label={t('上传头图', 'Upload cover')}
      />
      {value === null ? (
        <button
          type="button"
          className="cover-empty"
          onClick={() => inputRef.current?.click()}
          disabled={busy}
        >
          {uploading ? <span className="media-picker-loading" aria-hidden="true" /> : <span aria-hidden="true">＋</span>}
          <span>{uploading ? t('上传中…', 'Uploading…') : t('上传头图', 'Upload cover')}</span>
        </button>
      ) : (
        <div className="cover-preview">
          <ProgressiveImage src={mediaUrl(value)} alt={t('文章头图', 'Post cover')} />
          <div className="cover-actions">
            <Button small disabled={busy} onClick={() => inputRef.current?.click()}>
              {uploading ? t('上传中…', 'Uploading…') : t('更换', 'Change')}
            </Button>
            <Button small ghost disabled={busy} onClick={() => onChange(null)}>
              {t('移除', 'Remove')}
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
