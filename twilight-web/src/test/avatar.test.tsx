// avatar.test.tsx —— 站长头像（AvatarPicker / AuthorAvatar / Profile 三态 / SDK 形状）
//                     + 侧栏 TOC 位置与间距、底栏下划线的样式契约
//
// 头像与文章头图是同一套「引用而非所有权」语义的另一处落地（见 article-cover.test.tsx
// 的同名说明），所以这里只在一种情况下断言 deleteMedia：前端自己刚上传了非图片。
// 换头像、移除头像都只解除引用——那张图还留在媒体库里，用户可能只是反悔。
//
// 侧栏与底栏那几条是**文本级** CSS 契约（读 src/styles/app.css 源码）：jsdom 不做
// 布局，DOM 断言看不见「侧边栏距顶栏 26px」这件事，而这些间距/宽度正是需求
// 2 与 5 的本体。真浏览器的截图验证由 .workbuddy/scripts 下的可视化脚本负责，
// 这里钉住的是让视觉成立的那几条声明不被悄悄删掉。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { createClient } from 'twilight-blog-api'
import AuthorAvatar from '../components/AuthorAvatar'
import AvatarPicker from '../components/media/AvatarPicker'
import PublicSidebar from '../components/PublicSidebar'
import Profile from '../pages/admin/Profile'
import { ToastProvider } from '../lib/toast'
import { resetSiteCacheForTests } from '../lib/site'
import {
  adminMe,
  getSharedClientMock,
  makeMedia,
  makePage,
  resetClientMock,
} from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})

const client = getSharedClientMock()

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => { resolve = res })
  return { promise, resolve }
}

/** 给隐藏的 file input 塞文件再触发 change——jsdom 里 files 只读，必须 defineProperty。 */
function chooseFiles(container: HTMLElement, files: File[] | null) {
  const input = container.querySelector('input[type="file"]') as HTMLInputElement
  Object.defineProperty(input, 'files', { value: files, configurable: true })
  fireEvent.change(input)
}

function renderPicker(props: { url?: string; disabled?: boolean; withBusyCallback?: boolean } = {}) {
  const onChange = vi.fn()
  const onBusyChange = vi.fn()
  const utils = render(
    <ToastProvider>
      <AvatarPicker
        url={props.url ?? ''}
        onChange={onChange}
        // withBusyCallback: false 用来走 `onBusyChange?.()` 的空值分支（该 prop 可选）。
        {...(props.withBusyCallback === false ? {} : { onBusyChange })}
        disabled={props.disabled}
      />
    </ToastProvider>,
  )
  return { ...utils, onChange, onBusyChange }
}

beforeEach(() => {
  vi.clearAllMocks()
  resetClientMock(client)
  client.setCurrentUser(null)
  resetSiteCacheForTests()
  client.blog.getSite.mockResolvedValue({ description: '测试站' })
})

describe('AuthorAvatar', () => {
  it('未设头像：渲染首字母占位块（装饰性，不进读屏）', () => {
    const { container } = render(<AuthorAvatar />)
    const placeholder = container.querySelector('span.avatar') as HTMLElement
    expect(placeholder.textContent).toBe('T')
    expect(placeholder.getAttribute('aria-hidden')).toBe('true')
    expect(container.querySelector('img')).toBeNull()
  })

  it('设了头像：渲染 <img>，地址来自 /api/site，alt 为空（名字就在旁边）', async () => {
    client.blog.getSite.mockResolvedValue({ description: '测试站', avatar_url: '/api/media/9' })
    const { container } = render(<AuthorAvatar />)
    await waitFor(() => expect(container.querySelector('img.avatar')).toBeTruthy())
    const img = container.querySelector('img.avatar') as HTMLImageElement
    expect(img.getAttribute('src')).toBe('/api/media/9')
    expect(img.getAttribute('alt')).toBe('')
  })
})

