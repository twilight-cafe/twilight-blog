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
import ContentView from '../../components/ContentView'

type Tab = 'active' | 'trash'

export default function PagesAdmin() {
  const toast = useToast()
  const { confirm } = useDialog()
  const [tab, setTab] = useState<Tab>('active')
  const [list, setList] = useState<Page[]>([])
  const [loading, setLoading] = useState(true)
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
        toast(getErrorMessage(error, '加载页面失败'))
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
    if (!(await confirm('删除页面', `确定将「${page.title}」移入回收站？`))) return
    try {
      await blog.deletePage(page.id)
      toast('已移入回收站')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const restore = async (page: Page) => {
    try {
      await blog.restorePage(page.id)
      toast('已恢复')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  const permanent = async (page: Page) => {
    if (!(await confirm('彻底删除页面', `确定彻底删除「${page.title}」？此操作不可撤销。`))) return
    try {
      await blog.permanentlyDeletePage(page.id)
      toast('已彻底删除')
      reload()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  return (
    <>
      <div className="ph">
        <h2>页面</h2>
        <span className="kicker" style={{ marginTop: 6 }}>PAGES · {list.length}</span>
        <span className="sp" />
        <Link className="btn pri sm" to="/admin/pages/new">新建页面</Link>
      </div>
      <div className="atabs">
        <Chip solid={tab === 'active'} onClick={() => setTab('active')}>页面</Chip>
        <Chip solid={tab === 'trash'} onClick={() => setTab('trash')}>回收站</Chip>
      </div>
      {loading ? <Loading height={160} /> : list.length === 0 ? <Empty text="暂无页面" /> : list.map((page) => (
        <div key={page.id} className="pcard rv" style={{ marginBottom: 10 }}>
          <div className="ph2">
            <Link to={tab === 'active' ? `/admin/pages/${page.id}` : '#'}><b>{page.title}</b></Link>
            <span className="mono" style={{ fontSize: 10.5, color: 'var(--faint)' }}>{fmtDateTime(page.updated_at)}</span>
            <span style={{ flex: 1 }} />
            {tab === 'active' ? (
              <>
                <Link className="miniact" to={`/admin/pages/${page.id}`}>编辑</Link>
                <Button ghost small onClick={() => void remove(page)}>删除</Button>
              </>
            ) : (
              <>
                <Button ghost small onClick={() => void restore(page)}>恢复</Button>
                <Button ghost small onClick={() => void permanent(page)}>彻底删除</Button>
              </>
            )}
          </div>
        </div>
      ))}
    </>
  )
}

export function PageEdit() {
  const { id = '' } = useParams()
  const pageId = Number(id)
  const navigate = useNavigate()
  const toast = useToast()
  const [page, setPage] = useState<Page | null>(null)
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!pageId || !Number.isFinite(pageId) || pageId <= 0) {
      toast('页面不存在')
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
        toast(getErrorMessage(error, '加载页面失败'))
        navigate('/admin/pages')
      })

    return () => {
      alive = false
    }
  }, [pageId, navigate, toast])

  const save = async () => {
    if (!title.trim()) {
      toast('标题不能为空')
      return
    }
    setSaving(true)
    try {
      await blog.updatePage(pageId, { title: title.trim(), content })
      toast('页面已保存')
      navigate('/admin/pages')
    } catch (error) {
      toast(getErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  if (!page) return <Loading height={200} />

  return (
    <>
      <div className="ed-top">
        <Button ghost small onClick={() => navigate('/admin/pages')}>← 返回</Button>
        <span style={{ flex: 1 }} />
        <Button primary small disabled={saving} onClick={() => void save()}>保存</Button>
      </div>
      <div className="pcard" style={{ padding: 20, display: 'grid', gap: 14 }}>
        <Input placeholder="页面标题" value={title} onChange={(event) => setTitle(event.target.value)} style={{ fontSize: 24, fontFamily: 'var(--serif)' }} />
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
          <textarea
            className="inp box"
            style={{ minHeight: 420 }}
            value={content}
            onChange={(event) => setContent(event.target.value)}
            placeholder="内容（纯文本 / 后续可扩展）"
          />
          <div style={{ border: '1px solid var(--line)', borderRadius: 'var(--radius)', overflow: 'auto', maxHeight: 480 }}>
            <ContentView text={content} trusted />
          </div>
        </div>
      </div>
    </>
  )
}

export function NewPage() {
  const navigate = useNavigate()
  const toast = useToast()
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [saving, setSaving] = useState(false)

  const save = async () => {
    if (!title.trim()) {
      toast('标题不能为空')
      return
    }
    setSaving(true)
    try {
      await blog.createPage({ title: title.trim(), content })
      toast('页面已创建')
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
        <Button ghost small onClick={() => navigate('/admin/pages')}>← 返回</Button>
        <span style={{ flex: 1 }} />
        <Button primary small disabled={saving} onClick={() => void save()}>保存</Button>
      </div>
      <div className="pcard" style={{ padding: 20, display: 'grid', gap: 14 }}>
        <Input placeholder="页面标题" value={title} onChange={(event) => setTitle(event.target.value)} style={{ fontSize: 24, fontFamily: 'var(--serif)' }} />
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
          <textarea
            className="inp box"
            style={{ minHeight: 420 }}
            value={content}
            onChange={(event) => setContent(event.target.value)}
            placeholder="内容（纯文本 / 后续可扩展）"
          />
          <div style={{ border: '1px solid var(--line)', borderRadius: 'var(--radius)', overflow: 'auto', maxHeight: 480 }}>
            <ContentView text={content} trusted />
          </div>
        </div>
      </div>
    </>
  )
}
