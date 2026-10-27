// site.ts —— 站点级公开信息（网站描述、站长头像）的前端读取与标题约定
//
// 描述与头像都存在后端，通过公开的 GET /api/site 下发。它们能公开，是因为二者
// 本来就是「展示给所有读者的东西」：描述出现在首页标语与页脚，头像出现在侧栏
// 作者卡。描述由管理员在 /admin/site-settings 里改（见 SiteSettings.tsx），
// 头像由管理员在 /admin/profile 里改（见 Profile.tsx）。
//
// 为什么两者共用一份缓存、一次请求：它们落在同一个作者卡里（见 PublicSidebar），
// 拆成两次请求只是把一块本就要一起渲染的内容分成两次往返。缓存的是**整个快照**，
// 因此改任一项都会同时通知两边的订阅者。
//
// 缓存策略：模块级单例 + 订阅。描述在一次会话里几乎不变，但改完保存后首页标语、
// 页脚与侧栏作者卡要能立即反映新值，所以 publish 会通知所有订阅者，而不是等下一次
// 刷新页面。头像同理：在「账号」页换完头像，侧栏作者卡应当立刻跟着变，而不是等
// 用户下一次刷新（这就是 refreshSiteMeta 存在的理由）。
//
// 描述的五个落点（改这里时请一并考虑）：首页标语、页脚简介、侧栏作者卡、
// 首页标签页标题、<meta name="description">。前三者分别由 PublicShell 与
// PublicSidebar 订阅，首页标题由本模块的 useHomeDocumentTitle 维护，而 meta
// 由本模块**转交**给 head.ts 写入（那里是描述的唯一下笔处，页面级摘要优先于
// 站点级——理由见 head.ts 文件头）。描述既然是可配置的站点文案，就不该有一个
// 落点还留着硬编码。

import { useEffect, useState } from 'react'
import { blog } from './client'
import { setSiteDescription } from './head'
import { getLocale, useLocale, type Locale } from './i18n'
import { applyBorderRadius, applyPostBannerHeight, notifyShellFlagsChanged } from './flags'

const FALLBACK_DESCRIPTION = '以代码为笔，以时间为墨。'
const FALLBACK_DESCRIPTION_EN = 'Written in code, sealed in time.'

/** 站长未填写描述时的内置文案；与界面语言一致，避免英文界面出现中文句子。 */
function fallbackDescription(locale: Locale): string {
  return locale === 'en' ? FALLBACK_DESCRIPTION_EN : FALLBACK_DESCRIPTION
}

/** 与后端 viewmodel.MaxSiteDescriptionLength 镜像；改一处必须改另一处。 */
const DESCRIPTION_MAX = 200

/** 与后端 config.DefaultCodeBlockMaxLines 镜像；改一处必须改另一处。 */
const DEFAULT_CODE_LINES = 20

/** 与后端 config.DefaultHomePageSize 镜像；改一处必须改另一处。 */
const DEFAULT_HOME_PAGE_SIZE = 12

/** 与后端 config.DefaultBorderRadius 镜像；改一处必须改另一处。 */
const DEFAULT_BORDER_RADIUS = 4

/** 与后端 config.DefaultPostBannerHeight 镜像；改一处必须改另一处。 */
const DEFAULT_POST_BANNER_HEIGHT = 400

/** 站点公开信息的一份快照。两个字段都可能为空串（未设置 / 后端只回了其中一项）。 */
export interface SiteMeta {
  description: string
  /** 英文站点描述；空串表示没有英文版，英文界面回退中文描述。 */
  descriptionEn: string
  /** 站长头像的站内地址；空串表示没设，调用方回退到首字母占位块。 */
  avatarUrl: string
  /** 代码块最大显示行数；决定块级代码框的限高（超出部分块内滚动）。 */
  codeBlockMaxLines: number
  /** 首页每页文章数；设置页用它回显当前值（首页本身读外壳属性，见 lib/flags.ts）。 */
  homePageSize: number
  /**
   * 站点统一圆角（px）；设置页用它回显当前值。
   *
   * 与 homePageSize 同理：**真正生效的圆角**由 main.tsx 从外壳属性写进
   * --radius（见 lib/flags.ts 的 applyBorderRadius），这里这一份只服务于
   * 设置页回显。两处若不一致，设置页会显示一个页面并未采用的数值。
   */
  borderRadius: number
  /** 文章头图最大高度（px）；设置页回显，真正生效的值来自外壳属性。 */
  postBannerHeight: number
  /** 微语功能是否启用；设置页回显。前端首屏决策读外壳属性。 */
  momentsEnabled: boolean
}

