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
// PublicSidebar 订阅，后两者由本模块自己维护——描述既然是可配置的站点文案，
// 就不该有一个落点还留着硬编码。

import { useEffect, useState } from 'react'
import { blog } from './client'

const FALLBACK_DESCRIPTION = '以代码为笔，以时间为墨。'

/** 与后端 viewmodel.MaxSiteDescriptionLength 镜像；改一处必须改另一处。 */
const DESCRIPTION_MAX = 200

/** 站点公开信息的一份快照。两个字段都可能为空串（未设置 / 后端只回了其中一项）。 */
export interface SiteMeta {
  description: string
  /** 站长头像的站内地址；空串表示没设，调用方回退到首字母占位块。 */
  avatarUrl: string
}

/** 请求失败时的快照。刻意不进缓存（见 loadMeta），只是给调用方一个可用的值。 */
const EMPTY_META: SiteMeta = { description: '', avatarUrl: '' }

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
function toMeta(settings: { description?: string; avatar_url?: string }): SiteMeta {
  return {
    description: settings.description ?? '',
    avatarUrl: settings.avatar_url ?? '',
  }
}

/**
 * 把描述同步到 <meta name="description">。服务端渲染的外壳里已经带了一份默认值，
 * 这里只做覆盖：拿不到描述（接口失败、站点未设置）时页面仍是那一份文案，
 * 不会留下一个空的 meta 标签。
 */
function applyMetaDescription(value: string) {
  const tag = document.querySelector('meta[name="description"]')
  if (tag) tag.setAttribute('content', value)
}

function publish(next: SiteMeta) {
  cache = next
  // 站点描述是「网站描述」这个设置唯一的对外落点，标题与 meta 都跟着它走。
  applyMetaDescription(next.description || FALLBACK_DESCRIPTION)
  for (const listener of listeners) listener(next)
}

/**
 * 拉取站点公开信息（带模块级缓存，并发共享同一个 in-flight 请求）。
 *
 * 失败时返回 EMPTY_META **且不写缓存**：一次网络抖动不该把「没有描述、没有头像」
 * 变成这次会话的定论，下一个订阅者应该还有机会重试。
 */
function loadMeta(): Promise<SiteMeta> {
  if (cache !== null) return Promise.resolve(cache)
  if (inFlight) return inFlight

  inFlight = blog
    .getSite()
    .then((settings) => {
      const meta = toMeta(settings)
      publish(meta)
      return meta
    })
    .catch(() => EMPTY_META)
    .finally(() => {
      inFlight = null
    })
  return inFlight
}

/** 拉取站点描述；失败时返回空串，调用方用内置文案兜底。 */
export function loadSiteDescription(): Promise<string> {
  return loadMeta().then((meta) => meta.description)
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

/** 保存新描述并同步所有订阅者（站点设置页调用）。 */
export async function saveSiteDescription(description: string): Promise<string> {
  const saved = await blog.updateSite({ description })
  // 用服务端返回的整份快照覆盖：它同时带着头像，因此改描述不会把头像从缓存里抹掉。
  publish(toMeta(saved))
  return saved.description ?? ''
}

/**
 * 订阅站点描述的 hook。首次挂载触发加载；未加载完成或后端未设置时
 * 返回内置文案，页面始终有话可说。
 */
export function useSiteDescription(): string {
  const [value, setValue] = useState(() => cache?.description ?? '')

  useEffect(() => {
    const listener = (next: SiteMeta) => setValue(next.description)
    listeners.add(listener)
    void loadSiteDescription().then((loaded) => setValue(loaded))
    return () => {
      listeners.delete(listener)
    }
  }, [])

  return value || FALLBACK_DESCRIPTION
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

export { DESCRIPTION_MAX, FALLBACK_DESCRIPTION }

/**
 * 仅供测试：清空模块级缓存。vitest 里模块单例会跨用例存活，
 * 不清的话上一个用例保存的值会泄漏进下一个用例的「初始描述」。
 */
export function resetSiteCacheForTests() {
  cache = null
  inFlight = null
}
