// helpers.tsx —— 测试共享工具
// - makeClientMock：vi.mock('../lib/client') 的工厂。lib/client 的语义在测试里
//   用最小实现复刻（同源 store + 自动生成 vi.fn 的 blog Proxy），行为契约与真实
//   模块一致：setCurrentUser 触发订阅者、logout 恒清身份、getErrorMessage 对
//   5xx 回退 fallback。
// - renderWithProviders：包上 Toast/Dialog 两个 Provider 与 MemoryRouter。
import { useSyncExternalStore, type ReactElement, type ReactNode } from 'react'
import { render } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { vi } from 'vitest'
import { DialogProvider } from '../lib/dialog'
import { ToastProvider } from '../lib/toast'

export function makeClientMock() {
  let user: unknown = null
  const listeners = new Set<() => void>()

  const getCurrentUser = () => user
  const setCurrentUser = (next: unknown) => {
    if (user === next) return
    user = next
    for (const listener of [...listeners]) listener()
  }
  const subscribe = (listener: () => void) => {
    listeners.add(listener)
    return () => {
      listeners.delete(listener)
    }
  }
  const useCurrentUser = () => useSyncExternalStore(subscribe, getCurrentUser, getCurrentUser)

  const blogTarget: Record<string, ReturnType<typeof vi.fn>> = {}

  return {
    getCurrentUser,
    setCurrentUser,
    subscribe,
    useCurrentUser,
    refreshAuth: vi.fn(() => Promise.resolve(user)),
    logout: vi.fn(() => {
      setCurrentUser(null)
      return Promise.resolve()
    }),
    getErrorMessage: (error: unknown, fallback = '操作失败'): string => {
      if (error && typeof error === 'object' && typeof (error as { status?: unknown }).status === 'number') {
        return (error as { status: number }).status >= 500 ? fallback : ''
      }
      if (error instanceof Error) return error.message || fallback
      return fallback
    },
    blog: new Proxy(blogTarget, {
      get(target, property) {
        if (typeof property !== 'string') return undefined
        if (!target[property]) target[property] = vi.fn()
        return target[property]
      },
    }),
    /** 供 resetClientMock 遍历清队；Proxy 的 get 陷阱不会拦截这个静态属性。 */
    __blogTarget: blogTarget,
  }
}

export type ClientMock = Omit<ReturnType<typeof makeClientMock>, '__blogTarget'> & {
  __blogTarget: Record<string, ReturnType<typeof vi.fn>>
}

/**
 * 彻底重置 mock：clearAllMocks 只清调用记录，**清不掉 once 队列**——上一个用例
 * 没消费完的 mockResolvedValueOnce 会泄漏到下一个用例（FIFO 被前面未消费项占用）。
 * 每个用例的 beforeEach 里调用，blog 方法逐个 mockReset（连实现一起清），
 * refreshAuth/logout 重建默认实现。
 */
export function resetClientMock(mock: ClientMock): void {
  for (const fn of Object.values(mock.__blogTarget)) {
    if (fn && typeof fn.mockReset === 'function') fn.mockReset()
  }
  mock.refreshAuth.mockReset()
  mock.refreshAuth.mockImplementation(() => Promise.resolve(mock.getCurrentUser()))
  mock.logout.mockReset()
  mock.logout.mockImplementation(() => {
    mock.setCurrentUser(null)
    return Promise.resolve()
  })
}

/**
 * 代际内单例。vi.doMock 的工厂可能被不同导入方的说明符（'../lib/client' 与
 * './client'）各执行一次，若每次都新建实例，测试与被测模块就会各拿各的 mock。
 * 用模块级缓存保证同一次模块求值（同一代 registry）只建一份。
 */
const globalForClientMock = globalThis as unknown as { __twClientMock?: ClientMock }

export function getSharedClientMock(): ClientMock {
  if (!globalForClientMock.__twClientMock) globalForClientMock.__twClientMock = makeClientMock()
  return globalForClientMock.__twClientMock
}

export function renderWithProviders(ui: ReactElement, { route = '/', withRouter = true }: { route?: string; withRouter?: boolean } = {}) {
  function Wrapper({ children }: { children: ReactNode }) {
    const tree = (
      <ToastProvider>
        <DialogProvider>{children}</DialogProvider>
      </ToastProvider>
    )
    return withRouter ? <MemoryRouter initialEntries={[route]}>{tree}</MemoryRouter> : tree
  }
  return render(ui, { wrapper: Wrapper })
}

/* ----------------------------- 数据构造器 ----------------------------- */

export const NOW = 1_700_000_000_000 // 毫秒级时间戳（>1e12）

export function makeArticle(overrides: Partial<import('twilight-blog-api').Article> = {}): import('twilight-blog-api').Article {
  return {
    id: 1,
    title: '测试文章',
    content: '这是正文内容。',
    created_at: NOW,
    updated_at: NOW,
    // 默认没有头图。它是**必填**字段（服务端恒输出 null），所以构造器也必须给出，
    // 否则每个用到 makeArticle 的用例都会缺一个字段。需要头图的用例显式覆盖它。
    cover_media_id: null,
    tags: ['Go'],
    ...overrides,
  }
}

export function makePage(overrides: Partial<import('twilight-blog-api').Page> = {}): import('twilight-blog-api').Page {
  return {
    id: 1,
    title: '关于',
    content: '# 关于\n正文',
    created_at: NOW,
    updated_at: NOW,
    ...overrides,
  }
}

export function makeTag(overrides: Partial<import('twilight-blog-api').Tag> = {}): import('twilight-blog-api').Tag {
  return { id: 1, name: 'Go', article_count: 3, ...overrides }
}

export function makeMoment(overrides: Partial<import('twilight-blog-api').Moment> = {}): import('twilight-blog-api').Moment {
  return { id: 1, content: '一条微语', media_items: [], created_at: NOW, updated_at: NOW, ...overrides }
}

export function makeComment(overrides: Partial<import('twilight-blog-api').Comment> = {}): import('twilight-blog-api').Comment {
  return {
    id: 1,
    author: { type: 'user', id: 7, nickname: '访客甲' },
    target_type: 'article',
    target_id: 1,
    content: '顶一个',
    like_count: 0,
    liked_by_me: false,
    created_at: NOW,
    updated_at: NOW,
    ...overrides,
  }
}

export function makeMedia(overrides: Partial<import('twilight-blog-api').MediaItem> = {}): import('twilight-blog-api').MediaItem {
  return {
    id: 1,
    url: '/uploads/a.png',
    original_name: 'a.png',
    media_type: 'image',
    mime_type: 'image/png',
    size: 1024,
    converted: false,
    created_at: NOW,
    ...overrides,
  }
}

export function makeUser(overrides: Partial<import('twilight-blog-api').UserResponse> = {}): import('twilight-blog-api').UserResponse {
  return {
    id: 7,
    username: 'alice',
    nickname: '爱丽丝',
    email: 'alice@example.com',
    has_password: true,
    created_at: NOW,
    updated_at: NOW,
    ...overrides,
  }
}

export const adminMe: import('twilight-blog-api').MeResponse = { role: 'admin', name: '站长', username: 'admin' }
export const visitorMe: import('twilight-blog-api').MeResponse = { role: 'user', user: makeUser() }

/** flush：把微任务队列跑完（await 化的 effect、Promise 链）。 */
export async function flush(times = 3): Promise<void> {
  for (let i = 0; i < times; i += 1) {
    // eslint-disable-next-line no-await-in-loop
    await Promise.resolve()
  }
}
