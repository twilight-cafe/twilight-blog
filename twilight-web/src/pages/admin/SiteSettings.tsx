// SiteSettings.tsx —— 站点设置（仅管理员，/admin/site-settings）
//
// 目前只有「网站描述」一项。它就是公开站点上那段给所有读者看的文案，因此：
//   - 读接口 GET /api/site 是公开的（描述本来就展示给所有人）；
//   - 写接口 PUT /api/admin/site 仅站长（AdminLayout 的守卫会先把非管理员挡在
//     这一页之外，后端再兜一次 403——前端守卫只是省一次注定失败的请求）。
//
// 页面刻意做成「左边写、右边立即预览」：描述最终出现在首页标语、页脚简介、
// 侧栏作者卡、首页标签页标题与 <meta name="description"> 五个地方，只给一个
// 输入框的话，站长没法判断写多长才不会被截断。预览用的是与前台同一套类名，
// 所见即所得。
//
// 长度计数按**折叠空白后**的字符数算，而不是输入框里的原始字符数——服务端
// 落库前会把换行与连续空白折叠成单个空格（见 viewmodel.normalizeSiteDescription），
// 拿原始长度计数会让「看起来超了、其实没超」成为一种日常困惑。

import { useEffect, useMemo, useState } from 'react'
import { Button, Input } from 'ink-design'
import { getErrorMessage } from '../../lib/client'
import {
  DESCRIPTION_MAX,
  FALLBACK_DESCRIPTION,
  loadSiteDescription,
  saveSiteDescription,
} from '../../lib/site'
import { useToast } from '../../lib/toast'
import { Loading } from '../../components/common'

/**
 * 与后端 normalizeSiteDescription 镜像：换行/制表符折成空格，再压缩连续空白。
 * 只用于「计数」与「是否改过」的判断，真正的清洗仍由服务端做——前端这份只是
 * 让界面不要骗人，不能当成校验。
 */
function collapse(value: string): string {
  return value.replace(/\s+/g, ' ').trim()
}

export default function SiteSettings() {
  const toast = useToast()
  const [value, setValue] = useState('')
  const [saved, setSaved] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let alive = true
    // loadSiteDescription 内部已兜住失败（返回空串），这里不需要 catch。
    void loadSiteDescription()
      .then((current) => {
        if (!alive) return
        setValue(current)
        setSaved(current)
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [])

  const normalized = useMemo(() => collapse(value), [value])
  // 计数按**码点**而不是 UTF-16 长度：后端的上限是 utf8.RuneCountInString，
  // 而 JS 的 .length 会把 emoji 这类星平面字符算成两个，导致界面先喊「超了」、
  // 服务端却照收。展开成数组再取长度才与后端同口径。
  const count = useMemo(() => [...normalized].length, [normalized])
  const tooLong = count > DESCRIPTION_MAX
  const dirty = normalized !== saved

  const submit = async () => {
    if (tooLong) {
      toast(`网站描述不能超过 ${DESCRIPTION_MAX} 个字符`)
      return
    }
    setBusy(true)
    try {
      // 服务端返回的是清洗后的最终值，用它覆盖输入框：否则「我明明删了那两个
      // 空格，怎么还是未保存状态」会一直缠着站长。
      const result = await saveSiteDescription(normalized)
      setValue(result)
      setSaved(result)
      toast(result ? '网站描述已更新' : '已清空网站描述，前台将使用内置文案')
    } catch (error) {
      toast(getErrorMessage(error, '保存失败'))
    } finally {
      setBusy(false)
    }
  }

  if (loading) return <Loading height={220} />

  return (
    <>
      <div className="ph">
        <h2>站点设置</h2>
        <span className="kicker" style={{ marginTop: 6 }}>SITE SETTINGS · ADMIN</span>
      </div>
      <p className="hint" style={{ marginBottom: 16 }}>
        这里的文案面向所有读者。它同时用于首页标语、页脚简介、侧栏作者卡、首页标签页标题与搜索引擎摘要；
        留空则回退到内置文案「{FALLBACK_DESCRIPTION}」。
      </p>

      <div className="set-grid">
        <form
          className="pcard rv"
          onSubmit={(event) => {
            event.preventDefault()
            void submit()
          }}
        >
          <div className="ph2">
            <b>网站描述</b>
            <span className="sp" />
            <span className={`set-count${tooLong ? ' over' : ''}`}>{count} / {DESCRIPTION_MAX}</span>
          </div>
          <div className="bd2">
            <div className="fld">
              <label>Description</label>
              <Input
                variant="box"
                multiline
                placeholder={FALLBACK_DESCRIPTION}
                value={value}
                onChange={(event) => setValue(event.target.value)}
              />
            </div>
            <p className="hint" style={{ marginTop: 8 }}>
              换行与连续空格会被自动折叠成一个空格；上限 {DESCRIPTION_MAX} 字（按字符数，不是字节数）。
            </p>
            <div className="act-row">
              <Button
                ghost
                small
                type="button"
                disabled={busy || (value === '' && saved === '')}
                onClick={() => setValue('')}
              >
                清空
              </Button>
              <span className="sp" />
              <span className="set-state">
                {dirty ? '有未保存的修改' : '已保存'}
              </span>
              <Button primary small type="submit" disabled={busy || tooLong || !dirty}>
                {busy ? '保存中…' : '保存'}
              </Button>
            </div>
          </div>
        </form>

        <div className="pcard rv">
          <div className="ph2"><b>预览</b><span className="sp" /><span className="set-state">前台实际效果</span></div>
          <div className="bd2 set-preview">
            <div className="set-preview-block">
              <span className="kicker">首页标语</span>
              <div className="set-slogan">{normalized || FALLBACK_DESCRIPTION}</div>
            </div>
            <div className="set-preview-block">
              <span className="kicker">页脚简介</span>
              <p className="set-footer">{normalized || FALLBACK_DESCRIPTION}</p>
            </div>
            <div className="set-preview-block">
              <span className="kicker">侧栏作者卡</span>
              {/* 与 PublicSidebar 的作者卡同款标记：预览要是另一套排版，
                  它就只是在描述一个不会发生的事情。 */}
              <div className="public-sidebar set-sidebar">
                <div className="box">
                  <div className="author">
                    <span className="avatar">T</span>
                    <div>
                      <b>Twilight</b>
                      <p>{normalized || FALLBACK_DESCRIPTION}</p>
                    </div>
                  </div>
                </div>
              </div>
            </div>
            <div className="set-preview-block">
              <span className="kicker">首页标签页标题</span>
              <div className="set-title">Twilight · {normalized || FALLBACK_DESCRIPTION}</div>
            </div>
          </div>
        </div>
      </div>
    </>
  )
}
