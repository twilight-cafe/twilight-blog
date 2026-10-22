// MomentsAdmin.tsx —— 微语管理（active / 回收站）
// 发布时只把媒体「id」带上（mediaIds）：媒体先经 MediaPicker 上传拿回 id，
// 再随微语一起提交，前端不直传二进制。remove=软删（deleteMoment），trash 下才可
// 永久删（permanentlyDeleteMoment）。同样写死 page:1/pageSize:12、无分页。
// 列表里已存在的微语通过 item.media_items 展示（MediaGallery 负责缩略图行与灯箱，
// 与公开微语流共用同一个组件——两边各写一份时前台那份漏了，微语页因此不显示图）。

import { useCallback, useEffect, useState } from 'react'
import { Button, Chip, Input } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtDateTime } from '../../lib/format'
import type { MediaItem, Moment } from 'twilight-blog-api'
import { useDialog } from '../../lib/dialog'
import { useToast } from '../../lib/toast'
import ContentView from '../../components/ContentView'
import { ImageGroup } from '../../lib/imageGroup'
import MediaPicker from '../../components/media/MediaPicker'
import MediaGallery from '../../components/media/MediaGallery'
import { Empty, Loading } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import { useI18n } from '../../lib/i18n'

type Tab = 'active' | 'trash'

export default function MomentsAdmin() {
  const { t } = useI18n()
  const toast = useToast()
  const { confirm } = useDialog()
  const [tab, setTab] = useState<Tab>('active')
  const [list, setList] = useState<Moment[]>([])
  const [content, setContent] = useState('')
  const [mediaItems, setMediaItems] = useState<MediaItem[]>([])
  const [uploadingMedia, setUploadingMedia] = useState(false)
  const [publishing, setPublishing] = useState(false)
  const [loading, setLoading] = useState(true)
  // 首屏加载超过阈值才显示等待提示（见 lib/loading.ts）。
  const pending = useDelayedLoading(loading)
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
        toast(getErrorMessage(error, t('加载微语失败', 'Failed to load moments')))
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
      toast(t('内容不能为空', 'The content cannot be empty'))
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
      toast(t('微语已发布', 'Moment posted'))
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
    if (!(await confirm(t('删除微语', 'Delete moment'), t('确定删除这条微语？', 'Delete this moment?')))) return
    try {
      await blog.deleteMoment(item.id)
      toast(t('已删除', 'Deleted'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const restore = async (item: Moment) => {
    try {
      await blog.restoreMoment(item.id)
      toast(t('已恢复', 'Restored'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const permanent = async (item: Moment) => {
    if (!(await confirm(t('彻底删除微语', 'Delete moment permanently'), t('确定彻底删除？此操作不可撤销。', 'Permanently delete this moment? This cannot be undone.')))) return
    try {
      await blog.permanentlyDeleteMoment(item.id)
      toast(t('已彻底删除', 'Permanently deleted'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  return (
    <>
      <div className="ph">
        <h2>{t('微语', 'Moments')}</h2>
        <span className="kicker" style={{ marginTop: 6 }}>MOMENTS · {list.length}</span>
      </div>
      {tab === 'active' ? (
        <div className="pcard rv" style={{ marginBottom: 18 }}>
          <div className="ph2"><b>{t('发布微语', 'Post a moment')}</b></div>
          <div style={{ padding: '14px 20px 18px' }}>
            <Input variant="box" multiline placeholder={t('写点什么…', 'Write something…')} value={content} onChange={(event) => setContent(event.target.value)} />
            <div style={{ marginTop: 12 }}>
              <MediaPicker
                items={mediaItems}
                onChange={setMediaItems}
                onBusyChange={setUploadingMedia}
                disabled={uploadingMedia}
              />
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 12 }}>
              <Button primary small disabled={uploadingMedia || publishing} onClick={() => void publish()}>{publishing ? t('发布中…', 'Posting…') : t('发布', 'Post')}</Button>
            </div>
          </div>
        </div>
      ) : null}
      <div className="atabs">
        <Chip solid={tab === 'active'} onClick={() => setTab('active')}>{t('微语', 'Moments')}</Chip>
        <Chip solid={tab === 'trash'} onClick={() => setTab('trash')}>{t('回收站', 'Recycle bin')}</Chip>
      </div>
      {list.length === 0 ? (
        loading ? <Loading height={160} visible={pending} /> : <Empty text={t('还没有微语', 'No moments yet')} />
      ) : (
        // 发布 / 删除 / 切标签后的刷新：旧列表原地变暗，新数据到达后替换。
        <div aria-busy={loading} data-busy={String(loading)}>
          {list.map((item) => (
            <div key={item.id} className="pcard rv" style={{ marginBottom: 12 }}>
              <div className="ph2">
                <b style={{ fontFamily: 'var(--serif)', fontSize: 15 }}>{t('微语', 'Moment')}</b>
                <span className="mono" style={{ fontSize: 10.5, color: 'var(--faint)' }}>{fmtDateTime(item.created_at)}</span>
                <span style={{ flex: 1 }} />
                {tab === 'active'
                  ? <Button ghost small onClick={() => void remove(item)}>{t('删除', 'Delete')}</Button>
                  : (
                    <>
                      <Button ghost small onClick={() => void restore(item)}>{t('恢复', 'Restore')}</Button>
                      <Button ghost small onClick={() => void permanent(item)}>{t('彻底删除', 'Delete permanently')}</Button>
                    </>
                  )}
              </div>
              <div className="bd2" style={{ paddingTop: 6 }}>
                {/* 一条微语一张卡：正文图与配图行下载完再一起出现。 */}
                <ImageGroup>
                  <ContentView text={item.content} trusted />
                  {item.media_items && item.media_items.length > 0 ? <MediaGallery items={item.media_items} /> : null}
                </ImageGroup>
              </div>
            </div>
          ))}
        </div>
      )}
    </>
  )
}
