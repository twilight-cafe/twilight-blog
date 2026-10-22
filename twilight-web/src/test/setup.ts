// setup.ts —— jsdom 环境补齐
// jsdom 不实现的浏览器 API 在这里给出可控替身；每个用例后清理 DOM。
//
// 本文件**只对 jsdom 用例生效**：个别用例（如 ink-design-alias）跑在 node 环境里
// （它要 import vite 配置，而 vite 连带加载的 esbuild 在 jsdom 的 Uint8Array 上会
// 直接终止整个测试文件），那种环境下既没有 window 也没有 DOM 需要补齐。

import { afterEach, beforeAll, vi, type Mock } from 'vitest'
import { cleanup } from '@testing-library/react'
import { message } from 'ink-design'
import { loadMarkdown } from '../lib/markdown-loader'
import { clearEmailCodeCooldown } from '../lib/email-code-cooldown'
import { resetLocaleForTests } from '../lib/i18n'

if (typeof window !== 'undefined') installJsdomFixtures()

function installJsdomFixtures(): void {
  // Markdown 渲染器（KaTeX + highlight.js）已改成懒加载块（见 markdown-loader.ts），
  // ContentView 用 peek 取它——**加载过之后才是同步渲染**。这里预取一次，让所有
  // 断言正文的用例保持「渲染即出内容」，不必逐个从 getByText 改成 findByText。
  //
  // 懒加载本身的行为不由这里负责：三个状态（未到位/已在手/失败降级）由
  // content-view-lazy.test.tsx 守着，那一组用例会 resetModules 主动回到未加载态。
  beforeAll(async () => {
    await loadMarkdown()
  })

  afterEach(() => {
    cleanup()
    // ink-design 的全局 message 单例把 2.2s 自动关闭计时器挂在它自己的
    // React root 上；测试只卸载 Testing Library 的根，不销毁这个单例。
    // 若最后一条 toast 是在用例结束前 2.2s 内创建的，计时器会在 jsdom
    // 已销毁之后触发，Vitest 报 “window is not defined” 并让覆盖率任务
    // 以非零状态退出。这里显式 destroy，让每条用例结束时计时器与 root 一起清理。
    message.destroy()
    window.localStorage.clear()
    // 验证码冷却也是**模块级**状态（见 lib/email-code-cooldown）：不清的话，
    // 上一个用例发过码，下一个用例一进页面按钮就是「60 秒后可重发」且 disabled，
    // 表现为一连串与冷却毫不相干的用例莫名其妙点不动按钮。
    // 抹掉它相当于「重新打开页面」——真实浏览器里刷新就会回到无冷却的初始态。
    clearEmailCodeCooldown()
    // 语言也是模块级状态：一个用例切到英文后，下一个用例会莫名其妙地
    // 拿到英文界面。恢复默认语言等同于「重新打开页面」。
    resetLocaleForTests()
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
}
