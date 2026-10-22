// @vitest-environment node
//
// media-name-limit.test.ts —— 守护「媒体展示名长度上限」这个前后端镜像常量
//
// 同一个上限出现在两处：
//   - 后端 model.MaxMediaOriginalNameLength —— 权威规则（写库前判定）；
//   - 前端 MediaAdmin 的 MEDIA_NAME_MAX —— 只是为了让用户别白跑一次注定 400 的请求。
//
// 两边漂移不会有任何报错：症状是「界面放行、服务端 400」或者反过来「界面提前拦下
// 服务端本会接受的名字」，两者都只在真机上被发现。这类静默失效正是
// shell-flags.test.ts 守着的那一类，所以这里用同一种办法：读源码文本、把数字对撞。
//
// 只读文件系统，用 node 环境（jsdom 既不参与也不必要）。

import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'

const GO_MODEL = fileURLToPath(new URL('../../../twilight-blog/internal/model/media.go', import.meta.url))
const MEDIA_ADMIN = fileURLToPath(new URL('../pages/admin/MediaAdmin.tsx', import.meta.url))

/** 从源码里取出上限数字；取不到就抛（正则写错时不能静默变成 0）。 */
function limitIn(source: string, pattern: RegExp, label: string): number {
  const match = pattern.exec(source)
  if (!match) throw new Error(`${label} 里找不到上限常量（正则是 ${pattern}）`)
  return Number(match[1])
}

const goLimit = limitIn(readFileSync(GO_MODEL, 'utf8'), /MaxMediaOriginalNameLength\s*=\s*(\d+)/, 'model/media.go')
const tsLimit = limitIn(readFileSync(MEDIA_ADMIN, 'utf8'), /MEDIA_NAME_MAX\s*=\s*(\d+)/, 'MediaAdmin.tsx')

describe('媒体展示名长度上限：前后端镜像', () => {
  it('两个常量存在且相等', () => {
    expect(tsLimit).toBe(goLimit)
  })

  it('上限是一个可用的正数（防止某次改动把它写成 0 而两边一起「相等」）', () => {
    expect(Number.isInteger(goLimit)).toBe(true)
    expect(goLimit).toBeGreaterThan(0)
  })
})
