// site-nav.test.tsx —— 站点描述缓存/订阅 与 导航构建
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderHook, act, waitFor } from '@testing-library/react'
import { getSharedClientMock, type ClientMock } from './helpers'

// 注意：vi.resetModules() 会把相对路径的 vi.mock 注册一并清掉（vitest v5 行为），
// 所以这里用 doMock 在每次重建注册表后重新登记 mock；mock 本体是挂在 globalThis
// 上的单例（见 helpers.getSharedClientMock），不依赖模块实例的身份。
async function fresh(): Promise<{ site: typeof import('../lib/site'); client: ClientMock }> {
  vi.resetModules()
  vi.doMock('../lib/client', async () => {
    const { getSharedClientMock } = await import('./helpers')
    return getSharedClientMock()
  })
  const site = await import('../lib/site')
  return { site, client: getSharedClientMock() }
}

beforeEach(() => {
  document.title = ''
  document.querySelector('meta[name="description"]')?.remove()
})

describe('site.ts', () => {
  it('loadSiteDescription：成功拉取并写入 meta；失败回退空串', async () => {
    document.head.insertAdjacentHTML('beforeend', '<meta name="description" content="default">')
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValueOnce({ description: '自建博客' })
    await expect(site.loadSiteDescription()).resolves.toBe('自建博客')
    expect(document.querySelector('meta[name="description"]')?.getAttribute('content')).toBe('自建博客')

    // 缓存生效：第二次不再发请求
    await expect(site.loadSiteDescription()).resolves.toBe('自建博客')
    expect(client.blog.getSite).toHaveBeenCalledTimes(1)

    // 新模块实例（缓存清空）+ 失败 → 空串
    const fresh2 = await fresh()
    fresh2.client.blog.getSite.mockRejectedValueOnce(new Error('down'))
    await expect(fresh2.site.loadSiteDescription()).resolves.toBe('')
  })

  it('description 为 null 时按空串发布；并发共享 in-flight', async () => {
    const { site, client } = await fresh()
    let resolveGet: (value: unknown) => void = () => {}
    client.blog.getSite.mockImplementationOnce(() => new Promise((resolve) => { resolveGet = resolve }))
    const combined = Promise.all([site.loadSiteDescription(), site.loadSiteDescription()])
    await Promise.resolve()
    resolveGet({ description: null })
    const [a, b] = await combined
    expect(a).toBe('')
    expect(b).toBe('')
    expect(client.blog.getSite).toHaveBeenCalledTimes(1)
  })

  it('saveSiteDescription 通知订阅者并同步 meta', async () => {
    document.head.insertAdjacentHTML('beforeend', '<meta name="description" content="old">')
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValue({ description: '旧描述' })
    client.blog.updateSite.mockResolvedValueOnce({ description: '新描述' })
    const { result } = renderHook(() => site.useSiteDescription())
    await waitFor(() => expect(result.current).toBe('旧描述'))
    await act(async () => {
      await expect(site.saveSiteDescription('新描述')).resolves.toBe('新描述')
    })
    await waitFor(() => expect(result.current).toBe('新描述'))
    expect(document.querySelector('meta[name="description"]')?.getAttribute('content')).toBe('新描述')
  })

  it('useSiteDescription：未加载时用内置文案兜底；空描述也回退', async () => {
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValueOnce({ description: '' })
    const { result } = renderHook(() => site.useSiteDescription())
    expect(result.current).toBe(site.FALLBACK_DESCRIPTION)
    await waitFor(() => expect(result.current).toBe(site.FALLBACK_DESCRIPTION)) // 空串 → 兜底
  })

  it('useDocumentTitle / useHomeDocumentTitle', async () => {
    const { site } = await fresh()
    const { rerender } = renderHook(({ title }) => site.useDocumentTitle(title), { initialProps: { title: '归档' } })
    expect(document.title).toBe('归档 · Twilight')
    rerender({ title: '标签' })
    expect(document.title).toBe('标签 · Twilight')

    const { client } = await fresh()
    client.blog.getSite.mockResolvedValueOnce({ description: '墨与代码' })
    renderHook(() => site.useHomeDocumentTitle())
    // 首渲染用兜底描述，加载完成后变为站点描述
    await waitFor(() => expect(document.title).toBe('Twilight · 墨与代码'))
  })

  it('无 meta 标签时 applyMetaDescription 安全跳过', async () => {
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValueOnce({ description: 'x' })
    await expect(site.loadSiteDescription()).resolves.toBe('x')
    expect(document.querySelector('meta[name="description"]')).toBeNull()
  })
})

describe('nav.ts', () => {
  it('buildNav 追加独立页面；isNavActive 只在首页用全等', async () => {
    await fresh()
    const nav = await import('../lib/nav')
    const items = nav.buildNav([
      { id: 9, title: '关于', content: '', created_at: 1, updated_at: 1 },
    ])
    expect(items.slice(0, 5).map((item) => item.label)).toEqual(['首页', '归档', '标签', '搜索', '微语'])
    expect(items[5]).toEqual({ to: '/pages/9', label: '关于' })

    expect(nav.isNavActive('/', '/')).toBe(true)
    expect(nav.isNavActive('/archive', '/')).toBe(false)
    expect(nav.isNavActive('/archive', '/archive')).toBe(true)
    expect(nav.isNavActive('/archive/2', '/archive')).toBe(false)
  })

  it('loadPagesForNav：单页直接返回；多页并发补齐', async () => {
    const { client } = await fresh()
    const nav = await import('../lib/nav')

    client.blog.listPages.mockResolvedValueOnce({
      items: [{ id: 1, title: 'A', content: '', created_at: 1, updated_at: 1 }],
      page: 1, page_size: 12, total: 1, total_pages: 1,
    })
    await expect(nav.loadPagesForNav()).resolves.toHaveLength(1)
    expect(client.blog.listPages).toHaveBeenCalledTimes(1)

    const page = (ids: number[]) => ({
      items: ids.map((id) => ({ id, title: `P${id}`, content: '', created_at: 1, updated_at: 1 })),
      page: 1, page_size: 12, total: 14, total_pages: 2,
    })
    client.blog.listPages.mockImplementation(async (options: { page?: number }) =>
      options?.page === 1 ? page([1]) : page([2]))
    const result = await nav.loadPagesForNav()
    expect(result.map((item) => item.id)).toEqual([1, 2])
    expect(client.blog.listPages).toHaveBeenLastCalledWith({ page: 2, pageSize: 12 })
  })
})
