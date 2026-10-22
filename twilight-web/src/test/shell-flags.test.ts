// @vitest-environment node
//
// shell-flags.test.ts —— 守护「站点功能开关必须走 CSP 允许的通道」这条跨端契约
//
// 背景是一次真实事故：搜索开关最初由外壳里的一段内联脚本下发——
//
//   <script>window.__SITE__ = { searchEnabled: ... };</script>
//
// 而站点的 CSP 是 `script-src 'self'`，既没有 'unsafe-inline'、也没有
// hash/nonce（见 internal/middleware/security.go）。**浏览器直接拒绝执行**
// 那段脚本，于是开关在线上从未生效过：关闭搜索时前端读不到 false，走
// 「属性缺失=全开」兜底照样渲染出搜索入口，点进去 404。
//
// 更糟的是当时所有测试都是绿的——它们断言的是「HTML 文本里有这个字符串」，
// 而那句话对「浏览器肯不肯执行」没有任何约束。所以本文件的断言一律打在
// **通道**上：属性通道与 CSP 的相容性，以及前后端是不是在读同一个键。
//
// 现在开关走 <html data-search-enabled="...">。这个通道在 HTML 解析时即就绪
// （仍早于入口 chunk），且完全不必放宽 CSP。
//
// 本文件只读文件系统，用 node 环境（jsdom 既不参与也不必要）。

import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'

const WEB_ROOT = fileURLToPath(new URL('../../', import.meta.url))
const REPO_ROOT = join(WEB_ROOT, '..')
const SHELL = join(REPO_ROOT, 'twilight-blog/internal/view/template/index.html')
const SECURITY_GO = join(REPO_ROOT, 'twilight-blog/internal/middleware/security.go')
const FLAGS_TS = join(WEB_ROOT, 'src/lib/flags.ts')
const SDK_SITE_TS = join(REPO_ROOT, 'twilight-blog-api/src/endpoints/site.ts')

/** 浏览器**不执行**、CSP 也不约束的 <script type>。 */
const DATA_BLOCK_TYPES = new Set([
  'application/json',
  'application/ld+json',
  'text/template',
  'text/x-template',
])

/** 读取标签属性串里的某个属性值（去掉包裹的引号）。 */
function attr(attrs: string, name: string): string | undefined {
  const match = new RegExp(`\\b${name}\\s*=\\s*("[^"]*"|'[^']*'|[^\\s>]+)`, 'i').exec(attrs)
  return match?.[1].replace(/^["']|["']$/g, '')
}

/** 外壳里所有会被浏览器执行的**内联** <script>（带 src 的是外部文件，不算）。 */
function executableInlineScripts(html: string): string[] {
  const found: string[] = []
  for (const match of html.matchAll(/<script\b([^>]*)>/gi)) {
    const attrs = match[1]
    if (attr(attrs, 'src') !== undefined) continue
    if (DATA_BLOCK_TYPES.has((attr(attrs, 'type') ?? '').toLowerCase())) continue
    found.push(`<script${attrs}>`)
  }
  return found
}

/**
 * 剥掉 HTML 注释后再扫描。
 *
 * 不是洁癖，两个理由：
 *   ① html/template 渲染时**会丢弃 HTML 注释**，所以模板源文件里的注释到不了
 *      浏览器——测试读的是源文件，必须补上这一步才与真实产物等价（Go 侧的
 *      TestIndexShellHasNoInlineScript 测的是渲染结果，天然不含注释）；
 *   ② 本文件与模板的说明里都**故意**引用了那段被删掉的内联脚本作为事故记录，
 *      不剥注释的话，那些引用会把它自己的测试判红。
 */
function stripComments(html: string): string {
  return html.replace(/<!--[\s\S]*?-->/g, '')
}

/**
 * 剥掉 JS/TS 源码里的注释后再扫描。
 *
 * 与上面剥 HTML 注释同源，但理由更硬：本文件有一组断言是在**源码文本里找
 * 片段**，而说明那些断言的注释本身就会引用被禁的写法（「不要写成
 * `json: { ...input }`」这种话不写出来就没人知道禁的是什么）。不剥注释，
 * 注释就会替代码回答断言——既能凭空把测试**判红**（正则在注释里先命中），
 * 也能凭空把测试**判绿**（注释里写了正确写法，而代码里没写）。
 * 两种都是「测试不再测试它声称测试的东西」。
 *
 * 只处理行注释与块注释；字符串字面量里的 `//`（例如 URL）确实会被误伤，
 * 但这里扫的片段都在 json 字面量内，没有 URL，故不引入完整的词法扫描。
 */
function stripTsComments(code: string): string {
  return code.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ \t]*\/\/.*$/gm, '')
}

