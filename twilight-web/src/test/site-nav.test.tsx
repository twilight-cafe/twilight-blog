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
  // 功能开关由外壳写在 <html> 的 data-search-enabled 属性上；测试各自显式设置，
  // 缺省时必须拆掉，否则上一个用例的假外壳会泄漏进下一个用例的 flag 断言。
  delete document.documentElement.dataset.searchEnabled
  delete document.documentElement.dataset.homePageSize
  delete document.documentElement.dataset.borderRadius
  // --radius 会被 applyBorderRadius 写进内联 style，跨用例必须清掉，
  // 否则前一个用例设的圆角会留在下一个用例的 DOM 上。
  document.documentElement.style.removeProperty('--radius')
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

  it('外壳没有 description 标签时由 head.ts 补上（描述的唯一写入者是它）', async () => {
    const { site, client } = await fresh()
    expect(document.querySelector('meta[name="description"]')).toBeNull()
    client.blog.getSite.mockResolvedValueOnce({ description: 'x' })
    await expect(site.loadSiteDescription()).resolves.toBe('x')
    // 以前 site.ts 找不到这个标签就跳过写入，于是「外壳没带」= 整站没有描述。
    // 现在写入权在 head.ts，它按需创建；页面级摘要优先于站点级的规则见 head-seo.test.tsx。
    expect(document.querySelector('meta[name="description"]')?.getAttribute('content')).toBe('x')
  })

  it('codeBlockMaxLines：正常下发、旧服务端缺键回退默认值、失败用内置值', async () => {
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValueOnce({ description: 'd', code_block_max_lines: 35 })
    const meta = await site.loadSiteMeta()
    expect(meta.codeBlockMaxLines).toBe(35)

    // 更旧的服务端没有这个键：与失败同路，回退到与服务端默认值一致的 20。
    const fresh2 = await fresh()
    fresh2.client.blog.getSite.mockResolvedValueOnce({ description: 'd' })
    await expect(fresh2.site.loadSiteMeta()).resolves.toMatchObject({ codeBlockMaxLines: 20 })

    const fresh3 = await fresh()
    fresh3.client.blog.getSite.mockRejectedValueOnce(new Error('down'))
    await expect(fresh3.site.loadSiteMeta()).resolves.toMatchObject({ codeBlockMaxLines: 20 })
  })

  it('useSiteCodeLines：订阅快照并在保存后立即更新', async () => {
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValue({ description: 'd', code_block_max_lines: 12 })
    client.blog.updateSite.mockResolvedValueOnce({ description: 'd', code_block_max_lines: 48 })
    const { result } = renderHook(() => site.useSiteCodeLines())
    await waitFor(() => expect(result.current).toBe(12))
    await act(async () => {
      await site.saveSiteSettings({ description: 'd', codeBlockMaxLines: 48 })
    })
    await waitFor(() => expect(result.current).toBe(48))
  })

  it('saveSiteSettings：省略 codeBlockMaxLines 时请求体不带该字段（服务端保持现值）', async () => {
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValue({ description: 'd', code_block_max_lines: 20 })
    client.blog.updateSite.mockResolvedValue({ description: '新', code_block_max_lines: 20 })
    await site.saveSiteSettings({ description: '新' })
    expect(client.blog.updateSite).toHaveBeenCalledWith({ description: '新' })
    // SDK 入参形状：undefined 字段由 JSON.stringify 丢弃，但 mock 收到的调用
    // 参数里就是 undefined——toHave 的对象相等会忽略 undefined 键。
  })

  it('saveSiteSettings：带上 codeBlockMaxLines 时原样透传', async () => {
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValue({ description: 'd', code_block_max_lines: 20 })
    client.blog.updateSite.mockResolvedValue({ description: 'd', code_block_max_lines: 7 })
    await site.saveSiteSettings({ description: 'd', codeBlockMaxLines: 7 })
    expect(client.blog.updateSite).toHaveBeenCalledWith({ description: 'd', code_block_max_lines: 7 })
  })

  it('homePageSize：正常下发、旧服务端缺键回退默认值', async () => {
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValueOnce({ description: 'd', home_page_size: 5 })
    await expect(site.loadSiteMeta()).resolves.toMatchObject({ homePageSize: 5 })

    // 更旧的服务端没有这个键：回退到与服务端默认值一致的 12（= handler 上限）。
    const fresh2 = await fresh()
    fresh2.client.blog.getSite.mockResolvedValueOnce({ description: 'd' })
    await expect(fresh2.site.loadSiteMeta()).resolves.toMatchObject({ homePageSize: 12 })

    const fresh3 = await fresh()
    fresh3.client.blog.getSite.mockRejectedValueOnce(new Error('down'))
    await expect(fresh3.site.loadSiteMeta()).resolves.toMatchObject({ homePageSize: 12 })
  })

  it('saveSiteSettings：homePageSize 省略时不进请求体、带上时原样透传', async () => {
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValue({ description: 'd', home_page_size: 12 })
    // 省略 = 服务端保持现值（与 codeBlockMaxLines 相同的三态语义）。
    client.blog.updateSite.mockResolvedValue({ description: 'd', home_page_size: 12 })
    await site.saveSiteSettings({ description: 'd' })
    expect(client.blog.updateSite).toHaveBeenCalledWith({ description: 'd' })
    // 注意：请求对象里带了 keys，但值为 undefined——服务器看不到它们。
    expect(client.blog.updateSite.mock.calls[0][0]).not.toHaveProperty('home_page_size', 0)

    client.blog.updateSite.mockResolvedValue({ description: 'd', home_page_size: 4 })
    await site.saveSiteSettings({ description: 'd', homePageSize: 4 })
    expect(client.blog.updateSite).toHaveBeenLastCalledWith({ description: 'd', home_page_size: 4 })
  })

  it('borderRadius：正常下发、旧服务端缺键回退默认值', async () => {
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValueOnce({ description: 'd', border_radius: 16 })
    await expect(site.loadSiteMeta()).resolves.toMatchObject({ borderRadius: 16 })

    const fresh2 = await fresh()
    fresh2.client.blog.getSite.mockResolvedValueOnce({ description: 'd' })
    await expect(fresh2.site.loadSiteMeta()).resolves.toMatchObject({ borderRadius: 4 })

    const fresh3 = await fresh()
    fresh3.client.blog.getSite.mockRejectedValueOnce(new Error('down'))
    await expect(fresh3.site.loadSiteMeta()).resolves.toMatchObject({ borderRadius: 4 })
  })

  it('borderRadius：服务端回 0 时必须保留 0，不能被兜底成 4', async () => {
    // 这是本次改动最要紧的一条：0 是合法配置（直角），不是「未设置」。
    // 若 toMeta 里把 `?? DEFAULT` 写成 `|| DEFAULT`，或加载逻辑用
    // 「0 即缺失」判断，站长设的直角就会被静默换成圆角——
    // 表现为「设置页显示 0，页面却是圆的」，且没有任何报错可查。
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValueOnce({ description: 'd', border_radius: 0 })
    await expect(site.loadSiteMeta()).resolves.toMatchObject({ borderRadius: 0 })
  })

  it('saveSiteSettings：borderRadius 省略时不进请求体、带上 0 时原样透传', async () => {
    const { site, client } = await fresh()
    client.blog.getSite.mockResolvedValue({ description: 'd' })
    client.blog.updateSite.mockResolvedValue({ description: 'd', border_radius: 4 })
    await site.saveSiteSettings({ description: 'd' })
    expect(client.blog.updateSite).toHaveBeenCalledWith({ description: 'd' })
    expect(client.blog.updateSite.mock.calls[0][0]).not.toHaveProperty('border_radius', 0)

    // 显式传 0（直角）必须原样进入请求体——省略与 0 是两回事。
    client.blog.updateSite.mockResolvedValue({ description: 'd', border_radius: 0 })
    await site.saveSiteSettings({ description: 'd', borderRadius: 0 })
    expect(client.blog.updateSite).toHaveBeenLastCalledWith({ description: 'd', border_radius: 0 })
  })
})

