// client.test.ts —— 认证 store、401 代理、错误文案
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderHook } from '@testing-library/react'

// twilight-blog-api 整体替换：BlogApiError 用可判定的子类，isBlogApiError 用
// 标记属性判定（与真实实现的契约一致：instanceof 或结构判定都算）。
vi.mock('twilight-blog-api', () => {
  class BlogApiError extends Error {
    status?: number
    body?: unknown
    constructor(message: string, options: { status?: number; method?: string; path?: string; body?: unknown }) {
      super(message)
      this.name = 'BlogApiError'
      this.status = options.status
      this.body = options.body
    }
  }
  const isBlogApiError = (error: unknown): error is BlogApiError =>
    typeof error === 'object' && error !== null && (error as { __tag?: unknown }).__tag === 'blog-api-error'
  const createClient = vi.fn(() => new Proxy({} as Record<string, ReturnType<typeof vi.fn>>, {
    get(target, property) {
      if (typeof property !== 'string') return undefined
      if (!target[property]) target[property] = vi.fn()
      return target[property]
    },
  }))
  return { BlogApiError, isBlogApiError, createClient }
})

import { BlogApiError, createClient } from 'twilight-blog-api'
import {
  blog,
  fetchMe,
  getErrorMessage,
  getCurrentUser,
  logout,
  refreshAuth,
  setCurrentUser,
  subscribe,
  useCurrentUser,
} from '../lib/client'

/** client.ts 模块初始化时 createClient 返回的原始客户端对象（对象身份在 mockClear 后仍有效）。 */
const raw = (vi.mocked(createClient).mock.results[0] as { value: Record<string, ReturnType<typeof vi.fn>> }).value

function taggedError(status: number, message = 'boom') {
  const error = new BlogApiError(message, { status, method: 'GET', path: '/' })
  ;(error as unknown as { __tag: string }).__tag = 'blog-api-error'
  return error
}

beforeEach(() => {
  setCurrentUser(null)
  vi.clearAllMocks()
})

describe('auth store', () => {
  it('setCurrentUser 通知订阅者；相同引用不重复通知；退订后不再通知；坏订阅者不拦住别人', () => {
    const first = vi.fn()
    const second = vi.fn(() => {
      throw new Error('bad listener')
    })
    const third = vi.fn()

    const unsubscribeFirst = subscribe(first)
    subscribe(second)
    subscribe(third)

    const user = { role: 'admin' }
    setCurrentUser(user as never)
    expect(first).toHaveBeenCalledTimes(1)
    expect(third).toHaveBeenCalledTimes(1) // second 抛错但没有拦住 third

    setCurrentUser(user as never)
    expect(first).toHaveBeenCalledTimes(1) // 相同引用

    unsubscribeFirst()
    setCurrentUser(null)
    expect(first).toHaveBeenCalledTimes(1)
    expect(getCurrentUser()).toBeNull()
  })

  it('useCurrentUser 绑定 store', () => {
    const { result, rerender } = renderHook(() => useCurrentUser())
    expect(result.current).toBeNull()
    const user = { role: 'user' }
    setCurrentUser(user as never)
    rerender()
    expect(result.current).toBe(user)
  })
})

describe('refreshAuth', () => {
  it('成功后写入 store；并发调用共享同一请求', async () => {
    const me = { role: 'admin' }
    raw.me.mockResolvedValueOnce(me)
    const [a, b] = await Promise.all([refreshAuth(), refreshAuth()])
    expect(a).toBe(me)
    expect(b).toBe(me)
    expect(raw.me).toHaveBeenCalledTimes(1)
    expect(getCurrentUser()).toBe(me)
  })

  it('401 记为匿名；其他错误向上抛', async () => {
    raw.me.mockRejectedValueOnce(taggedError(401))
    await expect(refreshAuth()).resolves.toBeNull()

    raw.me.mockRejectedValueOnce(new BlogApiError('network down', { method: 'GET', path: '/' }))
    await expect(refreshAuth()).rejects.toThrow('network down')
  })
})

describe('fetchMe', () => {
  it('401 → null；成功 → me；其他错误上抛', async () => {
    raw.me.mockResolvedValueOnce({ role: 'user' })
    await expect(fetchMe()).resolves.toEqual({ role: 'user' })

    raw.me.mockRejectedValueOnce({ status: 401 })
    await expect(fetchMe()).resolves.toBeNull()

    raw.me.mockRejectedValueOnce(new Error('offline'))
    await expect(fetchMe()).rejects.toThrow('offline')
  })
})

