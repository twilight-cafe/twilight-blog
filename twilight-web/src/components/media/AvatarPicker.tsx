// AvatarPicker.tsx —— 站长头像选择器（受控，最多一张，圆形预览）
//
// 与 CoverPicker 同源（都是「上传一张图并记住它的 id」），差别只在外形：头像在
// 作者卡里是一个圆，头图在文章页是一条横幅——同一份逻辑配不同的预览形态，
// 比把 148×84 的横幅预览塞进作者卡要诚实得多。
//
// 组件本身是**哑**的：它只知道「现在该显示哪张图」（url）与「用户选了哪一张」
// （onChange 收到媒体 id，或 null 表示移除），不关心这张图是刚上传的还是早就存
// 在服务端的。这份状态由调用方持有（见 Profile.tsx 的 AvatarEdit）——那里的保存
// 按钮要把 id 和表单其余字段一起提交，而「还没保存的改动」只有它说得清。
//
// 「引用不是所有权」在这里同样成立：换头像、移除头像都只解除引用，绝不删媒体
// 文件（见 service.AdminAuth.UpdateAvatar）。唯一的例外是下面那段：用户上传了
// 一个**不是图片**的文件，那份文件此刻还没有被任何人引用过，留着它就是媒体库里
// 一个来历不明的孤儿——所以顺手删掉再提示。

import { useRef, useState, type ChangeEvent } from 'react'
import { Button } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import ProgressiveImage from '../ProgressiveImage'
import { useToast } from '../../lib/toast'
import { useI18n } from '../../lib/i18n'

interface AvatarPickerProps {
  /** 当前要展示的头像地址；空串表示还没有头像。 */
  url: string
  /** 上传成功回传新媒体 id；点「移除」回传 null。 */
  onChange: (id: number | null) => void
  /** 上传期间为 true，供外层表单禁用提交（上传没结束就保存会存下空头像）。 */
  onBusyChange?: (busy: boolean) => void
  disabled?: boolean
}

export default function AvatarPicker({ url, onChange, onBusyChange, disabled = false }: AvatarPickerProps) {
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
        // <input accept="image/*"> 只是文件对话框的过滤条件，拖拽与「所有文件」
        // 都能绕过它，而服务端对媒体上传是照单全收的。删掉刚上传的那一份，
        // 否则媒体库里会多出一个没人引用的文件。删除失败也不改变「不能当头像」
        // 这个结论，所以只吞掉错误。
        try {
          await blog.deleteMedia(uploaded.id)
        } catch {
          // 忽略：清理失败不影响这里的用户提示。
        }
        toast('头像必须是图片')
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
    <div className="avatar-picker">
      <input
        ref={inputRef}
        type="file"
        accept="image/*"
        hidden
        onChange={(event) => void handleFile(event)}
        aria-label={t('上传头像', 'Upload avatar')}
      />
      {url === '' ? (
        <button
          type="button"
          className="avatar-picker-empty"
          onClick={() => inputRef.current?.click()}
          disabled={busy}
          aria-label={t('上传头像', 'Upload avatar')}
        >
          {uploading
            ? <span className="media-picker-loading" aria-hidden="true" />
            : <span aria-hidden="true">＋</span>}
        </button>
      ) : (
        <>
          <ProgressiveImage className="avatar-picker-preview" frameClassName="round" src={url} alt={t('站长头像', 'Owner avatar')} />
          <div className="avatar-actions">
            <Button small disabled={busy} onClick={() => inputRef.current?.click()}>
              {uploading ? t('上传中…', 'Uploading…') : t('更换', 'Change')}
            </Button>
            <Button small ghost disabled={busy} onClick={() => onChange(null)}>
              {t('移除', 'Remove')}
            </Button>
          </div>
        </>
      )}
    </div>
  )
}
