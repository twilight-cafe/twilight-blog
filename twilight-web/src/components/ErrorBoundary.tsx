// ErrorBoundary.tsx —— 顶层渲染错误边界
//
// 存在理由：以前任何一处渲染期异常都会让 React 卸载整棵树，用户只看到一片
// 空白，控制台里的 TypeError 也没人看见。搜索页就踩过这条坑（命中无 tags 的
// 文章时 `result.tags.length` 抛错），一个接口字段缺失能让整站「看起来挂了」。
// 有了边界，故障被限制在一条可读的提示上，而不是无声白屏。
//
// 为什么用 class：componentDidCatch / getDerivedStateFromError 至今没有函数式
// 等价物。这是 React 明确保留的类组件场景。
//
// 为什么回退界面不依赖 Router 或 Toast：故障有可能正是路由或 Provider 引起的，
// 此时再让回退界面去 useNavigate / useToast 只会在同一个坑里再摔一次。
// 因此它只用原生 <a> 与 location.reload，不引入任何上下文依赖。

import { Component, type ErrorInfo, type ReactNode } from 'react'

interface Props {
  children: ReactNode
}

interface State {
  error: Error | null
}

export default class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // 仍然打到控制台：回退界面只给用户看一句人话，排查细节留在这里。
    console.error('[error-boundary] 渲染失败', error, info.componentStack)
  }

  render(): ReactNode {
    const { error } = this.state
    if (!error) return this.props.children

    return (
      <div className="login-bg">
        <div className="login">
          <div className="lg">
            <span className="sq">T</span>
            <h1>Twilight · 页面出错了</h1>
          </div>
          <p className="sub">RENDER ERROR</p>
          <p className="sec">这一页在渲染时抛出了异常，已被拦截，其余页面仍可正常访问。</p>
          <p className="sec" style={{ color: 'var(--fg)', wordBreak: 'break-word' }}>{error.message}</p>
          <div style={{ display: 'flex', gap: 8, marginTop: 14 }}>
            <a className="btn pri" style={{ flex: 1, justifyContent: 'center' }} href="/">回到首页</a>
            <button type="button" className="btn gho" style={{ flex: 1, justifyContent: 'center' }} onClick={() => window.location.reload()}>
              重新加载
            </button>
          </div>
        </div>
      </div>
    )
  }
}
