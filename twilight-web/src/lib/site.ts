// site.ts —— 站点级文案（网站描述）的前端读取与标题约定
//
// 网站描述存在后端 settings.json，通过公开的 GET /api/site 下发。它是
// 「本来就已经展示给所有读者的文案」，所以读取无需鉴权；管理员在
// /admin/site-settings 里修改（见 SiteSettings.tsx）。
//
// 缓存策略：模块级单例 + 订阅。描述在一次会话里几乎不变，但改完保存后
// 首页标语与页脚要能立即反映新值，所以 publish 会通知所有订阅者，而不是
// 等下一次刷新页面。
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

let cache: string | null = null
let inFlight: Promise<string> | null = null
const listeners = new Set<(value: string) => void>()

/**
 * 把描述同步到 <meta name="description">。index.html 里预置了一份默认值，
 * 这里只做覆盖：拿不到描述（接口失败、站点未设置）时页面仍是 index.html 的文案，
 * 不会留下一个空的 meta 标签。
 */
function applyMetaDescription(value: string) {
  const tag = document.querySelector('meta[name="description"]')
  if (tag) tag.setAttribute('content', value)
}

function publish(value: string) {
  cache = value
  // 站点描述是「网站描述」这个设置唯一的对外落点，标题与 meta 都跟着它走。
  applyMetaDescription(value || FALLBACK_DESCRIPTION)
  for (const listener of listeners) listener(value)
}

/** 拉取站点描述（带模块级缓存）。失败时返回空串，调用方用内置文案兜底。 */
export function loadSiteDescription(): Promise<string> {
  if (cache !== null) return Promise.resolve(cache)
  if (inFlight) return inFlight

  inFlight = blog.getSite()
    .then((settings) => {
      publish(settings.description ?? '')
      return cache!
    })
    .catch(() => '')
    .finally(() => {
      inFlight = null
    })
  return inFlight
}

/** 保存新描述并同步所有订阅者（站点设置页调用）。 */
export async function saveSiteDescription(description: string): Promise<string> {
  const saved = await blog.updateSite({ description })
  publish(saved.description ?? '')
  return saved.description ?? ''
}

/**
 * 订阅站点描述的 hook。首次挂载触发加载；未加载完成或后端未设置时
 * 返回内置文案，页面始终有话可说。
 */
export function useSiteDescription(): string {
  const [value, setValue] = useState(() => cache ?? '')

  useEffect(() => {
    const listener = (next: string) => setValue(next)
    listeners.add(listener)
    void loadSiteDescription().then((loaded) => setValue(loaded))
    return () => {
      listeners.delete(listener)
    }
  }, [])

  return value || FALLBACK_DESCRIPTION
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
