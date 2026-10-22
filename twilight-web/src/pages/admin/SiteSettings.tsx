// SiteSettings.tsx —— 站点设置（仅管理员，/admin/site-settings）
//
// 三项设置：网站描述、代码块最大显示行数、首页每页文章数。前两项属于「面向所有
// 读者的站点级渲染参数」：描述出现在公开站点的五处文案落点；行数决定块级代码框
// 的限高（超出部分块内滚动）。第三项决定首页列表一页放几篇，多的分页展示。
// 读接口 GET /api/site 公开（内容本来就展示给所有人）；写接口 PUT /api/admin/site
// 仅站长（AdminLayout 的守卫会先把非管理员挡在这一页之外，后端再兜一次 403——
// 前端守卫只是省一次注定失败的请求）。
//
// 页面刻意做成「左边写、右边立即预览」：描述最终出现在首页标语、页脚简介、
// 侧栏作者卡、首页标签页标题与 <meta name="description"> 五个地方，只给一个
// 输入框的话，站长没法判断写多长才不会被截断。预览用的是与前台同一套类名，
// 所见即所得。
//
// 长度计数按**折叠空白后**的字符数算，而不是输入框里的原始字符数——服务端
// 落库前会把换行与连续空白折叠成单个空格（见 viewmodel.normalizeSiteDescription），
// 拿原始长度计数会让「看起来超了、其实没超」成为一种日常困惑。
//
// 三项设置共享一个表单、一次保存：分两次提交的话，「看起来是一个表单」的
// 界面会出现两个互相覆盖的保存时刻（保存描述把行数打回旧值，或反之）。

import { useEffect, useMemo, useState } from 'react'
import { Button, Input, Switch } from 'ink-design'
import { getErrorMessage } from '../../lib/client'
import {
  DEFAULT_BORDER_RADIUS,
  DEFAULT_CODE_LINES,
  DEFAULT_HOME_PAGE_SIZE,
  DESCRIPTION_MAX,
  FALLBACK_DESCRIPTION,
  loadAdminSiteSettings,
  loadSiteMeta,
  saveSiteSettings,
} from '../../lib/site'
import { useToast } from '../../lib/toast'
import { Loading } from '../../components/common'
import { useDelayedLoading } from '../../lib/loading'
import AuthorAvatar from '../../components/AuthorAvatar'
import { useI18n } from '../../lib/i18n'

/**
 * 与后端 normalizeSiteDescription 镜像：换行/制表符折成空格，再压缩连续空白。
 * 只用于「计数」与「是否改过」的判断，真正的清洗仍由服务端做——前端这份只是
 * 让界面不要骗人，不能当成校验。
 */
function collapse(value: string): string {
  return value.replace(/\s+/g, ' ').trim()
}

/** 与后端 viewmodel 的 Min/MaxCodeBlockLines 镜像；改一处必须改另一处。 */
const CODE_LINES_MIN = 3
const CODE_LINES_MAX = 500

/**
 * 与后端 viewmodel 的 Min/MaxHomePageSize 镜像；改一处必须改另一处。
 *
 * 上界 12 不是「看起来合理」而是**硬约束**：首页发出的 page_size 会被列表端点
 * 按 handler.maxPageSize（同为 12）校验，设得更大只会让首页第一次请求就 400。
 * 所以这里既限制了输入框，也让后端在保存时二次校验（viewmodel.Validate）。
 */
const HOME_PAGE_MIN = 1
const HOME_PAGE_MAX = 12

/**
 * 与后端 viewmodel 的 Min/MaxBorderRadius 镜像；改一处必须改另一处。
 *
 * 下界 0 是**合法值**（直角），与上面两个常量最要紧的区别就在这里：
 * 0 表示「我要直角」，不是「没填」。所以校验写的是「是否落在区间内」，
 * 绝不能写成 `radius <= 0` 之类的早退——那会让站长永远设不出直角。
 *
 * 区间只是审美边界：圆角过大只会难看，不会让任何请求失败（不像首页每页
 * 篇数的上界被 handler.maxPageSize 钉死，越界会让首页 400）。
 */
const RADIUS_MIN = 0
const RADIUS_MAX = 24

