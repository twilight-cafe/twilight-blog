/// <reference types="vite/client" />

/**
 * 服务端渲染的外壳（Go template/index.html）注入的站点功能开关。
 * 外壳在入口 chunk 之前同步执行，因此 SPA 的首个渲染**之前**这些值已就绪。
 * dev 模式由 vite.config.ts 的 indexShellPlugin 注入同名对象（全开）。
 */
interface Window {
  /** 站点功能开关。键缺失（如旧二进制外壳）时前端按「功能全开」兜底。 */
  __SITE__?: {
    /** 搜索（内嵌 Meilisearch）是否启用；false 时前端隐藏全部搜索入口。 */
    searchEnabled?: boolean
  }
}