describe('AvatarPicker', () => {
  it('无头像：点占位圆打开文件对话框；选中图片后上传并回传 id', async () => {
    const uploaded = makeMedia({ id: 77, media_type: 'image', original_name: 'avatar.png' })
    client.blog.uploadMedia.mockResolvedValueOnce([uploaded])

    const { container, onChange, onBusyChange } = renderPicker()
    // 空态：圆形虚线占位，没有预览
    const empty = container.querySelector('.avatar-picker-empty') as HTMLButtonElement
    expect(empty).toBeTruthy()
    expect(container.querySelector('.avatar-picker-preview')).toBeNull()

    // 点占位框应当去点隐藏的 file input——用一个 spy 证明「点了」，而不是只证明没报错。
    const click = vi.spyOn(HTMLInputElement.prototype, 'click')
    fireEvent.click(empty)
    expect(click).toHaveBeenCalledTimes(1)
    click.mockRestore()

    chooseFiles(container, [new File(['x'], 'avatar.png', { type: 'image/png' })])
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(77))
    // busy 必须复位，否则外层表单会永远停在「保存」不可用的状态。
    expect(onBusyChange).toHaveBeenNthCalledWith(1, true)
    expect(onBusyChange).toHaveBeenLastCalledWith(false)
    // input.value 必须清空，否则再选同一个文件不会触发 change。
    expect((container.querySelector('input[type="file"]') as HTMLInputElement).value).toBe('')
  })

  it('files 为 null 或空选择：直接返回，不上传、不碰 busy', async () => {
    const { container, onChange, onBusyChange } = renderPicker()
    chooseFiles(container, null)
    chooseFiles(container, [])
    await act(async () => {})
    expect(client.blog.uploadMedia).not.toHaveBeenCalled()
    expect(onChange).not.toHaveBeenCalled()
    expect(onBusyChange).not.toHaveBeenCalled()
  })

  it('上传结果为空数组：提示失败且不改头像', async () => {
    client.blog.uploadMedia.mockResolvedValueOnce([])
    const { container, onChange } = renderPicker()
    chooseFiles(container, [new File(['x'], 'a.png', { type: 'image/png' })])
    await waitFor(() => expect(screen.getAllByText('上传失败').length).toBeGreaterThan(0))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('上传的是非图片：删掉刚上传的那一份并提示；清理失败也照样提示', async () => {
    const audio = makeMedia({ id: 88, media_type: 'audio', original_name: 'bgm.mp3' })
    client.blog.uploadMedia.mockResolvedValueOnce([audio])
    const { container, onChange } = renderPicker()

    chooseFiles(container, [new File(['x'], 'bgm.mp3', { type: 'audio/mpeg' })])
    await waitFor(() => expect(screen.getAllByText('头像必须是图片').length).toBeGreaterThan(0))
    // 这份文件还没有被任何人引用过，留着它就是媒体库里一个来历不明的孤儿。
    expect(client.blog.deleteMedia).toHaveBeenCalledWith(88)
    expect(onChange).not.toHaveBeenCalled()

    // 第二次：deleteMedia 失败（清理是尽力而为，不能因此改变「不能当头像」这个结论）。
    client.blog.uploadMedia.mockResolvedValueOnce([audio])
    client.blog.deleteMedia.mockRejectedValueOnce(new Error('删不掉'))
    chooseFiles(container, [new File(['x'], 'bgm.mp3', { type: 'audio/mpeg' })])
    await waitFor(() => expect(screen.getAllByText('头像必须是图片').length).toBeGreaterThan(1))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('上传抛错：按服务端信息提示，头像不变', async () => {
    client.blog.uploadMedia.mockRejectedValueOnce(new Error('文件太大'))
    const { container, onChange } = renderPicker()
    chooseFiles(container, [new File(['x'], 'huge.png', { type: 'image/png' })])
    await waitFor(() => expect(screen.getAllByText('文件太大').length).toBeGreaterThan(0))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('上传中：空态显示 loading 并禁用；有头像时「更换」替换成「上传中…」', async () => {
    const gate = deferred<ReturnType<typeof makeMedia>[]>()
    client.blog.uploadMedia.mockImplementation(() => gate.promise)
    const { container, rerender } = render(
      <ToastProvider>
        <AvatarPicker url="" onChange={vi.fn()} />
      </ToastProvider>,
    )

    chooseFiles(container, [new File(['x'], 'a.png', { type: 'image/png' })])
    await waitFor(() => expect(container.querySelector('.media-picker-loading')).toBeTruthy())
    // 上传中必须禁止再次点击，否则会并发上传出两张图。
    expect((container.querySelector('.avatar-picker-empty') as HTMLButtonElement).disabled).toBe(true)

    // 有头像时的上传中态：走的是另一支 JSX（预览 + 两个按钮），文案也不同。
    rerender(
      <ToastProvider>
        <AvatarPicker url="/api/media/42" onChange={vi.fn()} disabled />
      </ToastProvider>,
    )
    expect(container.querySelector('.avatar-picker-preview')?.getAttribute('src')).toBe('/api/media/42')
    expect(container.querySelector('.avatar-actions')?.textContent).toContain('上传中…')
    expect((screen.getByRole('button', { name: '移除' }) as HTMLButtonElement).disabled).toBe(true)

    await act(async () => {
      gate.resolve([])
    })
  })

  it('上传还没回来就卸载：finally 里的 ref 守卫早退，不抛错', async () => {
    const gate = deferred<ReturnType<typeof makeMedia>[]>()
    client.blog.uploadMedia.mockImplementation(() => gate.promise)
    const { container, unmount } = render(
      <ToastProvider>
        <AvatarPicker url="" onChange={vi.fn()} />
      </ToastProvider>,
    )

    chooseFiles(container, [new File(['x'], 'a.png', { type: 'image/png' })])
    // 上传中离开页面（比如点了「返回」）：inputRef 已被 React 置空，收尾逻辑必须能容忍。
    unmount()
    await act(async () => {
      gate.resolve([])
    })
  })

  it('有头像：预览、更换、移除；disabled 时两个按钮都禁用；移除不删媒体', async () => {
    const onChange = vi.fn()
    const { container, rerender } = render(
      <ToastProvider>
        <AvatarPicker url="/api/media/42" onChange={onChange} />
      </ToastProvider>,
    )

    expect(container.querySelector('.avatar-picker-empty')).toBeNull()
    const preview = container.querySelector('.avatar-picker-preview') as HTMLImageElement
    expect(preview.getAttribute('src')).toBe('/api/media/42')
    expect(preview.getAttribute('alt')).toBe('站长头像')

    const click = vi.spyOn(HTMLInputElement.prototype, 'click')
    fireEvent.click(screen.getByRole('button', { name: '更换' }))
    expect(click).toHaveBeenCalledTimes(1)
    click.mockRestore()

    // 移除只解除引用：不调 deleteMedia（那张图还在媒体库里，用户可能只是反悔）。
    fireEvent.click(screen.getByRole('button', { name: '移除' }))
    expect(onChange).toHaveBeenCalledWith(null)
    expect(client.blog.deleteMedia).not.toHaveBeenCalled()

    // disabled：两个按钮都不可点（外部正在保存，不能被改出新值）。
    rerender(
      <ToastProvider>
        <AvatarPicker url="/api/media/42" onChange={onChange} disabled />
      </ToastProvider>,
    )
    expect((screen.getByRole('button', { name: '更换' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '移除' }) as HTMLButtonElement).disabled).toBe(true)
  })
})

/* --------------------------- 侧栏：TOC 在作者卡下方 --------------------------- */

describe('PublicSidebar 的 TOC 位置', () => {
  it('有目录时 toc-box 必须排在作者卡（home-all）之后', () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/post/1']}>
        <PublicSidebar
          pages={[makePage({ id: 3, title: '关于' })]}
          toc={[{ id: 'a', level: 1, text: '第一章' }]}
        />
      </MemoryRouter>,
    )
    const authorCard = container.querySelector('.home-all') as HTMLElement
    const toc = container.querySelector('.toc-box') as HTMLElement
    expect(authorCard).toBeTruthy()
    expect(toc).toBeTruthy()
    // 位置契约：作者卡在前，目录在后。目录长度取决于正文章节数，不受控制，
    // 放在作者卡之上会把它推出侧栏滚动区（需求 2 的本体）。
    expect(authorCard.compareDocumentPosition(toc) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})

/* ------------------------------ Profile 三态 ------------------------------ */

function renderAdminProfile() {
  client.setCurrentUser(adminMe)
  return render(
    <ToastProvider>
      <Profile />
    </ToastProvider>,
  )
}

function submitProfileForm(container: HTMLElement) {
  const form = (Array.from(container.querySelectorAll('form')) as HTMLFormElement[])[0]
  fireEvent.submit(form)
}

describe('Profile 的头像三态', () => {
  it('没碰头像：保存时 avatarMediaId 这个键不出现（缺席 = 不改）', async () => {
    const { container } = renderAdminProfile()
    await waitFor(() => expect(screen.getByText('站长资料')).toBeTruthy())

    const inputs = Array.from(
      container.querySelectorAll('form input:not([type="file"])'),
    ) as HTMLInputElement[]
    fireEvent.change(inputs[0], { target: { value: '新站名' } })
    client.blog.updateAdminProfile.mockResolvedValueOnce(adminMe)
    submitProfileForm(container)

    await waitFor(() => expect(client.blog.updateAdminProfile).toHaveBeenCalledTimes(1))
    const payload = client.blog.updateAdminProfile.mock.calls[0][0] as Record<string, unknown>
    expect(payload.name).toBe('新站名')
    // 塌缩成 `?? null` 会让「只改显示名」静默摘掉头像；写成恒传又会让头像永远摘不掉。
    // 键必须缺席——这正是三态里的「keep」。
    expect('avatarMediaId' in payload).toBe(false)
  })

  it('换头像：新 id 随保存下发；成功后刷新站点缓存（侧栏作者卡立刻跟上）', async () => {
    const uploaded = makeMedia({ id: 55, media_type: 'image' })
    client.blog.uploadMedia.mockResolvedValueOnce([uploaded])
    client.blog.updateAdminProfile.mockResolvedValueOnce(adminMe)
    const { container } = renderAdminProfile()
    await waitFor(() => expect(screen.getByText('站长资料')).toBeTruthy())

    const callsBefore = client.blog.getSite.mock.calls.length
    chooseFiles(container, [new File(['x'], 'a.png', { type: 'image/png' })])
    await waitFor(() => expect(container.querySelector('.avatar-picker-preview')).toBeTruthy())
    // 预览用的是保存后会显示的同一个地址函数（mediaUrl），不会出现「预览一张、存下另一张」。
    expect(container.querySelector('.avatar-picker-preview')?.getAttribute('src')).toBe('/api/media/55')

    submitProfileForm(container)
    await waitFor(() => expect(client.blog.updateAdminProfile).toHaveBeenCalledWith({
      name: '站长',
      avatarMediaId: 55,
    }))
    // 头像不是这条接口的返回值，侧栏订阅的是站点缓存——不刷新就会「保存成功但头像没变」。
    await waitFor(() => expect(client.blog.getSite.mock.calls.length).toBeGreaterThan(callsBefore))
    // 保存成功后草稿复位：再提交一次也不该带上头像键。
    client.blog.updateAdminProfile.mockResolvedValueOnce(adminMe)
    submitProfileForm(container)
    await waitFor(() => expect(client.blog.updateAdminProfile).toHaveBeenCalledTimes(2))
    expect('avatarMediaId' in (client.blog.updateAdminProfile.mock.calls[1][0] as Record<string, unknown>)).toBe(false)
  })

  it('移除头像：显式 null 随保存下发（摘除靠它，不能被吞掉）', async () => {
    client.blog.getSite.mockResolvedValue({ description: '测试站', avatar_url: '/api/media/9' })
    client.blog.updateAdminProfile.mockResolvedValueOnce(adminMe)
    const { container } = renderAdminProfile()
    await waitFor(() => expect(container.querySelector('.avatar-picker-preview')).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: '移除' }))
    await waitFor(() => expect(container.querySelector('.avatar-picker-empty')).toBeTruthy())

    submitProfileForm(container)
    await waitFor(() => expect(client.blog.updateAdminProfile).toHaveBeenCalledWith({
      name: '站长',
      avatarMediaId: null,
    }))
  })

  it('上传没结束就保存：按钮禁用（存下去会是一张还没拿到 id 的图）', async () => {
    const gate = deferred<ReturnType<typeof makeMedia>[]>()
    client.blog.uploadMedia.mockImplementation(() => gate.promise)
    const { container } = renderAdminProfile()
    await waitFor(() => expect(screen.getByText('站长资料')).toBeTruthy())

    const save = (screen.getByRole('button', { name: '保存' }) as HTMLButtonElement)
    expect(save.disabled).toBe(false)
    chooseFiles(container, [new File(['x'], 'a.png', { type: 'image/png' })])
    await waitFor(() => expect(save.disabled).toBe(true))
    await act(async () => {
      gate.resolve([])
    })
  })
})

/* --------------------------- SDK 下发的请求体形状 --------------------------- */

/**
 * 与 article-cover.test.tsx 同理：不用 client mock，用真实模块 + fetch 桩，
 * 钉的正是封装层自己的三态下发规则。null 被吞掉的表现是「头像永远摘不掉」，
 * 全程无报错——只有看请求体里键是否存在才测得到。
 */
describe('updateAdminProfile 的请求体形状（真实模块）', () => {
  function recordingClient() {
    const bodies: Record<string, unknown>[] = []
    const fetchStub = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      bodies.push(JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>)
      return new Response(JSON.stringify({ role: 'admin', name: 'n' }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      })
    })
    return { api: createClient({ baseUrl: 'https://blog.test', fetch: fetchStub as unknown as typeof fetch }), bodies }
  }

  it('省略即不动；null 必须下发；数字随保存下发', async () => {
    const { api, bodies } = recordingClient()

    await api.updateAdminProfile({ name: '只改名' })
    expect(bodies[0]).toEqual({ name: '只改名' })
    expect('avatar_media_id' in bodies[0]).toBe(false)

    await api.updateAdminProfile({ name: '摘掉头像', avatarMediaId: null })
    expect(bodies[1]).toEqual({ name: '摘掉头像', avatar_media_id: null })

    await api.updateAdminProfile({ name: '换一张', avatarMediaId: 9 })
    expect(bodies[2]).toEqual({ name: '换一张', avatar_media_id: 9 })

    await api.updateAdminProfile({ name: '连登录名一起改', username: 'root', avatarMediaId: 9 })
    expect(bodies[3]).toEqual({ name: '连登录名一起改', username: 'root', avatar_media_id: 9 })
  })
})

/* ------------------------- 侧栏间距 / 底栏下划线（CSS 契约） ------------------------- */

describe('侧栏间距与底栏下划线的样式契约', () => {
  // 与 article-cover.test.tsx 的说明相同：直接读源码文件，绕开 vitest 对 CSS 的处理。
  const appCss = readFileSync(resolve(process.cwd(), 'src/styles/app.css'), 'utf8')

  function ruleBlock(selector: string): string {
    const start = appCss.indexOf(`${selector} {`)
    if (start === -1) return ''
    return appCss.slice(start, appCss.indexOf('}', start))
  }

  it('侧边栏与文章头图共用同一个顶栏间距变量（--top-gap）', () => {
    const rootRule = ruleBlock(':root')
    expect(rootRule, '找不到 :root 规则').not.toBe('')
    expect(rootRule).toMatch(/--top-gap:\s*26px/)

    // 两处都用 var(--top-gap) 而不是各自的魔法数：间距要改就一起改，
    // 这正是需求 2 的「侧边栏间距 = 头图间距」。
    expect(ruleBlock('.public-sidebar')).toMatch(/margin-top:\s*var\(--top-gap\)/)
    expect(ruleBlock('.post-cover')).toMatch(/margin-top:\s*var\(--top-gap\)/)
  })

  // 头图的视觉契约：高度由站点设置固定，顶部平齐、底部到高度末端渐隐。
  it('文章头图固定为配置高度，顶部平齐并在末端渐隐', () => {
    const cover = ruleBlock('.post-cover')
    expect(cover).toMatch(/height:\s*var\(--post-banner-height, 400px\)/)
    expect(cover).toMatch(/background:\s*transparent/)

    const frame = ruleBlock('.post-cover .img-frame')
    expect(frame).toMatch(/height:\s*100%/)
    expect(frame).toMatch(/mask-image:\s*linear-gradient\(to bottom/)
    // 结束点必须是 100%：渐隐要一直持续到给定高度的最后。
    expect(frame).toMatch(/transparent 100%\)/)

    const img = ruleBlock('.post-cover img')
    expect(img).toMatch(/height:\s*100%/)
    expect(img).toMatch(/object-fit:\s*cover/)
    expect(img).toMatch(/object-position:\s*top/)
  })

  it('侧栏永不出现滑动条：弹性分配高度、滑动条隐藏、盒子只做透明度入场', () => {
    // 三件事合起来才是「滑动条总是不出现」：
    //   ① 侧栏按剩余高度分配（作者卡固定、目录盒收缩、目录列表内部滚），
    //      不再整栏 overflow-y 硬扛，目录多寡都不改变侧栏自身的高度；
    //   ② 该滚的地方仍能滚，但滑动条一律不画（Chrome/Safari 与 Firefox 各一条）；
    //   ③ 盒子入场不再用带 translateY 的 fade-up——位移不参与布局，却会被算进
    //      可滚动溢出：目录盒是最后一个盒子，它入场的那 8px 就是「中途闪一下
    //      滑动条、最终又没有」的来路。
    const sidebar = ruleBlock('.public-sidebar')
    expect(sidebar).toMatch(/display:\s*flex/)
    expect(sidebar).toMatch(/flex-direction:\s*column/)
    expect(sidebar).toMatch(/scrollbar-width:\s*none/)
    expect(sidebar).not.toMatch(/overflow-y:\s*auto/)

    const webkit = ruleBlock('.public-sidebar::-webkit-scrollbar')
    expect(webkit, '找不到 ::-webkit-scrollbar 规则').not.toBe('')
    expect(webkit).toMatch(/width:\s*0/)

    // 目录列表在侧栏里用剩余高度，而不是 60vh（60vh + 作者卡会超出侧栏高度）。
    const tocList = ruleBlock('.public-sidebar .toc-list')
    expect(tocList).toMatch(/max-height:\s*none/)
    expect(tocList).toMatch(/flex:/)
    expect(tocList).toMatch(/scrollbar-width:\s*none/)
    expect(ruleBlock('.toc-box')).not.toMatch(/max-height/)

    // 侧栏盒子：只有透明度入场，没有位移。.public-sidebar .box 出现过两次
    // （边框/内边距与入场动画），这里按「带 animation 的那一块」取。
    expect(appCss).toMatch(/\.public-sidebar \.box \{[^}]*animation:\s*fade-in/)
    expect(appCss).not.toMatch(/\.public-sidebar \.box \{[^}]*animation:\s*fade-up/)
    expect(appCss).toMatch(/@keyframes fade-in\s*\{[^}]*opacity:\s*0/)

    // 站点设置里的作者卡预览复用了 .public-sidebar：它必须还是普通块，
    // 不能被侧栏的弹性列布局影响。
    expect(ruleBlock('.set-sidebar')).toMatch(/display:\s*block/)
  })

  it('底栏链接的下划线只出现在文字下方（width: fit-content）', () => {
    // 块级元素在 inkui 全局 a 的 background-size 0%→100% 动画下，
    // 下划线会横贯整列宽度；fit-content 把元素收到文字那么宽。
    const rule = ruleBlock('footer.site a')
    expect(rule, '找不到 footer.site a 规则').not.toBe('')
    expect(rule).toMatch(/width:\s*fit-content/)
  })
})
