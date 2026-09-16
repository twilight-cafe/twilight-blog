// content-media.test.tsx —— ContentView 渲染后处理 与 媒体组件
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import ContentView from '../components/ContentView'
import MediaThumb from '../components/media/MediaThumb'
import MediaLightbox from '../components/media/MediaLightbox'
import MediaPicker from '../components/media/MediaPicker'
import { ToastProvider } from '../lib/toast'
import { refreshSiteMeta, resetSiteCacheForTests } from '../lib/site'
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
    expect(container.querySelector('video')).toBeTruthy()
    expect(container.querySelector('.media-thumb-play')).toBeTruthy()
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
})
