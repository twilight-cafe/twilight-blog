// progressive-image.test.tsx —— 图片占位（骨架 → 淡入 / 失败态）
//
// jsdom 不会真的去解码图片，所以这里手工发 load / error 事件；「首次挂载时
// 图片已经在缓存里」那条路径则通过改写 HTMLImageElement.prototype 的
// complete / naturalWidth 来模拟——这也是它在真实浏览器里唯一能被观察到的地方
// （缓存命中不发 load，组件必须靠 ref 回调里的 complete 判定终态）。
//
// 原型上的两个 getter 是 jsdom 自带的，改写前先存下描述符，afterEach 原样还回去；
// 直接 delete 会把 jsdom 的实现一起删掉，后续用例读 complete 会变成 undefined。

import { afterEach, describe, expect, it } from 'vitest'
import { fireEvent, render } from '@testing-library/react'
import ProgressiveImage from '../components/ProgressiveImage'

const completeDescriptor = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'complete')
const naturalWidthDescriptor = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'naturalWidth')

function stubImageCache(complete: boolean, naturalWidth: number): void {
  Object.defineProperty(HTMLImageElement.prototype, 'complete', { configurable: true, get: () => complete })
  Object.defineProperty(HTMLImageElement.prototype, 'naturalWidth', { configurable: true, get: () => naturalWidth })
}

function restoreImageCache(): void {
  if (completeDescriptor) Object.defineProperty(HTMLImageElement.prototype, 'complete', completeDescriptor)
  if (naturalWidthDescriptor) Object.defineProperty(HTMLImageElement.prototype, 'naturalWidth', naturalWidthDescriptor)
}

function frameOf(container: HTMLElement): HTMLElement {
  return container.querySelector('.img-frame') as HTMLElement
}

function imageOf(container: HTMLElement): HTMLImageElement {
  return container.querySelector('img') as HTMLImageElement
}

afterEach(restoreImageCache)

describe('ProgressiveImage', () => {
  it('加载中：骨架 + 16:9 占位，并按传入属性渲染 <img>', () => {
    const { container } = render(
      <ProgressiveImage src="/a.png" alt="一张图" ratio="16 / 9" loading="lazy" />,
    )

    const frame = frameOf(container)
    expect(frame.className).toBe('img-frame loading')
    expect(frame.style.aspectRatio).toBe('16 / 9')
    expect(frame.querySelector('.img-skeleton')?.getAttribute('aria-hidden')).toBe('true')

    const img = imageOf(container)
    expect(img.getAttribute('src')).toBe('/a.png')
    expect(img.getAttribute('alt')).toBe('一张图')
    expect(img.getAttribute('loading')).toBe('lazy')
  })

  it('外框类名合并；没给 ratio 时不写宽高比', () => {
    const { container } = render(<ProgressiveImage src="/a.png" frameClassName="round" />)
    const frame = frameOf(container)
    expect(frame.className).toBe('img-frame loading round')
    expect(frame.style.aspectRatio).toBe('')
  })

  it('load：骨架淡出、图片淡入，占位比例清空', () => {
    const { container } = render(<ProgressiveImage src="/a.png" ratio="16 / 9" />)
    const frame = frameOf(container)

    fireEvent.load(imageOf(container))

    expect(frame.classList.contains('loaded')).toBe(true)
    expect(frame.classList.contains('loading')).toBe(false)
    expect(frame.style.aspectRatio).toBe('')
  })

  it('error：判为失败并保留占位比例（斜纹底需要一块面积）', () => {
    const { container } = render(<ProgressiveImage src="/a.png" ratio="4 / 3" />)
    const frame = frameOf(container)

    fireEvent.error(imageOf(container))

    expect(frame.classList.contains('failed')).toBe(true)
    expect(frame.style.aspectRatio).toBe('4 / 3')
  })

  it('缓存命中：首次渲染直接是 loaded，不闪一下骨架', () => {
    stubImageCache(true, 640)
    const { container } = render(<ProgressiveImage src="/cached.png" ratio="16 / 9" />)

    const frame = frameOf(container)
    expect(frame.classList.contains('loaded')).toBe(true)
    expect(frame.style.aspectRatio).toBe('')
  })

  it('缓存里是一张坏图（complete 但 naturalWidth 为 0）：判为失败', () => {
    stubImageCache(true, 0)
    const { container } = render(<ProgressiveImage src="/broken.png" />)

    expect(frameOf(container).classList.contains('failed')).toBe(true)
  })

  it('src 变化：回到骨架态；换上的新图若已缓存则立刻落地', () => {
    const { container, rerender } = render(<ProgressiveImage src="/a.png" ratio="16 / 9" />)
    const frame = frameOf(container)

    rerender(<ProgressiveImage src="/b.png" ratio="16 / 9" />)
    expect(frame.classList.contains('loading')).toBe(true)
    expect(frame.style.aspectRatio).toBe('16 / 9')

    // 新图已在缓存里：ref 回调身份随 src 变化而重跑，读 complete 直接判终态。
    stubImageCache(true, 100)
    rerender(<ProgressiveImage src="/c.png" ratio="16 / 9" />)
    expect(frame.classList.contains('loaded')).toBe(true)
  })
})
