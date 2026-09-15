// NotFound.tsx —— 404 页面（纯展示，从简）。
import { Link } from 'react-router-dom'
import { Button } from 'ink-design'
import { useDocumentTitle } from '../../lib/site'

export default function NotFound() {
  useDocumentTitle('页面不存在')
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
