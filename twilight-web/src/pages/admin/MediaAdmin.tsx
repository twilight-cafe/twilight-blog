// MediaAdmin.tsx —— 媒体库管理
// 上传调 blog.uploadMedia（单文件，大小/类型限制由后端强制；这里 hidden input 没
// 加 accept，所以理论上能传音频等任意类型）。
// 注意：媒体没有「回收站/软删」概念——deleteMedia 是即删即毁，无二次确认外的
// 恢复路径，与文章/页面不同。
// 渲染上：image 直接 <img>，非 image（如 audio）只显示「MP3」占位文字而非播放器，
// 这是展示层的简化（若日后支持音频应换成 <audio>）。
// 复制按钮给的是**绝对地址**（toAbsoluteUrl），因为那是要贴进浏览器地址栏的；
// <img src> 仍用接口给的相对路径，理由见 lib/url.ts。
//
// 重命名（renameMedia）改的是**展示名** original_name：后台列表、前台缩略图的
// alt/aria-label、灯箱标题都读它。磁盘文件名（storage_name，随机串 + 真实扩展名）
// 与 /api/media/<id> 地址都不变，所以文章头图、微语配图、站长头像这些按 id 的引用
// 一处都不用改。服务端还会强制沿用**现有展示名的后缀**（sunset.png 改完仍是
// .png，而不是被换成磁盘上的 .webp）：展示名是上传时那个名字的留档，真实格式由
// mime_type 记录，前端也从不靠文件名判断类型。

import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, CopyButton, Pagination } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtDateTime, fmtSize } from '../../lib/format'
import { toAbsoluteUrl } from '../../lib/url'
import type { MediaItem } from 'twilight-blog-api'
import { useDialog } from '../../lib/dialog'
import { useToast } from '../../lib/toast'
import { Empty, Loading } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import { useI18n } from '../../lib/i18n'
import ProgressiveImage from '../../components/ProgressiveImage'
import { ImageGroup } from '../../lib/imageGroup'
import Icon from '../../lib/icons'

/**
 * 展示名长度上限。与后端 model.MaxMediaOriginalNameLength 镜像，改一处必须改
 * 另一处（同 SiteSettings.tsx 里那批常量；一致性由 media-name-limit.test.ts 钉住）。
 *
 * 按**码点**而不是 JS 的 .length 计数：后者把 emoji 这类星平面字符算两个，
 * 于是界面先喊「超了」而服务端照收。路径分隔符与控制字符不在前端复刻——
 * 那是服务端的权威规则（见 service.normalizeMediaName），客户端只拦明显发不出去的。
 *
 * ⚠️ 两边量的**不是同一个字符串**：这里量用户输入的那串（刚才说的码点数），
 * 服务端量的是「剥掉用户写的后缀、补上这条记录现有后缀」之后的最终名。所以在
 * 「输入长度距上限不足后缀长度」的窄窗口里（例：现有 sunset.png + 198 个字的输入），
 * 前端会放行、服务端回 400 invalid media name。这是刻意的取舍：前端复刻一遍后缀
 * 拼接规则才能对齐，而那正是最容易漂移的一处；服务端始终是权威。
 */
const MEDIA_NAME_MAX = 200

