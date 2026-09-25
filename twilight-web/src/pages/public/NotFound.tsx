// NotFound.tsx —— 404 页面（纯展示，从简）。
import { Link } from 'react-router-dom'
import { Button } from 'ink-design'
import { useDocumentTitle } from '../../lib/site'
import { useCanonicalPath, usePageHead } from '../../lib/head'

export default function NotFound() {
  useDocumentTitle('页面不存在')
  // 404 一律 noindex：它被文章页与独立页在「取不到内容」时就地复用，
  // 而那个时刻地址栏里往往还写着 /post/123 这类看似正常的地址。
  usePageHead({ path: useCanonicalPath(), title: '页面不存在', robots: 'noindex, nofollow' })
  return (
    <main className="wrap">
      <div className="e404 rv">
        <div>
          <h1>404<span className="ink" /></h1>
          <p>这一页像被墨涂掉了——别急，好的内容都还在。</p>
          <div className="row">
            <Link to="/" style={{ textDecoration: 'none' }}><Button primary>回到首页</Button></Link>
            <Link to="/archive" style={{ textDecoration: 'none' }}><Button ghost>翻翻归档</Button></Link>
          </div>
        </div>
      </div>
    </main>
  )
}
