// head.ts —— 页面级 <head> 的统一写入口：canonical / og:* / twitter:* / robots / JSON-LD
//
// 为什么需要它：Go 渲染的 HTML 外壳只能带**站点级**信息（title 与 description，
// 来自 settings.json），而 canonical、og:type=article、发布时间、结构化数据只有
// 前端拿得到数据之后才知道。SPA 拿得到，但必须**在运行时把它们写进 head**——
// 注意这对不执行 JS 的抓取器（微信、多数社交平台）等于没做，真正的服务端渲染是
// 后端的事（见交付说明里的待办）。
//
// 它同时是 description 的**唯一写入者**。以前 site.ts 直接写
// `meta[name=description]`，而文章页要写自己的摘要——谁能赢取决于 /api/site 与文章
// 接口谁先返回，同一篇文章刷新两次可能给出不同的分享文案。现在站点级描述通过
// setSiteDescription 交给本模块，**页面级优先**，两种到达顺序收敛到同一个结果。
//
// 契约（改动前请一并读）：
//  1. 每次 applyHead 都会**对账**：本模块管理的那组标签里，这一页没给的一律删掉。
//     所以「从文章页返回首页」不会把上一页的 og:type=article 与 JSON-LD 带过去，
//     也不需要页面自己写卸载清理。前提是**每个路由页面都必须调用 usePageHead**。
//  2. og:url / og:image / canonical 必须是绝对地址，统一走 toAbsoluteUrl（按当前
//     origin 拼，与媒体「复制链接」同一套规则）。
//  3. JSON-LD 的序列化必须转义 `<` `>` `&`：一个标题里写 `</script>` 就能把
//     `<script type="application/ld+json">` 提前闭合，把后面的标签全变成正文。
//  4. 本模块**不碰 document.title**。标题继续由 site.ts 的 useDocumentTitle /
//     useHomeDocumentTitle 拥有，两处都写会重新引入「谁最后写谁赢」的竞态。
//     代价是调用方要把同一个标题字符串传两次（显式优于隐式，且能被测试盯住）。

import { useEffect } from 'react'
import { useLocation } from 'react-router-dom'
import { excerpt } from './format'
import { toAbsoluteUrl } from './url'

/** 本模块管理的 meta 键（property 或 name）。对账时以这份名单为准。 */
const MANAGED_META_KEYS = [
  'description',
  'robots',
  'og:type',
  'og:url',
  'og:title',
  'og:description',
  'og:image',
  'article:published_time',
  'twitter:card',
  'twitter:title',
  'twitter:description',
  'twitter:image',
] as const

/** JSON-LD 用独立节点，并打上 data-head 以便与外壳自带的结构化数据区分。 */
const JSON_LD_SELECTOR = 'script[type="application/ld+json"][data-head="page"]'

const JSON_LD_ESCAPES: Record<string, string> = {
  '<': '\\u003c',
  '>': '\\u003e',
  '&': '\\u0026',
}

/** 把结构化数据序列化成能安全放进 <script> 的字符串（见契约 3）。 */
export function serializeJsonLd(value: unknown): string {
  return JSON.stringify(value).replace(/[<>&]/g, (character) => JSON_LD_ESCAPES[character])
}

/**
 * 正文摘成一段纯文本描述：先摘掉 Markdown 的噪声（代码块、图片、链接目标、
 * 行内代码、标题符号），再交给 excerpt 折叠空白并截断。
 *
 * 不直接用 excerpt 的原因很实际：meta description 会被搜索结果的摘要与分享卡片
 * 直接引用，`## 小标题` `![图](./a.png)` 出现在那里是纯噪声。
 */
export function plainDescription(markdown: string, max = 150): string {
  const text = (markdown || '')
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/!\[[^\]]*\]\([^)]*\)/g, ' ')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/^#{1,6}\s+/gm, '')
    .replace(/^>+\s?/gm, '')
    .replace(/\|/g, ' ')
    // 强调与删除线是**包住**正文的符号，不是词边界：换成空格会在中文里凭空插一个
    // 空格（`正文**内容**` → `正文 内容`）。去掉即可——拉丁文里 `**bold**` 两侧
    // 本来就有空白，不存在把词粘起来的风险。
    .replace(/[*_~]/g, '')
  return excerpt(text, max)
}

