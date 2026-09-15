// Editor.tsx —— 文章编辑器（新建 / 编辑共用）
// articleId 为空即「新建」，loaded 初始即为 true；有 id 则拉取已有文章再编辑。
// 工具栏 insert 直接操作 textarea 选区，在光标处包裹 Markdown 标记（** 等）。
// 保存时新建走 createArticle、编辑走 updateArticle；新建成功后 replace 到
// /admin/editor/<id>，避免再次「保存」又创建一篇。
// 缺失的能力（潜在隐患）：没有草稿自动保存、没有 localStorage 暂存、也没有
// beforeunload 离开拦截——未保存的改动在刷新/关闭时直接丢失。顶部「已同步」只是
// 静态文案，并不代表真的做了自动同步，容易误导。

import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Button, DatePicker, Input, Select, Toolbar, ToolbarButton, ToolbarSeparator } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { toDate, toUnixSeconds } from '../../lib/format'
import CoverPicker from '../../components/media/CoverPicker'
import ContentView from '../../components/ContentView'
import { useToast } from '../../lib/toast'
import { Loading } from '../../components/common'

export default function Editor() {
  const { id } = useParams()
  const articleId = id ? Number(id) : null
  const navigate = useNavigate()
  const toast = useToast()
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const [loaded, setLoaded] = useState(articleId === null)
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [tags, setTags] = useState<string[]>([])
  // null 表示「没有头图」。它同时是「新建时还没选」与「用户点了移除」，
  // 两种情况下保存的结果一致（都不带头图），所以不需要再分一态。
  const [coverId, setCoverId] = useState<number | null>(null)
  // null 在**新建**时表示「用服务器当前时间」；编辑时初始化自 created_at，
  // 因此只有用户显式清空才会再次变成 null（那时保持原发布时间不变）。
  const [publishedAt, setPublishedAt] = useState<Date | null>(null)
  const [coverBusy, setCoverBusy] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (articleId === null) return
    if (!Number.isFinite(articleId) || articleId <= 0) {
      toast('文章不存在')
      navigate('/admin/posts')
      return
    }
    blog.getArticle(articleId)
      .then((article) => {
        setTitle(article.title)
        setContent(article.content)
        setTags(article.tags)
        setCoverId(article.cover_media_id)
        setPublishedAt(toDate(article.created_at))
        setLoaded(true)
      })
      .catch((error) => {
        toast(getErrorMessage(error, '加载文章失败'))
        navigate('/admin/posts')
      })
  }, [articleId, navigate, toast])

  const insert = (before: string, after = '') => {
    // 工具栏与 textarea 同帧渲染，点击时 ref 恒已挂载；断言免去不可达的空守卫。
    const textarea = textareaRef.current!
    const start = textarea.selectionStart
    const end = textarea.selectionEnd
    const selected = content.slice(start, end)
    const next = content.slice(0, start) + before + selected + after + content.slice(end)
    setContent(next)
    requestAnimationFrame(() => {
      textarea.focus()
      textarea.selectionStart = start + before.length
      textarea.selectionEnd = start + before.length + selected.length
    })
  }

  const save = async () => {
    if (!title.trim()) {
      toast('标题不能为空')
      return
    }
    setSaving(true)
    // 时间戳一律以秒发送（服务端 created_at 就是秒）。publishedAt 为 null 时
    // 不下发该字段：新建由服务端取当前时间，更新保持原值。
    const publishedSeconds = publishedAt ? toUnixSeconds(publishedAt) : undefined
    try {
      if (articleId) {
        const payload: Parameters<typeof blog.updateArticle>[1] = { title: title.trim(), content, tags, coverMediaId: coverId }
        if (publishedSeconds !== undefined) payload.publishedAt = publishedSeconds
        await blog.updateArticle(articleId, payload)
        toast('文章已保存')
      } else {
        const article = await blog.createArticle({
          title: title.trim(),
          content,
          tags,
          coverMediaId: coverId,
          ...(publishedSeconds === undefined ? {} : { publishedAt: publishedSeconds }),
        })
        toast('文章已创建')
        navigate(`/admin/editor/${article.id}`, { replace: true })
      }
    } catch (error) {
      toast(getErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  if (!loaded) return <Loading height={400} />

  return (
    <>
      <div className="ed-top">
        <Button ghost small onClick={() => navigate('/admin/posts')}>← 返回</Button>
        <span className="saved"><i />{saving ? '保存中…' : '已同步'}</span>
        <span style={{ flex: 1 }} />
        <Button primary small disabled={saving || coverBusy} onClick={() => void save()}>保存</Button>
      </div>
      <Input
        className="ed-title"
        placeholder="标题…"
        value={title}
        onChange={(event) => setTitle(event.target.value)}
        style={{ border: 'none', fontSize: 28, fontFamily: 'var(--serif)' }}
      />
      <div className="ed-meta">
        <span>标签</span>
        <Select mode="tags" value={tags} onChange={(vals) => setTags(vals.map(String))} placeholder="输入标签后回车" />
      </div>
      <div className="ed-meta">
        <span>发布时间</span>
        <DatePicker
          value={publishedAt}
          onChange={(date) => setPublishedAt(date)}
          showTime
          format="YYYY-MM-DD HH:mm"
          allowClear
          placeholder={articleId ? '留空则保持原发布时间' : '留空则用当前时间'}
        />
        <span className="ed-hint">列表按发布时间倒序，改它会改变文章的排列位置</span>
      </div>
      <div className="ed-meta ed-cover-row">
        <span>头图</span>
        <CoverPicker value={coverId} onChange={setCoverId} onBusyChange={setCoverBusy} disabled={saving} />
      </div>
      <div className="ed-grid" style={{ gridTemplateColumns: '1fr 1fr' }}>
        <div style={{ minWidth: 0, display: 'flex', flexDirection: 'column' }}>
          <Toolbar>
            {[
              { label: 'H2', tip: '标题 2', run: () => insert('\n## ', '\n') },
              { label: 'H3', tip: '标题 3', run: () => insert('\n### ', '\n') },
              { label: 'B', tip: '加粗', run: () => insert('**', '**') },
              { label: 'I', tip: '斜体', run: () => insert('*', '*') },
              { label: '</>', tip: '行内代码', run: () => insert('`', '`') },
              { label: '{ }', tip: '代码块', run: () => insert('\n```\n', '\n```\n') },
              { label: '❝', tip: '引用', run: () => insert('\n> ', '\n') },
              { label: '☑', tip: '任务列表', run: () => insert('\n- [ ] ', '\n') },
              { label: '⊞', tip: '表格', run: () => insert('\n| 列1 | 列2 |\n| --- | --- |\n| 值 | 值 |\n') },
            ].map((button, index) => (
              <ToolbarButton key={index} tooltip={button.tip} onClick={button.run}>{button.label}</ToolbarButton>
            ))}
            <ToolbarSeparator />
          </Toolbar>
          <textarea
            ref={textareaRef}
            className="mds"
            style={{ width: '100%', minHeight: 520, border: 0, outline: 'none', resize: 'vertical', background: 'var(--softer)', fontFamily: 'var(--mono)', fontSize: 12.5, lineHeight: 1.9, padding: '18px 20px', color: 'var(--mut)' }}
            value={content}
            onChange={(event) => setContent(event.target.value)}
            spellCheck={false}
          />
        </div>
        <div className="pane">
          <div className="ph2" style={{ borderBottom: '1px solid var(--line)' }}><b>预览</b></div>
          <div style={{ padding: '18px 22px', overflow: 'auto', maxHeight: 660 }}>
            <ContentView text={content} trusted />
          </div>
        </div>
      </div>
    </>
  )
}
