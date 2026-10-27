// content-media.test.tsx —— ContentView 渲染后处理 与 媒体组件
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import ContentView, { enhanceCodeBlocks } from '../components/ContentView'
import MediaThumb from '../components/media/MediaThumb'
import MediaLightbox from '../components/media/MediaLightbox'
import MediaPicker from '../components/media/MediaPicker'
import MediaGallery from '../components/media/MediaGallery'
import { ToastProvider } from '../lib/toast'
import { refreshSiteMeta, resetSiteCacheForTests } from '../lib/site'
import { setLocale } from '../lib/i18n'
import { getSharedClientMock, makeMedia } from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

describe('ContentView', () => {
  // ContentView 订阅站点快照（代码块行数上限）：不给 getSite 一个实现的话，
  // 懒建的 vi.fn() 返回 undefined，loadMeta 的 .then 会直接崩掉整个用例。
  beforeEach(() => {
    resetSiteCacheForTests()
    client.blog.getSite.mockReset()
    client.blog.getSite.mockResolvedValue({ description: '', avatar_url: '', code_block_max_lines: 20 })
  })
  afterEach(() => {
    resetSiteCacheForTests()
    setLocale('zh')
  })

  it('默认走评论白名单管线；trusted 走文章管线', () => {
    const { container, rerender } = render(<ContentView text="**粗体**" />)
    expect(container.querySelector('.plain-body')?.innerHTML).toContain('<strong>粗体</strong>')

    rerender(<ContentView text="# 标题" trusted className="extra" />)
    expect(container.querySelector('h1#标题')).toBeTruthy()
    expect(container.querySelector('.plain-body')?.className).toBe('plain-body extra')

    rerender(<ContentView text="" />)
    expect(container.querySelector('.plain-body')?.innerHTML).toBe('')
  })

  it('代码块被增强：容器 + 语言标签 + 复制按钮（clipboard 成功路径与计时还原）', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    vi.useFakeTimers()
    try {
      const { container } = render(<ContentView text={'```ts\nconst a = 1\n```'} trusted />)

      const wrapper = container.querySelector('.code-block')
      expect(wrapper).toBeTruthy()
      expect(container.querySelector('.code-lang')?.textContent).toBe('ts')
      const copy = container.querySelector('.code-copy') as HTMLButtonElement
      expect(copy.textContent).toBe('复制')

      await act(async () => {
        fireEvent.click(copy)
      })
      expect(copy.textContent).toBe('已复制')
      expect(writeText).toHaveBeenCalledWith('const a = 1\n')

      await act(async () => {
        vi.advanceTimersByTime(1700)
      })
      expect(copy.textContent).toBe('复制')
    } finally {
      vi.useRealTimers()
    }
  })

  it('连续复制两次：第一次的还原计时器不会提前结束第二次反馈', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    vi.useFakeTimers()
    try {
      const { container } = render(<ContentView text={'```ts\nconst a = 1\n```'} trusted />)
      const copy = container.querySelector('.code-copy') as HTMLButtonElement

      await act(async () => {
        fireEvent.click(copy)
      })
      await act(async () => {
        vi.advanceTimersByTime(1000)
      })
      await act(async () => {
        fireEvent.click(copy)
      })

      // 第一次点击的 1600ms 计时器已被取消：到 1700ms 时仍应显示第二次的反馈。
      await act(async () => {
        vi.advanceTimersByTime(700)
      })
      expect(copy.textContent).toBe('已复制')

      // 第二次点击自己的计时器照常生效。
      await act(async () => {
        vi.advanceTimersByTime(1000)
      })
      expect(copy.textContent).toBe('复制')
    } finally {
      vi.useRealTimers()
    }
  })

  it('复制反馈期间发生重渲染：计时器不会被提交时的 cleanup 提前清掉', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    vi.useFakeTimers()
    try {
      const { container, rerender } = render(<ContentView text={'```ts\nconst a = 1\n```'} trusted />)
      const copy = () => container.querySelector('.code-copy') as HTMLButtonElement
      await act(async () => {
        fireEvent.click(copy())
      })
      expect(copy().textContent).toBe('已复制')

      // 任意父级提交都会重跑无依赖的 layout effect；它的 cleanup 只能负责
      // 卸载清理，不能把仍在计时的反馈取消。
      rerender(<ContentView text={'```ts\nconst a = 1\n```'} trusted />)
      await act(async () => {
        vi.advanceTimersByTime(1700)
      })
      expect(copy().textContent).toBe('复制')
    } finally {
      vi.useRealTimers()
    }
  })

  it('语言切换后已增强的复制按钮文案跟着更新', () => {
    const { container, rerender } = render(<ContentView text={'```ts\nconst a = 1\n```'} trusted />)
    // 每次查询都重新取：React 19 在部分提交里会重设 innerHTML，
    // 旧按钮可能已经被替换成新的 DOM 节点。
    const copy = () => container.querySelector('.code-copy') as HTMLButtonElement
    expect(copy().textContent).toBe('复制')

    act(() => setLocale('en'))
    // 真实场景里是订阅了语言的父组件触发这次重渲染；这里直接重渲染同内容。
    rerender(<ContentView text={'```ts\nconst a = 1\n```'} trusted />)
    expect(copy().textContent).toBe('Copy')
    expect(copy().title).toBe('Copy code')
    expect(copy().getAttribute('aria-label')).toBe('Copy code')
  })

  it('已包壳的代码块重跑增强时更新复制按钮文案', () => {
    act(() => setLocale('en'))
    const root = document.createElement('div')
    root.innerHTML = '<div class="code-block"><pre><code class="language-ts">x</code></pre>' +
      '<button class="code-copy">复制</button></div>'
    enhanceCodeBlocks(root, new Set())
    const copy = root.querySelector('.code-copy') as HTMLButtonElement
    expect(copy.textContent).toBe('Copy')
    expect(copy.title).toBe('Copy code')
    expect(copy.getAttribute('aria-label')).toBe('Copy code')
  })

  it('已包壳但复制按钮缺失时重跑增强不报错', () => {
    const root = document.createElement('div')
    root.innerHTML = '<div class="code-block"><pre><code class="language-ts">x</code></pre></div>'
    expect(() => enhanceCodeBlocks(root, new Set())).not.toThrow()
  })

  it('复制成功的反馈期间重跑增强不覆盖按钮文案，但标题与 aria 跟随语言', () => {
    act(() => setLocale('en'))
    const root = document.createElement('div')
    root.innerHTML = '<div class="code-block"><pre><code class="language-ts">x</code></pre>' +
      '<button class="code-copy copied" title="复制代码" aria-label="复制代码">已复制</button></div>'
    enhanceCodeBlocks(root, new Set())
    const copy = root.querySelector('.code-copy') as HTMLButtonElement
    // 短暂的成功反馈不能被打断……
    expect(copy.textContent).toBe('已复制')
    // ……但悬浮提示与读屏标签不该停在旧语言。
    expect(copy.title).toBe('Copy code')
    expect(copy.getAttribute('aria-label')).toBe('Copy code')
  })

  it('clipboard 缺失时回退 textarea+execCommand；execCommand 抛错显示失败', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    const { container } = render(<ContentView text={'```\nplain\n```'} trusted />)
    const copy = container.querySelector('.code-copy') as HTMLButtonElement

    const exec = vi.fn()
    document.execCommand = exec
    await act(async () => {
      fireEvent.click(copy)
    })
    await waitFor(() => expect(copy.textContent).toBe('已复制'))
    expect(exec).toHaveBeenCalledWith('copy')

    document.execCommand = vi.fn(() => {
      throw new Error('denied')
    }) as unknown as typeof document.execCommand
    const second = render(<ContentView text={'```\nplain2\n```'} trusted />)
    const copy2 = second.container.querySelector('.code-copy') as HTMLButtonElement
    await act(async () => {
      fireEvent.click(copy2)
    })
    await waitFor(() => expect(copy2.textContent).toBe('复制失败'))
    document.execCommand = vi.fn() as unknown as typeof document.execCommand
  })

  it('无代码块时不做增强；卸载清理计时器不报错', () => {
    const { container, unmount } = render(<ContentView text="普通**文本**" trusted />)
    expect(container.querySelector('.code-block')).toBeNull()
    expect(() => unmount()).not.toThrow()
  })

  it('限高行数经 --code-max-lines 注入容器；站点设置更新后跟随', async () => {
    const { container } = render(<ContentView text="文本" trusted />)
    const el = container.querySelector('.plain-body') as HTMLElement
    // getSite 回的是 beforeEach 里的 20。
    await waitFor(() => expect(el.style.getPropertyValue('--code-max-lines')).toBe('20'))

    // 站长改完设置的回传路径：refreshSiteMeta 丢缓存重拉 → publish → 订阅者更新。
    client.blog.getSite.mockResolvedValue({ description: '', avatar_url: '', code_block_max_lines: 42 })
    await act(async () => {
      await refreshSiteMeta()
    })
    await waitFor(() => expect(el.style.getPropertyValue('--code-max-lines')).toBe('42'))
  })
})