export interface PageHead {
  /** 站内路径（可含查询串，如 `/tags?id=3`）。canonical 与 og:url 由它拼绝对地址。 */
  path: string
  /** 与 useDocumentTitle 传入的同一个字符串。用于 og:title / twitter:title。 */
  title: string
  /** 页面级描述。不给时回落到站点描述（由 site.ts 通过 setSiteDescription 推送）。 */
  description?: string
  type?: 'website' | 'article'
  /** 分享图：站内相对路径或已是绝对地址；不传则不打这个标签。 */
  image?: string
  /** 毫秒时间戳，仅文章页：写成 article:published_time（ISO 8601）。 */
  publishedAt?: number
  /** 传给 `<meta name="robots">` 的内容，如 'noindex, nofollow'；不给则删掉该标签。 */
  robots?: string
  /** 结构化数据（WebSite / BlogPosting），不给则删掉本模块的 JSON-LD 节点。 */
  jsonLd?: Record<string, unknown>
}

/** 站点级描述（settings.json 的「网站描述」）由 site.ts 推送进来。 */
let siteDescription = ''
/** 最近一次应用过的页面 head：站点描述后到时用它重放，保证两个顺序结果一致。 */
let applied: PageHead | null = null

/**
 * 一个 meta 键该挂在哪个属性上：Open Graph 与它的文章扩展按约定走 property，
 * 其余（twitter:*、description、robots）走 name。**写入与选择器必须共用这一个
 * 判断**——分开写就会出现「按 name 找、按 property 写」的错位，表现为第二页找不到
 * 上一页写的标签而重复添加。twitter:* 也在这里走 name：它的键名里同样带冒号，
 * 按「含冒号即 property」的粗规则会被写成 property 而被抓取器忽略。
 */
function metaAttribute(key: string): 'property' | 'name' {
  return key.startsWith('og:') || key.startsWith('article:') ? 'property' : 'name'
}

function setMeta(key: string, content: string | null): void {
  const attribute = metaAttribute(key)
  const existing = document.head.querySelector(`meta[${attribute}="${key}"]`)
  if (content === null) {
    existing?.remove()
    return
  }
  if (existing) {
    existing.setAttribute('content', content)
    return
  }
  const tag = document.createElement('meta')
  tag.setAttribute(attribute, key)
  tag.setAttribute('content', content)
  document.head.appendChild(tag)
}

/**
 * 描述的唯一落点：页面级优先于站点级，两者都没有时**保持现状**（外壳已经带了一份
 * 服务端知道默认值的 description，写空串只会把它抹掉）。
 */
function applyDescription(): void {
  const value = applied?.description || siteDescription
  if (value) setMeta('description', value)
}

function applyJsonLd(value: Record<string, unknown> | undefined): void {
  const existing = document.head.querySelector(JSON_LD_SELECTOR)
  if (!value) {
    existing?.remove()
    return
  }
  const script = existing ?? document.createElement('script')
  script.setAttribute('type', 'application/ld+json')
  script.setAttribute('data-head', 'page')
  script.textContent = serializeJsonLd(value)
  if (!existing) document.head.appendChild(script)
}

/** 把一页的 head 写进文档，并回收这一页没有声明的受管标签（见契约 1）。 */
export function applyHead(head: PageHead): void {
  applied = head

  applyDescription()
  setMeta('robots', head.robots ?? null)
  setMeta('og:type', head.type ?? 'website')
  setMeta('og:url', toAbsoluteUrl(head.path))
  setMeta('og:title', head.title)
  setMeta('og:description', head.description || siteDescription || null)
  setMeta('og:image', head.image ? toAbsoluteUrl(head.image) : null)
  setMeta('article:published_time', head.publishedAt ? new Date(head.publishedAt).toISOString() : null)
  setMeta('twitter:card', head.image ? 'summary_large_image' : 'summary')
  setMeta('twitter:title', head.title)
  setMeta('twitter:description', head.description || siteDescription || null)
  setMeta('twitter:image', head.image ? toAbsoluteUrl(head.image) : null)

  const canonical = document.head.querySelector('link[rel="canonical"]')
  if (canonical) {
    canonical.setAttribute('href', toAbsoluteUrl(head.path))
  } else {
    const link = document.createElement('link')
    link.setAttribute('rel', 'canonical')
    link.setAttribute('href', toAbsoluteUrl(head.path))
    document.head.appendChild(link)
  }

  applyJsonLd(head.jsonLd)
}

