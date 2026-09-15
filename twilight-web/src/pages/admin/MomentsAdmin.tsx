// MomentsAdmin.tsx —— 微语管理（active / 回收站）
// 发布时只把媒体「id」带上（mediaIds）：媒体先经 MediaPicker 上传拿回 id，
// 再随微语一起提交，前端不直传二进制。remove=软删（deleteMoment），trash 下才可
// 永久删（permanentlyDeleteMoment）。同样写死 page:1/pageSize:12、无分页。
// 列表里已存在的微语通过 item.media_items 展示（MomentMedia 调内部灯箱）。

import { useCallback, useEffect, useState } from 'react'
import { Button, Chip, Input } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtDateTime } from '../../lib/format'
import type { MediaItem, Moment } from 'twilight-blog-api'
import { useDialog } from '../../lib/dialog'
import { useToast } from '../../lib/toast'
import ContentView from '../../components/ContentView'
import MediaPicker from '../../components/media/MediaPicker'
import MediaLightbox from '../../components/media/MediaLightbox'
import MediaThumb from '../../components/media/MediaThumb'
import { Empty, Loading } from '../../components/common'

type Tab = 'active' | 'trash'

function MomentMedia({ items }: { items: MediaItem[] }) {
  const [previewIndex, setPreviewIndex] = useState<number | null>(null)

  // 调用处（L188）已保证 items 非空数组，这里不再重复守卫。
  return (
    <div className="media-gallery sm" style={{ marginTop: 10 }}>
      <div className="media-picker-row">
        {items.map((item, index) => (
          <MediaThumb key={item.id} item={item} onOpen={() => setPreviewIndex(index)} />
        ))}
      </div>
      <MediaLightbox
        items={items}
        index={previewIndex}
        onClose={() => setPreviewIndex(null)}
      />
    </div>
  )
}


export default function MomentsAdmin() {
  const toast = useToast()
  const { confirm } = useDialog()
  const [tab, setTab] = useState<Tab>('active')
  const [list, setList] = useState<Moment[]>([])
  const [content, setContent] = useState('')
  const [mediaItems, setMediaItems] = useState<MediaItem[]>([])
  const [uploadingMedia, setUploadingMedia] = useState(false)
  const [publishing, setPublishing] = useState(false)
  const [loading, setLoading] = useState(true)
  const [reloadKey, setReloadKey] = useState(0)

  // One effect owns the fetch; a stale response (tab switched, unmounted) is
  // dropped instead of overwriting the newer list.
  useEffect(() => {
    let alive = true
    setLoading(true)

    const run = async () => {
      try {
        const result = tab === 'active'
          ? await blog.listMoments({ page: 1, pageSize: 12 })
          : await blog.listTrashMoments({ page: 1, pageSize: 12 })
        if (!alive) return
        setList(result.items)
      } catch (error) {
        if (!alive) return
        setList([])
        toast(getErrorMessage(error, '加载微语失败'))
      } finally {
        if (alive) setLoading(false)
      }
    }

    void run()
    return () => {
      alive = false
    }
  }, [reloadKey, tab, toast])

  const reload = useCallback(() => setReloadKey((value) => value + 1), [])

  const publish = async () => {
    if (!content.trim()) {
      toast('内容不能为空')
      return
    }
    const mediaIds = mediaItems.map((item) => item.id)
    setPublishing(true)
    const input = {
      content: content.trim(),
      ...(mediaIds.length > 0 ? { mediaIds } : {}),
    } as Parameters<typeof blog.createMoment>[0]
    try {
      await blog.createMoment(input)
      toast('微语已发布')
      setContent('')
      setMediaItems([])
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    } finally {
      setPublishing(false)
    }
  }

  const remove = async (item: Moment) => {
    if (!(await confirm('删除微语', '确定删除这条微语？'))) return
    try {
      await blog.deleteMoment(item.id)
      toast('已删除')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const restore = async (item: Moment) => {
    try {
      await blog.restoreMoment(item.id)
      toast('已恢复')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const permanent = async (item: Moment) => {
    if (!(await confirm('彻底删除微语', '确定彻底删除？此操作不可撤销。'))) return
    try {
      await blog.permanentlyDeleteMoment(item.id)
      toast('已彻底删除')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  return (
    <>
      <div className="ph">
        <h2>微语</h2>
        <span className="kicker" style={{ marginTop: 6 }}>MOMENTS · {list.length}</span>
      </div>
      {tab === 'active' ? (
        <div className="pcard rv" style={{ marginBottom: 18 }}>
          <div className="ph2"><b>发布微语</b></div>
          <div style={{ padding: '14px 20px 18px' }}>
            <Input variant="box" multiline placeholder="写点什么…" value={content} onChange={(event) => setContent(event.target.value)} />
            <div style={{ marginTop: 12 }}>
              <MediaPicker
                items={mediaItems}
                onChange={setMediaItems}
                onBusyChange={setUploadingMedia}
                disabled={uploadingMedia}
              />
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 12 }}>
              <Button primary small disabled={uploadingMedia || publishing} onClick={() => void publish()}>{publishing ? '发布中…' : '发布'}</Button>
            </div>
          </div>
        </div>
      ) : null}
      <div className="atabs">
        <Chip solid={tab === 'active'} onClick={() => setTab('active')}>微语</Chip>
        <Chip solid={tab === 'trash'} onClick={() => setTab('trash')}>回收站</Chip>
      </div>
      {loading ? <Loading height={160} /> : list.length === 0 ? <Empty text="还没有微语" /> : list.map((item) => (
        <div key={item.id} className="pcard rv" style={{ marginBottom: 12 }}>
          <div className="ph2">
            <b style={{ fontFamily: 'var(--serif)', fontSize: 15 }}>微语</b>
            <span className="mono" style={{ fontSize: 10.5, color: 'var(--faint)' }}>{fmtDateTime(item.created_at)}</span>
            <span style={{ flex: 1 }} />
            {tab === 'active'
              ? <Button ghost small onClick={() => void remove(item)}>删除</Button>
              : (
                <>
                  <Button ghost small onClick={() => void restore(item)}>恢复</Button>
                  <Button ghost small onClick={() => void permanent(item)}>彻底删除</Button>
                </>
              )}
          </div>
          <div className="bd2" style={{ paddingTop: 6 }}>
            <ContentView text={item.content} trusted />
              {item.media_items && item.media_items.length > 0 ? <MomentMedia items={item.media_items} /> : null}

          </div>
        </div>
      ))}
    </>
  )
}