describe('MediaThumb', () => {
  it('图片：点击主体触发 onOpen；无 onRemove 时不显示删除', () => {
    const onOpen = vi.fn()
    const { container } = render(<MediaThumb item={makeMedia()} onOpen={onOpen} />)
    fireEvent.click(container.querySelector('.media-thumb-open') as HTMLButtonElement)
    expect(onOpen).toHaveBeenCalledOnce()
    expect(container.querySelector('.media-thumb-remove')).toBeNull()
    expect(container.querySelector('img')).toBeTruthy()
  })

  it('视频：渲染 video 与播放标记；有 onRemove 显示删除', () => {
    const onRemove = vi.fn()
    const { container } = render(
      <MediaThumb item={makeMedia({ media_type: 'video', mime_type: 'video/mp4' })} onRemove={onRemove} />,
    )
    const open = container.querySelector('.media-thumb-open') as HTMLButtonElement
    expect(container.querySelector('video')).toBeTruthy()
    expect(container.querySelector('.media-thumb-play')).toBeTruthy()
    // 首帧未到：骨架盖在黑框上；loadeddata 之后 CSS 用这个属性淡出骨架。
    expect(open.querySelector('.img-skeleton')).toBeTruthy()
    expect(open.getAttribute('data-video-ready')).toBeNull()
    fireEvent.loadedData(container.querySelector('video') as HTMLVideoElement)
    expect(open.getAttribute('data-video-ready')).toBe('true')
    fireEvent.click(container.querySelector('.media-thumb-remove') as HTMLButtonElement)
    expect(onRemove).toHaveBeenCalledOnce()
  })
})

