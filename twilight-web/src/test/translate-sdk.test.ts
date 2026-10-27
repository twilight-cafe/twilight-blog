// translate-sdk.test.ts —— AI 翻译三个端点的请求形状
//
// 这些断言打在 SDK 的封装层上：路径、方法与请求体都是后端契约的一部分，
// 而 mock 掉整个 lib/client 的界面测试看不到这一层。

import { describe, expect, it, vi } from 'vitest'
import { createClient } from 'twilight-blog-api'

function makeApi(response: unknown = {}) {
  const fetchFn = vi.fn(async () =>
    new Response(JSON.stringify(response), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }),
  )
  const client = createClient({
    baseUrl: 'https://blog.test',
    fetch: fetchFn as unknown as typeof fetch,
  })
  return { client, fetchFn }
}

function call(fetchFn: ReturnType<typeof vi.fn>, index = 0) {
  const [url, init] = fetchFn.mock.calls[index] as [string, RequestInit]
  return { url, init }
}

describe('translateArticle', () => {
  it('POST /api/admin/translate，只发送四个契约字段', async () => {
    const { client, fetchFn } = makeApi({ title: 'Hello', content: 'Body' })
    const result = await client.translateArticle({
      title: '标题',
      content: '正文',
      from: 'zh',
      to: 'en',
    })
    expect(result).toEqual({ title: 'Hello', content: 'Body' })

    const { url, init } = call(fetchFn)
    expect(url).toBe('https://blog.test/api/admin/translate')
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({
      title: '标题',
      content: '正文',
      from: 'zh',
      to: 'en',
    })
  })

  it('英文翻中文同样可用', async () => {
    const { client, fetchFn } = makeApi({ title: '你好', content: '' })
    await client.translateArticle({ title: 'Hello', content: '', from: 'en', to: 'zh' })
    expect(JSON.parse(String(call(fetchFn).init.body))).toEqual({
      title: 'Hello',
      content: '',
      from: 'en',
      to: 'zh',
    })
  })
})

describe('translate config', () => {
  it('读取脱敏配置走 GET /api/admin/translate/config', async () => {
    const config = {
      enabled: true,
      url: 'https://api.example.com/v1',
      model: 'gpt-test',
      timeout_seconds: 60,
      max_tokens: 4096,
      api_key_configured: true,
      configured: true,
    }
    const { client, fetchFn } = makeApi(config)
    await expect(client.getTranslateConfig()).resolves.toEqual(config)
    const { url, init } = call(fetchFn)
    expect(url).toBe('https://blog.test/api/admin/translate/config')
    expect(init.method).toBe('GET')
  })

  it('更新配置走 PUT；省略字段不出现在请求体里', async () => {
    const { client, fetchFn } = makeApi({})
    await client.updateTranslateConfig({ model: 'gpt-new' })
    const { url, init } = call(fetchFn)
    expect(url).toBe('https://blog.test/api/admin/translate/config')
    expect(init.method).toBe('PUT')
    expect(JSON.parse(String(init.body))).toEqual({ model: 'gpt-new' })
  })

  it('提供密钥与数值时逐个下发（不能被悄悄丢掉）', async () => {
    const { client, fetchFn } = makeApi({})
    await client.updateTranslateConfig({
      enabled: false,
      url: 'https://api.example.com/v2',
      model: 'gpt-new',
      api_key: 'sk-test',
      timeout_seconds: 120,
      max_tokens: 8192,
      thinking_effort: 'high',
    })
    expect(JSON.parse(String(call(fetchFn).init.body))).toEqual({
      enabled: false,
      url: 'https://api.example.com/v2',
      model: 'gpt-new',
      api_key: 'sk-test',
      timeout_seconds: 120,
      max_tokens: 8192,
      thinking_effort: 'high',
    })
  })
})
