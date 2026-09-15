// main.tsx —— 应用入口
// StrictMode + BrowserRouter 包裹 App，最外层再套 ErrorBoundary：任何一处
// 渲染期异常都会变成一条可读提示，而不是整页白屏。注意全局样式顺序：
// highlight.js 的 github-dark 主题被写死引入，意味着代码块在「亮色模式」下
// 也保持深色背景——与整站明暗切换并不完全一致，是已知的可改进点（见交付汇报）。

import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import ErrorBoundary from './components/ErrorBoundary'
import 'highlight.js/styles/github-dark.min.css'
import 'katex/dist/katex.min.css'
import 'ink-design/styles.css'
import './styles/app.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ErrorBoundary>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </ErrorBoundary>
  </React.StrictMode>,
)
