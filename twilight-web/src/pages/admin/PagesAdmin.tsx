// PagesAdmin.tsx —— 独立页面管理（active / 回收站）
// 结构同 Posts：active 下的「删除」是软删（deletePage 进回收站，可 restore），
// trash 下才有永久删除（permanentlyDeletePage）。注意本页列表写死 page:1 /
// pageSize:12、没有 Pager，页面超过 12 个时回收站/列表只显示前 12 条。
// 同文件还导出 PageEdit（编辑已有）与 NewPage（新建），正文是 Markdown，右侧
// ContentView 实时预览；两者都不做草稿自动保存，离开即丢弃未保存改动。

import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { Button, Chip, Input } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { fmtDateTime } from '../../lib/format'
import type { Page } from 'twilight-blog-api'
import { useDialog } from '../../lib/dialog'
import { useToast } from '../../lib/toast'
import { Empty, Loading } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import { useI18n } from '../../lib/i18n'
import ContentView from '../../components/ContentView'
import { ImageGroup } from '../../lib/imageGroup'

type Tab = 'active' | 'trash'

export default function PagesAdmin() {
  const { t } = useI18n()
  const toast = useToast()
  const { confirm } = useDialog()
  const [tab, setTab] = useState<Tab>('active')
  const [list, setList] = useState<Page[]>([])
  const [loading, setLoading] = useState(true)
  // 首屏加载超过阈值才显示等待提示（见 lib/loading.ts）。
  const pending = useDelayedLoading(loading)
  const [reloadKey, setReloadKey] = useState(0)

  // One effect owns the fetch; a stale response is dropped instead of
  // overwriting the list that belongs to the current tab.
  useEffect(() => {
    let alive = true
    setLoading(true)

    const run = async () => {
      try {
        const result = tab === 'active'
          ? await blog.listPages({ page: 1, pageSize: 12 })
          : await blog.listTrashPages({ page: 1, pageSize: 12 })
        if (!alive) return
        setList(result.items)
      } catch (error) {
        if (!alive) return
        setList([])
        toast(getErrorMessage(error, t('加载页面失败', 'Failed to load the page')))
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

  const remove = async (page: Page) => {
    if (!(await confirm(t('删除页面', 'Delete page'), t(`确定将「${page.title}」移入回收站？`, `Move “${page.title}” to the recycle bin?`)))) return
    try {
      await blog.deletePage(page.id)
      toast(t('已移入回收站', 'Moved to the recycle bin'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const restore = async (page: Page) => {
    try {
      await blog.restorePage(page.id)
      toast(t('已恢复', 'Restored'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const permanent = async (page: Page) => {
    if (!(await confirm(t('彻底删除页面', 'Delete page permanently'), t(`确定彻底删除「${page.title}」？此操作不可撤销。`, `Permanently delete “${page.title}”? This cannot be undone.`)))) return
    try {
      await blog.permanentlyDeletePage(page.id)
      toast(t('已彻底删除', 'Permanently deleted'))
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  return (
    <>
      <div className="ph">
        <h2>{t('页面', 'Pages')}</h2>
        <span className="kicker" style={{ marginTop: 6 }}>PAGES · {list.length}</span>
        <span className="sp" />
        <Link className="btn pri sm" to="/admin/pages/new">{t('新建页面', 'New page')}</Link>
      </div>
      <div className="atabs">
        <Chip solid={tab === 'active'} onClick={() => setTab('active')}>{t('页面', 'Pages')}</Chip>
        <Chip solid={tab === 'trash'} onClick={() => setTab('trash')}>{t('回收站', 'Recycle bin')}</Chip>
      </div>
      {list.length === 0 ? (
        loading ? <Loading height={160} visible={pending} /> : <Empty text={t('暂无页面', 'No pages yet')} />
      ) : (
        // 刷新时旧列表原地变暗，新数据到达后替换（见 app.css 的 [data-busy]）。
        <div aria-busy={loading} data-busy={String(loading)}>
          {list.map((page) => (
            <div key={page.id} className="pcard rv" style={{ marginBottom: 10 }}>
              <div className="ph2">
                <Link to={tab === 'active' ? `/admin/pages/${page.id}` : '#'}><b>{page.title}</b></Link>
                <span className="mono" style={{ fontSize: 10.5, color: 'var(--faint)' }}>{fmtDateTime(page.updated_at)}</span>
                <span style={{ flex: 1 }} />
                {tab === 'active' ? (
                  <>
                    <Link className="miniact" to={`/admin/pages/${page.id}`}>{t('编辑', 'Edit')}</Link>
                    <button className="miniact danger" type="button" onClick={() => void remove(page)}>{t('删除', 'Delete')}</button>
                  </>
                ) : (
                  <>
                    <button className="miniact" type="button" onClick={() => void restore(page)}>{t('恢复', 'Restore')}</button>
                    <button className="miniact danger" type="button" onClick={() => void permanent(page)}>{t('彻底删除', 'Delete permanently')}</button>
                  </>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </>
  )
}

export function PageEdit() {
  const { t } = useI18n()
  const { id = '' } = useParams()
  const pageId = Number(id)
  const navigate = useNavigate()
  const toast = useToast()
  const [page, setPage] = useState<Page | null>(null)
  // 正在取页面：骨架延迟出现，期间它已经占住标题 + 正文编辑区的位置。
  const pending = useDelayedLoading(!page)
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!pageId || !Number.isFinite(pageId) || pageId <= 0) {
      toast(t('页面不存在', 'Page not found'))
      navigate('/admin/pages')
      return
    }

    let alive = true
    blog.getPage(pageId)
      .then((data) => {
        if (!alive) return
        setPage(data)
        setTitle(data.title)
        setContent(data.content)
      })
      .catch((error) => {
        if (!alive) return
        toast(getErrorMessage(error, t('加载页面失败', 'Failed to load the page')))
        navigate('/admin/pages')
      })

    return () => {
      alive = false
    }
  }, [pageId, navigate, toast])

  const save = async () => {
    if (!title.trim()) {
      toast(t('标题不能为空', 'The title cannot be empty'))
      return
    }
    setSaving(true)
    try {
      await blog.updatePage(pageId, { title: title.trim(), content })
      toast(t('页面已保存', 'Page saved'))
      navigate('/admin/pages')
    } catch (error) {
      toast(getErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  if (!page) return <Loading height={200} visible={pending} />

  return (
    <>
      <div className="ed-top">
        <Button ghost small onClick={() => navigate('/admin/pages')}>{t('← 返回', '← Back')}</Button>
        <span style={{ flex: 1 }} />
        <Button primary small disabled={saving} onClick={() => void save()}>{t('保存', 'Save')}</Button>
      </div>
      <div className="pcard" style={{ padding: 20, display: 'grid', gap: 14 }}>
        <Input placeholder={t('页面标题', 'Page title')} value={title} onChange={(event) => setTitle(event.target.value)} style={{ fontSize: 24, fontFamily: 'var(--serif)' }} />
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
          <textarea
            className="inp box"
            style={{ minHeight: 420 }}
            value={content}
            onChange={(event) => setContent(event.target.value)}
            placeholder={t('内容（纯文本 / 后续可扩展）', 'Content (plain text for now)')}
          />
          <div style={{ border: '1px solid var(--line)', borderRadius: 'var(--radius)', overflow: 'auto', maxHeight: 480 }}>
            <ImageGroup>
              <ContentView text={content} trusted />
            </ImageGroup>
          </div>
        </div>
      </div>
    </>
  )
}

export function NewPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const toast = useToast()
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [saving, setSaving] = useState(false)

  const save = async () => {
    if (!title.trim()) {
      toast(t('标题不能为空', 'The title cannot be empty'))
      return
    }
    setSaving(true)
    try {
      await blog.createPage({ title: title.trim(), content })
      toast(t('页面已创建', 'Page created'))
      navigate('/admin/pages')
    } catch (error) {
      toast(getErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <div className="ed-top">
        <Button ghost small onClick={() => navigate('/admin/pages')}>{t('← 返回', '← Back')}</Button>
        <span style={{ flex: 1 }} />
        <Button primary small disabled={saving} onClick={() => void save()}>{t('保存', 'Save')}</Button>
      </div>
      <div className="pcard" style={{ padding: 20, display: 'grid', gap: 14 }}>
        <Input placeholder={t('页面标题', 'Page title')} value={title} onChange={(event) => setTitle(event.target.value)} style={{ fontSize: 24, fontFamily: 'var(--serif)' }} />
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
          <textarea
            className="inp box"
            style={{ minHeight: 420 }}
            value={content}
            onChange={(event) => setContent(event.target.value)}
            placeholder={t('内容（纯文本 / 后续可扩展）', 'Content (plain text for now)')}
          />
          <div style={{ border: '1px solid var(--line)', borderRadius: 'var(--radius)', overflow: 'auto', maxHeight: 480 }}>
            <ImageGroup>
              <ContentView text={content} trusted />
            </ImageGroup>
          </div>
        </div>
      </div>
    </>
  )
}