/** 外壳模板的正文（已剥注释，等价于渲染结果里的那一部分）。 */
const SHELL_HTML = stripComments(readFileSync(SHELL, 'utf8'))

describe('外壳开关通道：不得依赖内联脚本（CSP 会拒绝执行）', () => {
  const html = SHELL_HTML

  it('外壳里没有可执行的内联 <script>', () => {
    // 事故现场就是这一段。它被拒绝执行时服务端毫发无损：响应照发、200、
    // 测试照绿——只有浏览器控制台里有一行 CSP 报错。
    expect(executableInlineScripts(html)).toEqual([])
  })

  it('入口脚本仍以外部文件形式引入（说明上一条不是因为完全没有脚本）', () => {
    expect(html).toMatch(/<script type="module" src=/)
  })

  it('开关是 <html> 上的属性，且带运行期占位符', () => {
    const htmlTag = /<html\b([^>]*)>/i.exec(html)?.[1] ?? ''
    // 占位符必须原样在：Go 的 index.go 与 vite.config.ts 的 dev 替换都按它匹配，
    // 改名会让两侧同时填不进去（而 dev 侧会直接把 {{...}} 字面量发到页面上）。
    expect(htmlTag).toContain('data-search-enabled="{{.SearchEnabled}}"')
  })

  it('首页每页数量也是 <html> 上的属性，且带运行期占位符', () => {
    const htmlTag = /<html\b([^>]*)>/i.exec(html)?.[1] ?? ''
    // 与搜索开关同一条通道，理由见 flags.ts 的 homePageSize：首页列表在挂载后
    // 立刻要发请求，等 /api/site 回来才用正确 pageSize 就晚了。
    expect(htmlTag).toContain('data-home-page-size="{{.HomePageSize}}"')
  })

  it('站点圆角也是 <html> 上的属性，且带运行期占位符', () => {
    const htmlTag = /<html\b([^>]*)>/i.exec(html)?.[1] ?? ''
    // 同一条通道；理由见 flags.ts 的 borderRadius：等 /api/site 回来再改
    // --radius 会让首屏先按默认圆角画一遍、拿到响应后再跳变。
    expect(htmlTag).toContain('data-border-radius="{{.BorderRadius}}"')
  })
})