describe('flags.ts', () => {
  it('isSearchEnabled：属性缺失/显式 true 为启用；显式 false 关闭', async () => {
    await fresh()
    const flags = await import('../lib/flags')
    // 旧二进制的外壳没有这个属性 → 全开兜底。
    expect(flags.isSearchEnabled()).toBe(true)

    document.documentElement.dataset.searchEnabled = 'true'
    expect(flags.isSearchEnabled()).toBe(true)

    document.documentElement.dataset.searchEnabled = 'false'
    expect(flags.isSearchEnabled()).toBe(false)

    // 只有字面量 'false' 才算关闭：dataset 的值是字符串，不能取布尔值。
    // 空串与属性缺失一样都落到「启用」。
    document.documentElement.dataset.searchEnabled = ''
    expect(flags.isSearchEnabled()).toBe(true)

    delete document.documentElement.dataset.searchEnabled
    expect(flags.isSearchEnabled()).toBe(true)
  })

  it('homePageSize：读属性、属性缺失/空串/非法值一律回退 12', async () => {
    await fresh()
    const flags = await import('../lib/flags')
    // 属性缺失（老二进制的外壳）→ 兜底。这里若用 Number(raw ?? '') 就会得到 0，
    // 首页会发出一个 page_size=0 的请求。
    expect(flags.homePageSize()).toBe(12)

    document.documentElement.dataset.homePageSize = '4'
    expect(flags.homePageSize()).toBe(4)

    // dataset 是字符串，且可以是空串（模板占位符没填进去时）。
    document.documentElement.dataset.homePageSize = ''
    expect(flags.homePageSize()).toBe(12)

    // 非数字 / 0 / 负数都被拦在同一处：任何非法值都不该变成一个请求参数。
    document.documentElement.dataset.homePageSize = 'abc'
    expect(flags.homePageSize()).toBe(12)

    document.documentElement.dataset.homePageSize = '0'
    expect(flags.homePageSize()).toBe(12)

    document.documentElement.dataset.homePageSize = '-3'
    expect(flags.homePageSize()).toBe(12)

    // 超过 handler 上限的值**不在这里夹取**（服务端的配置校验已经挡住了它）；
    // 这里只是不该把它变成 NaN 或 0。仍有值就传，让后端回 400 而不是静默改语义。
    document.documentElement.dataset.homePageSize = '40'
    expect(flags.homePageSize()).toBe(40)
  })

  it('borderRadius：读属性、0 是合法值、越界才回退 4', async () => {
    await fresh()
    const flags = await import('../lib/flags')

    // 属性缺失（老二进制的外壳）→ 兜底，与升级前的 --radius: 4px 一致。
    expect(flags.borderRadius()).toBe(4)

    document.documentElement.dataset.borderRadius = '12'
    expect(flags.borderRadius()).toBe(12)

    // 本项设置与 homePageSize 最要紧的分别就在这里：**0 必须原样返回**。
    // 若照抄 `parsed < 1`，站长设成直角后前台会永远按兜底的 4px 渲染，
    // 而设置页显示 0 —— 没有任何报错可查。
    document.documentElement.dataset.borderRadius = '0'
    expect(flags.borderRadius()).toBe(0)

    // 边界值 24 收，25 与负数落到兜底。
    document.documentElement.dataset.borderRadius = '24'
    expect(flags.borderRadius()).toBe(24)
    document.documentElement.dataset.borderRadius = '25'
    expect(flags.borderRadius()).toBe(4)
    document.documentElement.dataset.borderRadius = '-1'
    expect(flags.borderRadius()).toBe(4)

    // 空串 / 非数字（模板占位符没填进去时的形态）一律兜底。
    document.documentElement.dataset.borderRadius = ''
    expect(flags.borderRadius()).toBe(4)
    document.documentElement.dataset.borderRadius = 'abc'
    expect(flags.borderRadius()).toBe(4)
  })

  it('applyBorderRadius：把读到的值写进 :root 的 --radius 内联样式', async () => {
    await fresh()
    const flags = await import('../lib/flags')

    // 写内联样式而不是改样式表：--radius 的消费方遍及 InkUI 与站点样式，
    // 内联的优先级又高于 :root 里的静态兜底，是唯一可行的注入点。
    document.documentElement.dataset.borderRadius = '0'
    flags.applyBorderRadius()
    expect(document.documentElement.style.getPropertyValue('--radius')).toBe('0px')

    document.documentElement.dataset.borderRadius = '16'
    flags.applyBorderRadius()
    expect(document.documentElement.style.getPropertyValue('--radius')).toBe('16px')

    // 属性缺失 → 落到兜底值 4px，而不是空串（空串会让所有 border-radius 失效）。
    delete document.documentElement.dataset.borderRadius
    flags.applyBorderRadius()
    expect(document.documentElement.style.getPropertyValue('--radius')).toBe('4px')
  })
})

