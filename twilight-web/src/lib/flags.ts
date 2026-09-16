// flags.ts —— 站点功能开关的前端读取
//
// 开关由服务端渲染的 HTML 外壳注入（window.__SITE__，见 Go 的 template/index.html），
// 而不是从 /api/site 拉取。理由是时序：搜索入口挂不挂必须在**首个渲染之前**确定，
// 走接口意味着首帧先渲染出搜索框、响应回来后再撤掉——用户会看到一次闪烁，
// 而「先画出再隐藏」对开关类功能来说等价于没关干净。
//
// 键缺失时按「功能全开」兜底：旧二进制的外壳没有这段内联脚本，升级前端
// 而不升级后端时不能把人家正在用的功能凭空藏掉。

/** 搜索（内嵌 Meilisearch）是否启用。外壳缺省（键缺失）视为启用。 */
export function isSearchEnabled(): boolean {
  return window.__SITE__?.searchEnabled !== false
}