/** 请求失败时的快照。刻意不进缓存（见 loadMeta），只是给调用方一个可用的值。 */
const EMPTY_META: SiteMeta = {
  description: '',
  descriptionEn: '',
  avatarUrl: '',
  codeBlockMaxLines: DEFAULT_CODE_LINES,
  homePageSize: DEFAULT_HOME_PAGE_SIZE,
  borderRadius: DEFAULT_BORDER_RADIUS,
  postBannerHeight: DEFAULT_POST_BANNER_HEIGHT,
  momentsEnabled: true,
}

let cache: SiteMeta | null = null
let inFlight: Promise<SiteMeta> | null = null
const listeners = new Set<(value: SiteMeta) => void>()

/**
 * 把接口响应收敛成快照。入参是 SDK 的 SiteSettingsResponse（键是服务端原样的
 * snake_case），出参是本模块自己的 SiteMeta。`??` 不是多余的防御：这份数据可能
 * 来自更旧的服务端（没有 avatar_url 这个键），而「字段缺席」与「空串」在界面上
 * 是同一件事。
 *
 * 这里不需要再判 settings 本身是空——真出现 null 响应时，本函数会在 then 里抛，
 * 而紧随其后的 catch 会把它收敛成 EMPTY_META，与「请求失败」同一条出路。
 */
function toMeta(settings: {
  description?: string
  description_en?: string
  avatar_url?: string
  code_block_max_lines?: number
  home_page_size?: number
  border_radius?: number
  post_banner_height?: number
  moments_enabled?: boolean
}): SiteMeta {
  return {
    description: settings.description ?? '',
    descriptionEn: settings.description_en ?? '',
    avatarUrl: settings.avatar_url ?? '',
    // 后端该字段恒输出；?? 兜底的是「更旧的服务端」——此时与失败同路，
    // 回退到与服务端默认值一致的行数，保证两侧渲染口径一致。
    codeBlockMaxLines: settings.code_block_max_lines ?? DEFAULT_CODE_LINES,
    // 同理。注意首页**不用**这个值发列表请求（首次请求必须已经带上正确的
    // page_size，等这里回来就晚了，见 lib/flags.ts 的 homePageSize），
    // 它只服务于设置页回显与订阅者。
    homePageSize: settings.home_page_size ?? DEFAULT_HOME_PAGE_SIZE,
    // 同理。注意这里**不能**写成 `?? DEFAULT` 之外再补 `|| DEFAULT`：
    // 0 是合法的直角，`|| ` 会把它当假值换成 4。
    borderRadius: settings.border_radius ?? DEFAULT_BORDER_RADIUS,
    postBannerHeight: settings.post_banner_height ?? DEFAULT_POST_BANNER_HEIGHT,
    // 字段缺席（更旧的服务端）按「启用」兜底，与外壳属性的缺省语义一致。
    momentsEnabled: settings.moments_enabled ?? true,
  }
}

/**
 * 把描述交给 head.ts —— 它是 `meta[name=description]` 的**唯一写入者**。
 *
 * 以前这里自己写那个标签，于是与「文章页写自己的摘要」构成竞态：谁能赢取决于
 * /api/site 与文章接口谁先返回，同一页刷新两次可能给出不同的分享文案。
 * 现在两边都只是把值交给 head.ts，页面级优先，顺序不再影响结果。
 */
function publish(next: SiteMeta) {
  cache = next
  // 站点描述是「网站描述」这个设置唯一的对外落点，标题与 meta 都跟着它走。
  // 这里也按当前语言取用：英文界面首次加载时若先写中文描述，没有页面级摘要的
  // 页面（归档 / 标签 / 搜索）会在下一次订阅者 effect 之前短暂带着中文 meta。
  const locale = getLocale()
  const localized = locale === 'en'
    ? (next.descriptionEn || next.description)
    : next.description
  setSiteDescription(localized || fallbackDescription(locale))
  for (const listener of listeners) listener(next)
}

/**
 * 发起一次站点公开信息请求：带模块级缓存，并发共享同一个 in-flight 请求。
 * 失败时 reject 且不写缓存——由调用方决定「回退默认值」还是「明确报错」。
 */
function requestMeta(): Promise<SiteMeta> {
  if (cache !== null) return Promise.resolve(cache)
  if (inFlight) return inFlight

  inFlight = blog
    .getSite()
    .then((settings) => {
      const meta = toMeta(settings)
      publish(meta)
      return meta
    })
    .finally(() => {
      inFlight = null
    })
  return inFlight
}

/**
 * 拉取站点公开信息（公开页面用）。
 *
 * 失败时返回 EMPTY_META **且不写缓存**：一次网络抖动不该把「没有描述、没有头像」
 * 变成这次会话的定论，下一个订阅者应该还有机会重试。
 */
