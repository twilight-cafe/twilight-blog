// markdown.ts —— 文章的 Markdown 渲染与 XSS 防护
//
// 这个模块服务于两类内容，安全等级不同，因此走两套独立的 unified pipeline：
//   1) 文章 / 页面 / 动态（作者本人撰写，可信）—— 只做 URL scheme 守卫，
//      允许完整的 Markdown、GFM 表格、KaTeX 公式、代码高亮等富表现力。
//   2) 评论（访客提交，不可信）—— 在 URL 守卫之外，再叠一层 tag/attribute
//      白名单清洗（rehypeCommentSanitize），把脚本、事件处理器、远程资源等
//      全部剥离后再序列化。
//
// 关键不变量：remark-rehype 以 allowDangerousHtml:false 运行，即解析阶段就
// 不会把原始 HTML 透传下来；下面的白名单是「纵深防御」，只保留已知插件会产
// 出的标签/属性，把未知元素降级为「仅保留其孩子」。
//
// 注意（性能隐患）：articleProcessor / commentProcessor / tocProcessor 都是
// 模块级单例，但每次渲染都会同步 processSync 整段重跑，没有对 (markdown 字符串)
// 做 memo 缓存。评论列表较大或正文很长时，频繁重渲染可能有开销。
//
// tocProcessor 单独一条轻量 pipeline：只解析语法树、不跑 highlight/KaTeX，
// 单纯遍历 heading 抽取目录，开销明显小于正文渲染。

import { unified } from 'unified'
import remarkParse from 'remark-parse'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import remarkRehype from 'remark-rehype'
import rehypeKatex from 'rehype-katex'
import rehypeHighlight from 'rehype-highlight'
import rehypeHighlightCodeLines from 'rehype-highlight-code-lines'
import rehypeStringify from 'rehype-stringify'

// 这两个样式表被刻意**移出首屏包**（原来在 main.tsx 里静态引入）：KaTeX 的 CSS
// 连同 62 个字体文件、highlight.js 的代码主题，都只在这条管线真的渲染出内容时
// 才用得上。放在这里，Vite 会把它们拆成随本块一起加载的独立 CSS。
//
// 先后顺序变了也不会出事：app.css 里所有与代码块/公式相关的规则都写在
// `.plain-body` 之下（特异性更高），后加载的主题样式覆盖不了它们。
import 'katex/dist/katex.min.css'
import 'highlight.js/styles/github-dark.min.css'

import { escapeHtml } from './format'

export interface TocItem {
  id: string
  text: string
  level: number
}

export interface HastNode {
  type?: string
  tagName?: string
  properties?: Record<string, unknown>
  children?: unknown[]
  value?: unknown
}

/** 导出供测试探针直呼（与 sanitizeStyleValue 同一先例）：null/非对象输入在合法树里不可达。 */
export function nodeText(node: unknown): string {
  if (!node) return ''
  if (Array.isArray(node)) return node.map((child) => nodeText(child)).join('')
  if (typeof node !== 'object') return ''
  const value = node as { type?: string; value?: unknown; children?: unknown[] }
  if (
    value.type === 'text' ||
    value.type === 'inlineCode' ||
    // mdast 的数学节点：目录直接读这里。HAST 侧在 KaTeX 之前是 <code> 文本，
    // 两边都从同一段 LaTeX 取值，标题 id 才能与目录链接对得上。
    value.type === 'inlineMath' ||
    value.type === 'math'
  ) {
    return String(value.value ?? '')
  }
  if (value.children) return nodeText(value.children)
  return ''
}

function slugifyHeading(value: string): string {
  const slug = (value || '')
    .trim()
    .toLowerCase()
    .replace(/<[^>]*>/g, '')
    .replace(/[^\p{L}\p{N}\s_-]/gu, '')
    .trim()
    .replace(/[\s_]+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^-+|-+$/g, '')

  return slug || 'section'
}

