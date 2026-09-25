// url.ts —— 站内相对路径 → 可直接贴进浏览器地址栏的绝对地址
//
// 为什么需要转换：接口给媒体的 url 是**站内相对路径**（/api/media/{id}），因为它
// 同时要喂给 <img src> / <video src>——那两处**必须**用相对路径：写死 origin 会在
// 反向代理、多域名部署或 vite 代理下裂图（见 twilight-blog-api 的 mediaUrl）。
// 「复制链接」是唯一需要绝对地址的动作，所以只在复制这一处转换，不去改数据层的形状。
//
// 基准取 window.location.origin（用户此刻正在浏览的地址），而不是 VITE_API_BASE：
// 复制出来的链接是给人贴进地址栏的，用当前 origin 一定打得到；而在本项目的两种部署
// 下两者本就等价（生产 Go 内嵌 SPA 同源，开发由 vite 代理 /api）。

/**
 * 把站内相对路径拼成绝对地址；已经是绝对 URL（`https://…`、`//…`）的原样返回。
 *
 * 兜底返回原值的理由：调用点在**渲染期**求值（CopyButton 的 text），畸形输入若直接
 * 抛出会连带炸掉整个媒体库页面；降级成「复制到相对路径」比白屏好。
 */
export function toAbsoluteUrl(path: string): string {
  try {
    return new URL(path, window.location.origin).href
  } catch {
    return path
  }
}
