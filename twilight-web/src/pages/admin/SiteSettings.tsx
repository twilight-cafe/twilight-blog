// SiteSettings.tsx —— 站点设置（仅管理员，/admin/site-settings）
//
// 三项设置：网站描述、代码块最大显示行数、首页每页文章数。前两项属于「面向所有
// 读者的站点级渲染参数」：描述出现在公开站点的五处文案落点；行数决定块级代码框
// 的限高（超出部分块内滚动）。第三项决定首页列表一页放几篇，多的分页展示。
// 读接口 GET /api/site 公开（内容本来就展示给所有人）；写接口 PUT /api/admin/site
// 仅站长（AdminLayout 的守卫会先把非管理员挡在这一页之外，后端再兜一次 403——
// 前端守卫只是省一次注定失败的请求）。
//
// 页面按「站点文案 / 外观参数 / AI 翻译」三个页签分页：前两组写的是同一段
// settings.json 站点设置，共享一个表单与一次保存；AI 翻译写的是另一个段落，
// 单独一张表单。页签由 ?tab= 决定，刷新与后退都能回到原页签。
//
// 页面刻意做成「左边写、右边立即预览」：描述最终出现在首页标语、页脚简介、
// 侧栏作者卡、首页标签页标题与 <meta name="description"> 五个地方，只给一个
// 输入框的话，站长没法判断写多长才不会被截断。预览用的是与前台同一套类名，
// 所见即所得（预览只在「站点文案」页签里）。
//
// 长度计数按**折叠空白后**的字符数算，而不是输入框里的原始字符数——服务端
// 落库前会把换行与连续空白折叠成单个空格（见 viewmodel.normalizeSiteDescription），
// 拿原始长度计数会让「看起来超了、其实没超」成为一种日常困惑。
//
// 三项设置共享一个表单、一次保存：分两次提交的话，「看起来是一个表单」的
// 界面会出现两个互相覆盖的保存时刻（保存描述把行数打回旧值，或反之）。

