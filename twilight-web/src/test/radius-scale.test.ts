// @vitest-environment node
//
// radius-scale.test.ts —— 守护「站点统一圆角」的作用面与分页器间距
//
// 这三条都是**静默失效**型的视觉契约：写错了不会有任何构建 / 类型 / 运行时
// 报错，只是在浏览器里看起来不一致——而「看起来不一致」恰恰是最难被发现的
// 一类回归。所以断言打在 app.css 的文本上，node 环境读文件，与
// inkui-css-parts.test.ts 同一路数。
//
//   ① 分页器与上下内容保持 16px 间距；
//   ② 胶囊形按钮（「查看网页」、评论区的点赞/回复）跟随 --radius，
//      不再各自写死 99px / 999px；
//   ③ ink-design 内部写死圆角的目标元素，必须在站点侧覆盖回 --radius：
//      站点不改 ink-design 源码，这层覆盖一旦被删，那些元素会静默用回
//      InkUI 的固定值，与站长设置的圆角脱钩。

import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'

const APP_CSS = fileURLToPath(new URL('../styles/app.css', import.meta.url))

/**
 * 剥掉注释再断言。说明性注释里**必然**会出现被禁的写法（「不要写成 99px」），
 * 不剥就会让断言红在自己写的说明上；反过来，注释里恰好写全了也会让断言凭空
 * 变绿——两种假信号都出现过，所以剥注释是这类文本断言的前置条件。
 */
const css = readFileSync(APP_CSS, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')

/** 取以 `selector` 开头的那条规则（到第一个 `}`；站点样式没有嵌套规则）。 */
function rule(selector: string): string {
  const at = css.indexOf(selector)
  expect(at, `app.css 里找不到规则 ${selector}`).toBeGreaterThanOrEqual(0)
  return css.slice(at, css.indexOf('}', at))
}

describe('站点统一圆角：作用面与分页器间距', () => {
  it('分页器与上下内容保持 16px 间距', () => {
    const block = rule('.pagination {')
    expect(block).toMatch(/margin-top:\s*16px/)
    expect(block).toMatch(/margin-bottom:\s*16px/)
  })

  it('胶囊形按钮跟随 --radius，不再写死 99px / 999px', () => {
    expect(rule('.sbtn {')).toMatch(/border-radius:\s*var\(--radius\)/)
    expect(rule('.cmt .ops button {')).toMatch(/border-radius:\s*var\(--radius\)/)
  })

  it('ink-design 的目标元素在站点侧被覆盖回 --radius', () => {
    // 这些选择器列在同一条规则里；少一个，对应组件就悄悄用回 InkUI 的固定圆角。
    const overlay = css.match(/\.inp\.box[^{]*\{[^}]*\}/)?.[0]
    expect(overlay, '找不到覆盖 ink-design 目标元素的规则块').toBeTruthy()
    // 必须带 !important：Toolbar / Select / CopyButton 只在懒加载页面里用，它们的
    // 样式片段被拆进 Editor-*.css / MediaAdmin-*.css，由 __vitePreload 在路由切换时
    // 才插入 <link>，**晚于** main.css —— 同特异性下后加载者胜，没有 !important
    // 这条覆盖会被 InkUI 的原值（4px / 3px）盖回去，且构建与单测都不会报错。
    expect(overlay!).toMatch(/border-radius:\s*var\(--radius\)\s*!important/)
    for (const selector of ['.cpbtn', '.tbtn', '.modal-close', '.select-tag', '.anav a']) {
      expect(overlay!).toContain(selector)
    }
  })

  it('特殊元素保持自身形状，不被卷进统一圆角', () => {
    // 反向断言：这几条若也被改成 var(--radius)，设置页那句「头像、标签徽章与
    // 代码框不跟随」就成了假话。
    expect(rule('.utag {')).toMatch(/border-radius:\s*99px/)
    // 头像规则在选择器表里是跨行写的（`.avatar,\n.cav {`），锚在后一个上。
    expect(rule('.cav {')).toMatch(/border-radius:\s*50%/)
    expect(rule('.plain-body code {')).toMatch(/border-radius:\s*4px/)
    expect(rule('.plain-body .code-block .code-copy {')).toMatch(/border-radius:\s*5px/)
  })
})
