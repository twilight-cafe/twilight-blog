// admin-lazy-routes.test.tsx —— 后台路由懒加载的两个边界：挂起中、chunk 拉取失败
//
// 为什么单独一个文件：这两个场景需要把某个后台页面换成「永远挂起」或「导入即失败」的
// 替身，而这是**模块级** mock，会污染同文件内其它用例（App 的路由表测试要求每个页面
// 都能正常渲染出来）。用独立文件隔离，好过在主测试里加一个测试专用开关——那种开关
// 一旦被误留在生产路径上，就是「加载失败时什么都不显示」的来源。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

import App from '../App'
import ErrorBoundary from '../components/ErrorBoundary'
import { adminMe, getSharedClientMock, resetClientMock } from './helpers'

// 永远挂起：抛出一个永不 resolve 的 Promise，React 视为「还在加载」，
// Suspense 因此停在 fallback 上。
vi.mock('../pages/admin/Dashboard', () => ({
  default: () => {
    throw new Promise(() => {})
  },
}))
vi.mock('../pages/admin/Register', () => ({
  default: () => {
    throw new Promise(() => {})
  },
}))
// 页面加载失败：用一个「渲染即抛」的替身，而不是让 vi.mock 工厂抛错——工厂抛错会被
// vitest 包成它自己的文案（"[vitest] There was an error when mocking a module…"），
// 断言那层文案毫无意义。这里要守的契约是「懒加载页面之上必须有错误边界」，
// 而渲染期抛错与导入被拒（React.lazy 会把 rejection 重新抛在渲染期）走的是同一条路。
vi.mock('../pages/admin/SignIn', () => ({
  default: () => {
    throw new Error('Failed to fetch dynamically imported module')
  },
}))

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

beforeEach(() => {
  resetClientMock(client)
  client.setCurrentUser(adminMe)
  client.refreshAuth.mockResolvedValue(adminMe)
  client.blog.getInstallStatus.mockResolvedValue({ installed: true })
  client.blog.getSite.mockResolvedValue({ description: '', avatar_url: '', code_block_max_lines: 20 })
})

function renderApp(route: string) {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <App />
    </MemoryRouter>,
  )
}

describe('后台路由懒加载', () => {
  it('页面 chunk 未就绪：只有内容区显示 LOADING，控制台外壳保持挂载', async () => {
    renderApp('/admin')

    await waitFor(() => expect(screen.getByText('LOADING…')).toBeTruthy(), { timeout: 3000 })
    // 关键断言：壳层（侧栏/顶栏）没有被 fallback 顶掉。边界若放到整个 Routes 外面
    // 就会整屏闪，这条就是防那个的。
    expect(document.body.textContent).toContain('ADMIN CONSOLE')
    expect(document.body.textContent).toContain('后台 / 概览')
  })

  it('登录/注册这类独立页：各自的 Suspense 也会给出加载态', async () => {
    renderApp('/admin/register')

    await waitFor(() => expect(screen.getByText('LOADING…')).toBeTruthy(), { timeout: 3000 })
  })

  it('chunk 拉取失败：交给顶层错误边界给出可读提示，而不是白屏', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      render(
        <MemoryRouter initialEntries={['/admin/login']}>
          <ErrorBoundary>
            <App />
          </ErrorBoundary>
        </MemoryRouter>,
      )

      await waitFor(() => expect(screen.getByText('Twilight · 页面出错了')).toBeTruthy(), { timeout: 3000 })
      expect(document.body.textContent).toContain('Failed to fetch dynamically imported module')
    } finally {
      errorSpy.mockRestore()
    }
  })
})