function makeHeadingId(text: string, used: Set<string>): string {
  const base = slugifyHeading(text)
  let id = base
  let index = 1
  while (used.has(id)) {
    index += 1
    id = `${base}-${index}`
  }
  used.add(id)
  return id
}

// 导出供测试探针直呼（与 sanitizeStyleValue 同一先例）：visit 的空/非对象守卫
// 在合法 markdown 树里不可达。生产代码不经此导出。
export function rehypeHeadingIds() {
  return (tree: { type?: string; children?: unknown[] }) => {
    const used = new Set<string>()

    const visit = (node: unknown): void => {
      if (!node || typeof node !== 'object') return
      const current = node as { type?: string; tagName?: string; properties?: Record<string, unknown>; children?: unknown[] }
      if (current.type === 'element' && /^h[1-6]$/.test(current.tagName ?? '')) {
        const text = nodeText(node).trim()
        current.properties = {
          ...(current.properties || {}),
          id: makeHeadingId(text, used),
        }
      }
      if (current.children) {
        for (const child of current.children) visit(child)
      }
    }

    visit(tree)
  }
}

/* --------------------------------------------------------------------------- *
 * URL scheme guard
 *
 * Markdown link/image destinations are attacker controlled in comments and can
 * be `javascript:`, `vbscript:` or `data:` URLs. Browsers execute those from
 * `href`, so every `href`/`src` whose scheme is not http, https, mailto or a
 * relative/anchor reference is removed. The element itself is kept (so its text
 * survives) minus the dangerous attribute.
 * --------------------------------------------------------------------------- */

const SAFE_URL_SCHEMES = new Set(['http', 'https', 'mailto'])

/**
 * Characters browsers strip while parsing a URL. `java\nscript:alert(1)` is
 * executed as `javascript:alert(1)`, so they have to be removed before the
 * scheme is inspected.
 */
const URL_IGNORED_CHARS = /[\u0000-\u0020\u007f]/g

/**
 * 导出仅为让测试探针能直接验证它（与 sanitizeStyleValue 同一先例）：
 * URL 守卫的多数分支在合法 markdown 里不可达，测试需用手搓 hast 树直呼。
 * 生产代码只通过 renderMarkdown / renderCommentMarkdown 间接触达它。
 */