describe('MediaLightbox', () => {
  const items = [
    makeMedia({ id: 1, original_name: 'a.png' }),
    makeMedia({ id: 2, original_name: 'b.mp4', media_type: 'video', mime_type: 'video/mp4' }),
  ]

  it('index 为 null 或越界时渲染 null', () => {
    const { container } = render(<MediaLightbox items={items} index={null} onClose={() => {}} />)
    expect(container.querySelector('.media-lightbox')).toBeNull()
    const { container: c2 } = render(<MediaLightbox items={items} index={5} onClose={() => {}} />)
    expect(c2.querySelector('.media-lightbox')).toBeNull()
    // 负数下标同样当作关闭（调用方只可能传 >= 0，但越界判据不能只挡右边）。
    const { container: c3 } = render(<MediaLightbox items={items} index={-1} onClose={() => {}} />)
    expect(c3.querySelector('.media-lightbox')).toBeNull()
  })

  it('图片灯箱：Esc 关闭、关闭按钮、点背景关闭；舞台点击不关闭', () => {
    const onClose = vi.fn()
    render(<MediaLightbox items={items} index={0} onClose={onClose} />)
    expect(screen.getByText('a.png')).toBeTruthy()
    expect(screen.getByText('IMAGE')).toBeTruthy()

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByText('a.png')) // 位于舞台内，stopPropagation
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByLabelText('关闭'))
    expect(onClose).toHaveBeenCalledTimes(2)
    fireEvent.click(screen.getByRole('presentation'))
    expect(onClose).toHaveBeenCalledTimes(3)
  })

  it('视频灯箱：VIDEO 标记；打开时锁 body 滚动，Esc 关闭', () => {
    const onClose = vi.fn()
    render(<MediaLightbox items={items} index={1} onClose={onClose} />)
    expect(screen.getByText('VIDEO')).toBeTruthy()
    expect(document.body.style.overflow).toBe('hidden')
    act(() => {
      fireEvent.keyDown(window, { key: 'Escape' })
    })
    expect(onClose).toHaveBeenCalled()
  })

  it('卸载还原 body 滚动', () => {
    const { unmount } = render(<MediaLightbox items={items} index={0} onClose={() => {}} />)
    expect(document.body.style.overflow).toBe('hidden')
    unmount()
    expect(document.body.style.overflow).toBe('')
  })
})

// 灯箱里的图片同样等「下载 + 解码」完成才显示：在此之前幻灯片上是一层骨架。
// jsdom 不做解码，所以「等 decode」的那条在元素上临时挂一个可控的 decode。
describe('MediaLightbox 图片占位', () => {
  const items = [makeMedia({ id: 1, original_name: 'a.png', url: '/uploads/a.png' })]

  function slide(): HTMLElement {
    return document.querySelector('.media-lightbox-slide') as HTMLElement
  }

  it('下载完成前只画骨架，完成后才显示', async () => {
    render(<MediaLightbox items={items} index={0} onClose={vi.fn()} />)
    expect(slide().getAttribute('data-loaded')).toBe('false')
    expect(slide().querySelector('.img-skeleton')?.getAttribute('aria-hidden')).toBe('true')

    fireEvent.load(slide().querySelector('img') as HTMLImageElement)
    await waitFor(() => expect(slide().getAttribute('data-loaded')).toBe('true'))
  })

  it('load 之后还要等 decode：解码完成才会显示', async () => {
    render(<MediaLightbox items={items} index={0} onClose={vi.fn()} />)
    const img = slide().querySelector('img') as HTMLImageElement
    let resolve!: () => void
    Object.defineProperty(img, 'decode', {
      configurable: true,
      value: () => new Promise<void>((done) => { resolve = done }),
    })

    fireEvent.load(img)
    expect(slide().getAttribute('data-loaded')).toBe('false')

    await act(async () => {
      resolve()
      await Promise.resolve()
    })
    expect(slide().getAttribute('data-loaded')).toBe('true')
  })

  it('缓存命中（complete 且 naturalWidth > 0）不闪骨架', () => {
    const complete = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'complete')
    const naturalWidth = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'naturalWidth')
    Object.defineProperty(HTMLImageElement.prototype, 'complete', { configurable: true, get: () => true })
    Object.defineProperty(HTMLImageElement.prototype, 'naturalWidth', { configurable: true, get: () => 800 })
    try {
      render(<MediaLightbox items={items} index={0} onClose={vi.fn()} />)
      expect(slide().getAttribute('data-loaded')).toBe('true')
      // 同一张图再响一次 load（缓存命中时 ref 与 onLoad 都会报）：幂等，不重复渲染。
      fireEvent.load(slide().querySelector('img') as HTMLImageElement)
      expect(slide().getAttribute('data-loaded')).toBe('true')
    } finally {
      if (complete) Object.defineProperty(HTMLImageElement.prototype, 'complete', complete)
      if (naturalWidth) Object.defineProperty(HTMLImageElement.prototype, 'naturalWidth', naturalWidth)
    }
  })
})