describe('401 会话过期代理', () => {
  it('方法被透传；promise 拒绝且 401 时清空身份并继续抛出', async () => {
    setCurrentUser({ role: 'admin' } as never)
    raw.listArticles.mockRejectedValueOnce({ status: 401 })
    await expect(blog.listArticles({ page: 1, pageSize: 12 })).rejects.toEqual({ status: 401 })
    expect(getCurrentUser()).toBeNull()
  })

  it('非 401 的失败不清空身份', async () => {
    setCurrentUser({ role: 'admin' } as never)
    raw.listArticles.mockRejectedValueOnce({ status: 403 })
    await expect(blog.listArticles({ page: 1, pageSize: 12 })).rejects.toEqual({ status: 403 })
    expect(getCurrentUser()).toEqual({ role: 'admin' })
  })

  it('同步返回值原样透传', () => {
    raw.someSyncMethod = vi.fn(() => 42) as never
    expect((blog as unknown as { someSyncMethod: () => number }).someSyncMethod()).toBe(42)
  })

  it('非函数属性直接透传（不进 promise 守卫）', () => {
    ;(raw as unknown as { version: string }).version = '9.9.9'
    expect((blog as unknown as { version: string }).version).toBe('9.9.9')
  })

  it('BlogApiError 实例的 401 同样触发清理', async () => {
    setCurrentUser({ role: 'admin' } as never)
    raw.getArticle.mockRejectedValueOnce(new BlogApiError('expired', { status: 401, method: 'GET', path: '/' }))
    await expect(blog.getArticle(1)).rejects.toThrow('expired')
    expect(getCurrentUser()).toBeNull()
  })
})

describe('logout', () => {
  it('服务端失败也清空本地身份', async () => {
    setCurrentUser({ role: 'admin' } as never)
    raw.logout.mockRejectedValueOnce(new Error('network'))
    await expect(logout()).resolves.toBeUndefined()
    expect(getCurrentUser()).toBeNull()
  })

  it('成功路径同样清空', async () => {
    setCurrentUser({ role: 'admin' } as never)
    raw.logout.mockResolvedValueOnce({ message: 'ok' })
    await logout()
    expect(getCurrentUser()).toBeNull()
  })
})

describe('getErrorMessage', () => {
  it('BlogApiError：5xx 回退，4xx 用 message', () => {
    expect(getErrorMessage(new BlogApiError('服务器错误', { status: 500, method: 'GET', path: '/' }), '兜底')).toBe('兜底')
    expect(getErrorMessage(new BlogApiError('标题太长', { status: 422, method: 'GET', path: '/' }), '兜底')).toBe('标题太长')
    expect(getErrorMessage(taggedError(502), '兜底')).toBe('兜底')
    expect(getErrorMessage(taggedError(404), '兜底')).toBe('boom')
  })

  it('普通 Error / 空消息 / 非 Error', () => {
    expect(getErrorMessage(new Error('plain'))).toBe('plain')
    expect(getErrorMessage(new Error(''))).toBe('操作失败')
    expect(getErrorMessage('whatever')).toBe('操作失败')
    expect(getErrorMessage(null, '自定义')).toBe('自定义')
  })
})

describe('baseUrl 解析', () => {
  it('VITE_API_BASE 合法时透传给 createClient，非法/缺省回退同源', async () => {
    vi.resetModules()
    vi.stubEnv('VITE_API_BASE', 'https://api.example.com')
    await import('../lib/client')
    expect(vi.mocked(createClient)).toHaveBeenLastCalledWith({ baseUrl: 'https://api.example.com' })

    vi.resetModules()
    vi.stubEnv('VITE_API_BASE', 'ftp://nope')
    await import('../lib/client')
    expect(vi.mocked(createClient)).toHaveBeenLastCalledWith({ baseUrl: window.location.origin })

    vi.unstubAllEnvs()
    vi.resetModules()
    vi.stubEnv('VITE_API_BASE', undefined)
    await import('../lib/client')
    expect(vi.mocked(createClient)).toHaveBeenLastCalledWith({ baseUrl: window.location.origin })
    vi.unstubAllEnvs()
  })
})
