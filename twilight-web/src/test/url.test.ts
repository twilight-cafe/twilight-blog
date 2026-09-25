// url.test.ts —— toAbsoluteUrl 的全分支覆盖
//
// 断言刻意用 `window.location.origin` 作基准前缀而不是写死 `http://localhost:3000`：
// 被测的就是「以当前 origin 为基准拼接」，写死等于把 jsdom 的默认 url 也当成契约，
// 换测试环境就会假红。
import { describe, expect, it } from 'vitest'
import { toAbsoluteUrl } from '../lib/url'

const origin = window.location.origin

describe('toAbsoluteUrl', () => {
  it('站内相对路径（接口给媒体的真实形状）拼成绝对地址', () => {
    expect(toAbsoluteUrl('/api/media/12')).toBe(`${origin}/api/media/12`)
    // 顺带钉住「结果确实是绝对 URL」——上面那条在 origin 异常时可能假绿
    expect(toAbsoluteUrl('/api/media/12')).toMatch(/^https?:\/\/.+\/api\/media\/12$/)
  })

  it('已经是绝对 URL 的原样返回，不被二次拼接', () => {
    expect(toAbsoluteUrl('https://cdn.example.com/x.png')).toBe('https://cdn.example.com/x.png')
    // 协议相对地址按「继承当前协议」解析，也不该变成 origin + 原串
    expect(toAbsoluteUrl('//cdn.example.com/x.png')).toBe(`${window.location.protocol}//cdn.example.com/x.png`)
  })

  it('查询串与片段保留', () => {
    expect(toAbsoluteUrl('/api/media/12?dl=1#top')).toBe(`${origin}/api/media/12?dl=1#top`)
  })

  it('畸形输入不抛，原样返回（渲染期求值，抛会炸掉整个媒体库）', () => {
    expect(() => toAbsoluteUrl('http://[')).not.toThrow()
    expect(toAbsoluteUrl('http://[')).toBe('http://[')
  })

  it('空串退化为站点根（文档化这个边界，而非断言它是期望行为）', () => {
    expect(toAbsoluteUrl('')).toBe(`${origin}/`)
  })
})
