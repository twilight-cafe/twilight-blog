// NotFound.tsx —— 404 页面（纯展示，从简）。
import { Link } from 'react-router-dom'
import { Button } from 'ink-design'
import { useDocumentTitle } from '../../lib/site'
import { useCanonicalPath, usePageHead } from '../../lib/head'
import { useI18n } from '../../lib/i18n'

export default function NotFound() {
  const { t } = useI18n()
  useDocumentTitle(t('页面不存在', 'Page not found'))
  // 404 一律 noindex：它被文章页与独立页在「取不到内容」时就地复用，
  // 而那个时刻地址栏里往往还写着 /post/123 这类看似正常的地址。
  usePageHead({ path: useCanonicalPath(), title: t('页面不存在', 'Page not found'), robots: 'noindex, nofollow' })
  return (
    <main className="wrap">
      <div className="e404 rv">
        <div>
          <h1>404<span className="ink" /></h1>
          <p>{t('这一页像被墨涂掉了——别急，好的内容都还在。', 'This page has been inked out — but the good stuff is still here.')}</p>
          <div className="row">
            <Link to="/" style={{ textDecoration: 'none' }}><Button primary>{t('回到首页', 'Back to home')}</Button></Link>
            <Link to="/archive" style={{ textDecoration: 'none' }}><Button ghost>{t('翻翻归档', 'Browse the archive')}</Button></Link>
          </div>
        </div>
      </div>
    </main>
  )
}
