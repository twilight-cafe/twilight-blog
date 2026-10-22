// client.ts —— API 客户端、登录态管理与 401 鉴权
//
// 设计要点：
// - 登录态（session）存在后端下发的 httpOnly cookie 里，前端 JS 读不到，
//   所以「是否登录」由 `GET /api/me` 决定，结果缓存在内存变量 currentUser。
// - 不把 token 写进 localStorage：刷新页面后用 cookie 重新调 /api/me 推导身份。
// - baseURL 优先取 VITE_API_BASE（必须是 http/https），否则回退到同源
//   window.location.origin —— 因前端产物被嵌入 Go 后端，同源即可。
// - `blog` 是对 SDK 客户端的 Proxy 包装：拦截每个方法，遇到 401 就把缓存身份
//   清空（会话过期/被吊销），但原错误仍向上抛出；403 不在此处理（已登录但权限不足）。

import { useSyncExternalStore } from 'react'
import { t } from './i18n'
import {
  BlogApiError,
  createClient,
  isBlogApiError,
  type BlogClient,
  type MeResponse,
} from 'twilight-blog-api'

function resolveBaseUrl(): string {
  const configured = import.meta.env.VITE_API_BASE as string | undefined
  if (configured && /^https?:\/\//i.test(configured)) return configured
  return window.location.origin
}

// 导出仅为测试分支覆盖（纯函数，无行为副作用）。
export function isUnauthorizedError(error: unknown): boolean {
  if (error instanceof BlogApiError || isBlogApiError(error)) return error.status === 401
  if (typeof error === 'object' && error !== null) {
    return (error as { status?: unknown }).status === 401
  }
  return false
}

/* --------------------------------------------------------------------------- *
 * Auth store
 *
 * The session lives in an httpOnly cookie the script cannot read, so "am I
 * signed in?" is answered by the API (`GET /api/me`) and cached in memory.
 * Nothing is written to localStorage: there is no token to persist, and a
 * reload re-derives the identity from the cookie.
 * --------------------------------------------------------------------------- */

let currentUser: MeResponse | null = null
const listeners = new Set<() => void>()

function emit(): void {
  for (const listener of [...listeners]) {
    try {
      listener()
    } catch {
      // A misbehaving subscriber must not stop the others from updating.
    }
  }
}

/** The last identity resolved from the API, or null when signed out. */
export function getCurrentUser(): MeResponse | null {
  return currentUser
}

export function setCurrentUser(user: MeResponse | null): void {
  // Identity comparison keeps a repeated 401 from re-notifying every listener.
  if (currentUser === user) return
  currentUser = user
  emit()
}

export function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/**
 * Re-reads the current session. Concurrent callers share one request so a
 * page-load burst of components does not stampede `/api/me`.
 */
let refreshInFlight: Promise<MeResponse | null> | null = null

export function refreshAuth(): Promise<MeResponse | null> {
  if (refreshInFlight) return refreshInFlight

  refreshInFlight = fetchMe()
    .then((me) => {
      setCurrentUser(me)
      return me
    })
    .finally(() => {
      refreshInFlight = null
    })

  return refreshInFlight
}

/** React binding for the store. */
export function useCurrentUser(): MeResponse | null {
  return useSyncExternalStore(subscribe, getCurrentUser, getCurrentUser)
}

/* --------------------------------------------------------------------------- *
 * Client
 * --------------------------------------------------------------------------- */

const rawBlog: BlogClient = createClient({ baseUrl: resolveBaseUrl() })

/**
 * A 401 means the cookie session is gone (expired, revoked, or cleared). Drop
 * the cached identity so the console falls back to the sign-in page instead of
 * rendering actions that would all fail. The original error is still rethrown
 * for the caller's own handling.
 *
 * 403 is deliberately not treated this way: it means "signed in, wrong role".
 */
function installSessionExpiryWatch(client: BlogClient): BlogClient {
  return new Proxy(client, {
    get(target, property, receiver) {
      const value = Reflect.get(target, property, receiver)
      if (typeof value !== 'function') return value

      const method = value as (...args: unknown[]) => unknown
      return function wrapped(this: unknown, ...args: unknown[]): unknown {
        const result = method.apply(target, args)
        if (result && typeof (result as PromiseLike<unknown>).then === 'function') {
          return (result as Promise<unknown>).catch((error: unknown) => {
            if (isUnauthorizedError(error)) setCurrentUser(null)
            throw error
          })
        }
        return result
      }
    },
  }) as BlogClient
}

export const blog: BlogClient = installSessionExpiryWatch(rawBlog)

/** Resolves the signed-in identity, or null when not signed in. */
export async function fetchMe(): Promise<MeResponse | null> {
  try {
    return await blog.me()
  } catch (error) {
    if (isUnauthorizedError(error)) return null
    throw error
  }
}

/**
 * Signs out. Revoking the session server-side is best effort: the cached
 * identity is always dropped so a network failure cannot leave the UI looking
 * signed in.
 */
export async function logout(): Promise<void> {
  try {
    await blog.logout()
  } catch {
    // Ignore: local sign-out must still succeed.
  } finally {
    setCurrentUser(null)
  }
}

/**
 * Turns any thrown value into a message safe to show the user.
 *
 * 5xx 一律回退到调用方给的文案：这类响应的正文不受本站控制（反向代理、网关、
 * panic 处理器都可能往里写堆栈或内部路径），把它显示给用户既无帮助又属于信息泄露。
 * SDK 侧已经把 5xx 的 message 降级成固定文案，这里是第二道——两边各自成立，
 * 谁被单独改动都不会让原文重新出现在界面上。原文仍在 error.body 里可供排查。
 */
export function getErrorMessage(error: unknown, fallback = t('操作失败', 'Action failed')): string {
  if (error instanceof BlogApiError || isBlogApiError(error)) {
    if (typeof error.status === 'number' && error.status >= 500) return fallback
    return error.message || fallback
  }
  if (error instanceof Error) return error.message || fallback
  return fallback
}