/**
 * 站点级描述入队。site.ts 在自己的 publish 里调用它：描述到手时若当前页有自己的
 * 摘要，页面级仍然赢（这就是竞态被消掉的地方）。
 */
export function setSiteDescription(value: string): void {
  siteDescription = value
  applyDescription()
}

/**
 * 订阅当前页面的 head。依赖是字段签名而不是对象本身——调用方写的是对象字面量，
 * 每次渲染都是新身份，直接放进依赖会让 effect 每次渲染都重写一遍 head。
 */
export function usePageHead(head: PageHead): void {
  const signature = JSON.stringify(head)
  useEffect(() => {
    applyHead(head)
    // eslint 的 exhaustive-deps 会想让人把 head 放进依赖，但那正是要避免的：
    // signature 已经覆盖了全部字段，同一个 signature 下 head 的内容必然相同。
  }, [signature])
}

/**
 * 当前地址的站内形式（含查询串），canonical 与 og:url 都用它。
 *
 * **canonical 策略就写在这一处**：指向自身。列表页的分页是组件状态、根本不进 URL；
 * 真正带参数的是 `/tags?id=N`（标签聚合）与 `/search?q=…`——前者是**各自独立的内容**，
 * 因此指向自身而不是收敛到 `/tags`；后者整页 noindex，不需要收敛。
 * 以后若要改成「第一页收敛 / 剥掉跟踪参数」，改这一个函数即可。
 */
export function useCanonicalPath(): string {
  const location = useLocation()
  return location.pathname + location.search
}

/**
 * 文章与独立页面共用的 head（纯函数，便于直接断言序列化结果）。
 * 描述取正文摘出的纯文本，结构化数据是 BlogPosting。
 */
export function articlePageHead(input: {
  path: string
  title: string
  content: string
  publishedAt: number
  image?: string
}): PageHead {
  const description = plainDescription(input.content)
  return {
    path: input.path,
    title: input.title,
    description,
    type: 'article',
    image: input.image,
    publishedAt: input.publishedAt,
    jsonLd: articleJsonLd({
      title: input.title,
      description,
      publishedAt: input.publishedAt,
      url: toAbsoluteUrl(input.path),
      image: input.image,
    }),
  }
}

/** 结构化数据构造器（纯函数，便于直接断言序列化结果）。 */
export function websiteJsonLd(name: string, description: string, url: string): Record<string, unknown> {
  return {
    '@context': 'https://schema.org',
    '@type': 'WebSite',
    name,
    description,
    url,
  }
}

export function articleJsonLd(article: {
  title: string
  description: string
  publishedAt: number
  url: string
  image?: string
}): Record<string, unknown> {
  return {
    '@context': 'https://schema.org',
    '@type': 'BlogPosting',
    headline: article.title,
    description: article.description,
    datePublished: new Date(article.publishedAt).toISOString(),
    mainEntityOfPage: article.url,
    ...(article.image ? { image: [article.image] } : {}),
  }
}

/**
 * 仅供测试：清空模块状态与文档里的受管标签。
 * jsdom 的 document 在一个测试文件内跨用例存活，不清的话上一用例的 canonical
 * 会被下一用例当成「外壳里本来就有的」。
 */
export function resetHeadForTests(): void {
  applied = null
  siteDescription = ''
  const managed = [
    ...MANAGED_META_KEYS.map((key) => `meta[${metaAttribute(key)}="${key}"]`),
    'link[rel="canonical"]',
    JSON_LD_SELECTOR,
  ]
  for (const selector of managed) {
    for (const node of document.head.querySelectorAll(selector)) node.remove()
  }
}
