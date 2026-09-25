// MediaAdmin.tsx —— 媒体库管理
// 上传调 blog.uploadMedia（单文件，大小/类型限制由后端强制；这里 hidden input 没
// 加 accept，所以理论上能传音频等任意类型）。
// 注意：媒体没有「回收站/软删」概念——deleteMedia 是即删即毁，无二次确认外的
// 恢复路径，与文章/页面不同。
// 渲染上：image 直接 <img>，非 image（如 audio）只显示「MP3」占位文字而非播放器，
// 这是展示层的简化（若日后支持音频应换成 <audio>）。
// 复制按钮给的是**绝对地址**（toAbsoluteUrl），因为那是要贴进浏览器地址栏的；
// <img src> 仍用接口给的相对路径，理由见 lib/url.ts。

import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, CopyButton, Pagination } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtDateTime, fmtSize } from '../../lib/format'
import { toAbsoluteUrl } from '../../lib/url'
import type { MediaItem } from 'twilight-blog-api'
import { useDialog } from '../../lib/dialog'
import { useToast } from '../../lib/toast'
import { Empty, Loading } from '../../components/common'
import Icon from '../../lib/icons'

export default function MediaAdmin() {
  const toast = useToast()
  const { confirm } = useDialog()
  const inputRef = useRef<HTMLInputElement>(null)
  const [list, setList] = useState<MediaItem[]>([])
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(1)
  const [loading, setLoading] = useState(true)
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
        toast(getErrorMessage(error, '加载媒体失败'))
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
      toast('上传成功')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    } finally {
      setUploading(false)
      if (inputRef.current) inputRef.current.value = ''
    }
  }

  const remove = async (item: MediaItem) => {
    if (!(await confirm('删除媒体', `确定删除「${item.original_name}」？`))) return
    try {
      await blog.deleteMedia(item.id)
      toast('已删除')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  return (
    <>
      <div className="ph">
        <h2>媒体</h2>
        <span className="kicker" style={{ marginTop: 6 }}>MEDIA · {list.length}</span>
        <span style={{ flex: 1 }} />
        <Button primary small disabled={uploading} onClick={() => inputRef.current?.click()}>
          {uploading ? '上传中…' : '上传'}
        </Button>
        <input ref={inputRef} type="file" hidden onChange={(event) => { const file = event.target.files?.[0]; if (file) void upload(file) }} />
      </div>
      {loading ? <Loading height={200} /> : list.length === 0 ? <Empty text="媒体库为空" /> : (
        <div className="mgrid">
          {list.map((item) => (
            <div key={item.id} className={`mtile ${item.media_type === 'audio' ? 'doc' : ''}`}>
              <div className="mth">
                {item.media_type === 'image' ? (
                  <img src={item.url} alt={item.original_name} loading="lazy" style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', objectFit: 'cover' }} />
                ) : (
                  <span>MP3</span>
                )}
                <div className="mact">
                  <CopyButton text={toAbsoluteUrl(item.url)}><Icon name="copy" size={13} /></CopyButton>
                  <button type="button" title="删除" onClick={() => void remove(item)}><Icon name="x" size={13} /></button>
                </div>
              </div>
              <div className="minfo">
                <b>{item.original_name}</b>
                <span>{fmtSize(item.size)} · {fmtDateTime(item.created_at)}</span>
              </div>
            </div>
          ))}
        </div>
      )}
      <Pagination align="center" current={page} total={totalPages} pageSize={1} onChange={setPage} />
    </>
  )
}
