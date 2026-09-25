// markdown-loader.test.ts —— 懒加载渲染器的取用、复用与失败重试
//
// 单独一个文件：这里要看的就是加载器自己的模块级状态，而它只能靠
// vi.resetModules() 回到起点；和别的用例共享注册表就没法观察了。
//
// 注意 vi.resetModules() 会把相对路径的 vi.mock 注册一并清掉（vitest v5 行为），
// 所以每个用例都重新 doMock 一次要替换的模块（与 site-nav.test.tsx 同一先例）。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

/** 从干净的模块注册表里取一份加载器。 */
async function freshLoader(): Promise<typeof import('../lib/markdown-loader')> {
  vi.resetModules()
  return import('../lib/markdown-loader')
}

/** 让惰性块「下载失败」：工厂抛错会让动态 import 变成 rejection。 */
function failChunkLoad(): void {
  vi.doMock('../lib/markdown', () => {
    throw new Error('chunk 下载失败')
  })
}

/**
 * 同上，但数得住「真的又去加载了一次」。
 *
 * 光断言「第二次也 rejects」是**测不出重试**的：把失败缓存起来的实现同样会
 * rejects，绿得毫无信息。所以这里数工厂被调用的次数——只有失败**没有**被缓存，
 * 第二次调用才会重新去求值一次惰性块。
 */
function failChunkLoadCounting(): { attempts: () => number } {
  let attempts = 0
  vi.doMock('../lib/markdown', () => {
    attempts += 1
    throw new Error('chunk 下载失败')
  })
  return { attempts: () => attempts }
}

beforeEach(() => {
  vi.resetModules()
})

afterEach(() => {
  vi.doUnmock('../lib/markdown')
})

describe('markdown-loader', () => {
  it('起始未加载；拿到之后 peek 同步可读，且是同一个实例', async () => {
    const loader = await freshLoader()
    expect(loader.peekMarkdown()).toBeNull()

    const module = await loader.loadMarkdown()
    expect(typeof module.renderMarkdown).toBe('function')
    expect(typeof module.extractToc).toBe('function')
    expect(loader.peekMarkdown()).toBe(module)
  })

  it('已加载后 loadMarkdown 直接 resolve 同一个实例（不再走动态 import）', async () => {
    const loader = await freshLoader()
    const first = await loader.loadMarkdown()
    const second = await loader.loadMarkdown()
    expect(second).toBe(first)
  })

  it('并发调用共享同一个 in-flight 请求', async () => {
    const loader = await freshLoader()
    const [a, b] = await Promise.all([loader.loadMarkdown(), loader.loadMarkdown()])
    expect(a).toBe(b)
  })

  it('加载失败：reject 出去、不写入缓存，下一次调用会真的重试', async () => {
    const counter = failChunkLoadCounting()
    const loader = await freshLoader()

    await expect(loader.loadMarkdown()).rejects.toThrow()
    expect(loader.peekMarkdown()).toBeNull()
    const afterFirst = counter.attempts()

    // 失败**不能**被缓存：一次网络抖动就把整站正文永久降级成纯文本是不可接受的。
    // 判据是「又加载了一次」，不是「又 rejects 了一次」（后者的信息量为零）。
    await expect(loader.loadMarkdown()).rejects.toThrow()
    expect(counter.attempts()).toBeGreaterThan(afterFirst)
  })

  it('reset 之后回到未加载状态，并能重新加载', async () => {
    const loader = await freshLoader()
    await loader.loadMarkdown()
    loader.resetMarkdownLoaderForTests()
    expect(loader.peekMarkdown()).toBeNull()
    // 先 await 再比：写成 peek() 与 loadMarkdown() 同一行的话，peek 会在
    // await 之前求值，于是永远拿到 null（这条断言自己就假红过一次）。
    const reloaded = await loader.loadMarkdown()
    expect(loader.peekMarkdown()).toBe(reloaded)
  })

  it('prefetchMarkdown 永不抛出：失败也不会变成没人接的 rejection', async () => {
    failChunkLoad()
    const loader = await freshLoader()

    const rejections: unknown[] = []
    const onRejection = (reason: unknown) => rejections.push(reason)
    process.on('unhandledRejection', onRejection)
    try {
      expect(() => loader.prefetchMarkdown()).not.toThrow()
      // 给它足够的时间真的落定：prefetch 内部已经 catch，事件不该被触发。
      await new Promise((resolve) => setTimeout(resolve, 10))
      expect(rejections).toEqual([])
    } finally {
      process.off('unhandledRejection', onRejection)
    }
  })

  it('loadToc：正常抽出标题层级', async () => {
    const loader = await freshLoader()
    await expect(loader.loadToc('# 一级\n\n正文\n\n## 二级\n')).resolves.toEqual([
      { id: '一级', text: '一级', level: 1 },
      { id: '二级', text: '二级', level: 2 },
    ])
  })

  it('loadToc：渲染器拿不到时给空目录，而不是把 reject 抛给页面', async () => {
    failChunkLoad()
    const loader = await freshLoader()
    await expect(loader.loadToc('# 一级')).resolves.toEqual([])
  })
})
