// image-group.test.tsx —— 「一批图一起出现」的闸门（lib/imageGroup.tsx）
//
// 钉住的是三条用户可见的规则：
//   ① 组里的图**全部**下载并解码完成之后才一起出现——一张到齐不解锁；
//   ② 已经在屏幕上、这一轮没变化的图不会被重新盖回骨架（闸门一次性）；
//   ③ 永远等不齐时不能让整页停在加载态（超时兜底）。
//
// jsdom 不做图片解码，也没有 HTMLImageElement#decode：需要验证「等 decode」
// 的用例会在具体元素上临时挂一个可控的 decode（原型上没有，用完即随元素丢弃）。

import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, waitFor } from '@testing-library/react'
import { useLayoutEffect, useRef, useState } from 'react'
import { ImageGroup, IMAGE_GROUP_TIMEOUT, useImageGroup } from '../lib/imageGroup'
import ProgressiveImage from '../components/ProgressiveImage'
import { enhanceImages } from '../components/ContentView'

/** 读出当前组是否开闸（不在组里时是 none）。 */
function Probe() {
  const group = useImageGroup()
  return <span data-probe={group ? String(group.open) : 'none'} />
}

function probe(): string {
  return document.querySelector('[data-probe]')?.getAttribute('data-probe') ?? 'missing'
}

function frames(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>('.img-frame'))
}

function images(container: HTMLElement): HTMLImageElement[] {
  return Array.from(container.querySelectorAll<HTMLImageElement>('img'))
}

/** 手动登记一次的组件：让「销账」与「重复销账」可直接验证。 */
function ManualTrack({ onTrack }: { onTrack: (release: () => void) => void }) {
  const track = useImageGroup()?.track
  // 回调放 ref：父组件每次渲染都会给一个新的箭头函数，直接进依赖会让效果
  // 反复「销账 → 重新登记」，测的就不是幂等性了。
  const latest = useRef(onTrack)
  latest.current = onTrack
  useLayoutEffect(() => {
    if (!track) return
    const release = track()
    latest.current(release)
    return release
  }, [track])
  return null
}

afterEach(() => {
  vi.useRealTimers()
})

describe('ImageGroup 闸门', () => {
  it('一张到齐不解锁，全部到齐后整组一起显示', () => {
    const { container } = render(
      <ImageGroup>
        <ProgressiveImage src="/a.png" />
        <ProgressiveImage src="/b.png" />
      </ImageGroup>,
    )
    const [a, b] = frames(container)
    expect(a.classList.contains('loading')).toBe(true)

    fireEvent.load(images(container)[0])
    // a 已经就绪，但 b 还没到：a 仍然只画骨架。
    expect(frames(container)[0].classList.contains('loading')).toBe(true)
    expect(frames(container)[1].classList.contains('loading')).toBe(true)

    fireEvent.load(images(container)[1])
    expect(frames(container)[0].classList.contains('loaded')).toBe(true)
    expect(frames(container)[1].classList.contains('loaded')).toBe(true)
    expect(b.classList.contains('loading')).toBe(false)
  })

  it('失败也算「有结果」：不会因为一张坏图让整组永远停在骨架', () => {
    const { container } = render(
      <ImageGroup>
        <ProgressiveImage src="/a.png" />
        <ProgressiveImage src="/bad.png" />
      </ImageGroup>,
    )

    fireEvent.load(images(container)[0])
    fireEvent.error(images(container)[1])

    expect(frames(container)[0].classList.contains('loaded')).toBe(true)
    expect(frames(container)[1].classList.contains('failed')).toBe(true)
  })

  it('开闸之后才加入的图不会再拖住已经显示的图', () => {
    function Adder() {
      const [extra, setExtra] = useState(false)
      return (
        <ImageGroup>
          <ProgressiveImage src="/a.png" />
          {extra ? <ProgressiveImage src="/b.png" /> : null}
          <button type="button" onClick={() => setExtra(true)}>add</button>
        </ImageGroup>
      )
    }
    const { container, getByText } = render(<Adder />)

    fireEvent.load(images(container)[0])
    expect(frames(container)[0].classList.contains('loaded')).toBe(true)

    fireEvent.click(getByText('add'))
    // 新图自己回到骨架，但已经显示的 a 不受影响。
    expect(frames(container)[0].classList.contains('loaded')).toBe(true)
    expect(frames(container)[1].classList.contains('loading')).toBe(true)

    fireEvent.load(images(container)[1])
    expect(frames(container)[1].classList.contains('loaded')).toBe(true)
  })

  it('图片在加载途中被卸载时销账：整组不会卡死', () => {
    function Toggler() {
      const [show, setShow] = useState(true)
      return (
        <ImageGroup>
          <Probe />
          {show ? <ProgressiveImage src="/slow.png" /> : null}
          <button type="button" onClick={() => setShow(false)}>remove</button>
        </ImageGroup>
      )
    }
    const { getByText } = render(<Toggler />)
    expect(probe()).toBe('false')

    fireEvent.click(getByText('remove'))
    expect(probe()).toBe('true')
  })

  it('空组直接开闸；不在组里时拿不到闸门', () => {
    const grouped = render(
      <ImageGroup>
        <Probe />
      </ImageGroup>,
    )
    expect(grouped.container.querySelector('[data-probe]')?.getAttribute('data-probe')).toBe('true')

    const plain = render(<Probe />)
    expect(plain.container.querySelector('[data-probe]')?.getAttribute('data-probe')).toBe('none')
  })

  it('销账是幂等的：重复调用不会把计数压成负数', () => {
    const captured: { release: (() => void) | null } = { release: null }
    render(
      <ImageGroup>
        <Probe />
        <ManualTrack onTrack={(fn) => { captured.release = fn }} />
      </ImageGroup>,
    )
    expect(probe()).toBe('false')

    act(() => {
      captured.release?.()
      captured.release?.()
    })
    expect(probe()).toBe('true')
  })

  it(`等满 ${IMAGE_GROUP_TIMEOUT}ms 还没齐就放行：永远挂住的请求不该让整页停在骨架`, () => {
    vi.useFakeTimers()
    render(
      <ImageGroup>
        <Probe />
        <ProgressiveImage src="/never.png" />
      </ImageGroup>,
    )
    expect(probe()).toBe('false')

    act(() => {
      vi.advanceTimersByTime(IMAGE_GROUP_TIMEOUT)
    })
    expect(probe()).toBe('true')
  })

  it('组内图片一律 eager 加载；组外保留调用方给的 loading', () => {
    const grouped = render(
      <ImageGroup>
        <ProgressiveImage src="/a.png" loading="lazy" />
      </ImageGroup>,
    )
    expect(images(grouped.container)[0].getAttribute('loading')).toBe('eager')

    const plain = render(<ProgressiveImage src="/b.png" loading="lazy" />)
    expect(images(plain.container)[0].getAttribute('loading')).toBe('lazy')
  })
})

