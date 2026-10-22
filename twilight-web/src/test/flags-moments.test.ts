// flags-moments.test.ts —— 外壳属性通道的两项新参数
//
// 两条规则与既有的 search / homePageSize / borderRadius 完全相同：
//   ① 属性缺失时按「功能全开 / 默认值」兜底（前端先升级、后端后升级不能让功能消失）；
//   ② dataset 的值是字符串，必须显式与 'false' 比较；数字先用 parseInt 再判 NaN。
// 这里逐条守住，避免「关闭状态被当成启用」或「空串被 Number('') 变成 0」。

import { describe, expect, it } from 'vitest'

import {
  FALLBACK_POST_BANNER_HEIGHT,
  applyPostBannerHeight,
  isMomentsEnabled,
  postBannerHeight,
} from '../lib/flags'

function shell(attributes: Record<string, string>): void {
  const root = document.documentElement
  for (const key of Object.keys(root.dataset)) delete root.dataset[key]
  for (const [key, value] of Object.entries(attributes)) root.dataset[key] = value
}

describe('isMomentsEnabled', () => {
  it('属性缺失时视为启用（老二进制没有这个属性）', () => {
    shell({})
    expect(isMomentsEnabled()).toBe(true)
  })

  it('显式 true / 其它值都算启用', () => {
    shell({ momentsEnabled: 'true' })
    expect(isMomentsEnabled()).toBe(true)
    shell({ momentsEnabled: '1' })
    expect(isMomentsEnabled()).toBe(true)
  })

  it('只有字符串 "false" 才算关闭（Boolean("false") 是 true，不能直接用）', () => {
    shell({ momentsEnabled: 'false' })
    expect(isMomentsEnabled()).toBe(false)
  })
})

describe('postBannerHeight', () => {
  it('属性缺失 / 空串 / 非数字时回退 400', () => {
    shell({})
    expect(postBannerHeight()).toBe(FALLBACK_POST_BANNER_HEIGHT)
    shell({ postBannerHeight: '' })
    expect(postBannerHeight()).toBe(FALLBACK_POST_BANNER_HEIGHT)
    shell({ postBannerHeight: 'tall' })
    expect(postBannerHeight()).toBe(FALLBACK_POST_BANNER_HEIGHT)
  })

  it('越界值（含 0 与负数）回退 400', () => {
    shell({ postBannerHeight: '0' })
    expect(postBannerHeight()).toBe(FALLBACK_POST_BANNER_HEIGHT)
    shell({ postBannerHeight: '119' })
    expect(postBannerHeight()).toBe(FALLBACK_POST_BANNER_HEIGHT)
    shell({ postBannerHeight: '801' })
    expect(postBannerHeight()).toBe(FALLBACK_POST_BANNER_HEIGHT)
  })

  it('区间端点与中间值原样采用', () => {
    shell({ postBannerHeight: '120' })
    expect(postBannerHeight()).toBe(120)
    shell({ postBannerHeight: '400' })
    expect(postBannerHeight()).toBe(400)
    shell({ postBannerHeight: '800' })
    expect(postBannerHeight()).toBe(800)
  })

  it('applyPostBannerHeight 把值写进 --post-banner-height', () => {
    shell({ postBannerHeight: '260' })
    applyPostBannerHeight()
    expect(document.documentElement.style.getPropertyValue('--post-banner-height')).toBe('260px')

    shell({})
    applyPostBannerHeight()
    expect(document.documentElement.style.getPropertyValue('--post-banner-height')).toBe('400px')
  })
})
