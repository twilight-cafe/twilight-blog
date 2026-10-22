// dialog.tsx —— 全局 confirm / prompt 对话框
//
// 直接转发 ink-design 的 `DialogProvider` / `useDialog`（0.1.3 起）：Promise 式 API、
// 单实例（并发调用时后一次覆盖前一次，前一个 Promise 立刻按取消结算，不会
// 永远悬着）、关闭后的退场由 Modal 的 `closeDuration` 接管——这些都不需要
// 站点再实现一遍。
//
// 站点保留这个模块作为唯一入口：App 与测试都从这里取。这样将来库挪动导出
// 位置时，只需要改这一行；调用方不需要知道它来自哪里。
//
// API 备忘（0.1.3 起，0.1.4 未变）：
//   confirm(title, message?, options?) → Promise<boolean>
//   prompt(title, { placeholder, initialValue, maxLength, validate, ... })
//     → Promise<string | null>（确定返回原文，取消 null；站点自己在提交前 trim）

// 纯转发：这个模块没有可执行语句，因此不在覆盖率统计里（见 vitest.config.ts
// 的 coverage.exclude）。**如果将来在这里加逻辑，请同时把它从 exclude 里移出**，
// 否则新代码会悄悄不受覆盖率门约束。
export { DialogProvider, useDialog } from 'ink-design'