describe('ProgressiveImage 的解码门', () => {
  it('load 之后还要等 decode 完成才显示', async () => {
    const { container } = render(<ProgressiveImage src="/a.png" />)
    const img = images(container)[0]
    let resolve!: () => void
    Object.defineProperty(img, 'decode', {
      configurable: true,
      value: () => new Promise<void>((done) => { resolve = done }),
    })

    fireEvent.load(img)
    // 字节到齐、解码未完：仍然只画骨架。
    expect(frames(container)[0].classList.contains('loading')).toBe(true)

    await act(async () => {
      resolve()
      await Promise.resolve()
    })
    expect(frames(container)[0].classList.contains('loaded')).toBe(true)
  })

  it('decode 失败按坏图处理（字节在，但解不出来）', async () => {
    const { container } = render(<ProgressiveImage src="/a.png" />)
    const img = images(container)[0]
    Object.defineProperty(img, 'decode', {
      configurable: true,
      value: () => Promise.reject(new Error('decode failed')),
    })

    fireEvent.load(img)
    await waitFor(() => expect(frames(container)[0].classList.contains('failed')).toBe(true))
  })

  it('换图之后旧图的解码结果不会算到新图上', async () => {
    const { container, rerender } = render(<ProgressiveImage src="/a.png" />)
    const img = images(container)[0]
    let resolve!: () => void
    Object.defineProperty(img, 'decode', {
      configurable: true,
      value: () => new Promise<void>((done) => { resolve = done }),
    })

    // a 的 load 到了，解码还挂着；此时换图，a 的结果晚一步落地。
    fireEvent.load(img)
    rerender(<ProgressiveImage src="/b.png" />)
    await act(async () => {
      resolve()
      await Promise.resolve()
    })

    // 新图 b 仍在骨架态：a 的「解码完成」不能算成 b 的。
    expect(frames(container)[0].classList.contains('loading')).toBe(true)
  })
})

