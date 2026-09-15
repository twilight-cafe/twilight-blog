// sdk-update-content.test.ts —— 真实 SDK 的「省略正文」线上请求体契约
//
// 为什么这条用例放在 twilight-web：SDK 包（twilight-blog-api）自己没有测试基座
// （无 vitest 依赖、无 test 脚本），而前端通过 vite alias 直引 SDK 的**源码**，
// 因此这里是唯一能真实执行 SDK 源码的 harness。它不是「测前端」，
// 而是借前端的测试环境去测 SDK 的真实运行时行为。
//
// 覆盖的缺口：既有的前端用例都把 client mock 掉了，断言的全是
// `updateArticle(id, { ..., content: '...' })` 这条**显式带值**路径
// （编辑器总会带上正文）。「调用方省略 content → 请求体里不含该字段」
// 这条分支此前只有类型层面的约束，没有任何运行时验证——而那正是
// R3-15 要堵的口子：无条件下发会把「只改标题」变成「静默清空正文」。
//
// 一个必须先说清的事实（决定本用例能反证什么、不能反证什么）：
// `JSON.stringify({ title: 't', content: undefined })` 的结果就是 `{"title":"t"}`，
// undefined 属性本来就会被丢。因此 SDK 里那句
// `if (input.content !== undefined) { json.content = input.content }`
// 在**线上格式上是冗余的**——单独删掉它，本用例依然会通过。
// 本用例钉住的不是那行守卫，而是它要保护的性质：真正的回归形态
// （`json.content = input.content ?? ''`，即把「省略」误当成「清空」）
// 会产出 `{"title":"t","content":""}`，本用例会精确失败。
import { describe, expect, it } from 'vitest'
import { createClient } from 'twilight-blog-api'

interface CapturedRequest {
  url: string
  method: string
  body: Record<string, unknown> | null
}

// 注入式 fetch：记录每个出站请求，回一个空的 2xx JSON。
// 通过 createClient 的 fetch 选项注入而非 vi.stubGlobal('fetch')，
// 是为了不依赖全局状态——用例之间不会互相污染，也不必在收尾时还原全局。
function makeCapturingFetch(): { fetch: typeof fetch; calls: CapturedRequest[] } {
  const calls: CapturedRequest[] = []
  const fetchFn = (async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const raw = init?.body
    calls.push({
      url: String(input),
      method: (init?.method ?? 'GET').toUpperCase(),
      body: typeof raw === 'string' ? (JSON.parse(raw) as Record<string, unknown>) : null,
    })
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
  return { fetch: fetchFn, calls }
}

const BASE_URL = 'https://blog.example.com'

describe('SDK 更新接口的正文边界（R3-15 的线上契约）', () => {
  it('updateArticle：省略 content 时请求体里不得出现该字段', async () => {
    const { fetch, calls } = makeCapturingFetch()
    const client = createClient({ baseUrl: BASE_URL, fetch })

    await client.updateArticle(7, { title: '只改标题' })

    expect(calls).toHaveLength(1)
    expect(calls[0].method).toBe('PUT')
    expect(calls[0].url).toBe(`${BASE_URL}/api/admin/articles/7`)
    // 最强的形状断言：整个请求体必须恰好只有 title。
    // 若有人把省略实现成 `content ?? ''`，这里会看到 {"title":...,"content":""} 而失败。
    expect(calls[0].body).toEqual({ title: '只改标题' })
    expect('content' in (calls[0].body as Record<string, unknown>)).toBe(false)
  })

  it('updateArticle：显式传空串必须原样下发（「清空正文」必须可表达）', async () => {
    const { fetch, calls } = makeCapturingFetch()
    const client = createClient({ baseUrl: BASE_URL, fetch })

    await client.updateArticle(7, { title: '清空正文', content: '' })

    // 契约的另一面：空串是「明确的清空意图」，不能被打扮成「不改」。
    // 若哪天有人把空串也当成省略，这里会失败——那是有意的信号：
    // 那等于让「清空正文」变成一个无法表达的操作。
    expect(calls[0].body).toEqual({ title: '清空正文', content: '' })
  })

  it('updateArticle：只改标签时不得顺手带上正文', async () => {
    const { fetch, calls } = makeCapturingFetch()
    const client = createClient({ baseUrl: BASE_URL, fetch })

    await client.updateArticle(7, { title: '旧标题', tags: ['Go', '安全'] })

    expect(calls[0].body).toEqual({ title: '旧标题', tags: ['Go', '安全'] })
    expect('content' in (calls[0].body as Record<string, unknown>)).toBe(false)
  })

  it('updatePage：省略 content 不发该字段，显式空串则照发', async () => {
    const { fetch, calls } = makeCapturingFetch()
    const client = createClient({ baseUrl: BASE_URL, fetch })

    await client.updatePage(3, { title: '只改标题' })
    await client.updatePage(3, { title: '清空', content: '' })

    expect(calls[0].url).toBe(`${BASE_URL}/api/admin/pages/3`)
    expect(calls[0].body).toEqual({ title: '只改标题' })
    expect(calls[1].body).toEqual({ title: '清空', content: '' })
  })
})
