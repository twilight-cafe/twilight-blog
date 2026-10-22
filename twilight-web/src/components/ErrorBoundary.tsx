// ErrorBoundary.tsx —— 顶层渲染错误边界
//
// 错误捕获本身由 ink-design 的 `ErrorBoundary` 提供（0.1.3 起；class 实现、
// getDerivedStateFromError / componentDidCatch 只能写成 class），站点只补两样：
//   ① 品牌化的回退界面（Twilight 的登录壳样式）；
//   ② 把细节继续打到控制台。
//
// 存在理由：以前任何一处渲染期异常都会让 React 卸载整棵树，用户只看到一片
// 空白，控制台里的 TypeError 也没人看见。有了边界，故障被限制在一条可读的
// 提示上，而不是无声白屏。
//
// 为什么回退界面不依赖 Router 或 Toast：故障有可能正是路由或 Provider 引起的，
// 此时再让回退界面去 useNavigate / useToast 只会在同一个坑里再摔一次。
// 因此它只用原生 <a> 与 location.reload，不引入任何上下文依赖。

import { ErrorBoundary as InkErrorBoundary } from 'ink-design'
import type { ReactNode } from 'react'
import { t } from '../lib/i18n'

export default function ErrorBoundary({ children }: { children: ReactNode }) {
  return (
    <InkErrorBoundary
      onError={(error, info) => {
        // 仍然打到控制台：回退界面只给用户看一句人话，排查细节留在这里。
        console.error('[error-boundary] 渲染失败', error, info.componentStack)
      }}
      // 用函数形式拿到 error 本体：回退界面要把 message 原样显示出来。
      fallback={(error) => (
        <div className="login-bg">
          <div className="login">
            <div className="lg">
              <span className="sq">T</span>
              <h1>Twilight · {t('页面出错了', 'Something went wrong')}</h1>
            </div>
            <p className="sub">RENDER ERROR</p>
            <p className="sec">{t('这一页在渲染时抛出了异常，已被拦截，其余页面仍可正常访问。', 'This page threw while rendering. It was caught, and the rest of the site still works.')}</p>
            <p className="sec" style={{ color: 'var(--fg)', wordBreak: 'break-word' }}>{error.message}</p>
            <div style={{ display: 'flex', gap: 8, marginTop: 14 }}>
              <a className="btn pri" style={{ flex: 1, justifyContent: 'center' }} href="/">{t('回到首页', 'Back to home')}</a>
              <button type="button" className="btn gho" style={{ flex: 1, justifyContent: 'center' }} onClick={() => window.location.reload()}>
                {t('重新加载', 'Reload')}
              </button>
            </div>
          </div>
        </div>
      )}
    >
      {children}
    </InkErrorBoundary>
  )
}