import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Button, Input, Select, Switch } from 'ink-design'
import { blog, getErrorMessage } from '../../lib/client'
import {
  DEFAULT_BORDER_RADIUS,
  DEFAULT_CODE_LINES,
  DEFAULT_HOME_PAGE_SIZE,
  DESCRIPTION_MAX,
  FALLBACK_DESCRIPTION,
  FALLBACK_DESCRIPTION_EN,
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

/**
 * 把数字输入框里的文本解析成整数；空串、小数、指数、十六进制与垃圾文本一律返回 NaN。
 *
 * 不能用 Number.parseInt：它会把 "3.5" 读成 3、把 "3abc" 读成 3，用户输入的
 * 与最终保存的是两个不同的值，而且界面不会报错。也不能只靠 Number()：它会把
 * "1e3" 读成 1000、把 "0x10" 读成 16，同样不是用户看到的那组字符。数字框的
 * step 只在原生表单校验里生效，键盘清空/粘贴/程序化提交仍可能把非整数带进来。
 */
function parseInteger(value: string): number {
  const trimmed = value.trim()
  if (!/^[+-]?\d+$/.test(trimmed)) return NaN
  return Number(trimmed)
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

/**
 * 站点设置的三个页签。默认的「站点文案」不写进 URL，地址保持干净；
 * 其余两个写入 `?tab=`，刷新与浏览器后退都能回到同一页签。
 */
type SettingsTab = 'copy' | 'appearance' | 'translate'

const TAB_IDS: readonly SettingsTab[] = ['copy', 'appearance', 'translate']

function parseSettingsTab(value: string | null): SettingsTab {
  return value === 'appearance' || value === 'translate' ? value : 'copy'
}

/**
 * 思维强度的预设档位。空串（不发送 reasoning_effort）不是选项，而是
 * 「未选择」状态，由下拉的 placeholder 呈现——否则它会以标签形式占位，
 * 长文案会把触发器撑高，和同表单的其他单行输入框对不齐。
 */
const THINKING_EFFORTS: ReadonlyArray<{ value: string; zh: string; en: string }> = [
  { value: 'minimal', zh: '极低', en: 'Minimal' },
  { value: 'low', zh: '低', en: 'Low' },
  { value: 'medium', zh: '中', en: 'Medium' },
  { value: 'high', zh: '高', en: 'High' },
  { value: 'xhigh', zh: '超高', en: 'X-High' },
  { value: 'max', zh: '最高', en: 'Max' },
  { value: 'ultra', zh: '极限', en: 'Ultra' },
]

/**
 * AI 翻译配置的默认值与区间，和后端 config 常量镜像。
 *
 * 改一处必须改另一处；越界值会先被前端拦下，服务端仍会再校验一次。
 */
const TRANSLATE_TIMEOUT_MIN = 5
const TRANSLATE_TIMEOUT_MAX = 600
const TRANSLATE_TIMEOUT_DEFAULT = 60
const TRANSLATE_TOKENS_MIN = 256
const TRANSLATE_TOKENS_MAX = 32768
const TRANSLATE_TOKENS_DEFAULT = 4096
const HTTP_URL_PATTERN = /^https?:\/\/[^\s]+$/i

/** 站点设置里 AI 翻译卡的本地快照，用来判断「有未保存的修改」。 */
interface TranslateSnapshot {
  enabled: boolean
  url: string
  model: string
  timeout: number
  maxTokens: number
  prompt: string
  effort: string
}

const EMPTY_TRANSLATE_SNAPSHOT: TranslateSnapshot = {
  enabled: true,
  url: '',
  model: '',
  timeout: TRANSLATE_TIMEOUT_DEFAULT,
  maxTokens: TRANSLATE_TOKENS_DEFAULT,
  prompt: '',
  effort: '',
}

export default function SiteSettings() {
  const { t, locale } = useI18n()
  const toast = useToast()
  // 页签由 URL 决定：默认页签省略 ?tab=，其余页签写入查询参数。
  const [params, setParams] = useSearchParams()
  const tab = parseSettingsTab(params.get('tab'))
  const selectTab = (next: SettingsTab) => {
    const nextParams = new URLSearchParams(params)
    if (next === 'copy') nextParams.delete('tab')
    else nextParams.set('tab', next)
    setParams(nextParams)
  }
  const [value, setValue] = useState('')
  const [saved, setSaved] = useState('')
  // 英文站点描述：英文界面优先展示它，留空则回退中文描述。
  const [valueEN, setValueEN] = useState('')
  const [savedEN, setSavedEN] = useState('')
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
  // notifyKnown 区分「服务端确实说它是空」与「管理端快照没加载成功」：
  // 后者不能把空串当成新值保存回去，否则一次加载失败会静默清掉已保存的邮箱。
  const [notifyText, setNotifyText] = useState('')
  const [savedNotify, setSavedNotify] = useState('')
  const [notifyKnown, setNotifyKnown] = useState(false)
  // AI 翻译：密钥永远不回显。输入框为空表示「这次不改密钥」；
  // 已保存的密钥是否存在由 api_key_configured 单独回答。
  const [translateEnabled, setTranslateEnabled] = useState(true)
  const [translateURL, setTranslateURL] = useState('')
  const [translateModel, setTranslateModel] = useState('')
  const [translateKey, setTranslateKey] = useState('')
  const [translateTimeoutText, setTranslateTimeoutText] = useState(String(TRANSLATE_TIMEOUT_DEFAULT))
  const [translateTokensText, setTranslateTokensText] = useState(String(TRANSLATE_TOKENS_DEFAULT))
  const [translateKeyConfigured, setTranslateKeyConfigured] = useState(false)
  const [translateConfigured, setTranslateConfigured] = useState(false)
  // 提示词模板：加载时用服务端下发的生效模板（未自定义时即内置默认值）。
  const [translatePrompt, setTranslatePrompt] = useState('')
  // 思维强度档位；空串表示不下发 reasoning_effort。
  const [translateEffort, setTranslateEffort] = useState('')
  // 「查询模型」的结果；只在本次会话里有效，不落库。
  const [modelOptions, setModelOptions] = useState<string[]>([])
  const [modelBusy, setModelBusy] = useState(false)
  const [savedTranslate, setSavedTranslate] = useState<TranslateSnapshot>(EMPTY_TRANSLATE_SNAPSHOT)
  const [translateBusy, setTranslateBusy] = useState(false)
  // 翻译配置加载失败：此时界面里全是默认值，保存会把真实的地址/模型/密钥
  // 覆盖成空白。宁可禁用保存并提示刷新，也不能把「没读到」当成「没配置」。
  const [translateLoadFailed, setTranslateLoadFailed] = useState(false)
  const [loading, setLoading] = useState(true)
  // 站点设置通常一次返回；LOADING 只在超过阈值时显示。
  const pending = useDelayedLoading(loading)
  const [busy, setBusy] = useState(false)
  // 站点快照加载失败：表单里只是默认值，保存会把真实设置覆盖成默认值。
  const [siteLoadFailed, setSiteLoadFailed] = useState(false)

  useEffect(() => {
    let alive = true
    // 三个快照各自失败互不影响：站点快照失败会明确禁用保存，管理端快照失败
    // 只把通知地址留空（且保存时不覆盖），翻译配置失败同样禁用其保存。
    void Promise.all([
      loadSiteMeta().catch(() => null),
      loadAdminSiteSettings().catch(() => null),
      blog.getTranslateConfig().catch(() => null),
    ])
      .then(([meta, admin, translation]) => {
        if (!alive) return
        if (meta) {
          setValue(meta.description)
          setSaved(meta.description)
          setValueEN(meta.descriptionEn)
          setSavedEN(meta.descriptionEn)
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
        } else {
          setSiteLoadFailed(true)
        }
        const notify = admin?.comment_notify_to ?? ''
        setNotifyText(notify)
        setSavedNotify(notify)
        setNotifyKnown(Boolean(admin))

        if (translation) {
          const snapshot: TranslateSnapshot = {
            enabled: translation.enabled,
            url: translation.url,
            model: translation.model,
            timeout: translation.timeout_seconds,
            maxTokens: translation.max_tokens,
            prompt: translation.prompt_template ?? '',
            effort: translation.thinking_effort ?? '',
          }
          setTranslateEnabled(snapshot.enabled)
          setTranslateURL(snapshot.url)
          setTranslateModel(snapshot.model)
          setTranslateTimeoutText(String(snapshot.timeout))
          setTranslateTokensText(String(snapshot.maxTokens))
          setTranslatePrompt(snapshot.prompt)
          setTranslateEffort(snapshot.effort)
          setTranslateKeyConfigured(translation.api_key_configured)
          setTranslateConfigured(translation.configured)
          setSavedTranslate(snapshot)
        } else {
          setTranslateLoadFailed(true)
        }
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [])

  const normalized = useMemo(() => collapse(value), [value])
  const normalizedEN = useMemo(() => collapse(valueEN), [valueEN])
  // 计数按**码点**而不是 UTF-16 长度：后端的上限是 utf8.RuneCountInString，
  // 而 JS 的 .length 会把 emoji 这类星平面字符算成两个，导致界面先喊「超了」、
  // 服务端却照收。展开成数组再取长度才与后端同口径。
  const count = useMemo(() => [...normalized].length, [normalized])
  const countEN = useMemo(() => [...normalizedEN].length, [normalizedEN])
  const tooLong = count > DESCRIPTION_MAX || countEN > DESCRIPTION_MAX
  // 未填描述时的内置文案也要跟随界面语言（预览与 placeholder 都显示它）。
  const fallbackDescription = t(FALLBACK_DESCRIPTION, FALLBACK_DESCRIPTION_EN)
  // 预览要展示**当前界面语言下的读者实际会看到的那一份**：英文界面优先英文
  // 描述，英文为空才回退中文（与 useSiteDescription 的规则一致）。若这里固定
  // 取中文，英文站长看到的就是一句自己根本不会在前台看到的文案。
  const previewDescription = locale === 'en'
    ? (normalizedEN || normalized || fallbackDescription)
    : (normalized || fallbackDescription)

  const lines = parseInteger(linesText)
  const linesValid = Number.isInteger(lines) && lines >= CODE_LINES_MIN && lines <= CODE_LINES_MAX
  const pageSize = parseInteger(pageSizeText)
  const pageSizeValid = Number.isInteger(pageSize) && pageSize >= HOME_PAGE_MIN && pageSize <= HOME_PAGE_MAX
  // 注意这里没有「0 视为无效」的判断：0 是合法的直角。
  const radius = parseInteger(radiusText)
  const radiusValid = Number.isInteger(radius) && radius >= RADIUS_MIN && radius <= RADIUS_MAX
  const banner = parseInteger(bannerText)
  const bannerValid = Number.isInteger(banner) && banner >= BANNER_MIN && banner <= BANNER_MAX
  const trimmedNotify = notifyText.trim()
  const notifyValid = trimmedNotify === '' || EMAIL_PATTERN.test(trimmedNotify)
  // 这次请求里通知地址是否可以被当作明确意图下发（见 submit 里的说明）。
  const notifyKnownNow = notifyKnown || trimmedNotify !== ''
  const dirty =
    normalized !== saved ||
    normalizedEN !== savedEN ||
    (linesValid && lines !== savedLines) ||
    (pageSizeValid && pageSize !== savedPageSize) ||
    (radiusValid && radius !== savedRadius) ||
    (bannerValid && banner !== savedBanner) ||
    moments !== savedMoments ||
    trimmedNotify !== savedNotify

  // 翻译配置的本地校验。密钥是否已保存由服务端下发的布尔值回答；
  // 输入框为空只表示「本次不改」，不能当成「没有密钥」。
  const translateTimeout = parseInteger(translateTimeoutText)
  const translateTimeoutValid =
    Number.isInteger(translateTimeout) &&
    translateTimeout >= TRANSLATE_TIMEOUT_MIN &&
    translateTimeout <= TRANSLATE_TIMEOUT_MAX
  const translateTokens = parseInteger(translateTokensText)
  const translateTokensValid =
    Number.isInteger(translateTokens) &&
    translateTokens >= TRANSLATE_TOKENS_MIN &&
    translateTokens <= TRANSLATE_TOKENS_MAX
  const trimmedTranslateURL = translateURL.trim()
  const translateURLValid = trimmedTranslateURL === '' || HTTP_URL_PATTERN.test(trimmedTranslateURL)
  const trimmedTranslateModel = translateModel.trim()
  const trimmedTranslateKey = translateKey.trim()
  const translateComplete =
    trimmedTranslateURL === '' ||
    (trimmedTranslateModel !== '' && (translateKeyConfigured || trimmedTranslateKey !== ''))
  const translateFormValid =
    translateURLValid && translateTimeoutValid && translateTokensValid && translateComplete
  const trimmedPrompt = translatePrompt.trim()
  // 模板是否真的改过。没改时不把「当前生效的默认模板」写回去：
  // 那会把内置模板固化成一份自定义副本，之后升级默认模板就再也享受不到。
  const promptChanged = trimmedPrompt !== savedTranslate.prompt.trim()
  const trimmedEffort = translateEffort.trim()
  const effortOptions = useMemo(
    () => THINKING_EFFORTS.map((option) => ({ value: option.value, label: t(option.zh, option.en) })),
    [t],
  )
  // 提示词模板按裁剪后的值比较：内置模板来自 prompt.tmpl，末尾带一个换行，
  // 而服务端落库前也会 TrimSpace。若直接拿 input.trim() 与原始快照比，
  // 页面刚打开（尚未编辑）就会显示「有未保存的修改」并点亮保存按钮。
  const translateDirty =
    translateEnabled !== savedTranslate.enabled ||
    trimmedTranslateURL !== savedTranslate.url ||
    trimmedTranslateModel !== savedTranslate.model ||
    (translateTimeoutValid && translateTimeout !== savedTranslate.timeout) ||
    (translateTokensValid && translateTokens !== savedTranslate.maxTokens) ||
    promptChanged ||
    trimmedEffort !== savedTranslate.effort ||
    trimmedTranslateKey !== ''

  // 每个页签只按**本页签可见的字段**判断能否提交。另一页签里的错误不能把
  // 保存按钮无声禁用——用户看不到那个字段，也就无从修复；这种跨页签错误
  // 改由 submit 跳到出错的页签并给出提示。
  const tabValid = tab === 'copy'
    ? !tooLong
    : linesValid && pageSizeValid && radiusValid && bannerValid && notifyValid

  const submit = async () => {
    // 表单还能被 Enter 触发；busy 期间重复提交会发两次只差毫秒的保存请求。
    if (busy) return
    // 快照没读到时表单是默认值；提交会把真实设置覆盖成默认值。
    if (siteLoadFailed) {
      toast(t('站点设置尚未加载成功，请刷新页面后重试', 'Site settings did not load. Refresh the page and try again.'))
      return
    }
    if (tooLong) {
      selectTab('copy')
      toast(t(`网站描述不能超过 ${DESCRIPTION_MAX} 个字符`, `The site description must be at most ${DESCRIPTION_MAX} characters`))
      return
    }
    if (!linesValid) {
      selectTab('appearance')
      toast(t(`代码块行数必须是 ${CODE_LINES_MIN} 到 ${CODE_LINES_MAX} 之间的整数`, `Code lines must be an integer between ${CODE_LINES_MIN} and ${CODE_LINES_MAX}`))
      return
    }
    if (!pageSizeValid) {
      selectTab('appearance')
      toast(t(`首页每页文章数必须是 ${HOME_PAGE_MIN} 到 ${HOME_PAGE_MAX} 之间的整数`, `Posts per page must be an integer between ${HOME_PAGE_MIN} and ${HOME_PAGE_MAX}`))
      return
    }
    if (!radiusValid) {
      selectTab('appearance')
      toast(t(`圆角必须是 ${RADIUS_MIN} 到 ${RADIUS_MAX} 之间的整数`, `The radius must be an integer between ${RADIUS_MIN} and ${RADIUS_MAX}`))
      return
    }
    if (!bannerValid) {
      selectTab('appearance')
      toast(t(`头图高度必须是 ${BANNER_MIN} 到 ${BANNER_MAX} 之间的整数`, `Banner height must be an integer between ${BANNER_MIN} and ${BANNER_MAX}`))
      return
    }
    if (!notifyValid) {
      selectTab('appearance')
      toast(t('评论通知邮箱格式不正确', 'The notification email address is not valid'))
      return
    }
    setBusy(true)
    try {
      // 服务端返回的是清洗后的最终值，用它覆盖输入框：否则「我明明删了那两个
      // 空格，怎么还是未保存状态」会一直缠着站长。
      const meta = await saveSiteSettings({
        description: normalized,
        descriptionEn: normalizedEN,
        codeBlockMaxLines: lines,
        homePageSize: pageSize,
        borderRadius: radius,
        postBannerHeight: banner,
        momentsEnabled: moments,
        // 加载失败且用户没填新地址时，不下发这个字段：服务端保持原值。
        // 已知为空（notifyKnown）才把空串当成「关闭通知」的明确意图。
        ...(notifyKnownNow ? { commentNotifyTo: trimmedNotify } : {}),
      })
      setValue(meta.description)
      setSaved(meta.description)
      setValueEN(meta.descriptionEn)
      setSavedEN(meta.descriptionEn)
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
      // 手填过一次之后，服务端确实存下了这个地址；下一次清空才代表「关闭通知」。
      setNotifyKnown(notifyKnownNow)
      // 只有两份描述都为空时，前台才真的回退到内置文案。
      // 只看中文会让「英文描述仍在」的保存被误报成「已清空」。
      toast(meta.description || meta.descriptionEn
        ? t('站点设置已更新', 'Site settings updated')
        : t('已清空网站描述，前台将使用内置文案', 'Description cleared — the built-in copy will be used'))
    } catch (error) {
      toast(getErrorMessage(error, t('保存失败', 'Save failed')))
    } finally {
      setBusy(false)
    }
  }

  /**
   * 保存 AI 翻译配置。与站点设置分开提交：它们写的是 settings.json 里两个
   * 互不相关的段落，合并保存会让「改个模型」顺带提交一个正在编辑的描述。
   */
  const submitTranslate = async () => {
    // 与站点设置同理：Enter 能绕过 disabled 的按钮，重复提交或提交非法配置。
    if (translateBusy) return
    // 配置没读到时表单里是默认值；提交等于用空白覆盖服务端已有的地址/模型。
    if (translateLoadFailed) {
      toast(t('翻译配置尚未加载成功，请刷新页面后重试', 'Translation settings did not load. Refresh the page and try again.'))
      return
    }
    if (!translateFormValid) {
      toast(t('请先修正 AI 翻译配置中的错误', 'Fix the invalid AI translation settings first'))
      return
    }
    setTranslateBusy(true)
    try {
      const saved = await blog.updateTranslateConfig({
        enabled: translateEnabled,
        url: trimmedTranslateURL,
        model: trimmedTranslateModel,
        timeout_seconds: translateTimeout,
        max_tokens: translateTokens,
        // 模板未改就不下发：服务端保持原值（未自定义时仍是内置默认模板）。
        ...(promptChanged ? { prompt_template: trimmedPrompt } : {}),
        thinking_effort: trimmedEffort,
        // 空密钥表示「保持已保存的值」；不把空串发出去，避免服务端误判。
        ...(trimmedTranslateKey === '' ? {} : { api_key: trimmedTranslateKey }),
      })
      const snapshot: TranslateSnapshot = {
        enabled: saved.enabled,
        url: saved.url,
        model: saved.model,
        timeout: saved.timeout_seconds,
        maxTokens: saved.max_tokens,
        prompt: saved.prompt_template ?? '',
        effort: saved.thinking_effort ?? '',
      }
      setTranslateEnabled(snapshot.enabled)
      setTranslateURL(snapshot.url)
      setTranslateModel(snapshot.model)
      setTranslateTimeoutText(String(snapshot.timeout))
      setTranslateTokensText(String(snapshot.maxTokens))
      setTranslatePrompt(snapshot.prompt)
      setTranslateEffort(snapshot.effort)
      setSavedTranslate(snapshot)
      setTranslateKey('')
      setTranslateKeyConfigured(saved.api_key_configured)
      setTranslateConfigured(saved.configured)
      toast(t('AI 翻译设置已更新', 'AI translation settings updated'))
    } catch (error) {
      toast(getErrorMessage(error, t('保存失败', 'Save failed')))
    } finally {
      setTranslateBusy(false)
    }
  }

  /**
   * 查询上游支持的模型。URL / 密钥留空时由服务端沿用已保存的值——
   * 设置页永远拿不到明文密钥，这是唯一能「用现有密钥试一下」的方式。
   */
  const queryModels = async () => {
    if (!translateURLValid) {
      toast(t('请先填写合法的 API 地址', 'Enter a valid API URL first'))
      return
    }
    setModelBusy(true)
    try {
      const result = await blog.listTranslateModels({
        ...(trimmedTranslateURL === '' ? {} : { url: trimmedTranslateURL }),
        ...(trimmedTranslateKey === '' ? {} : { api_key: trimmedTranslateKey }),
      })
      setModelOptions(result.models)
      toast(result.models.length === 0
        ? t('该服务没有返回任何模型', 'The provider returned no models')
        : t(`找到 ${result.models.length} 个模型`, `Found ${result.models.length} models`))
    } catch (error) {
      toast(getErrorMessage(error, t('查询模型失败', 'Failed to query models')))
    } finally {
      setModelBusy(false)
    }
  }

  if (loading) return <Loading height={220} visible={pending} />

  return (
    <>
      <div className="ph">
        <h2>{t('站点设置', 'Site settings')}</h2>
        <span className="kicker" style={{ marginTop: 6 }}>SITE SETTINGS · ADMIN</span>
      </div>
      <div className="set-tabs" role="tablist" aria-label={t('站点设置分页', 'Site settings sections')}>
        {TAB_IDS.map((id) => (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={tab === id}
            className={tab === id ? 'on' : ''}
            onClick={() => selectTab(id)}
          >
            {id === 'copy'
              ? t('站点文案', 'Site copy')
              : id === 'appearance'
                ? t('外观参数', 'Appearance')
                : t('AI 翻译', 'AI translation')}
          </button>
        ))}
      </div>

      {siteLoadFailed ? (
        <p className="hint" style={{ marginBottom: 16 }}>
          {t(
            '站点设置加载失败。为避免用默认值覆盖已保存的配置，本次不能保存；请刷新页面后重试。',
            'Site settings failed to load. Saving is disabled so the defaults cannot overwrite your saved settings; refresh the page and try again.',
          )}
        </p>
      ) : null}

      {tab === 'copy' ? (
        <p className="hint" style={{ marginBottom: 16 }}>
          {t('这里的文案面向所有读者。它同时用于首页标语、页脚简介、侧栏作者卡、首页标签页标题与搜索引擎摘要；留空则回退到内置文案。', 'This copy is shown to every reader: home slogan, footer intro, author card, home page title and search snippet. Leave it empty to fall back to the built-in copy.')}
        </p>
      ) : null}

      {tab === 'translate' ? null : (
      <div className={tab === 'copy' ? 'set-grid' : undefined}>
        <form
          className="pcard rv"
          onSubmit={(event) => {
            event.preventDefault()
            void submit()
          }}
        >
          <div className="ph2">
            <b>{tab === 'copy' ? t('站点文案', 'Site copy') : t('外观参数', 'Appearance')}</b>
            <span className="sp" />
            {tab === 'copy' ? (
              <span className={`set-count${count > DESCRIPTION_MAX ? ' over' : ''}`}>{count} / {DESCRIPTION_MAX}</span>
            ) : null}
          </div>
          <div className="bd2">
            {tab === 'copy' ? (
            <>
            <div className="fld">
              <label>Description</label>
              <Input
                variant="box"
                multiline
                placeholder={fallbackDescription}
                value={value}
                onChange={(event) => setValue(event.target.value)}
              />
            </div>
            <p className="hint" style={{ marginTop: 8 }}>
              {t(`换行与连续空格会被自动折叠成一个空格；上限 ${DESCRIPTION_MAX} 字（按字符数，不是字节数）。`, `Line breaks and repeated spaces collapse into one space; the limit is ${DESCRIPTION_MAX} characters (not bytes).`)}
            </p>
            <div className="fld" style={{ marginTop: 14 }}>
              <label>{t('Description（英文）', 'Description (English)')}</label>
              <Input
                variant="box"
                multiline
                placeholder={t('留空则英文界面回退中文描述', 'Leave empty to fall back to the Chinese description in English mode')}
                value={valueEN}
                onChange={(event) => setValueEN(event.target.value)}
              />
              <p className="hint" style={{ marginTop: 8 }}>
                <span className={`set-count${countEN > DESCRIPTION_MAX ? ' over' : ''}`}>{countEN} / {DESCRIPTION_MAX}</span>
              </p>
            </div>
            </>
            ) : (
            <>
            <div className="fld">
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
            </>
            )}
            <div className="act-row" style={{ marginTop: 14 }}>
              {tab === 'copy' ? (
                <Button
                  ghost
                  small
                  type="button"
                  disabled={busy || (value === '' && valueEN === '')}
                  onClick={() => {
                    // 中英文是同一张「站点文案」卡里的两个描述：按钮叫「清空描述」，
                    // 就应当把两份都清掉。只清中文会让英文界面继续展示旧文案。
                    setValue('')
                    setValueEN('')
                  }}
                >
                  {t('清空描述', 'Clear description')}
                </Button>
              ) : null}
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
                disabled={busy || siteLoadFailed || !dirty || !tabValid}
              >
                {busy ? t('保存中…', 'Saving…') : t('保存', 'Save')}
              </Button>
            </div>
          </div>
        </form>

        {tab === 'copy' ? (
        <div className="pcard rv">
          <div className="ph2"><b>{t('预览', 'Preview')}</b><span className="sp" /><span className="set-state">{t('前台实际效果', 'As seen by readers')}</span></div>
          <div className="bd2 set-preview">
            <div className="set-preview-block">
              <span className="kicker">{t('首页标语', 'Home slogan')}</span>
              <div className="set-slogan">{previewDescription}</div>
            </div>
            <div className="set-preview-block">
              <span className="kicker">{t('页脚简介', 'Footer intro')}</span>
              <p className="set-footer">{previewDescription}</p>
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
                      <p>{previewDescription}</p>
                    </div>
                  </div>
                </div>
              </div>
            </div>
            <div className="set-preview-block">
              <span className="kicker">{t('首页标签页标题', 'Home page title')}</span>
              <div className="set-title">Twilight · {previewDescription}</div>
            </div>
          </div>
        </div>
        ) : null}
      </div>
      )}

      {/* AI 翻译配置单独一张卡：它写的是 settings.json 的 translate 段，
          与上面的站点文案没有共享字段，分成两个表单可以各自独立保存。 */}
      {tab === 'translate' ? (
      <form
        className="pcard rv"
        style={{ marginTop: 18 }}
        onSubmit={(event) => {
          event.preventDefault()
          void submitTranslate()
        }}
      >
        <div className="ph2">
          <b>{t('AI 翻译', 'AI translation')}</b>
          <span className="sp" />
          <span className="set-state">
            {translateLoadFailed
              ? t('加载失败', 'Load failed')
              : !translateEnabled
                ? t('已停用', 'Disabled')
                : translateConfigured ? t('已就绪', 'Ready') : t('未配置完整', 'Not fully configured')}
          </span>
        </div>
        <div className="bd2">
          <div className="fld">
            <label>{t('启用 AI 翻译', 'Enable AI translation')}</label>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <Switch checked={translateEnabled} onChange={(event) => setTranslateEnabled(event.target.checked)} />
              <span className="hint" style={{ margin: 0 }}>
                {t(
                  '开启后，文章编辑器里会出现「AI 翻译」按钮。关闭不会删除已保存的地址与密钥。',
                  'When on, the post editor shows an AI translation button. Turning it off keeps the saved URL and key.',
                )}
              </span>
            </div>
          </div>

          <div className="fld" style={{ marginTop: 14 }}>
            <label>API URL</label>
            <Input
              variant="box"
              placeholder="https://api.openai.com/v1"
              value={translateURL}
              onChange={(event) => setTranslateURL(event.target.value)}
            />
            <p className="hint" style={{ marginTop: 8 }}>
              {t(
                'OpenAI 兼容地址，可写到 /v1，也可直接填完整的 /chat/completions 地址。留空表示停用翻译。',
                'An OpenAI-compatible endpoint: either the /v1 base or the full /chat/completions URL. Leave empty to disable.',
              )}
              {translateURLValid ? '' : t(' 地址必须以 http:// 或 https:// 开头。', ' The URL must start with http:// or https://.')}
            </p>
          </div>

          <div className="fld" style={{ marginTop: 14 }}>
            <label>Model</label>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
              <Input
                variant="box"
                placeholder="gpt-4o-mini"
                value={translateModel}
                onChange={(event) => setTranslateModel(event.target.value)}
                style={{ flex: 1, minWidth: 220 }}
              />
              <Button ghost small disabled={modelBusy} onClick={() => void queryModels()}>
                {modelBusy ? t('查询中…', 'Querying…') : t('查询模型', 'List models')}
              </Button>
            </div>
            {modelOptions.length > 0 ? (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginTop: 10 }}>
                {modelOptions.map((id) => (
                  <Button key={id} ghost small onClick={() => setTranslateModel(id)}>{id}</Button>
                ))}
              </div>
            ) : null}
            <p className="hint" style={{ marginTop: 8 }}>
              {t(
                '点「查询模型」会向该地址的 /models 发起一次只读请求；URL 或密钥留空时沿用已保存的值。',
                '“List models” makes one read-only request to /models. Empty URL or key falls back to the saved ones.',
              )}
            </p>
          </div>

          <div className="fld" style={{ marginTop: 14 }}>
            <label>API Key</label>
            <Input
              variant="box"
              type="password"
              autoComplete="off"
              placeholder={
                translateKeyConfigured
                  ? t('已保存，留空保持不变', 'Saved — leave empty to keep it')
                  : 'sk-…'
              }
              value={translateKey}
              onChange={(event) => setTranslateKey(event.target.value)}
            />
            <p className="hint" style={{ marginTop: 8 }}>
              {t(
                '密钥只保存在服务器的 settings.json（权限 0600，且不入库），接口永远不会回显它。',
                'The key is stored only in the server’s settings.json (0600, not in git) and is never echoed back by the API.',
              )}
            </p>
          </div>

          <div className="fld" style={{ marginTop: 14 }}>
            <label>{t('超时（秒）', 'Timeout (seconds)')}</label>
            <Input
              variant="box"
              type="number"
              min={TRANSLATE_TIMEOUT_MIN}
              max={TRANSLATE_TIMEOUT_MAX}
              step={1}
              value={translateTimeoutText}
              onChange={(event) => setTranslateTimeoutText(event.target.value)}
              style={{ width: 110 }}
            />
            <span className="hint" style={{ marginLeft: 10 }}>
              {t(
                `${TRANSLATE_TIMEOUT_MIN}–${TRANSLATE_TIMEOUT_MAX} 秒，默认 ${TRANSLATE_TIMEOUT_DEFAULT} 秒。`,
                `${TRANSLATE_TIMEOUT_MIN}–${TRANSLATE_TIMEOUT_MAX}s, default ${TRANSLATE_TIMEOUT_DEFAULT}s.`,
              )}
            </span>
          </div>

          <div className="fld" style={{ marginTop: 14 }}>
            <label>{t('输出上限（tokens）', 'Max output tokens')}</label>
            <Input
              variant="box"
              type="number"
              min={TRANSLATE_TOKENS_MIN}
              max={TRANSLATE_TOKENS_MAX}
              step={256}
              value={translateTokensText}
              onChange={(event) => setTranslateTokensText(event.target.value)}
              style={{ width: 110 }}
            />
            <span className="hint" style={{ marginLeft: 10 }}>
              {t(
                `${TRANSLATE_TOKENS_MIN}–${TRANSLATE_TOKENS_MAX}，默认 ${TRANSLATE_TOKENS_DEFAULT}。`,
                `${TRANSLATE_TOKENS_MIN}–${TRANSLATE_TOKENS_MAX}, default ${TRANSLATE_TOKENS_DEFAULT}.`,
              )}
            </span>
          </div>

          <div className="fld" style={{ marginTop: 14 }}>
            <label>{t('思维强度', 'Thinking effort')}</label>
            <Select
              className="set-effort"
              aria-label={t('思维强度', 'Thinking effort')}
              value={translateEffort === '' ? null : translateEffort}
              options={effortOptions}
              placeholder={t('不设置', 'Not set')}
              allowClear
              onChange={(value) => setTranslateEffort(value == null ? '' : String(value))}
              style={{ width: 220 }}
            />
            <p className="hint" style={{ marginTop: 8 }}>
              {t(
                '通过 reasoning_effort 下发给模型；不选择或清除后不发送该字段，对不支持该参数的服务保持兼容。',
                'Sent as reasoning_effort. Leaving it unselected (or clearing it) omits the field so providers without support stay compatible.',
              )}
            </p>
          </div>

          <div className="fld" style={{ marginTop: 14 }}>
            <label>{t('提示词模板', 'Prompt template')}</label>
            <textarea
              className="inp box"
              style={{ minHeight: 220, fontFamily: 'var(--mono)', fontSize: 12.5, lineHeight: 1.8 }}
              value={translatePrompt}
              onChange={(event) => setTranslatePrompt(event.target.value)}
              spellCheck={false}
            />
            <p className="hint" style={{ marginTop: 8 }}>
              {t(
                '模板可用 {{.Title}} / {{.Content}} 插入原始标题与正文，或用 {{.TitleJSON}} / {{.ContentJSON}} 插入 JSON 转义后的值；{{.From}} / {{.To}} 是语言名。清空并保存可恢复内置默认模板。',
                'Use {{.Title}} / {{.Content}} for the raw fields, {{.TitleJSON}} / {{.ContentJSON}} for JSON-safe values, and {{.From}} / {{.To}} for language names. Clear and save to restore the built-in template.',
              )}
            </p>
          </div>

          <div className="act-row" style={{ marginTop: 14 }}>
            <span className="hint" style={{ margin: 0 }}>
              {translateLoadFailed
                ? t(
                    '翻译配置加载失败。为避免用默认值覆盖已保存的地址与密钥，本次不能保存；请刷新页面后重试。',
                    'Translation settings failed to load. Saving is disabled so the blank defaults cannot overwrite your saved URL and key; refresh the page and try again.',
                  )
                : !translateEnabled
                  ? t(
                      'AI 翻译已停用。重新开启后可使用已保存的地址与密钥。',
                      'AI translation is off. Turn it back on to use the saved URL and key.',
                    )
                  : translateConfigured
                    ? t('配置完整，编辑器可以直接翻译。', 'Configuration complete — the editor can translate.')
                    : t(
                        '地址、模型与密钥三者齐备后才能翻译。',
                        'Translation needs the URL, model and API key.',
                      )}
            </span>
            <span className="sp" />
            <span className="set-state">
              <span className="swap-text" key={String(translateDirty)}>
                {translateDirty ? t('有未保存的修改', 'Unsaved changes') : t('已保存', 'Saved')}
              </span>
            </span>
            <Button
              primary
              small
              type="submit"
              disabled={translateBusy || translateLoadFailed || !translateDirty || !translateFormValid}
            >
              {translateBusy ? t('保存中…', 'Saving…') : t('保存翻译设置', 'Save translation settings')}
            </Button>
          </div>
        </div>
      </form>
      ) : null}
    </>
  )
}
