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
import type { Tag } from 'twilight-blog-api'
import { toDate, toUnixSeconds } from '../../lib/format'
import CoverPicker from '../../components/media/CoverPicker'
import ContentView from '../../components/ContentView'
import { ImageGroup } from '../../lib/imageGroup'
import { useToast } from '../../lib/toast'
import { useDialog } from '../../lib/dialog'
import { Loading } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import { useI18n } from '../../lib/i18n'
import { tagName } from '../../lib/localize'
import { fetchAllTags } from '../../lib/tags'

export default function Editor() {
  const { t, locale } = useI18n()
  const { id } = useParams()
  const articleId = id ? Number(id) : null
  const navigate = useNavigate()
  const toast = useToast()
  const dialog = useDialog()
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
  // AI 声明按语言独立：中文版与英文版各一个开关，互不影响。
  // 站长显式勾选；前台据此按当前语言显示提示，服务端不做推断。
  const [aiGeneratedZH, setAiGeneratedZH] = useState(false)
  const [aiGeneratedEN, setAiGeneratedEN] = useState(false)
  const [tags, setTags] = useState<string[]>([])
  // 已有标签的全集，喂给标签输入框做下拉建议。取不到时留空数组：
  // 建议只是增强，输入新标签并回车仍然可用。
  const [knownTags, setKnownTags] = useState<Tag[]>([])
  // null 表示「没有头图」。它同时是「新建时还没选」与「用户点了移除」，
  // 两种情况下保存的结果一致（都不带头图），所以不需要再分一态。
  const [coverId, setCoverId] = useState<number | null>(null)
  // null 在**新建**时表示「用服务器当前时间」；编辑时初始化自 created_at，
  // 因此只有用户显式清空才会再次变成 null（那时保持原发布时间不变）。
  const [publishedAt, setPublishedAt] = useState<Date | null>(null)
  const [coverBusy, setCoverBusy] = useState(false)
  const [saving, setSaving] = useState(false)
  // AI 翻译独立于保存：它只填充另一个语言页签，不写库、不改文章。
  const [translating, setTranslating] = useState(false)

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
        // 声明按语言独立；旧后端没有 ai_generated_en（undefined）时，
        // 英文开关沿用中文标记，避免升级前标过 AI 的文章突然变成「不是 AI」。
        setAiGeneratedZH(article.ai_generated === true)
        setAiGeneratedEN((article.ai_generated_en ?? article.ai_generated) === true)
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

  // 标签建议来自全量标签列表。失败时静默留空——标签框的核心能力是「输入后回车
  // 创建」，下拉建议只是让已有标签可点选，不该因为一次读取失败而报错打扰写文章。
  useEffect(() => {
    let alive = true
    fetchAllTags()
      .then((list) => {
        if (alive) setKnownTags(list)
      })
      .catch(() => {
        if (alive) setKnownTags([])
      })
    return () => {
      alive = false
    }
  }, [])

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

  // 把当前页签的标题与正文翻译到另一种语言。
  //
  // 三条产品决策都在这里：
  //   - 目标页签非空时先确认，避免一次误点覆盖已写好的译文；
  //   - 翻译成功后只把**目标语言**的「AI 生成」置为 true：译文是 AI 产物，
  //     原文若是人写的，不该被连带认领 AI 声明；
  //   - 成功后切到目标页签，让用户直接审阅译文，而不是留在原文页签猜结果。
  const translateToOther = async () => {
    const target: 'zh' | 'en' = langTab === 'zh' ? 'en' : 'zh'
    const targetTitle = target === 'en' ? titleEN : title
    const targetContent = target === 'en' ? contentEN : content
    if (!activeTitle.trim()) {
      toast(t('请先输入标题再翻译', 'Enter a title before translating'))
      return
    }
    if (targetTitle.trim() !== '' || targetContent.trim() !== '') {
      const confirmed = await dialog.confirm(
        t('覆盖目标语言？', 'Overwrite the other language?'),
        t(
          '目标语言的标题或正文已有内容，继续翻译会整体替换它们。',
          'The other language already has a title or body. Translating will replace both.',
        ),
      )
      if (!confirmed) return
    }

    setTranslating(true)
    try {
      const translated = await blog.translateArticle({
        title: activeTitle.trim(),
        content: activeContent,
        from: langTab,
        to: target,
      })
      if (target === 'en') {
        setTitleEN(translated.title)
        setContentEN(translated.content)
      } else {
        setTitle(translated.title)
        setContent(translated.content)
      }
      // 翻译产出的内容属于目标语言，只标目标语言那一份：
      // 手写中文被翻译成英文时，中文仍是人写的，不该替它认领 AI 声明。
      if (target === 'en') setAiGeneratedEN(true)
      else setAiGeneratedZH(true)
      setLangTab(target)
      toast(t('翻译完成，已切换到目标语言', 'Translated and switched to the target language'))
    } catch (error) {
      toast(getErrorMessage(error, t('翻译失败', 'Translation failed')))
    } finally {
      setTranslating(false)
    }
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
          aiGenerated: aiGeneratedZH,
          aiGeneratedEn: aiGeneratedEN,
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
          aiGenerated: aiGeneratedZH,
          aiGeneratedEn: aiGeneratedEN,
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
        <Button
          ghost
          small
          disabled={saving || translating}
          onClick={() => void translateToOther()}
        >
          {translating
            ? t('翻译中…', 'Translating…')
            : langTab === 'zh'
              ? t('AI 翻译为英文', 'Translate to English')
              : t('AI 翻译为中文', 'Translate to Chinese')}
        </Button>
        <Button primary small disabled={saving || translating || coverBusy} onClick={() => void save()}>{t('保存', 'Save')}</Button>
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
        <Select
          mode="tags"
          value={tags}
          onChange={(vals) => setTags(vals.map(String))}
          // 值是文章标签的规范名（中文列），展示名跟随界面语言；标签没有译名时
          // tagName 会回退中文，与前台标签云一致。
          options={knownTags.map((tag) => ({ value: tag.name, label: tagName(tag, locale) }))}
          placeholder={t('输入标签后回车', 'Type a tag and press Enter')}
        />
      </div>
      <div className="ed-meta">
        <span>{t('AI 生成（中文）', 'AI-generated (Chinese)')}</span>
        <Switch
          checked={aiGeneratedZH}
          onChange={(event) => setAiGeneratedZH(event.target.checked)}
          checkedChildren={t('是', 'Yes')}
          unCheckedChildren={t('否', 'No')}
        />
        <span className="ed-hint">
          {t('只影响中文读者看到的「本文由 AI 生成」提示。', 'Only controls the AI notice Chinese readers see.')}
        </span>
      </div>
      <div className="ed-meta">
        <span>{t('AI 生成（英文）', 'AI-generated (English)')}</span>
        <Switch
          checked={aiGeneratedEN}
          onChange={(event) => setAiGeneratedEN(event.target.checked)}
          checkedChildren={t('是', 'Yes')}
          unCheckedChildren={t('否', 'No')}
        />
        <span className="ed-hint">
          {t('只影响英文读者看到的提示，与中文开关互不影响。', 'Only controls the notice English readers see; independent of the Chinese flag.')}
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