describe('email-code-cooldown.ts', () => {
  it('倒计时随每秒 tick 递减，归零后自动停表', async () => {
    vi.useFakeTimers()
    try {
      await fresh()
      const cooldown = await import('../lib/email-code-cooldown')
      cooldown.clearEmailCodeCooldown()

      const { result } = renderHook(() => cooldown.useEmailCodeCooldown('a@x.com'))
      expect(result.current).toBe(0)

      // 起 5 秒（缩短周期，免得真的走完 60 秒）。
      act(() => cooldown.startEmailCodeCooldown('a@x.com', 5))
      expect(result.current).toBe(5)

      // 这一步覆盖的是 hook 里那个 setInterval 回调：没有它，倒计时会永远停在 5。
      act(() => { vi.advanceTimersByTime(1000) })
      expect(result.current).toBe(4)
      act(() => { vi.advanceTimersByTime(2000) })
      expect(result.current).toBe(2)

      // 走到归零后 effect 会清掉 interval——再推进时间也不该有变化
      // （remaining<=0 时不再挂定时器，避免每个登录页常驻一个唤醒源）。
      act(() => { vi.advanceTimersByTime(5000) })
      expect(result.current).toBe(0)
    } finally {
      vi.useRealTimers()
    }
  })

  it('按地址分桶归一化；seconds<=0 不启动', async () => {
    await fresh()
    const cooldown = await import('../lib/email-code-cooldown')
    cooldown.clearEmailCodeCooldown()

    // 大小写与首尾空白归一化：服务端把这些当成同一个收件箱，前端必须同口径，
    // 否则会显示「可以发送」而实际被 429 拒。
    cooldown.startEmailCodeCooldown('  A@X.com ')
    const { result } = renderHook(() => cooldown.useEmailCodeCooldown('a@x.com'))
    expect(result.current).toBeGreaterThan(0)

    // 另一个地址不受影响。
    const other = renderHook(() => cooldown.useEmailCodeCooldown('b@x.com'))
    expect(other.result.current).toBe(0)

    // seconds <= 0 是「不启动」而不是「立刻可发」的另一种写法：
    // 用于服务端返回一个已经过期的时刻时不清掉既有冷却。
    cooldown.startEmailCodeCooldown('b@x.com', 0)
    expect(other.result.current).toBe(0)
  })

  it('clearEmailCodeCooldown 支持按地址清与全清', async () => {
    await fresh()
    const cooldown = await import('../lib/email-code-cooldown')
    cooldown.clearEmailCodeCooldown()

    cooldown.startEmailCodeCooldown('a@x.com')
    const a = renderHook(() => cooldown.useEmailCodeCooldown('a@x.com'))
    cooldown.startEmailCodeCooldown('b@x.com')
    const b = renderHook(() => cooldown.useEmailCodeCooldown('b@x.com'))
    expect(a.result.current).toBeGreaterThan(0)
    expect(b.result.current).toBeGreaterThan(0)

    act(() => cooldown.clearEmailCodeCooldown('a@x.com'))
    expect(a.result.current).toBe(0)
    expect(b.result.current).toBeGreaterThan(0) // 只清指定地址

    act(() => cooldown.clearEmailCodeCooldown())
    expect(b.result.current).toBe(0)
  })

  it('isRateLimited：只认 BlogApiError 的 429；普通对象带 status 不算', async () => {
    await fresh()
    const cooldown = await import('../lib/email-code-cooldown')
    const { BlogApiError } = await import('twilight-blog-api')

    expect(cooldown.isRateLimited(new BlogApiError('太频繁', {
      status: 429, method: 'POST', path: '/api/auth/email-code',
    }))).toBe(true)
    // 非 429 的 BlogApiError（如 400）不算限流。
    expect(cooldown.isRateLimited(new BlogApiError('bad', {
      status: 400, method: 'POST', path: '/api/auth/email-code',
    }))).toBe(false)
    // 网络错误的 status 是 undefined。
    expect(cooldown.isRateLimited(new BlogApiError('断网', {
      method: 'POST', path: '/api/auth/email-code',
    }))).toBe(false)

    // 关键：一个恰好带 status=429 的普通对象**不**算——用 instanceof 收窄而不是
    // duck-type，否则任何含 status 字段的业务错误都会被误判成限流。
    expect(cooldown.isRateLimited({ status: 429 })).toBe(false)
    expect(cooldown.isRateLimited(null)).toBe(false)
    expect(cooldown.isRateLimited('429')).toBe(false)
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

  it('搜索关闭时 buildNav 摘掉搜索项；开启或缺省时保留', async () => {
    await fresh()
    const nav = await import('../lib/nav')

    document.documentElement.dataset.searchEnabled = 'false'
    expect(nav.buildNav([]).map((item) => item.to)).toEqual(['/', '/archive', '/tags', '/moments'])

    document.documentElement.dataset.searchEnabled = 'true'
    expect(nav.buildNav([]).map((item) => item.to)).toContain('/search')

    delete document.documentElement.dataset.searchEnabled
    expect(nav.buildNav([]).map((item) => item.to)).toContain('/search')
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
