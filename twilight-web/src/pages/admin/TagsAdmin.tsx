// TagsAdmin.tsx —— 标签管理
// 标签一次性全量拉取（fetchAllTags）。本页只支持「重命名」（renameTag）：没有
// 新增/删除标签的入口——新标签是在文章引用时由后端隐式创建的，删除则跟随文章
// 去标签，所以这里看不到 CRUD 里的 C/D。

import { useCallback, useEffect, useState } from 'react'
import { Button, Chip, Input } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import type { Tag } from 'twilight-blog-api'
import { useToast } from '../../lib/toast'
import { Empty, Loading } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import { useI18n } from '../../lib/i18n'

async function fetchAllTags(): Promise<Tag[]> {
  const first = await blog.listTags({ page: 1, pageSize: 12 })
  if (first.total_pages <= 1) return first.items
  const rest = await Promise.all(
    Array.from({ length: first.total_pages - 1 }, (_, index) =>
      blog.listTags({ page: index + 2, pageSize: 12 }),
    ),
  )
  return [...first.items, ...rest.flatMap((page) => page.items)]
}

export default function TagsAdmin() {
  const { t } = useI18n()
  const toast = useToast()
  const [tags, setTags] = useState<Tag[]>([])
  const [loading, setLoading] = useState(true)
  // 首屏加载超过阈值才显示等待提示（见 lib/loading.ts）。
  const pending = useDelayedLoading(loading)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editingName, setEditingName] = useState('')
  const [editingNameEN, setEditingNameEN] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setTags(await fetchAllTags())
    } catch {
      setTags([])
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const save = async (tag: Tag) => {
    if (!editingName.trim()) {
      toast(t('标签名不能为空', 'The tag name cannot be empty'))
      return
    }
    try {
      await blog.renameTag(tag.id, { name: editingName.trim(), nameEn: editingNameEN.trim() })
      toast(t('已保存', 'Saved'))
      setEditingId(null)
      await load()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  return (
    <>
      <div className="ph">
        <h2>{t('标签', 'Tags')}</h2>
        <span className="kicker" style={{ marginTop: 6 }}>TAGS · {tags.length}</span>
      </div>
      <div className="pcard rv">
        {/* 重命名后的刷新不再把整页换成 LOADING：页头与卡片留在原位，旧行
            变暗、新行替换（第一次加载仍然显示等待提示）。 */}
        <div className="bd2" style={{ padding: '8px 16px' }} aria-busy={loading} data-busy={String(loading)}>
          {tags.length === 0 ? (
            loading ? <Loading height={120} visible={pending} /> : <Empty text={t('暂无标签', 'No tags yet')} />
          ) : tags.map((tag) => (
            <div key={tag.id} className="frow">
              {editingId === tag.id ? (
                <>
                  <Input value={editingName} onChange={(event) => setEditingName(event.target.value)} placeholder={t('中文名', 'Chinese name')} style={{ maxWidth: 200 }} />
                  <Input value={editingNameEN} onChange={(event) => setEditingNameEN(event.target.value)} placeholder={t('英文名（可空）', 'English name (optional)')} style={{ maxWidth: 200 }} />
                  <Button small primary onClick={() => void save(tag)}>{t('保存', 'Save')}</Button>
                  <Button small ghost onClick={() => setEditingId(null)}>{t('取消', 'Cancel')}</Button>
                </>
              ) : (
                <>
                  <Chip>{tag.name}</Chip>
                  {tag.name_en ? <Chip>{tag.name_en}</Chip> : null}
                  <span className="mono" style={{ fontSize: 11, color: 'var(--faint)' }}>{tag.article_count} {t('篇', 'posts')}</span>
                  <span style={{ flex: 1 }} />
                  <Button ghost small onClick={() => { setEditingId(tag.id); setEditingName(tag.name); setEditingNameEN(tag.name_en) }}>{t('编辑', 'Edit')}</Button>
                </>
              )}
            </div>
          ))}
        </div>
      </div>
    </>
  )
}