describe('正文图（enhanceImages）接入闸门', () => {
  it('组没开闸时正文图只记住结果；开闸后一轮重渲染把终态补上', () => {
    const root = document.createElement('div')
    root.innerHTML = '<p>文字<img src="/body.png" alt="正文图">文字</p>'
    const track = vi.fn(() => vi.fn())
    enhanceImages(root, { open: false, track })

    const frame = root.querySelector<HTMLElement>('.img-frame')!
    fireEvent.load(root.querySelector('img')!)

    // 结果记在外框上，画面仍是骨架——它在等同组的头图。
    expect(frame.classList.contains('loading')).toBe(true)
    expect(frame.dataset.status).toBe('loaded')
    expect(track).toHaveBeenCalledTimes(1)

    // 组还没开时再跑一遍（同一次提交里的重渲染）：图仍在文档里，登记原样保留。
    enhanceImages(root, { open: false, track })
    expect(frame.classList.contains('loading')).toBe(true)

    // 开闸：ContentView 因 context 变化重渲染，再次执行时补上终态。
    enhanceImages(root, { open: true, track })
    expect(frame.classList.contains('loaded')).toBe(true)
    expect(frame.style.aspectRatio).toBe('')
    // 已经包过的图不会重复登记。
    expect(track).toHaveBeenCalledTimes(1)
  })

  it('仍在文档里的正文图不会被当成「被换掉的」而销账', () => {
    // 只有脱离文档的元素才释放登记；这里把根挂到 body 上，验证另一条路径。
    const root = document.createElement('div')
    root.innerHTML = '<img src="/a.png">'
    document.body.append(root)
    const track = vi.fn(() => vi.fn())
    try {
      enhanceImages(root, { open: false, track })
      enhanceImages(root, { open: false, track })
      expect(track).toHaveBeenCalledTimes(1)
    } finally {
      root.remove()
    }
  })

  it('关闸期间失败的正文图，开闸后显示失败态而不是永远骨架', () => {
    const root = document.createElement('div')
    root.innerHTML = '<img src="/bad.png">'
    const track = vi.fn(() => vi.fn())
    enhanceImages(root, { open: false, track })

    const frame = root.querySelector<HTMLElement>('.img-frame')!
    fireEvent.error(root.querySelector('img')!)
    expect(frame.dataset.status).toBe('failed')
    expect(frame.classList.contains('loading')).toBe(true)

    enhanceImages(root, { open: true, track })
    expect(frame.classList.contains('failed')).toBe(true)
    expect(frame.classList.contains('loading')).toBe(false)
  })

  it('同一张图的结果只落地一次（load 之后再 error 不改判）', () => {
    const root = document.createElement('div')
    root.innerHTML = '<img src="/a.png">'
    enhanceImages(root)
    const frame = root.querySelector<HTMLElement>('.img-frame')!
    const img = root.querySelector('img')!

    fireEvent.load(img)
    fireEvent.error(img)

    expect(frame.classList.contains('loaded')).toBe(true)
    expect(frame.classList.contains('failed')).toBe(false)
  })

  it('正文图就绪后销账：与 React 图片凑齐时整组才开闸', () => {
    // 正文图与一张 React 图同组：正文图先到，组仍然关着；React 图到了才开。
    const root = document.createElement('div')
    root.innerHTML = '<img src="/body.png">'

    const { container } = render(
      <ImageGroup>
        <Probe />
        <ProgressiveImage src="/a.png" />
        <BodyImages root={root} />
      </ImageGroup>,
    )

    fireEvent.load(root.querySelector('img')!)
    expect(probe()).toBe('false')

    fireEvent.load(images(container)[0])
    expect(probe()).toBe('true')
  })

  it('被 React 重设 innerHTML 换掉的正文图会销账，新的图重新登记', () => {
    const root = document.createElement('div')
    root.innerHTML = '<img src="/a.png">'
    const track = vi.fn(() => vi.fn())
    enhanceImages(root, { open: false, track })

    const release = track.mock.results[0].value as () => void
    // 模拟 React 用相同内容重设 innerHTML：旧元素整个被换掉。
    root.innerHTML = '<img src="/a.png">'
    enhanceImages(root, { open: false, track })

    expect(release).toHaveBeenCalledTimes(1)
    expect(track).toHaveBeenCalledTimes(2)
  })

  it('正文图同样等 decode 完成；decode 失败判为坏图', async () => {
    const root = document.createElement('div')
    root.innerHTML = '<img src="/body.png"><img src="/broken.png">'
    enhanceImages(root)
    const [good, broken] = Array.from(root.querySelectorAll('img'))

    let resolve!: () => void
    Object.defineProperty(good, 'decode', {
      configurable: true,
      value: () => new Promise<void>((done) => { resolve = done }),
    })
    Object.defineProperty(broken, 'decode', {
      configurable: true,
      value: () => Promise.reject(new Error('decode failed')),
    })

    fireEvent.load(good)
    const frame = root.querySelector<HTMLElement>('.img-frame')!
    expect(frame.classList.contains('loading')).toBe(true)

    await act(async () => {
      resolve()
      await Promise.resolve()
    })
    expect(frame.classList.contains('loaded')).toBe(true)

    fireEvent.load(broken)
    await waitFor(() => {
      expect(root.querySelectorAll<HTMLElement>('.img-frame')[1].classList.contains('failed')).toBe(true)
    })
  })
})

/** 把一个自带 DOM 的容器挂进组件树，让 ContentView 式的正文图参与同一个组。 */
function BodyImages({ root }: { root: HTMLElement }) {
  const group = useImageGroup()
  useLayoutEffect(() => {
    enhanceImages(root, group)
  })
  return null
}