describe('MediaLightbox 左右切换', () => {
  const slides = [
    makeMedia({ id: 1, original_name: 'a.png', url: '/uploads/a.png' }),
    makeMedia({ id: 2, original_name: 'b.png', url: '/uploads/b.png' }),
    makeMedia({ id: 3, original_name: 'c.mp4', url: '/uploads/c.mp4', media_type: 'video', mime_type: 'video/mp4' }),
  ]

  /** 轨道位移就是「现在看第几张」——顺带证明动画是 transform 做的（CSS 里才有 transition）。 */
  function transform(): string {
    return (document.querySelector('.media-lightbox-track') as HTMLElement).style.transform
  }

  it('第一张只有「下一张」；点后轨道平移到第二张，两侧按钮齐备', () => {
    render(<MediaLightbox items={slides} index={0} onClose={vi.fn()} />)

    expect(document.querySelector('.media-lightbox-nav.prev')).toBeNull()
    expect(transform()).toBe('translateX(0%)')

    fireEvent.click(screen.getByLabelText('下一张'))
    expect(screen.getByText('b.png')).toBeTruthy()
    expect(transform()).toBe('translateX(-100%)')
    expect(screen.getByLabelText('上一张')).toBeTruthy()
    expect(screen.getByLabelText('下一张')).toBeTruthy()

    fireEvent.click(screen.getByLabelText('上一张'))
    expect(screen.getByText('a.png')).toBeTruthy()
    expect(transform()).toBe('translateX(0%)')
  })

  it('最后一张只有「上一张」；只挂当前张与相邻两张，隔一张的不预下载', () => {
    render(<MediaLightbox items={slides} index={2} onClose={vi.fn()} />)

    expect(document.querySelector('.media-lightbox-nav.next')).toBeNull()
    const cells = document.querySelectorAll('.media-lightbox-slide')
    expect(cells).toHaveLength(3)
    // 当前张是视频；相邻的 b.png 已经挂上（拖动时才看得到内容），
    // 隔一张的 a.png 不挂——整本图册连视频一起预下载没有必要。
    expect(cells[2].querySelector('video')).toBeTruthy()
    expect(cells[1].querySelector('img')?.getAttribute('src')).toBe('/uploads/b.png')
    expect(cells[0].querySelector('img')).toBeNull()
    expect(screen.getByText('VIDEO')).toBeTruthy()

    fireEvent.click(screen.getByLabelText('上一张'))
    expect(screen.getByText('b.png')).toBeTruthy()
    expect(screen.getByText('IMAGE')).toBeTruthy()
    // 现在相邻的是 a.png（左）与 c.mp4（右），两张图片都在轨道上。
    expect(document.querySelectorAll('.media-lightbox-slide img')).toHaveLength(2)
    expect(cells[0].querySelector('img')?.getAttribute('src')).toBe('/uploads/a.png')
  })

  it('键盘 ← / → 与按钮同一套边界：到头了按了也不动', () => {
    render(<MediaLightbox items={slides} index={1} onClose={vi.fn()} />)
    expect(transform()).toBe('translateX(-100%)')

    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(screen.getByText('c.mp4')).toBeTruthy()
    expect(transform()).toBe('translateX(-200%)')
    fireEvent.keyDown(window, { key: 'ArrowRight' }) // 已是最后一张
    expect(transform()).toBe('translateX(-200%)')

    fireEvent.keyDown(window, { key: 'ArrowLeft' })
    expect(screen.getByText('b.png')).toBeTruthy()
    fireEvent.keyDown(window, { key: 'ArrowLeft' })
    fireEvent.keyDown(window, { key: 'ArrowLeft' }) // 已是第一张
    expect(transform()).toBe('translateX(0%)')
    expect(screen.getByText('a.png')).toBeTruthy()
  })

  it('父组件重渲染不会把用户滑到的张数打回打开请求值', () => {
    const onClose = vi.fn()
    const { rerender } = render(<MediaLightbox items={slides} index={0} onClose={onClose} />)
    fireEvent.click(screen.getByLabelText('下一张'))
    expect(transform()).toBe('translateX(-100%)')

    // index 仍是打开请求里的 0（父组件没收到切换通知）——重渲染后必须还停在第 2 张。
    rerender(<MediaLightbox items={slides} index={0} onClose={onClose} />)
    expect(transform()).toBe('translateX(-100%)')
    expect(screen.getByText('b.png')).toBeTruthy()
  })
})