export default function MediaAdmin() {
  const { t } = useI18n()
  const toast = useToast()
  const { confirm, prompt } = useDialog()
  const inputRef = useRef<HTMLInputElement>(null)
  const [list, setList] = useState<MediaItem[]>([])
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(1)
  const [loading, setLoading] = useState(true)
  // 首屏加载超过阈值才显示等待提示（见 lib/loading.ts）。
  const pending = useDelayedLoading(loading)
  const [uploading, setUploading] = useState(false)
  const [reloadKey, setReloadKey] = useState(0)

  useEffect(() => {
    let alive = true
    setLoading(true)

    const run = async () => {
      try {
        const result = await blog.listMedia({ page, pageSize: 12 })
        if (!alive) return
        setList(result.items)
        setTotalPages(result.total_pages)
      } catch (error) {
        if (!alive) return
        setList([])
        setTotalPages(1)
        toast(getErrorMessage(error, t('加载媒体失败', 'Failed to load media')))
      } finally {
        if (alive) setLoading(false)
      }
    }

    void run()
    return () => {
      alive = false
    }
  }, [page, reloadKey, toast])

  const reload = useCallback(() => setReloadKey((value) => value + 1), [])

  const upload = async (file: File) => {
    // 调用点（onChange）已保证 file 存在，无需重复守卫。
    setUploading(true)
    try {
      await blog.uploadMedia(file)
      toast(t('上传成功', 'Uploaded'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    } finally {
      setUploading(false)
      if (inputRef.current) inputRef.current.value = ''
    }
  }

  const remove = async (item: MediaItem) => {
    if (!(await confirm(t('删除媒体', 'Delete media'), t(`确定删除「${item.original_name}」？`, `Delete “${item.original_name}”?`)))) return
    try {
      await blog.deleteMedia(item.id)
      toast(t('已删除', 'Deleted'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  // 重命名只改展示名。输入框预填当前名字（dialog.prompt 的 initialValue），
  // 打开即全选，用户直接打字就是覆盖。
  //
  // 前端只拦「空」与「超长」这两种明显不该发出去的输入：后缀沿用现有展示名、
  // 控制字符、路径分隔符都由服务端判定（它才拿得到这条记录当前的名字），
  // 前端复刻一份只会漂移。
  const rename = async (item: MediaItem) => {
    const name = await prompt(t('重命名媒体', 'Rename media'), { placeholder: t('新的文件名', 'New file name'), initialValue: item.original_name })
    // null = 取消。注意不能写成 `if (!name)`：取消与「输入为空」需要不同的处置，
    // 前者静默返回，后者要提示（空名字服务端会 400，没必要白跑一次请求）。
    if (name === null) return
    const trimmed = name.trim()
    if (!trimmed) {
      toast(t('名字不能为空', 'The name cannot be empty'))
      return
    }
    if ([...trimmed].length > MEDIA_NAME_MAX) {
      toast(t(`名字不能超过 ${MEDIA_NAME_MAX} 个字符`, `The name must be at most ${MEDIA_NAME_MAX} characters`))
      return
    }
    try {
      await blog.renameMedia(item.id, { original_name: trimmed })
      toast(t('已重命名', 'Renamed'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  return (
    <>
      <div className="ph">
        <h2>{t('媒体', 'Media')}</h2>
        <span className="kicker" style={{ marginTop: 6 }}>MEDIA · {list.length}</span>
        <span style={{ flex: 1 }} />
        <Button primary small disabled={uploading} onClick={() => inputRef.current?.click()}>
          {uploading ? t('上传中…', 'Uploading…') : t('上传', 'Upload')}
        </Button>
        <input ref={inputRef} type="file" hidden onChange={(event) => { const file = event.target.files?.[0]; if (file) void upload(file) }} />
      </div>
      {list.length === 0 ? (
        loading ? <Loading height={200} visible={pending} /> : <Empty text={t('媒体库为空', 'The media library is empty')} />
      ) : (
        // 上传 / 删除 / 翻页后的刷新：已加载的格子留在原位变暗，新数据到达后
        // 替换（新格子照常走 mgrid 的逐格落位）。
        <div className="mgrid" aria-busy={loading} data-busy={String(loading)}>
          {/* 一页 = 一组：这一页的缩略图全下载完再一起落位。key={page} 让翻页
              换一组重新等齐，已在屏幕上的格子不会回到加载态。 */}
          <ImageGroup key={page}>
          {list.map((item) => (
            <div key={item.id} className={`mtile ${item.media_type === 'audio' ? 'doc' : ''}`}>
              <div className="mth">
                {item.media_type === 'image' ? (
                  // 外框撑满 .mth 的 4:3 格子（见 app.css 的 .mth .img-frame），
                  // 图片仍然裁切填充；骨架在慢速网络下先画出这块格子。
                  <ProgressiveImage src={item.url} alt={item.original_name} />
                ) : (
                  <span>MP3</span>
                )}
                <div className="mact">
                  <CopyButton text={toAbsoluteUrl(item.url)}><Icon name="copy" size={13} /></CopyButton>
                  <button type="button" title={t('重命名', 'Rename')} onClick={() => void rename(item)}><Icon name="edit" size={13} /></button>
                  <button type="button" title={t('删除', 'Delete')} onClick={() => void remove(item)}><Icon name="x" size={13} /></button>
                </div>
              </div>
              <div className="minfo">
                <b>{item.original_name}</b>
                <span>{fmtSize(item.size)} · {fmtDateTime(item.created_at)}</span>
              </div>
            </div>
          ))}
          </ImageGroup>
        </div>
      )}
      <Pagination align="center" current={page} total={totalPages} pageSize={1} onChange={setPage} />
    </>
  )
}
