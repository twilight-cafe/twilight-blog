// flags.ts —— 站点功能开关的前端读取
//
// 开关由服务端渲染的 HTML 外壳下发，写在 **<html> 的属性**上
// （data-search-enabled，见 Go 的 template/index.html），而不是 window 上的
// 内联 <script>。两个理由：
//
//  1. 时序：搜索入口挂不挂必须在**首个渲染之前**确定。走 /api/site 意味着
//     首帧先渲染出搜索框、响应回来后再撤掉——用户会看到一次闪烁，
//     而「先画出再隐藏」对开关类功能来说等价于没关干净。HTML 属性在解析时
//     就已就绪，同样满足这一点。
//  2. CSP：站点响应头是 `script-src 'self'`，不含 'unsafe-inline'/hash/nonce，
//     **任何内联脚本都会被浏览器拒绝执行**（见 middleware/security.go）。
//     这里原来读的是 window.__SITE__，而它由外壳里的一段内联脚本赋值——
//     那段脚本从未运行过：搜索关闭时前端读不到 false，走下面的「缺省=全开」
//     兜底，照样渲染出搜索入口，点进去 404。属性通道不碰 CSP。
//
// 属性缺失时按「功能全开」兜底：老二进制的外壳没有这个属性，升级前端
// 而不升级后端时不能把人家正在用的功能凭空藏掉。

/** 搜索（内嵌 Meilisearch）是否启用。外壳未标记（属性缺失）视为启用。 */
export function isSearchEnabled(): boolean {
  // 必须比字符串，不能取布尔值：dataset 的值是 string，
  // 而 Boolean('false') === true —— 那样「关闭」就永远关不上。
  return document.documentElement.dataset.searchEnabled !== 'false'
}

/** 首页每页文章数在属性缺失 / 解析不出来时的兜底。 */
export const FALLBACK_HOME_PAGE_SIZE = 12

/**
 * 首页每页文章数，读自外壳的 `data-home-page-size`。
 *
 * 与 isSearchEnabled 同属「首屏决策」类参数，因此走同一条属性通道——首页列表
 * 在挂载后立刻要发请求，等 /api/site 回来才知道 pageSize 意味着至少要发两次
 * 请求（或先按错的页数渲染再重排）。属性在入口 chunk 执行前就已解析完毕。
 *
 * 属性值经过 HTML 模板输出，是**字符串**：Number() 之前必须先用 Number.parseInt
 * 判空串——`Number('')` 是 0，而 `Number.parseInt('')` 是 NaN，只有后者能被
 * Number.isFinite 拦下来，避免把「属性缺失」静默变成一个 page_size=0 的请求。
 */
export function homePageSize(): number {
  const raw = document.documentElement.dataset.homePageSize
  const parsed = Number.parseInt(raw ?? '', 10)
  if (!Number.isFinite(parsed) || parsed < 1) return FALLBACK_HOME_PAGE_SIZE
  return parsed
}

/** 站点统一圆角（px）在属性缺失 / 解析不出来时的兜底。 */
export const FALLBACK_BORDER_RADIUS = 4

/** 站点统一圆角的合法区间，与 Go 侧 viewmodel.Min/MaxBorderRadius 对应。 */
export const MIN_BORDER_RADIUS = 0
export const MAX_BORDER_RADIUS = 24

/**
 * 站点统一圆角（px），读自外壳的 `data-border-radius`。
 *
 * 走属性通道的理由同 homePageSize：圆角要在首屏就定下来，等 /api/site
 * 回来再改 CSS 变量会让用户看到一次「圆角跳变」。
 *
 * **不能照抄 homePageSize 的 `parsed < 1`**：圆角的下界就是 0，
 * 0 是合法的「直角」。若沿用 `< 1`，站长把圆角设成 0 后前台会一直按
 * 兜底的 4px 渲染——设置页显示 0、页面却是圆角，属于最难排查的那种不一致。
 * 这里判的是**越界**（< 0 或 > 24），0 能正常通过。
 */
export function borderRadius(): number {
  const raw = document.documentElement.dataset.borderRadius
  const parsed = Number.parseInt(raw ?? '', 10)
  if (!Number.isFinite(parsed) || parsed < MIN_BORDER_RADIUS || parsed > MAX_BORDER_RADIUS) {
    return FALLBACK_BORDER_RADIUS
  }
  return parsed
}

/**
 * 把圆角写进 `--radius` CSS 变量。
 *
 * 在 main.tsx 的最开头调用（早于 React 渲染），让首帧就用上对的圆角——
 * 否则第一帧会按样式表里的默认 4px 画，再由 effect 改成设置值，出现闪烁。
 *
 * 写在 :root 上必须用 setProperty：--radius 的消费方遍及 InkUI 与站点样式，
 * 逐个改选择器不现实，变量是唯一可行的注入点。
 */
export function applyBorderRadius(): void {
  document.documentElement.style.setProperty('--radius', `${borderRadius()}px`)
}

/**
 * 微语（Moments）是否启用。外壳未标记（属性缺失）视为启用。
 *
 * 与 isSearchEnabled 同一条通道、同一条兜底：老二进制的 HTML 里没有
 * data-moments-enabled，不能因为前端先升级就把站长正在用的功能藏掉。
 * 关闭时导航 / 页脚 / 侧栏 / 后台菜单 / 仪表盘快捷入口一起过滤，公开的
 * /moments 页给出「未启用」提示。
 */
export function isMomentsEnabled(): boolean {
  return document.documentElement.dataset.momentsEnabled !== 'false'
}

/** 文章头图最大高度（px）在属性缺失 / 解析不出来时的兜底。 */
export const FALLBACK_POST_BANNER_HEIGHT = 400

/**
 * 头图高度的合法区间，与 Go 侧 viewmodel.Min/MaxPostBannerHeight 对应。
 *
 * 0 **不在**区间内：与圆角不同，0px 的头图没有意义，所以服务端把 0 当作
 * 「未设置」并写入默认值。这里同样按越界处理。
 */
export const MIN_POST_BANNER_HEIGHT = 120
export const MAX_POST_BANNER_HEIGHT = 800

/**
 * 文章头图最大高度（px），读自外壳的 `data-post-banner-height`。
 *
 * 走属性通道的理由同 homePageSize：文章数据到达后头图立刻要按最终高度占位，
 * 等 /api/site 回来再改 CSS 变量会让读者看到一次高度跳变。
 *
 * 解析方式与 homePageSize 一致：先 parseInt 再判 NaN。不能用 Number('')——
 * 空串会被它变成 0，而 0 恰好落在「非法」区间之外，会导致下发 0px 的头图。
 */
export function postBannerHeight(): number {
  const raw = document.documentElement.dataset.postBannerHeight
  const parsed = Number.parseInt(raw ?? '', 10)
  if (!Number.isFinite(parsed) || parsed < MIN_POST_BANNER_HEIGHT || parsed > MAX_POST_BANNER_HEIGHT) {
    return FALLBACK_POST_BANNER_HEIGHT
  }
  return parsed
}

/**
 * 把头图高度写进 `--post-banner-height` CSS 变量。
 *
 * 与 applyBorderRadius 一样在 main.tsx 的最开头调用：文章页首帧就要用对的
 * 高度画占位框，否则图片加载完成后整页会往下顶一截。
 */
export function applyPostBannerHeight(): void {
  document.documentElement.style.setProperty('--post-banner-height', `${postBannerHeight()}px`)
}