describe('外壳开关通道：dev server 必须把每个占位符都替换掉', () => {
  const VITE_CONFIG = join(WEB_ROOT, 'vite.config.ts')
  // 剥注释后扫描。**这一步不是洁癖**：vite.config.ts 里那些解释「漏填占位符会
  // 怎样」的注释本身就引用了 `{{.HomePageSize}}` / `{{.BorderRadius}}`。不剥的话，
  // 注释会替代码回答断言——既能把一条正确的实现判红（第一版就是这样：
  // Description/Title/SearchEnabled 三个明明填了也报缺失），也能在有人删掉
  // 替换之后凭空把测试判绿。那正是本文件开头记着的那类「测试不再测试它
  // 声称测试的东西」。
  const config = stripTsComments(readFileSync(VITE_CONFIG, 'utf8'))

  /** 外壳模板里出现的所有 `{{.Xxx}}` 动作名（去重）。 */
  function templateActions(): string[] {
    const names = new Set<string>()
    for (const match of SHELL_HTML.matchAll(/\{\{\s*\.([A-Za-z0-9_]+)\s*\}\}/g)) names.add(match[1])
    return [...names].sort()
  }

  /**
   * dev 侧真正执行了替换的动作名。
   *
   * 只认 `.replace(<正则>...)` 的形态，且正则里必须出现 `\{\{\.Xxx\}\}`。
   * 为什么不用 `config.includes('{{.Xxx}}')`：那串字面量在配置里到处都是
   * （插件读的是同一份外壳），任何一处提及都算数——等于没测。
   */
  function replacedActions(): string[] {
    const names = new Set<string>()
    for (const match of config.matchAll(/\.replace\(\s*\/[^/]*?\\\{\\\{\\?\.([A-Za-z0-9_]+)/g)) {
      names.add(match[1])
    }
    return [...names].sort()
  }

  it('模板里的每个 {{.Xxx}} 都在 dev server 的替换清单里', () => {
    // 这一条守的是一次**真实踩到的漏改**：外壳新增 data-home-page-size 与
    // data-border-radius 之后，vite.config.ts 的 indexShellPlugin 没跟着加替换，
    // 于是 dev server 把 `{{.HomePageSize}}` / `{{.BorderRadius}}` **字面量**
    // 原样发到页面上。
    //
    // 症状极具欺骗性：前端读到的属性值是那串花括号 → Number.parseInt 得 NaN →
    // 静默回落到兜底值。表现只是「开发环境里改这两项设置没反应」，而
    // tsc / 单测 / 构建**全部照绿**。上面那三条「模板里有占位符」的断言也照样过——
    // 它们只证明模板写了，不证明有人填。
    const done = replacedActions()
    const missing = templateActions().filter((name) => !done.includes(name))
    expect(
      missing,
      `vite.config.ts 的 dev 替换清单缺少: ${missing.join(', ')}（已填: ${done.join(', ')}）`,
    ).toEqual([])
  })

  it('模板里确实有占位符（否则上一条会因为集合为空而空过）', () => {
    // 兜底：正则写错或模板被改得没有动作时，上一条会「全过」——那是最危险的假绿。
    const actions = templateActions()
    expect(actions.length).toBeGreaterThan(0)
    expect(actions).toContain('SearchEnabled')
    // 另一半同样重要：抽取侧（replacedActions）也要有产出。若它的正则失效，
    // 上一条会因为「done 是空集」而把每个动作都报成缺失（红得莫名其妙），
    // 或反过来在模板也空时静默通过。
    expect(replacedActions().length).toBeGreaterThan(0)
  })
})

describe('外壳开关通道：前后端必须读同一个键', () => {
  it('<html> 上的 data-* 能推出 flags.ts 读的 dataset 键', () => {
    const htmlTag = /<html\b([^>]*)>/i.exec(SHELL_HTML)?.[1] ?? ''
    // data-search-enabled → dataset.searchEnabled，与 DOM 的 dataset 命名规则一致。
    const declaredKeys = [...htmlTag.matchAll(/\bdata-([a-z0-9-]+)\s*=/gi)].map((match) =>
      match[1].toLowerCase().replace(/-([a-z0-9])/g, (_, ch: string) => ch.toUpperCase()),
    )
    expect(declaredKeys).toContain('searchEnabled')
    expect(declaredKeys).toContain('homePageSize')
    expect(declaredKeys).toContain('borderRadius')

    const flags = readFileSync(FLAGS_TS, 'utf8')
    // 只改一边就是静默失效：外壳发了、前端没读，或前端读了、外壳没发。
    // 兜底是「全开」，所以症状会在**关闭**搜索时才出现（入口还在、点进去 404）。
    expect(flags).toContain('dataset.searchEnabled')
    // 必须按字符串比较：dataset 的值是 string，而 Boolean('false') === true——
    // 取布尔值的话「关闭」就永远关不上。
    expect(flags).toContain("!== 'false'")

    // 数字型参数（首页每页数量）：读的是同一个 dataset 键，但**不能**用
    // Number() 直接转——属性缺失时 dataset 是 undefined，Number(undefined)
    // 是 NaN（还好），可若写成 Number(raw ?? '') 就是 0，会被当成合法值发出去。
    expect(flags).toContain('dataset.homePageSize')
    expect(flags).toContain('Number.parseInt')
    expect(flags).toContain('Number.isFinite')

    // 圆角同样是数字型参数，但**下界是 0**（直角是合法值）。
    // 若照抄 homePageSize 的 `parsed < 1`，站长把圆角设成 0 后前台会永远
    // 按兜底的 4px 渲染——设置页显示 0、页面却是圆的。
    expect(flags).toContain('dataset.borderRadius')
    expect(flags).toContain('MIN_BORDER_RADIUS')
    expect(flags).toContain('applyBorderRadius')
  })
})

describe('外壳开关通道：CSP 不得为内联脚本开口子', () => {
  it("script-src 保持 'self'，且不含 unsafe-inline / hash / nonce", () => {
    const source = readFileSync(SECURITY_GO, 'utf8')
    const directive = /"script-src([^"]*)"/.exec(source)?.[1] ?? ''
    expect(directive).toContain("'self'")
    // 上面「外壳不许有内联脚本」的前提就是这一条。真要放宽 CSP，请连同那条
    // 断言与本文件的说明一起改——不要只改一边，那会让两者互相矛盾而无人察觉。
    expect(directive).not.toContain('unsafe-inline')
    expect(directive).not.toMatch(/nonce-|sha(256|384|512)-/)
  })
})

/**
 * 站点设置的**下发完整性**：四个字段必须一个不少地进请求体。
 *
 * 这一组守的是一次真实事故，而且它的表现方式很值得记下来：后台改「首页每页文章数」
 * 时请求体里**根本没有** home_page_size，但请求是 200、提示是「站点设置已更新」、
 * 回读还是老值。站长看到的是「保存成功、值又变回去了」，控制台里没有任何报错。
 *
 * 根因在封装层：`updateSite` 组装请求体时只写了 description 与
 * code_block_max_lines 两项。类型是对的（SiteSettingsInput 声明了全部四个字段）、
 * 调用方是对的（SiteSettings.tsx 确实把 pageSize 传了进来）、服务端也是对的
 * （Go 端点接受并持久化它）——断的就是中间这一行。
 *
 * 所以断言打在**源码文本**上而不是行为上：用 mock 或真实模块跑行为都测不到这一层
 * 的缺失（mock 会把封装整个换掉；而真实模块那条路已经由
 * article-cover.test.tsx 的「SDK 下发的站点设置请求体形状」覆盖）。
 * 这里补的是**静态**那一面——它不依赖任何运行时条件，改坏了立刻红。
 */
describe('站点设置下发完整性：每个字段一个都不能少', () => {
  const source = readFileSync(SDK_SITE_TS, 'utf8')
  // 剥注释之后再抽 json 块。不剥的话，上面那段解释「为什么不能写
  // `json: { ...input }`」的注释会先被正则命中，于是断言拿到的是注释文本
  // 而不是代码——测试红了，但红在自己写的说明上，与代码对不对无关。
  const code = stripTsComments(source)

  /** 取 path 为 /api/admin/site 的那次 request 的 json 字面量。 */
  function jsonBlock(): string {
    return /path:\s*['"]\/api\/admin\/site['"][\s\S]*?json:\s*\{([\s\S]*?)\}/.exec(code)?.[1] ?? ''
  }

  it('updateSite 的请求体逐个列出全部字段', () => {
    const body = jsonBlock()
    expect(body, '找不到 /api/admin/site 的 json 请求体').not.toBe('')

    // 每个都必须显式出现。这就是事故现场：少了两个，而没有任何一层会报错。
    for (const key of ['description', 'code_block_max_lines', 'home_page_size', 'border_radius', 'post_banner_height', 'moments_enabled', 'comment_notify_to']) {
      expect(body, `请求体缺少 ${key}`).toContain(key)
    }
  })

  it('不得用展开运算整体透传（省略与显式传值是两种语义）', () => {
    const body = jsonBlock()
    expect(body, '找不到 /api/admin/site 的 json 请求体').not.toBe('')
    // `{ ...input }` 看着更「完整」，实际更危险：它把契约里「调用方没提这件事」
    // 与「调用方明确要求这个值」压成了同一件事，将来调用方多带一个键就会被
    // 悄悄透传。逐个列出才是对的写法。
    expect(body).not.toMatch(/\.\.\.\s*input/)
  })

  it('取到的是实现而不是注释（否则上面两条实际上什么也没测）', () => {
    // 这条是**元断言**：它守的是上面两条的有效性。
    // 若哪天正则又匹配到注释（或匹配到空串），上面两条会以「缺字段」的形式
    // 失败，看起来像代码坏了；反过来若注释里正好写全了四个字段名，上面两条
    // 又会凭空变绿。所以先证明抽出来的是 json 字面量本身。
    const body = jsonBlock()
    // ① 必须含真实的属性取值（注释里引用的写法是 `...input`，不含 `input.` 前缀）。
    expect(body).toContain('input.description')
    // ② 不能带注释标记。stripTsComments 失效（或它漏了某种注释形态）时，
    //    抽到的块会残留 `//` 或 `/*`；这两样在真实的 json 字面量里不存在。
    expect(body).not.toContain('//')
    expect(body).not.toContain('/*')
    // ③ 行数与形态：每个键各占一行，整体是「多行对象」而不是被压平的片段。
    expect(body.trim().split('\n').length).toBe(7)
  })

  it('SDK 声明的字段与请求体里下发的字段对得上', () => {
    // 类型声明在 types.ts，这里只确认源码里没有「声明了却没下发」的字段。
    // （反过来「下发了没声明」由 tsc 兜住——访问不存在的属性会直接编译失败。）
    const declared = [...source.matchAll(/input\.([a-z_]+)/g)].map((m) => m[1])
    expect(declared).toContain('home_page_size')
    expect(declared).toContain('border_radius')
  })
})
