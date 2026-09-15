// setup.ts —— jsdom 环境补齐
// jsdom 不实现的浏览器 API 在这里给出可控替身；每个用例后清理 DOM。

import { afterEach, vi, type Mock } from 'vitest'
import { cleanup } from '@testing-library/react'

afterEach(() => {
  cleanup()
  window.localStorage.clear()
})

if (!window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }),
  })
}

// Reveal 组件依赖 IntersectionObserver；替身把实例挂到全局数组上，
// 用例可以手动触发回调来模拟「进入视口」。
interface FakeIntersectionObserverInstance {
  callback: IntersectionObserverCallback
  observe: (target: Element) => void
  unobserve: Mock
  disconnect: Mock
  targets: Element[]
}

const observedObservers: FakeIntersectionObserverInstance[] = []

class FakeIntersectionObserver implements IntersectionObserver {
  readonly root: Element | Document | null = null
  readonly rootMargin: string = ''
  readonly thresholds: readonly number[] = []
  readonly callback: IntersectionObserverCallback
  readonly targets: Element[] = []
  readonly unobserve: Mock = vi.fn()
  readonly disconnect: Mock = vi.fn()

  constructor(callback: IntersectionObserverCallback) {
    this.callback = callback
    // 把实例挂到静态数组上，测试通过它拿到实例来手动触发回调
    FakeIntersectionObserver.instances.push(this)
  }

  observe(target: Element): void {
    ;(this.targets as Element[]).push(target)
  }

  takeRecords(): IntersectionObserverEntry[] {
    return []
  }

  static instances: FakeIntersectionObserverInstance[] = []
}
Object.defineProperty(FakeIntersectionObserver, 'instances', {
  value: observedObservers as unknown as FakeIntersectionObserverInstance[],
})
// 用 defineProperty 而不是 vi.stubGlobal：这是环境固定装置，不是单个用例的替身。
// 用例里 vi.unstubAllGlobals()（比如换掉 fetch）不应把它们连带拆掉。
Object.defineProperty(globalThis, 'IntersectionObserver', {
  writable: true,
  configurable: true,
  value: FakeIntersectionObserver,
})

if (!window.ResizeObserver) {
  Object.defineProperty(globalThis, 'ResizeObserver', {
    writable: true,
    configurable: true,
    value: class {
      observe = vi.fn()
      unobserve = vi.fn()
      disconnect = vi.fn()
    },
  })
}

window.scrollTo = vi.fn()
Element.prototype.scrollIntoView = vi.fn()

if (!document.execCommand) {
  document.execCommand = vi.fn()
}