export function isSafeUrl(value: unknown): boolean {
  if (typeof value !== 'string') return false

  const url = value.replace(URL_IGNORED_CHARS, '')

  // An empty destination resolves against the current document: no scheme.
  if (!url) return true
  // Anchor, query and root/relative references.
  if (/^[#/?]/.test(url)) return true
  if (url.startsWith('./') || url.startsWith('../')) return true

  const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(url)
  // No scheme at all: a relative reference.
  if (!scheme) return true

  return SAFE_URL_SCHEMES.has(scheme[1].toLowerCase())
}

function forEachElement(node: unknown, visit: (element: HastNode) => void): void {
  if (!node || typeof node !== 'object') return
  const current = node as HastNode
  if (current.type === 'element') visit(current)
  if (Array.isArray(current.children)) {
    for (const child of current.children) forEachElement(child, visit)
  }
}

function rehypeUrlGuard() {
  return (tree: HastNode) => {
    forEachElement(tree, (element) => {
      const properties = element.properties
      if (!properties) return
      for (const name of ['href', 'src']) {
        if (name in properties && !isSafeUrl(properties[name])) delete properties[name]
      }
    })
  }
}

// 导出供测试探针直呼（与 sanitizeStyleValue 同一先例）：URL 守卫与评论白名单
// 是纵深防御，多数分支在合法 markdown 的公开管线里不可达。生产代码不经这些导出。
export { rehypeUrlGuard }

/* --------------------------------------------------------------------------- *
 * Comment sanitizer
 *
 * The pipeline never emits raw HTML (remark-rehype runs with
 * `allowDangerousHtml: false`), so this allowlist is defence in depth: it keeps
 * exactly what the legitimately configured plugins produce (markdown, GFM,
 * KaTeX, highlight.js, heading ids, code line numbers) and drops everything
 * else.
 * --------------------------------------------------------------------------- */

const COMMENT_ALLOWED_TAGS = new Set([
  // Markdown / GFM output.
  'p', 'br', 'hr',
  'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
  'ul', 'ol', 'li', 'blockquote', 'pre', 'code', 'span',
  'a', 'img', 'strong', 'em', 'del', 'sup', 'sub',
  'table', 'thead', 'tbody', 'tr', 'th', 'td',
  'section', 'div',
  // GFM task list checkboxes.
  'input',
  // KaTeX MathML + the matching HTML fallback markup.
  'math', 'semantics', 'annotation', 'annotation-xml',
  'mrow', 'mi', 'mo', 'mn', 'mtext', 'mstyle', 'mspace',
  'msup', 'msub', 'msubsup', 'mfrac', 'msqrt', 'mroot',
  'mover', 'munder', 'munderover', 'mtable', 'mtr', 'mtd', 'mlabeledtr',
  'mpadded', 'mphantom', 'menclose', 'mmultiscripts', 'mprescripts', 'none',
  'ms', 'merror',
])

/** Elements that are removed together with their children. */
const COMMENT_DROPPED_TAGS = new Set([
  'script', 'style', 'iframe', 'object', 'embed', 'applet', 'frame', 'frameset',
  'form', 'link', 'meta', 'base', 'svg', 'use', 'template', 'noscript',
  'head', 'body', 'html', 'title',
])

const COMMENT_GLOBAL_ATTRIBUTES = new Set([
  'class', 'id', 'href', 'src', 'alt', 'title', 'span',
  'data-line-number', 'aria-hidden', 'aria-label', 'aria-describedby',
])

/**
 * Elements that may carry an inline `style`.
 *
 * KaTeX is the only legitimate producer of inline styles in this pipeline: raw
 * HTML is disabled (`allowDangerousHtml: false`), markdown itself has no
 * syntax for attributes, and no other plugin emits one. Probing KaTeX across
 * its constructs shows the attribute only ever appears on these three tags
 * (`span` for the HTML fallback markup, `mstyle`/`mpadded` for the MathML side).
 *
 * Keeping the allowance scoped to them, instead of listing `style` among the
 * global attributes, means a future plugin cannot inherit a blanket licence to
 * write arbitrary CSS into a comment.
 */
const COMMENT_STYLE_TAGS = new Set(['span', 'mstyle', 'mpadded'])

/** Attributes that are only valid on one specific element. */
const COMMENT_TAG_ATTRIBUTES: Record<string, readonly string[]> = {
  input: ['type', 'checked', 'disabled'],
  ol: ['start'],
  table: ['align'],
  th: ['align'],
  td: ['align'],
  a: ['data-footnote-ref', 'data-footnote-backref'],
  section: ['data-footnotes'],
}

/** MathML presentation attributes; inert, but KaTeX relies on them. */
const MATHML_ATTRIBUTES = new Set([
  'xmlns', 'encoding', 'display', 'mathvariant', 'mathcolor', 'mathbackground',
  'mathsize', 'mathfamily', 'mathweight', 'mathstyle', 'displaystyle',
  'scriptlevel', 'scriptminsize', 'scriptsizemultiplier', 'linethickness',
  'columnalign', 'rowalign', 'columnspan', 'rowspan', 'columnlines', 'rowlines',
  'columnspacing', 'rowspacing', 'frame', 'framespacing', 'equalrows',
  'equalcolumns', 'denomalign', 'numalign', 'side', 'width', 'height', 'depth',
  'lspace', 'rspace', 'stretchy', 'symmetric', 'movablelimits', 'accent',
  'accentunder', 'largeop', 'minsize', 'maxsize', 'separator', 'notation',
  'actiontype', 'selection', 'alttext', 'close', 'open', 'separators', 'form',
  'intent', 'arg',
])

const MATHML_TAGS = new Set([
  'math', 'semantics', 'annotation', 'annotation-xml',
  'mrow', 'mi', 'mo', 'mn', 'mtext', 'mstyle', 'mspace',
  'msup', 'msub', 'msubsup', 'mfrac', 'msqrt', 'mroot',
  'mover', 'munder', 'munderover', 'mtable', 'mtr', 'mtd', 'mlabeledtr',
  'mpadded', 'mphantom', 'menclose', 'mmultiscripts', 'mprescripts', 'none',
  'ms', 'merror',
])

/** Slug and footnote ids: no quotes, spaces or angle brackets. */
const SAFE_ID = /^[\p{L}\p{N}][\p{L}\p{N}_.:-]{0,127}$/u

/** hast camelCase property name -> serialized HTML attribute name. */
function toHtmlAttributeName(name: string): string {
  if (name === 'className') return 'class'
  if (name === 'htmlFor') return 'for'
  return name.replace(/[A-Z]/g, (character) => `-${character.toLowerCase()}`)
}

function isAllowedCommentAttribute(tag: string, name: string): boolean {
  // Event handlers never survive, whatever their spelling.
  if (name.length > 64) return false
  if (name.toLowerCase().startsWith('on')) return false

  const attribute = toHtmlAttributeName(name)
  // 无需再对转换后的属性名做一次 on- 前缀检查：toHtmlAttributeName 只会在大写
  // 前插入「-」并转小写，不可能凭空造出一个 on- 前缀；而 name 的大小写变体
  // 已被上一行的大小写不敏感检查穷尽。该检查曾是防御性冗余，因不可达而被移除。
  if (attribute === 'style') return COMMENT_STYLE_TAGS.has(tag)
  if (COMMENT_GLOBAL_ATTRIBUTES.has(attribute)) return true
  if (MATHML_TAGS.has(tag) && MATHML_ATTRIBUTES.has(attribute)) return true

  const extra = COMMENT_TAG_ATTRIBUTES[tag]
  return Boolean(extra && extra.includes(attribute))
}

/**
 * CSS 转义序列。浏览器用 CSS tokenizer 解析样式值，`\75 rl(x)` 与 `url(x)` 是
 * **同一个东西**，而正则只看得到字面量——所以必须先解码、再判定。
 *
 * 两种形式（CSS Syntax §4.3.7）：
 *   - `\` + 1~6 位十六进制 + 一个可选的空白终止符 → 对应的码点；
 *   - `\` + 任意其他字符 → 该字符本身（转义成字面量，例如 `\)` 是 `)`）。
 */
const CSS_ESCAPE = /\\(?:([0-9a-fA-F]{1,6})[ \t\n]?|(.))/g

/**
 * 把样式值里的 CSS 转义序列解码成它真正代表的字符。
 *
 * 返回 null 表示遇到了无法安全归一化的输入，调用方必须 fail closed：
 * 解码出 NUL 或代理区码点、或解码后仍残留反斜杠（悬挂的转义）。
 *
 * 对合法内容这个函数是**恒等变换**——KaTeX 从不产出反斜杠，所以它不会有任何误伤；
 * 它存在的唯一意义是让下面那几条正则看到一个浏览器会看到的字符串。
 */
function decodeCssEscapes(value: string): string | null {
  let invalid = false
  const decoded = value.replace(CSS_ESCAPE, (_match, hex: string | undefined, literal: string | undefined) => {
    // 正则两个捕获组是「二选一」：hex 未命中时 literal 必命中，二者不会同时为 undefined。
    if (hex === undefined) return literal as string
    const codePoint = Number.parseInt(hex, 16)
    if (codePoint === 0 || (codePoint >= 0xd800 && codePoint <= 0xdfff) || codePoint > 0x10ffff) {
      invalid = true
      return ''
    }
    return String.fromCodePoint(codePoint)
  })
  if (invalid || decoded.includes('\\')) return null
  return decoded
}

/**
 * KaTeX sizes its fallback markup with inline styles, and CSP already allows
 * inline styles, so `style` is kept — on [`COMMENT_STYLE_TAGS`] only.
 *
 * The value must not be able to pull in a remote resource, execute anything, or
 * lift the element out of the content flow:
 *
 * - `url(...)`, `@import`, `expression(...)`, `javascript:`, `behavior:`,
 *   `-moz-binding` are dropped (remote fetches leak the reader's IP; the IE- and
 *   legacy-Gecko-era ones execute);
 * - a `position` other than `relative` is dropped. KaTeX only ever emits
 *   `position:relative`, while `fixed`/`absolute`/`sticky` combined with `top`
 *   and `left` — which comments *can* influence through things like
 *   `\rule`/`\raisebox` — are what turn a styled span into a full-page overlay;
 * - `content:` is dropped because it can inject text into the page.
 *
 * **先解码转义再跑这些正则**是承重的一步：少了它，`\75 rl(...)`、
 * `\70 osition:fixed`、`\63 ontent:` 全都能原样通过（实测确认），而浏览器
 * 会把它们当成 `url(...)`/`position:fixed`/`content:` 执行。
 *
 * 已知的残留：黑名单终究是黑名单，它挡不住「未来某个 KaTeX 版本或新增插件
 * 产出了这里没想到的属性」。真正彻底的两种做法见 README：让评论里的数学只走
 * MathML 输出（那样就不需要 `style` 了），或者把清洗搬到渲染发生的那一侧。
 *
 * 导出仅为让 Node 探针能直接验证它（见 `.pentest/`）——生产代码只通过
 * renderCommentMarkdown 间接触达它。
 */
export function sanitizeStyleValue(value: unknown): string | null {
  if (typeof value !== 'string') return null
  const cleaned = value.replace(/[\u0000-\u001f]/g, '')
  const normalized = decodeCssEscapes(cleaned)
  if (normalized === null) return null
  if (/url\s*\(|expression\s*\(|@import|javascript:|behavior\s*:|-moz-binding\s*:/i.test(normalized)) return null
  if (/position\s*:\s*(?!relative\b)/i.test(normalized)) return null
  if (/(^|;|\s)content\s*:/i.test(normalized)) return null
  return normalized
}

function sanitizeCommentChildren(parent: HastNode): void {
  if (!Array.isArray(parent.children)) return

  const kept: unknown[] = []
  for (const child of parent.children) {
    if (!child || typeof child !== 'object') continue
    const element = child as HastNode

    if (element.type === 'raw' || element.type === 'comment' || element.type === 'doctype') continue

    if (element.type !== 'element') {
      kept.push(child)
      continue
    }

    const tag = String(element.tagName ?? '').toLowerCase()
    element.tagName = tag

    if (!COMMENT_ALLOWED_TAGS.has(tag)) {
      if (COMMENT_DROPPED_TAGS.has(tag)) continue
      // Unknown but harmless element: keep its (sanitized) children only.
      sanitizeCommentChildren(element)
      if (Array.isArray(element.children)) kept.push(...element.children)
      continue
    }

    const properties = element.properties
    if (properties) {
      for (const name of Object.keys(properties)) {
        if (!isAllowedCommentAttribute(tag, name)) {
          delete properties[name]
          continue
        }
        if (name === 'href' || name === 'src') {
          if (!isSafeUrl(properties[name])) delete properties[name]
          continue
        }
        if (name === 'style' || name === 'styleValue') {
          const style = sanitizeStyleValue(properties[name])
          if (style === null) delete properties[name]
          else properties[name] = style
          continue
        }
        if (name === 'id' && !SAFE_ID.test(String(properties[name] ?? ''))) delete properties[name]
      }

      if (tag === 'input' && String(properties.type ?? '').toLowerCase() !== 'checkbox') {
        continue
      }
    }

    sanitizeCommentChildren(element)
    kept.push(element)
  }

  parent.children = kept
}

function rehypeCommentSanitize() {
  return (tree: HastNode) => {
    sanitizeCommentChildren(tree)
  }
}

export { rehypeCommentSanitize }

/* --------------------------------------------------------------------------- *
 * Processors
 * --------------------------------------------------------------------------- */

function createProcessor() {
  return unified()
    .use(remarkParse)
    .use(remarkGfm)
    .use(remarkMath)
    .use(remarkRehype)
    // 标题 id 必须在 KaTeX 之前算。KaTeX 会把一个公式展开成 MathML 注解 +
    // 可见字形两份文本，heading id 会变成 "emc2emc2emc2" 这类重复串；而目录
    // 从 mdast 只拿到一份 LaTeX，两边 slug 不一致，点目录就找不到目标。
    .use(rehypeHeadingIds)
    .use(rehypeKatex)
    // detect:false 表示不靠启发式猜测语言，只认 ```lang 围栏上显式标注的语言；
    // plainText 把 text/txt 当作纯文本、不做高亮，避免误把大段文本当代码上色。
    .use(rehypeHighlight, { detect: false, plainText: ['text', 'txt'] })
    // 代码块逐行加 data-line-number 属性（已列入 COMMENT_GLOBAL_ATTRIBUTES 白名单），
    // 供前端做行号展示；评论清洗时若该属性被剥掉则行号会丢失。
    .use(rehypeHighlightCodeLines, { showLineNumbers: true })
}

// Site-owner authored content: trusted, so only the URL scheme guard applies.
const articleProcessor = createProcessor()
  .use(rehypeUrlGuard)
  .use(rehypeStringify)

// Comments: untrusted, so the URL scheme guard plus a full allowlist applies.
const commentProcessor = createProcessor()
  .use(rehypeUrlGuard)
  .use(rehypeCommentSanitize)
  .use(rehypeStringify)

const tocProcessor = unified()
  .use(remarkParse)
  .use(remarkGfm)
  .use(remarkMath)

/**
 * Renders trusted (site-owner authored) article/page/moment bodies. Markdown,
 * GFM, math and highlighting are kept; only unsafe URL schemes are stripped.
 */
export function renderMarkdown(markdown: string): string {
  try {
    const file = articleProcessor.processSync({ value: markdown })
    return String(file)
  } catch {
    return `<p>${escapeHtml(markdown)}</p>`
  }
}

/**
 * Renders untrusted comment bodies: the same pipeline with a full tag and
 * attribute allowlist on top of the URL scheme guard.
 */
export function renderCommentMarkdown(markdown: string): string {
  try {
    const file = commentProcessor.processSync({ value: markdown })
    return String(file)
  } catch {
    return `<p>${escapeHtml(markdown)}</p>`
  }
}

/**
 * 导出供测试探针直呼（与 nodeText 同一先例）：visit 的空/非对象守卫在合法
 * mdast 里不可达，只有手搓畸形树才触得到。生产代码只经 extractToc 间接调用。
 */
export function collectToc(tree: unknown): TocItem[] {
  const used = new Set<string>()
  const items: TocItem[] = []

  const visit = (node: unknown): void => {
    if (!node || typeof node !== 'object') return
    const current = node as { type?: string; depth?: number; children?: unknown[] }
    if (current.type === 'heading') {
      const text = nodeText(node).trim()
      items.push({
        id: makeHeadingId(text, used),
        text,
        level: Number(current.depth) || 1,
      })
    }
    if (current.children) {
      for (const child of current.children) visit(child)
    }
  }

  visit(tree)
  return items
}

export function extractToc(markdown: string): TocItem[] {
  try {
    return collectToc(tocProcessor.runSync(tocProcessor.parse(markdown)))
  } catch {
    return []
  }
}
