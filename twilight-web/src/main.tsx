// main.tsx —— 应用入口
// StrictMode + BrowserRouter 包裹 App，最外层再套 ErrorBoundary：任何一处
// 渲染期异常都会变成一条可读提示，而不是整页白屏。
//
// 全局样式顺序：InkUI 的样式跟着各自组件走（各组件源码 import
// `../styles/parts/*.css`），app.css 是 main.tsx 里**最后**一条 import，
// 因此它排在所有 InkUI 样式之后——站点样式要能盖住组件默认样式，靠的就是这个
// 顺序（两边特异性常常相同）。`inkui-css-parts.test.ts` 守着这条。
//
// 这里**刻意不引入 KaTeX 与 highlight.js 的样式**：它们跟着
// `lib/markdown.ts` 走懒加载（见 `lib/markdown-loader.ts`）。放在这里会让它们
// 连同 62 个字体文件一起变成渲染阻塞资源，而只有「内容真的渲染」时才用得上。

import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import ErrorBoundary from './components/ErrorBoundary'
import { applyBorderRadius, applyPostBannerHeight } from './lib/flags'
import { applyLocale } from './lib/i18n'
import './styles/app.css'

// 在 React 渲染之前把站点圆角写进 --radius：放在这里而不是某个 effect 里，
// 是为了让**第一帧**就用上正确的圆角。若等到组件挂载后再改，用户会看到
// 一次肉眼可见的「圆角跳变」，而这正是走属性通道要避免的事。
applyBorderRadius()
applyPostBannerHeight()
applyLocale()

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ErrorBoundary>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </ErrorBoundary>
  </React.StrictMode>,
)
