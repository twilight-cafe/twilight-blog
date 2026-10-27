// latest.ts —— 让异步结果只在「仍是最新一轮请求」时写入状态。
//
// 典型场景是文章/页面详情：路由参数从 1 变成 2 时组件实例会被复用，
// 两次请求可能乱序返回。若旧请求先发起、后返回，直接 setState 会把
// 第 1 篇的内容覆盖到第 2 篇的地址上——地址栏与正文对不上，且没有任何报错。
//
// 调用方每次发请求前把 ref 递增得到 id，响应回来时用本函数比较：
// 只有 id 仍等于 ref.current 才执行写入。

export function applyIfLatest(ref: { current: number }, id: number, apply: () => void): void {
  if (ref.current === id) apply()
}