function loadMeta(): Promise<SiteMeta> {
  return requestMeta().catch(() => EMPTY_META)
}

/** 拉取站点描述；失败时返回空串，调用方用内置文案兜底。 */
export function loadSiteDescription(): Promise<string> {
  return loadMeta().then((meta) => meta.description)
}

/**
 * 拉取完整的站点公开快照（设置页用它同时初始化描述与代码块行数）。
 *
 * 与 loadMeta 不同，这里**不兜底**：设置页如果拿 EMPTY_META 当服务端现值，
 * 站长改一个字段再保存就会把其余设置全部写成默认值。失败应当被明确看到。
 */
export function loadSiteMeta(): Promise<SiteMeta> {
  return requestMeta()
}

/**
 * 拉取管理端站点设置（多一个评论通知收件地址）。
 *
 * 刻意**不进**模块级缓存：通知邮箱只在一个页面里用，缓存在模块级会让
 * 「保存后刷新设置页仍看到旧地址」在别处出现；公开快照的缓存则要继续共享。
 */
export async function loadAdminSiteSettings() {
  return blog.getAdminSite()
}

/**
 * 丢弃缓存并重新拉取。站长刚改完头像时调用——头像不是这次请求的返回值
 * （PUT /api/admin/profile 回的是会话身份），而侧栏作者卡正订阅着这份缓存，
 * 不主动刷新的话，用户会看到「保存成功但头像没变」。
 */
export function refreshSiteMeta(): Promise<SiteMeta> {
  cache = null
  inFlight = null
  return loadMeta()
}

/**
 * 保存站点设置并同步所有订阅者（站点设置页调用）。
 *
 * 入参用 camelCase 的显式字段而不是透传 SDK 输入：codeBlockMaxLines 省略时
 * 不带进请求体（SDK 层 JSON.stringify 丢弃 undefined），服务端保持现值。
 * 行数与描述合并为一次保存——它们落在同一个设置卡里，分成两次提交会让
 * 「看起来是一个表单」的界面产生两个互相覆盖的时刻。
 */
export async function saveSiteSettings(input: {
  description: string
  descriptionEn?: string
  codeBlockMaxLines?: number
  homePageSize?: number
  borderRadius?: number
  postBannerHeight?: number
  momentsEnabled?: boolean
  commentNotifyTo?: string
}): Promise<SiteMeta> {
  const saved = await blog.updateSite({
    description: input.description,
    description_en: input.descriptionEn,
    code_block_max_lines: input.codeBlockMaxLines,
    home_page_size: input.homePageSize,
    border_radius: input.borderRadius,
    post_banner_height: input.postBannerHeight,
    moments_enabled: input.momentsEnabled,
    comment_notify_to: input.commentNotifyTo,
  })
  // 用服务端返回的整份快照覆盖：它同时带着头像，因此改描述不会把头像从缓存里抹掉。
  const meta = toMeta(saved)
  publish(meta)
  syncShellFlags(saved)
  return meta
}

/**
 * 把保存后的开关与外观参数写回外壳属性。
 *
 * 这些值平时由服务端渲染进 <html data-*>，是「首屏决策」通道（理由见
 * lib/flags.ts）。问题在于：站长在同一个 SPA 会话里保存设置后，属性还是
 * 打开页面时的旧值——从后台回到前台，导航与页脚仍按旧的开关心渲染，
 * 圆角/头图也要等一次硬刷新才生效。保存成功后把新值写回属性，下一次
 * 挂载（例如从后台返回公开站点）就能立刻读到正确值。
 *
 * 已挂载的组件不会因为 dataset 变化而重渲染；这里解决的是「同一会话内
 * 后续导航」而不是「当前这一帧」——后者由描述/头像的订阅机制负责。
 *
 * 只写**服务端确实返回了**的字段：旧二进制的响应可能缺少这些键（升级时前端
 * 先于后端），此时 HTML 里由服务端渲染的原值才是对的，不能拿本地默认值覆盖。
 */
function syncShellFlags(saved: {
  moments_enabled?: boolean
  home_page_size?: number
  border_radius?: number
  post_banner_height?: number
}) {
  const root = document.documentElement
  let changed = false
  if (saved.moments_enabled !== undefined) {
    root.dataset.momentsEnabled = String(saved.moments_enabled)
    changed = true
  }
  if (saved.home_page_size !== undefined) {
    root.dataset.homePageSize = String(saved.home_page_size)
    changed = true
  }
  if (saved.border_radius !== undefined) {
    root.dataset.borderRadius = String(saved.border_radius)
    applyBorderRadius()
    changed = true
  }
  if (saved.post_banner_height !== undefined) {
    root.dataset.postBannerHeight = String(saved.post_banner_height)
    applyPostBannerHeight()
    changed = true
  }
  // dataset 变了不会让已挂载的组件重渲染（React 不观察 DOM 属性）。
  // 后台侧栏的「微语」菜单正挂在同一个会话里，保存后必须立即跟着开关走；
  // 只在确实写入了某个字段时通知，避免把「旧后端缺字段」当成一次变更。
  if (changed) notifyShellFlagsChanged()
}