describe('MediaLightbox 手势（拖动切换 / 下拉关闭）', () => {
  const slides = [
    makeMedia({ id: 1, original_name: 'a.png', url: '/uploads/a.png' }),
    makeMedia({ id: 2, original_name: 'b.png', url: '/uploads/b.png' }),
    makeMedia({ id: 3, original_name: 'c.mp4', url: '/uploads/c.mp4', media_type: 'video', mime_type: 'video/mp4' }),
  ]

  function transform(): string {
    return (document.querySelector('.media-lightbox-track') as HTMLElement).style.transform
  }

  function shell(): HTMLElement {
    return document.querySelector('.media-lightbox') as HTMLElement
  }

  /** 手势都打在视口上：真实浏览器里事件从图片冒出到视口，测试里直接派发到视口即可。 */
  function firePointer(
    name: 'pointerDown' | 'pointerMove' | 'pointerUp' | 'pointerCancel',
    x: number,
    y: number,
  ): void {
    const target = document.querySelector('.media-lightbox-viewport') as HTMLElement
    const init = { clientX: x, clientY: y, button: 0, pointerId: 1 }
    if (name === 'pointerDown') fireEvent.pointerDown(target, init)
    if (name === 'pointerMove') fireEvent.pointerMove(target, init)
    if (name === 'pointerUp') fireEvent.pointerUp(target, init)
    if (name === 'pointerCancel') fireEvent.pointerCancel(target, init)
  }

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('横向左拖超过阈值：跟手位移 → 松手滑到下一张', () => {
    render(<MediaLightbox items={slides} index={0} onClose={vi.fn()} />)

    firePointer('pointerDown', 200, 100)
    firePointer('pointerMove', 120, 100)
    // 拖动中位移是「百分比 + 手指像素」：跟手，但仍是同一套 transform 动画。
    expect(transform()).toBe('translateX(calc(0% + -80px))')
    expect(shell().className).toContain('dragging')

    // 方向锁定之后再移动：位移继续跟手（不需要重新判定轴）。
    firePointer('pointerMove', 100, 100)
    expect(transform()).toBe('translateX(calc(0% + -100px))')

    firePointer('pointerUp', 100, 100)
    expect(transform()).toBe('translateX(-100%)')
    expect(screen.getByText('b.png')).toBeTruthy()
    expect(shell().className).not.toContain('dragging')
  })

  it('横向右拖超过阈值：回到上一张', () => {
    render(<MediaLightbox items={slides} index={1} onClose={vi.fn()} />)

    firePointer('pointerDown', 200, 100)
    firePointer('pointerMove', 300, 100)
    expect(transform()).toBe('translateX(calc(-100% + 100px))')

    firePointer('pointerUp', 300, 100)
    expect(transform()).toBe('translateX(0%)')
    expect(screen.getByText('a.png')).toBeTruthy()
  })

  it('到头的方向继续拖：位移被阻尼，松手也不会换图', () => {
    // 第一张往右拖：0.35 的阻尼，松手弹回原处。
    const first = render(<MediaLightbox items={slides} index={0} onClose={vi.fn()} />)
    firePointer('pointerDown', 100, 100)
    firePointer('pointerMove', 180, 100)
    expect(transform()).toBe('translateX(calc(0% + 28px))')
    firePointer('pointerUp', 180, 100)
    expect(transform()).toBe('translateX(0%)')
    expect(screen.getByText('a.png')).toBeTruthy()
    first.unmount()

    // 最后一张往左拖：同样只是阻尼，不会滑出图册。
    render(<MediaLightbox items={slides} index={2} onClose={vi.fn()} />)
    firePointer('pointerDown', 300, 100)
    firePointer('pointerMove', 220, 100)
    expect(transform()).toBe('translateX(calc(-200% + -28px))')
    firePointer('pointerUp', 220, 100)
    expect(transform()).toBe('translateX(-200%)')
    expect(screen.getByText('c.mp4')).toBeTruthy()
  })

  it('位移不到阈值当成点击：不进入拖动，也不换图', () => {
    render(<MediaLightbox items={slides} index={0} onClose={vi.fn()} />)

    firePointer('pointerDown', 200, 100)
    firePointer('pointerMove', 205, 103)
    expect(shell().className).not.toContain('dragging')
    expect(transform()).toBe('translateX(0%)')

    firePointer('pointerUp', 205, 103)
    expect(transform()).toBe('translateX(0%)')
    expect(screen.getByText('a.png')).toBeTruthy()
  })

  it('横向拖动没到换图阈值：松手弹回当前张', () => {
    render(<MediaLightbox items={slides} index={0} onClose={vi.fn()} />)

    firePointer('pointerDown', 200, 100)
    firePointer('pointerMove', 180, 100) // 20px，过了拖动阈值但没到换图阈值
    firePointer('pointerUp', 180, 100)

    expect(transform()).toBe('translateX(0%)')
    expect(screen.getByText('a.png')).toBeTruthy()
  })

  it('下拉：拖动中背景变淡、舞台上移；未过阈值松手回弹且不关闭', () => {
    const onClose = vi.fn()
    render(<MediaLightbox items={slides} index={0} onClose={onClose} />)

    firePointer('pointerDown', 200, 100)
    firePointer('pointerMove', 200, 160)
    expect(shell().className).toContain('dragging')
    expect(shell().style.getPropertyValue('--lb-shift')).toBe('60px')
    expect(shell().style.getPropertyValue('--lb-fade')).toBe('0.9')
    // 纵向手势期间横向位移保持为纯百分比，不会被纵位移污染。
    expect(transform()).toBe('translateX(0%)')

    firePointer('pointerUp', 200, 160)
    expect(onClose).not.toHaveBeenCalled()
    expect(shell().style.getPropertyValue('--lb-shift')).toBe('')
  })

  it('下拉超过阈值：先播退场再关闭（260ms 后才真正卸载）', () => {
    vi.useFakeTimers()
    const onClose = vi.fn()
    render(<MediaLightbox items={slides} index={0} onClose={onClose} />)

    firePointer('pointerDown', 200, 100)
    firePointer('pointerMove', 200, 320) // 220px
    expect(shell().style.getPropertyValue('--lb-shift')).toBe('220px')
    firePointer('pointerUp', 200, 320)

    expect(shell().className).toContain('closing')
    expect(onClose).not.toHaveBeenCalled()
    act(() => {
      vi.advanceTimersByTime(259)
    })
    expect(onClose).not.toHaveBeenCalled()
    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('向上拖没有语义：位移按 0 处理', () => {
    render(<MediaLightbox items={slides} index={0} onClose={vi.fn()} />)

    firePointer('pointerDown', 200, 200)
    firePointer('pointerMove', 200, 100)
    expect(shell().className).toContain('dragging')
    expect(shell().style.getPropertyValue('--lb-shift')).toBe('0px')
  })

  it('系统减少动态效果：下拉关闭不等退场', () => {
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: true,
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
    vi.useFakeTimers()
    const onClose = vi.fn()
    render(<MediaLightbox items={slides} index={0} onClose={onClose} />)

    firePointer('pointerDown', 200, 100)
    firePointer('pointerMove', 200, 320)
    firePointer('pointerUp', 200, 320)
    act(() => {
      vi.advanceTimersByTime(0)
    })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('右键、按钮、视频上起手都不算拖拽', () => {
    const onClose = vi.fn()
    render(<MediaLightbox items={slides} index={2} onClose={onClose} />)

    // 右键
    fireEvent.pointerDown(document.querySelector('.media-lightbox-viewport') as HTMLElement, {
      clientX: 200,
      clientY: 100,
      button: 2,
      pointerId: 1,
    })
    firePointer('pointerMove', 80, 100)
    expect(shell().className).not.toContain('dragging')

    // 按钮（左右切换）
    fireEvent.pointerDown(screen.getByLabelText('上一张'), { clientX: 200, clientY: 100, button: 0, pointerId: 1 })
    firePointer('pointerMove', 80, 100)
    expect(shell().className).not.toContain('dragging')

    // 视频（控制条要自己用指针事件）
    fireEvent.pointerDown(document.querySelector('.media-lightbox-slide video') as HTMLElement, {
      clientX: 200,
      clientY: 100,
      button: 0,
      pointerId: 1,
    })
    firePointer('pointerMove', 200, 320)
    expect(shell().className).not.toContain('dragging')
    expect(onClose).not.toHaveBeenCalled()
  })

  it('没有起手的 move/up、以及系统 pointercancel 都不改变状态', () => {
    render(<MediaLightbox items={slides} index={1} onClose={vi.fn()} />)

    firePointer('pointerMove', 80, 100)
    firePointer('pointerUp', 80, 100)
    expect(transform()).toBe('translateX(-100%)')

    firePointer('pointerDown', 200, 100)
    firePointer('pointerMove', 120, 100)
    expect(shell().className).toContain('dragging')
    firePointer('pointerCancel', 120, 100)
    expect(shell().className).not.toContain('dragging')
    expect(transform()).toBe('translateX(-100%)')
  })

  it('支持指针捕获时把捕获交给视口，让手指划出图片后仍能收到事件', () => {
    render(<MediaLightbox items={slides} index={0} onClose={vi.fn()} />)
    const viewport = document.querySelector('.media-lightbox-viewport') as HTMLElement
    const capture = vi.fn()
    Object.defineProperty(viewport, 'setPointerCapture', { configurable: true, value: capture })

    fireEvent.pointerDown(viewport, { clientX: 200, clientY: 100, button: 0, pointerId: 7 })
    expect(capture).toHaveBeenCalledWith(7)
  })
})

describe('MediaGallery', () => {
  const items = [
    makeMedia({ id: 1, original_name: 'a.png' }),
    makeMedia({ id: 2, original_name: 'b.png' }),
  ]

  it('每个媒体一张缩略图；点第 N 张打开第 N 张的灯箱，关闭后预览下标复位', () => {
    const { container } = render(<MediaGallery items={items} />)

    const thumbs = Array.from(container.querySelectorAll('.media-thumb-open'))
    expect(thumbs).toHaveLength(2)
    // 打开前没有任何灯箱。
    expect(document.querySelector('.media-lightbox')).toBeNull()

    // 点第二张：灯箱必须拿到 index=1（点第一张却开第二张是最容易写错的一处）。
    fireEvent.click(thumbs[1])
    expect(document.querySelector('.media-lightbox')).toBeTruthy()
    expect(screen.getByText('b.png')).toBeTruthy()

    fireEvent.click(screen.getByLabelText('关闭'))
    expect(document.querySelector('.media-lightbox')).toBeNull()
  })

  it('Esc 关闭灯箱（onClose 把 index 复位成 null）', () => {
    const { container } = render(<MediaGallery items={[items[0]]} />)

    fireEvent.click(container.querySelector('.media-thumb-open') as HTMLButtonElement)
    expect(document.querySelector('.media-lightbox')).toBeTruthy()

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(document.querySelector('.media-lightbox')).toBeNull()
  })

  it('滑动切换后关闭，再点同一张缩略图从那张重新开始', () => {
    const { container } = render(<MediaGallery items={items} />)
    const thumbs = Array.from(container.querySelectorAll('.media-thumb-open'))

    fireEvent.click(thumbs[0])
    fireEvent.click(screen.getByLabelText('下一张'))
    expect(screen.getByText('b.png')).toBeTruthy()

    fireEvent.click(screen.getByLabelText('关闭'))
    expect(document.querySelector('.media-lightbox')).toBeNull()

    // 上一次滑到了第 2 张，但这次点的是第 1 张 —— 内部下标必须重新按打开请求对齐。
    fireEvent.click(thumbs[0])
    expect(screen.getByText('a.png')).toBeTruthy()
  })
})

describe('MediaLightbox 共享元素（缩略图 → 全屏）与计数', () => {
  const items = [
    makeMedia({ id: 1, original_name: 'a.png', url: '/uploads/a.png' }),
    makeMedia({ id: 2, original_name: 'b.png', url: '/uploads/b.png' }),
    makeMedia({ id: 3, original_name: 'c.mp4', url: '/uploads/c.mp4', media_type: 'video', mime_type: 'video/mp4' }),
  ]
  /** 灯箱里那张图最终占据的位置（jsdom 的 rect 恒为 0，必须自己给）。 */
  const imageRect = { left: 200, top: 150, width: 400, height: 300 }
  /** 被点开的那块缩略图：中心 (50,50)、宽 100。 */
  const origin = { left: 0, top: 0, width: 100, height: 100 }
  const START = 'translate(-350px, -250px) scale(0.25)'

  const rectDescriptor = Object.getOwnPropertyDescriptor(Element.prototype, 'getBoundingClientRect')!

  function stubRects(): void {
    Object.defineProperty(Element.prototype, 'getBoundingClientRect', {
      configurable: true,
      value: () => imageRect,
    })
  }

  function stubAnimate(): ReturnType<typeof vi.fn> {
    const animate = vi.fn(() => ({ cancel: vi.fn() }))
    Object.defineProperty(Element.prototype, 'animate', { configurable: true, value: animate })
    return animate
  }

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    Object.defineProperty(Element.prototype, 'getBoundingClientRect', rectDescriptor)
    delete (Element.prototype as unknown as { animate?: unknown }).animate
  })

  it('打开时从缩略图的位置长到最终尺寸', () => {
    stubRects()
    const animate = stubAnimate()

    render(<MediaLightbox items={items} index={0} origin={origin} onClose={vi.fn()} />)

    expect(animate).toHaveBeenCalledTimes(1)
    expect(animate.mock.calls[0][0]).toEqual([{ transform: START }, { transform: 'none' }])
    expect(animate.mock.calls[0][1]).toEqual({ duration: 380, easing: 'cubic-bezier(0.22, 1, 0.36, 1)' })
  })

  it('没有来源（例如由路由直接打开）时不做放大', () => {
    stubRects()
    const animate = stubAnimate()

    render(<MediaLightbox items={items} index={0} onClose={vi.fn()} />)
    expect(animate).not.toHaveBeenCalled()
  })

  it('系统要求减少动态效果时不做放大', () => {
    stubRects()
    const animate = stubAnimate()
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: true,
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))

    render(<MediaLightbox items={items} index={0} origin={origin} onClose={vi.fn()} />)
    expect(animate).not.toHaveBeenCalled()
  })

  it('视频没有共享元素：打开与关闭都不播这一段', () => {
    stubRects()
    const animate = stubAnimate()
    const onClose = vi.fn()

    render(<MediaLightbox items={items} index={2} origin={origin} onClose={onClose} />)
    expect(animate).not.toHaveBeenCalled()

    fireEvent.click(screen.getByLabelText('关闭'))
    // 没有图可以缩回去，立刻关闭（不等退场）。
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('浏览器没有 Web Animations 时静默跳过，并立刻关闭', () => {
    stubRects()
    const onClose = vi.fn()

    render(<MediaLightbox items={items} index={0} origin={origin} onClose={onClose} />)
    fireEvent.click(screen.getByLabelText('关闭'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('量不出尺寸（图片还没布局）时不放大，关闭也走普通路径', () => {
    const animate = stubAnimate()
    const onClose = vi.fn()

    render(<MediaLightbox items={items} index={0} origin={origin} onClose={onClose} />)
    expect(animate).not.toHaveBeenCalled()

    fireEvent.click(screen.getByLabelText('关闭'))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(document.querySelector('.media-lightbox')?.className).not.toContain('zoom-out')
  })

  it('关闭时缩回缩略图，240ms 之后才真正卸载', () => {
    vi.useFakeTimers()
    stubRects()
    const animate = stubAnimate()
    const onClose = vi.fn()
    render(<MediaLightbox items={items} index={0} origin={origin} onClose={onClose} />)
    // 打开时的放大也走同一个替身，先清掉再断言退场那一段。
    animate.mockClear()

    fireEvent.click(screen.getByLabelText('关闭'))
    expect(document.querySelector('.media-lightbox')?.className).toContain('zoom-out')
    expect(animate).toHaveBeenCalledTimes(1)
    expect(animate.mock.calls[0][0]).toEqual([{ transform: 'none' }, { transform: START }])
    expect(animate.mock.calls[0][1]).toEqual({
      duration: 240,
      easing: 'cubic-bezier(0.22, 1, 0.36, 1)',
      fill: 'forwards',
    })

    expect(onClose).not.toHaveBeenCalled()
    act(() => {
      vi.advanceTimersByTime(239)
    })
    expect(onClose).not.toHaveBeenCalled()
    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('退场途中再点一次关闭不会排第二个计时器', () => {
    vi.useFakeTimers()
    stubRects()
    stubAnimate()
    const onClose = vi.fn()
    render(<MediaLightbox items={items} index={0} origin={origin} onClose={onClose} />)

    fireEvent.click(screen.getByLabelText('关闭'))
    fireEvent.click(screen.getByLabelText('关闭'))
    act(() => {
      vi.advanceTimersByTime(240)
    })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('换过图之后没有「来时的缩略图」：关闭直接淡出，不缩回第一张', () => {
    stubRects()
    stubAnimate()
    const onClose = vi.fn()
    render(<MediaLightbox items={items} index={0} origin={origin} onClose={onClose} />)

    fireEvent.click(screen.getByLabelText('下一张'))
    fireEvent.click(screen.getByLabelText('关闭'))

    expect(onClose).toHaveBeenCalledTimes(1)
    expect(document.querySelector('.media-lightbox')?.className).not.toContain('zoom-out')
  })

  it('多于一张时显示「第几张 / 共几张」，换图后跟着走', () => {
    render(<MediaLightbox items={items} index={0} onClose={vi.fn()} />)
    expect(screen.getByText('1 / 3')).toBeTruthy()

    fireEvent.click(screen.getByLabelText('下一张'))
    expect(screen.getByText('2 / 3')).toBeTruthy()
  })

  it('单张不显示计数', () => {
    render(<MediaLightbox items={[items[0]]} index={0} onClose={vi.fn()} />)
    expect(document.querySelector('.media-lightbox-count')).toBeNull()
  })

  it('图册把被点击的缩略图位置传给灯箱（共享元素的起点）', () => {
    stubRects()
    const animate = stubAnimate()

    const { container } = render(<MediaGallery items={items.slice(0, 2)} />)
    fireEvent.click(container.querySelector('.media-thumb-open') as HTMLElement)

    // 缩略图与灯箱里的图 rect 相同 → 起点就是终点（位移 0、缩放 1）。
    expect(animate).toHaveBeenCalledTimes(1)
    expect(animate.mock.calls[0][0]).toEqual([
      { transform: 'translate(0px, 0px) scale(1)' },
      { transform: 'none' },
    ])
  })
})

describe('MediaPicker', () => {
  function pickFiles(container: HTMLElement, files: File[]) {
    const input = container.querySelector('input[type="file"]') as HTMLInputElement
    Object.defineProperty(input, 'files', { value: files, configurable: true })
    fireEvent.change(input)
  }

  beforeEach(() => {
    vi.clearAllMocks()
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('选择文件后上传成功：onChange 追加；busy 回调与 input.value 重置', async () => {
    const onChange = vi.fn()
    const onBusyChange = vi.fn()
    const uploaded = makeMedia({ id: 99, original_name: 'up.png' })
    client.blog.uploadMedia.mockResolvedValueOnce([uploaded])

    const { container } = render(
      <ToastProvider>
        <MediaPicker items={[]} onChange={onChange} onBusyChange={onBusyChange} />
      </ToastProvider>,
    )
    fireEvent.click(container.querySelector('.media-picker-add') as HTMLButtonElement)
    pickFiles(container, [new File(['x'], 'up.png', { type: 'image/png' })])

    await waitFor(() => expect(onChange).toHaveBeenCalledWith([uploaded]))
    expect(onBusyChange).toHaveBeenLastCalledWith(false)
    const input = container.querySelector('input[type="file"]') as HTMLInputElement
    expect(input.value).toBe('')
  })

  it('上传失败：toast 提示、busy 复位', async () => {
    const onChange = vi.fn()
    client.blog.uploadMedia.mockRejectedValueOnce(new Error('too large'))

    const { container } = render(
      <ToastProvider>
        <MediaPicker items={[]} onChange={onChange} />
      </ToastProvider>,
    )
    pickFiles(container, [new File(['x'], 'big.png', { type: 'image/png' })])
    await waitFor(() => expect(screen.getAllByText('too large').length).toBeGreaterThan(0))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('空文件选择直接返回；disabled 时加号按钮禁用；删除走 onChange', async () => {
    const onChange = vi.fn()
    const item = makeMedia()
    const { container, rerender } = render(
      <ToastProvider>
        <MediaPicker items={[item]} onChange={onChange} />
      </ToastProvider>,
    )

    pickFiles(container, [])
    expect(onChange).not.toHaveBeenCalled()

    rerender(
      <ToastProvider>
        <MediaPicker items={[item]} onChange={onChange} disabled />
      </ToastProvider>,
    )
    const add = container.querySelector('.media-picker-add') as HTMLButtonElement
    expect(add.disabled).toBe(true)

    rerender(
      <ToastProvider>
        <MediaPicker items={[item]} onChange={onChange} />
      </ToastProvider>,
    )
    fireEvent.click(container.querySelector('.media-thumb-remove') as HTMLButtonElement)
    expect(onChange).toHaveBeenCalledWith([])
  })

  it('点缩略图打开灯箱（与前台图册共用同一套预览）', () => {
    const { container } = render(
      <ToastProvider>
        <MediaPicker items={[makeMedia({ id: 7, original_name: 'p.png' })]} onChange={vi.fn()} />
      </ToastProvider>,
    )
    fireEvent.click(container.querySelector('.media-thumb-open') as HTMLElement)
    expect(document.querySelector('.media-lightbox')).toBeTruthy()
    expect(screen.getByText('p.png')).toBeTruthy()
  })
})