/**
 * 与后端 viewmodel 的 Min/MaxPostBannerHeight 镜像；改一处必须改另一处。
 * 0 在这里**不是**合法值：它只表示旧配置里的「未设置」。
 */
const BANNER_MIN = 120
const BANNER_MAX = 800
const BANNER_DEFAULT = 400

/** 只做「看起来像邮箱」的前端提示；真正的校验在服务端。 */
const EMAIL_PATTERN = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/

export default function SiteSettings() {
  const { t } = useI18n()
  const toast = useToast()
  const [value, setValue] = useState('')
  const [saved, setSaved] = useState('')
  // 行数用文本态：数字输入框允许中途为空（正在擦掉重输），把空串硬转成 0
  // 只会让「还没输完」显示成非法值。提交前再 parse。
  const [linesText, setLinesText] = useState(String(DEFAULT_CODE_LINES))
  const [savedLines, setSavedLines] = useState(DEFAULT_CODE_LINES)
  // 首页每页文章数同样用文本态：理由与行数一致（允许输入中途为空）。
  const [pageSizeText, setPageSizeText] = useState(String(DEFAULT_HOME_PAGE_SIZE))
  const [savedPageSize, setSavedPageSize] = useState(DEFAULT_HOME_PAGE_SIZE)
  // 圆角同样是文本态：允许输入中途为空。
  const [radiusText, setRadiusText] = useState(String(DEFAULT_BORDER_RADIUS))
  const [savedRadius, setSavedRadius] = useState(DEFAULT_BORDER_RADIUS)
  // 头图高度同样用文本态：允许输入中途为空。
  const [bannerText, setBannerText] = useState(String(BANNER_DEFAULT))
  const [savedBanner, setSavedBanner] = useState(BANNER_DEFAULT)
  const [moments, setMoments] = useState(true)
  const [savedMoments, setSavedMoments] = useState(true)
  // 评论通知地址：空串表示关闭通知。
  const [notifyText, setNotifyText] = useState('')
  const [savedNotify, setSavedNotify] = useState('')
  const [loading, setLoading] = useState(true)
  // 站点设置通常一次返回；LOADING 只在超过阈值时显示。
  const pending = useDelayedLoading(loading)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let alive = true
    // loadSiteMeta 内部已兜住失败（回退到内置值）；管理端快照单独失败时
    // 只把通知地址留空，不影响其余设置的回显。
    void Promise.all([
      loadSiteMeta(),
      loadAdminSiteSettings().catch(() => null),
    ])
      .then(([meta, admin]) => {
        if (!alive) return
        setValue(meta.description)
        setSaved(meta.description)
        setLinesText(String(meta.codeBlockMaxLines))
        setSavedLines(meta.codeBlockMaxLines)
        setPageSizeText(String(meta.homePageSize))
        setSavedPageSize(meta.homePageSize)
        setRadiusText(String(meta.borderRadius))
        setSavedRadius(meta.borderRadius)
        setBannerText(String(meta.postBannerHeight))
        setSavedBanner(meta.postBannerHeight)
        setMoments(meta.momentsEnabled)
        setSavedMoments(meta.momentsEnabled)
        const notify = admin?.comment_notify_to ?? ''
        setNotifyText(notify)
        setSavedNotify(notify)
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

  const lines = Number.parseInt(linesText, 10)
  const linesValid = Number.isInteger(lines) && lines >= CODE_LINES_MIN && lines <= CODE_LINES_MAX
  const pageSize = Number.parseInt(pageSizeText, 10)
  const pageSizeValid = Number.isInteger(pageSize) && pageSize >= HOME_PAGE_MIN && pageSize <= HOME_PAGE_MAX
  // 注意这里没有「0 视为无效」的判断：0 是合法的直角。
  const radius = Number.parseInt(radiusText, 10)
  const radiusValid = Number.isInteger(radius) && radius >= RADIUS_MIN && radius <= RADIUS_MAX
  const banner = Number.parseInt(bannerText, 10)
  const bannerValid = Number.isInteger(banner) && banner >= BANNER_MIN && banner <= BANNER_MAX
  const trimmedNotify = notifyText.trim()
  const notifyValid = trimmedNotify === '' || EMAIL_PATTERN.test(trimmedNotify)
  const dirty =
    normalized !== saved ||
    (linesValid && lines !== savedLines) ||
    (pageSizeValid && pageSize !== savedPageSize) ||
    (radiusValid && radius !== savedRadius) ||
    (bannerValid && banner !== savedBanner) ||
    moments !== savedMoments ||
    trimmedNotify !== savedNotify

  const submit = async () => {
    if (tooLong) {
      toast(t(`网站描述不能超过 ${DESCRIPTION_MAX} 个字符`, `The site description must be at most ${DESCRIPTION_MAX} characters`))
      return
    }
    if (!linesValid) {
      toast(t(`代码块行数必须是 ${CODE_LINES_MIN} 到 ${CODE_LINES_MAX} 之间的整数`, `Code lines must be an integer between ${CODE_LINES_MIN} and ${CODE_LINES_MAX}`))
      return
    }
    if (!pageSizeValid) {
      toast(t(`首页每页文章数必须是 ${HOME_PAGE_MIN} 到 ${HOME_PAGE_MAX} 之间的整数`, `Posts per page must be an integer between ${HOME_PAGE_MIN} and ${HOME_PAGE_MAX}`))
      return
    }
    if (!radiusValid) {
      toast(t(`圆角必须是 ${RADIUS_MIN} 到 ${RADIUS_MAX} 之间的整数`, `The radius must be an integer between ${RADIUS_MIN} and ${RADIUS_MAX}`))
      return
    }
    if (!bannerValid) {
      toast(t(`头图高度必须是 ${BANNER_MIN} 到 ${BANNER_MAX} 之间的整数`, `Banner height must be an integer between ${BANNER_MIN} and ${BANNER_MAX}`))
      return
    }
    if (!notifyValid) {
      toast(t('评论通知邮箱格式不正确', 'The notification email address is not valid'))
      return
    }
    setBusy(true)
    try {
      // 服务端返回的是清洗后的最终值，用它覆盖输入框：否则「我明明删了那两个
      // 空格，怎么还是未保存状态」会一直缠着站长。
      const meta = await saveSiteSettings({
        description: normalized,
        codeBlockMaxLines: lines,
        homePageSize: pageSize,
        borderRadius: radius,
        postBannerHeight: banner,
        momentsEnabled: moments,
        commentNotifyTo: trimmedNotify,
      })
      setValue(meta.description)
      setSaved(meta.description)
      setLinesText(String(meta.codeBlockMaxLines))
      setSavedLines(meta.codeBlockMaxLines)
      setPageSizeText(String(meta.homePageSize))
      setSavedPageSize(meta.homePageSize)
      setRadiusText(String(meta.borderRadius))
      setSavedRadius(meta.borderRadius)
      setBannerText(String(meta.postBannerHeight))
      setSavedBanner(meta.postBannerHeight)
      setMoments(meta.momentsEnabled)
      setSavedMoments(meta.momentsEnabled)
      setNotifyText(trimmedNotify)
      setSavedNotify(trimmedNotify)
      toast(meta.description ? t('站点设置已更新', 'Site settings updated') : t('已清空网站描述，前台将使用内置文案', 'Description cleared — the built-in copy will be used'))
    } catch (error) {
      toast(getErrorMessage(error, t('保存失败', 'Save failed')))
    } finally {
      setBusy(false)
    }
  }

  if (loading) return <Loading height={220} visible={pending} />

  return (
    <>
      <div className="ph">
        <h2>{t('站点设置', 'Site settings')}</h2>
        <span className="kicker" style={{ marginTop: 6 }}>SITE SETTINGS · ADMIN</span>
      </div>
      <p className="hint" style={{ marginBottom: 16 }}>
        {t('这里的文案面向所有读者。它同时用于首页标语、页脚简介、侧栏作者卡、首页标签页标题与搜索引擎摘要；留空则回退到内置文案。', 'This copy is shown to every reader: home slogan, footer intro, author card, home page title and search snippet. Leave it empty to fall back to the built-in copy.')}
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
            <b>{t('站点参数', 'Site parameters')}</b>
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
              {t(`换行与连续空格会被自动折叠成一个空格；上限 ${DESCRIPTION_MAX} 字（按字符数，不是字节数）。`, `Line breaks and repeated spaces collapse into one space; the limit is ${DESCRIPTION_MAX} characters (not bytes).`)}
            </p>
            <div className="fld" style={{ marginTop: 14 }}>
              <label>{t('代码块最大显示行数', 'Max code block lines')}</label>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                <Input
                  variant="box"
                  type="number"
                  min={CODE_LINES_MIN}
                  max={CODE_LINES_MAX}
                  step={1}
                  value={linesText}
                  onChange={(event) => setLinesText(event.target.value)}
                  style={{ width: 110 }}
                />
                <span className="hint" style={{ margin: 0 }}>
                  {t(`文章里的块级代码框超过这个行数后，在代码框内上下滚动（${CODE_LINES_MIN}–${CODE_LINES_MAX} 行）。`, `Code blocks longer than this scroll inside their box (${CODE_LINES_MIN}–${CODE_LINES_MAX} lines).`)}
                </span>
              </div>
            </div>
            <div className="fld" style={{ marginTop: 14 }}>
              <label>{t('首页每页文章数', 'Posts per home page')}</label>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                <Input
                  variant="box"
                  type="number"
                  min={HOME_PAGE_MIN}
                  max={HOME_PAGE_MAX}
                  step={1}
                  value={pageSizeText}
                  onChange={(event) => setPageSizeText(event.target.value)}
                  style={{ width: 110 }}
                />
                <span className="hint" style={{ margin: 0 }}>
                  {t(`首页每页展示的文章数，超出的分页展示（${HOME_PAGE_MIN}–${HOME_PAGE_MAX} 篇）。`, `How many posts each home page shows; the rest are paginated (${HOME_PAGE_MIN}–${HOME_PAGE_MAX}).`)}
                </span>
              </div>
            </div>
            <div className="fld" style={{ marginTop: 14 }}>
              <label>{t('圆角', 'Corner radius')}</label>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                <Input
                  variant="box"
                  type="number"
                  min={RADIUS_MIN}
                  max={RADIUS_MAX}
                  step={1}
                  value={radiusText}
                  onChange={(event) => setRadiusText(event.target.value)}
                  style={{ width: 110 }}
                />
                <span className="hint" style={{ margin: 0 }}>
                  {t(`卡片、按钮、输入框等方形排版件的统一圆角，单位 px（${RADIUS_MIN}–${RADIUS_MAX}，0 为直角）。头像等圆形元素、标签徽章与代码框不跟随。`, `Unified radius for cards, buttons and inputs, in px (${RADIUS_MIN}–${RADIUS_MAX}; 0 means square). Avatars, badges and code boxes keep their shape.`)}
                </span>
              </div>
              {/* 圆角是「看一眼就懂」的参数，给一排实时预览比文字描述省事得多。
                  用 radiusValid ? radius : savedRadius：输入框清空的中间态不该
                  把预览闪成 0。 */}
              <div className="radius-preview">
                <span
                  className="radius-preview-item"
                  style={{ borderRadius: `${radiusValid ? radius : savedRadius}px` }}
                >
                  {t('卡片', 'Card')}
                </span>
                <span
                  className="radius-preview-btn"
                  style={{ borderRadius: `${radiusValid ? radius : savedRadius}px` }}
                >
                  {t('按钮', 'Button')}
                </span>
                <span className="radius-preview-avatar">{t('头像不跟随', 'Avatars keep their shape')}</span>
              </div>
            </div>
            <div className="fld" style={{ marginTop: 14 }}>
              <label>{t('文章头图高度', 'Post banner height')}</label>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                <Input
                  variant="box"
                  type="number"
                  min={BANNER_MIN}
                  max={BANNER_MAX}
                  step={1}
                  value={bannerText}
                  onChange={(event) => setBannerText(event.target.value)}
                  style={{ width: 110 }}
                />
                <span className="hint" style={{ margin: 0 }}>
                  {t(`文章页头图的最大显示高度，单位 px（${BANNER_MIN}–${BANNER_MAX}）。竖版长图会被裁掉边缘，不会把标题推出首屏。`, `Maximum cover height on post pages, in px (${BANNER_MIN}–${BANNER_MAX}). Tall images are cropped instead of pushing the title off-screen.`)}
                </span>
              </div>
            </div>
            <div className="fld" style={{ marginTop: 14 }}>
              <label>{t('微语', 'Moments')}</label>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                <Switch checked={moments} onChange={(event) => setMoments(event.target.checked)} />
                <span className="hint" style={{ margin: 0 }}>
                  {moments ? t('已启用：前台显示微语入口。', 'Enabled: the public site shows Moments.') : t('已关闭：前台隐藏入口，公开接口返回 404。', 'Disabled: the public entry is hidden and the public API returns 404.')}
                  {t('已发布的内容不会被删除，重新开启即可恢复。', 'Published content is not deleted; re-enabling restores it.')}
                </span>
              </div>
            </div>
            <div className="fld" style={{ marginTop: 14 }}>
              <label>{t('评论通知邮箱', 'Comment notification email')}</label>
              <Input
                variant="box"
                type="email"
                placeholder="owner@example.com"
                value={notifyText}
                onChange={(event) => setNotifyText(event.target.value)}
              />
              <p className="hint" style={{ marginTop: 8 }}>
                {t('有新的访客评论时发一封纯文本通知到这里。留空表示关闭通知；站长自己发表的评论不会触发通知。', 'A plain-text notice is sent here when a visitor comments. Leave empty to disable; the owner’s own comments never trigger it.')}
              </p>
            </div>
            <div className="act-row" style={{ marginTop: 14 }}>
              <Button
                ghost
                small
                type="button"
                disabled={busy || (value === '' && saved === '')}
                onClick={() => setValue('')}
              >
                {t('清空描述', 'Clear description')}
              </Button>
              <span className="sp" />
              <span className="set-state">
                {/* key 绑在状态上：文字换掉时那一小块重挂载，CSS 的淡入才会重播
                    ——否则「已保存 / 有未保存的修改」只是瞬间换了个词。 */}
                <span className="swap-text" key={String(dirty)}>{dirty ? t('有未保存的修改', 'Unsaved changes') : t('已保存', 'Saved')}</span>
              </span>
              <Button
                primary
                small
                type="submit"
                disabled={busy || tooLong || !linesValid || !pageSizeValid || !radiusValid || !bannerValid || !notifyValid || !dirty}
              >
                {busy ? t('保存中…', 'Saving…') : t('保存', 'Save')}
              </Button>
            </div>
          </div>
        </form>

        <div className="pcard rv">
          <div className="ph2"><b>{t('预览', 'Preview')}</b><span className="sp" /><span className="set-state">{t('前台实际效果', 'As seen by readers')}</span></div>
          <div className="bd2 set-preview">
            <div className="set-preview-block">
              <span className="kicker">{t('首页标语', 'Home slogan')}</span>
              <div className="set-slogan">{normalized || FALLBACK_DESCRIPTION}</div>
            </div>
            <div className="set-preview-block">
              <span className="kicker">{t('页脚简介', 'Footer intro')}</span>
              <p className="set-footer">{normalized || FALLBACK_DESCRIPTION}</p>
            </div>
            <div className="set-preview-block">
              <span className="kicker">{t('侧栏作者卡', 'Author card')}</span>
              {/* 与 PublicSidebar 的作者卡同款标记：预览要是另一套排版，
                  它就只是在描述一个不会发生的事情。头像也是真的那一张
                  （在「账号」页设置），这里只预览描述，不预览头像。 */}
              <div className="public-sidebar set-sidebar">
                <div className="box">
                  <div className="author">
                    <AuthorAvatar />
                    <div>
                      <b>Twilight</b>
                      <p>{normalized || FALLBACK_DESCRIPTION}</p>
                    </div>
                  </div>
                </div>
              </div>
            </div>
            <div className="set-preview-block">
              <span className="kicker">{t('首页标签页标题', 'Home page title')}</span>
              <div className="set-title">Twilight · {normalized || FALLBACK_DESCRIPTION}</div>
            </div>
          </div>
        </div>
      </div>
    </>
  )
}
