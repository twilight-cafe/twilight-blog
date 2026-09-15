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
  const toast = useToast()
  const [tags, setTags] = useState<Tag[]>([])
  const [loading, setLoading] = useState(true)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editingName, setEditingName] = useState('')

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
      toast('标签名不能为空')
      return
    }
    try {
      await blog.renameTag(tag.id, { name: editingName.trim() })
      toast('已保存')
      setEditingId(null)
      await load()
    } catch (error) {
      toast(getErrorMessage(error))
    }
  }

  if (loading) return <Loading height={200} />

  return (
    <>
      <div className="ph">
        <h2>标签</h2>
        <span className="kicker" style={{ marginTop: 6 }}>TAGS · {tags.length}</span>
      </div>
      <div className="pcard rv">
        <div className="bd2" style={{ padding: '8px 16px' }}>
          {tags.length === 0 ? <Empty text="暂无标签" /> : tags.map((tag) => (
            <div key={tag.id} className="frow">
              {editingId === tag.id ? (
                <>
                  <Input value={editingName} onChange={(event) => setEditingName(event.target.value)} style={{ maxWidth: 240 }} />
                  <Button small primary onClick={() => void save(tag)}>保存</Button>
                  <Button small ghost onClick={() => setEditingId(null)}>取消</Button>
                </>
              ) : (
                <>
                  <Chip>{tag.name}</Chip>
                  <span className="mono" style={{ fontSize: 11, color: 'var(--faint)' }}>{tag.article_count} 篇</span>
                  <span style={{ flex: 1 }} />
                  <Button ghost small onClick={() => { setEditingId(tag.id); setEditingName(tag.name) }}>编辑</Button>
                </>
              )}
            </div>
          ))}
        </div>
      </div>
    </>
  )
}
