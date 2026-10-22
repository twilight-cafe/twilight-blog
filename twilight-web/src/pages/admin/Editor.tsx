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
import { Button, DatePicker, Input, Select, Switch, Toolbar, ToolbarButton, ToolbarSeparator } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import { toDate, toUnixSeconds } from '../../lib/format'
import CoverPicker from '../../components/media/CoverPicker'
import ContentView from '../../components/ContentView'
import { ImageGroup } from '../../lib/imageGroup'
import { useToast } from '../../lib/toast'
import { Loading } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import { useI18n } from '../../lib/i18n'

export default function Editor() {
  const { t } = useI18n()
  const { id } = useParams()
  const articleId = id ? Number(id) : null
  const navigate = useNavigate()
  const toast = useToast()
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const [loaded, setLoaded] = useState(articleId === null)
  // 新建文章没有请求，loaded 立即为真；编辑已有文章时骨架延迟出现。
  const pending = useDelayedLoading(!loaded)
  // 中英文两份内容并列编辑：标签页只切换「正在写哪一份」，不会互相覆盖。
  // 英文版整份都可以为空（前台回退中文），因此不做必填校验。
  const [langTab, setLangTab] = useState<'zh' | 'en'>('zh')
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [titleEN, setTitleEN] = useState('')
  const [contentEN, setContentEN] = useState('')
  // 站长显式勾选「AI 生成」；前台据此显示提示，服务端不做推断。
  const [aiGenerated, setAiGenerated] = useState(false)
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
      toast(t('文章不存在', 'Post not found'))
      navigate('/admin/posts')
      return
    }
    blog.getArticle(articleId)
      .then((article) => {
        setTitle(article.title)
        setContent(article.content)
        setTitleEN(article.title_en)
        setContentEN(article.content_en)
        setAiGenerated(article.ai_generated)
        setTags(article.tags)
        setCoverId(article.cover_media_id)
        setPublishedAt(toDate(article.created_at))
        setLoaded(true)
      })
      .catch((error) => {
        toast(getErrorMessage(error, t('加载文章失败', 'Failed to load the post')))
        navigate('/admin/posts')
      })
  }, [articleId, navigate, toast])

  // 正在编辑的那一份内容。工具栏与预览都跟着标签页走。
  const activeContent = langTab === 'zh' ? content : contentEN
  const setActiveContent = langTab === 'zh' ? setContent : setContentEN
  const activeTitle = langTab === 'zh' ? title : titleEN
  const setActiveTitle = langTab === 'zh' ? setTitle : setTitleEN

  const insert = (before: string, after = '') => {
    // 工具栏与 textarea 同帧渲染，点击时 ref 恒已挂载；断言免去不可达的空守卫。
    const textarea = textareaRef.current!
    const start = textarea.selectionStart
    const end = textarea.selectionEnd
    const selected = activeContent.slice(start, end)
    const next = activeContent.slice(0, start) + before + selected + after + activeContent.slice(end)
    setActiveContent(next)
    requestAnimationFrame(() => {
      textarea.focus()
      textarea.selectionStart = start + before.length
      textarea.selectionEnd = start + before.length + selected.length
    })
  }

  const save = async () => {
    if (!title.trim()) {
      toast(t('标题不能为空', 'The title cannot be empty'))
      return
    }
    setSaving(true)
    // 时间戳一律以秒发送（服务端 created_at 就是秒）。publishedAt 为 null 时
    // 不下发该字段：新建由服务端取当前时间，更新保持原值。
    const publishedSeconds = publishedAt ? toUnixSeconds(publishedAt) : undefined
    try {
      if (articleId) {
        const payload: Parameters<typeof blog.updateArticle>[1] = {
          title: title.trim(),
          content,
          titleEn: titleEN,
          contentEn: contentEN,
          aiGenerated,
          tags,
          coverMediaId: coverId,
        }
        if (publishedSeconds !== undefined) payload.publishedAt = publishedSeconds
        await blog.updateArticle(articleId, payload)
        toast(t('文章已保存', 'Post saved'))
      } else {
        const article = await blog.createArticle({
          title: title.trim(),
          content,
          titleEn: titleEN,
          contentEn: contentEN,
          aiGenerated,
          tags,
          coverMediaId: coverId,
          ...(publishedSeconds === undefined ? {} : { publishedAt: publishedSeconds }),
        })
        toast(t('文章已创建', 'Post created'))
        navigate(`/admin/editor/${article.id}`, { replace: true })
      }
    } catch (error) {
      toast(getErrorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  if (!loaded) return <Loading height={400} visible={pending} />

  return (
    <>
      <div className="ed-top">
        <Button ghost small onClick={() => navigate('/admin/posts')}>{t('← 返回', '← Back')}</Button>
        <span className="saved"><i /><span className="swap-text" key={String(saving)}>{saving ? t('保存中…', 'Saving…') : t('已同步', 'Synced')}</span></span>
        <span style={{ flex: 1 }} />
        <Button primary small disabled={saving || coverBusy} onClick={() => void save()}>{t('保存', 'Save')}</Button>
      </div>
      {/* 语言标签页：切换正在编辑的那一份。默认中文，与站点默认语言一致。 */}
      <div className="ed-lang-tabs" role="tablist" aria-label={t('文章语言', 'Post language')}>
        <button
          type="button"
          role="tab"
          aria-selected={langTab === 'zh'}
          className={langTab === 'zh' ? 'on' : ''}
          onClick={() => setLangTab('zh')}
        >
          {t('中文', 'Chinese')}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={langTab === 'en'}
          className={langTab === 'en' ? 'on' : ''}
          onClick={() => setLangTab('en')}
        >
          English
        </button>
      </div>
      <Input
        className="ed-title"
        placeholder={langTab === 'zh' ? t('标题…', 'Title…') : t('English title…', 'English title…')}
        value={activeTitle}
        onChange={(event) => setActiveTitle(event.target.value)}
        style={{ border: 'none', fontSize: 28, fontFamily: 'var(--serif)' }}
      />
      <div className="ed-meta">
        <span>{t('标签', 'Tags')}</span>
        <Select mode="tags" value={tags} onChange={(vals) => setTags(vals.map(String))} placeholder={t('输入标签后回车', 'Type a tag and press Enter')} />
      </div>
      <div className="ed-meta">
        <span>{t('AI 生成', 'AI-generated')}</span>
        <Switch
          checked={aiGenerated}
          onChange={(event) => setAiGenerated(event.target.checked)}
          checkedChildren={t('是', 'Yes')}
          unCheckedChildren={t('否', 'No')}
        />
        <span className="ed-hint">
          {t('勾选后，读者会在文章页看到「本文由 AI 生成」的提示。', 'When enabled, readers see an “AI-generated” notice on the post.')}
        </span>
      </div>
      <div className="ed-meta">
        <span>{t('发布时间', 'Published at')}</span>
        <DatePicker
          value={publishedAt}
          onChange={(date) => setPublishedAt(date)}
          showTime
          format="YYYY-MM-DD HH:mm"
          allowClear
          placeholder={articleId ? t('留空则保持原发布时间', 'Leave empty to keep the original date') : t('留空则用当前时间', 'Leave empty to use now')}
        />
        <span className="ed-hint">{t('列表按发布时间倒序，改它会改变文章的排列位置', 'Lists are ordered by publish time; changing it moves the post.')}</span>
      </div>
      <div className="ed-meta ed-cover-row">
        <span>{t('头图', 'Cover')}</span>
        <CoverPicker value={coverId} onChange={setCoverId} onBusyChange={setCoverBusy} disabled={saving} />
      </div>
      <div className="ed-grid" style={{ gridTemplateColumns: '1fr 1fr' }}>
        <div style={{ minWidth: 0, display: 'flex', flexDirection: 'column' }}>
          <Toolbar>
            {[
              { label: 'H2', tip: t('标题 2', 'Heading 2'), run: () => insert('\n## ', '\n') },
              { label: 'H3', tip: t('标题 3', 'Heading 3'), run: () => insert('\n### ', '\n') },
              { label: 'B', tip: t('加粗', 'Bold'), run: () => insert('**', '**') },
              { label: 'I', tip: t('斜体', 'Italic'), run: () => insert('*', '*') },
              { label: '</>', tip: t('行内代码', 'Inline code'), run: () => insert('`', '`') },
              { label: '{ }', tip: t('代码块', 'Code block'), run: () => insert('\n```\n', '\n```\n') },
              { label: '❝', tip: t('引用', 'Quote'), run: () => insert('\n> ', '\n') },
              { label: '☑', tip: t('任务列表', 'Task list'), run: () => insert('\n- [ ] ', '\n') },
              { label: '⊞', tip: t('表格', 'Table'), run: () => insert('\n| 列1 | 列2 |\n| --- | --- |\n| 值 | 值 |\n') },
            ].map((button, index) => (
              <ToolbarButton key={index} tooltip={button.tip} onClick={button.run}>{button.label}</ToolbarButton>
            ))}
            <ToolbarSeparator />
          </Toolbar>
          <textarea
            ref={textareaRef}
            className="mds"
            style={{ width: '100%', minHeight: 520, border: 0, outline: 'none', resize: 'vertical', background: 'var(--softer)', fontFamily: 'var(--mono)', fontSize: 12.5, lineHeight: 1.9, padding: '18px 20px', color: 'var(--mut)' }}
            value={activeContent}
            onChange={(event) => setActiveContent(event.target.value)}
            spellCheck={false}
          />
        </div>
        <div className="pane">
          <div className="ph2" style={{ borderBottom: '1px solid var(--line)' }}><b>{t('预览', 'Preview')}</b></div>
          <div style={{ padding: '18px 22px', overflow: 'auto', maxHeight: 660 }}>
            {/* 预览里的图同样「一屏一组」：全部下载并解码完再一起出现。 */}
            <ImageGroup>
              <ContentView text={activeContent} trusted />
            </ImageGroup>
          </div>
        </div>
      </div>
    </>
  )
}