/** 保存新描述并同步所有订阅者。语义与 saveSiteSettings 相同，只写描述一项。 */
export async function saveSiteDescription(description: string): Promise<string> {
  const meta = await saveSiteSettings({ description })
  return meta.description
}

/**
 * 订阅代码块行数上限的 hook。行数是渲染参数：任何渲染 Markdown 正文的地方
 * （文章、页面、微语、编辑器预览）都可以拿它设置限高 CSS 变量。
 */
export function useSiteCodeLines(): number {
  const [value, setValue] = useState(() => cache?.codeBlockMaxLines ?? DEFAULT_CODE_LINES)

  useEffect(() => {
    const listener = (next: SiteMeta) => setValue(next.codeBlockMaxLines)
    listeners.add(listener)
    void loadMeta().then((meta) => setValue(meta.codeBlockMaxLines))
    return () => {
      listeners.delete(listener)
    }
  }, [])

  return value
}

/**
 * 订阅站点描述的 hook。首次挂载触发加载；未加载完成或后端未设置时
 * 返回内置文案，页面始终有话可说。
 */
export function useSiteDescription(): string {
  const locale = useLocale()
  const [meta, setMeta] = useState<SiteMeta | null>(cache)

  useEffect(() => {
    const listener = (next: SiteMeta) => setMeta(next)
    listeners.add(listener)
    void loadMeta().then(setMeta)
    return () => {
      listeners.delete(listener)
    }
  }, [])

  // 英文界面优先取英文描述；没有英文版时回退中文（与文章/页面的回退规则一致）。
  const localized = locale === 'en'
    ? (meta?.descriptionEn || meta?.description || '')
    : (meta?.description || '')
  const value = localized || fallbackDescription(locale)

  // 站点描述也是不指定摘要的页面的 meta 兜底；切换语言时同步过去，
  // 否则英文界面的 meta 仍会停在上一种语言。
  useEffect(() => {
    setSiteDescription(value)
  }, [value])

  return value
}

/**
 * 订阅站长头像地址的 hook。空串表示没设头像，由调用方决定怎么占位。
 *
 * 与 useSiteDescription 共用同一份缓存与同一次请求，因此作者卡里并排的两样东西
 * 不会有先后：不会出现描述已经更新、头像还是上一张的中间态。
 */
export function useSiteAvatar(): string {
  const [value, setValue] = useState(() => cache?.avatarUrl ?? '')

  useEffect(() => {
    const listener = (next: SiteMeta) => setValue(next.avatarUrl)
    listeners.add(listener)
    void loadMeta().then((meta) => setValue(meta.avatarUrl))
    return () => {
      listeners.delete(listener)
    }
  }, [])

  return value
}

/**
 * 把标签页标题设为「<页面名> · Twilight」。文章页与独立页面不走这里：
 * 它们需要内容自己的标题而不是路由名，由各自的组件直接设置。
 */
export function useDocumentTitle(pageTitle: string) {
  useEffect(() => {
    document.title = `${pageTitle} · Twilight`
  }, [pageTitle])
}

/**
 * 首页标题：「Twilight · <站点描述>」。首页的「页面名」就是站点描述本身，
 * 套用 useDocumentTitle 会得到「以代码为笔，以时间为墨。 · Twilight」——
 * 品牌被挤到末尾，标签页与分享卡片上都认不出这是谁的站。
 */
export function useHomeDocumentTitle() {
  const description = useSiteDescription()
  useEffect(() => {
    document.title = `Twilight · ${description}`
  }, [description])
}

export {
  DESCRIPTION_MAX,
  DEFAULT_BORDER_RADIUS,
  DEFAULT_CODE_LINES,
  DEFAULT_HOME_PAGE_SIZE,
  FALLBACK_DESCRIPTION,
  FALLBACK_DESCRIPTION_EN,
}

/**
 * 仅供测试：清空模块级缓存。vitest 里模块单例会跨用例存活，
 * 不清的话上一个用例保存的值会泄漏进下一个用例的「初始描述」。
 * 站点描述在 head.ts 里也有一份，一并清掉——否则前一个用例 publish 过的描述
 * 会留在下一个用例的 head 里，表现为「我没给它却出现在 meta 上」。
 */
export function resetSiteCacheForTests() {
  cache = null
  inFlight = null
  setSiteDescription('')
}
