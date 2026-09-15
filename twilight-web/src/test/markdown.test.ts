// markdown.test.ts —— 渲染管线 / URL 守卫 / 评论白名单 / TOC
//
// remark-math 被替换成「遇到标记文本就抛错」的桩：三条公开入口
// （renderMarkdown / renderCommentMarkdown / extractToc）的 catch 分支在合法
// 输入下不可达，借它注入管线失败。其余测试避免使用该标记。
import { describe, expect, it, vi } from 'vitest'

vi.mock('remark-math', () => ({
  default: () => (tree: unknown) => {
    const visit = (node: unknown): void => {
      if (!node || typeof node !== 'object') return
      const current = node as { type?: string; value?: unknown; children?: unknown[] }
      if (current.type === 'text' && String(current.value ?? '').includes('FORCE_PIPELINE_FAIL')) {
        throw new Error('mock pipeline failure')
      }
      if (current.children) {
        for (const child of current.children) visit(child)
      }
    }
    visit(tree)
  },
}))

import {
  extractToc,
  isSafeUrl,
  nodeText,
  renderCommentMarkdown,
  renderMarkdown,
  rehypeCommentSanitize,
  rehypeHeadingIds,
  rehypeUrlGuard,
  sanitizeStyleValue,
  type HastNode,
} from '../lib/markdown'

function el(tagName: string, properties?: Record<string, unknown>, children: unknown[] = []): HastNode {
  return { type: 'element', tagName, properties, children }
}

describe('renderMarkdown（文章管线）', () => {
  it('标题生成 slug 化 id；重复标题追加序号', () => {
    const html = renderMarkdown('# Hello World\n\n## Hello World')
    expect(html).toContain('<h1 id="hello-world">')
    expect(html).toContain('<h2 id="hello-world-2">')
  })

  it('中文标题、行内代码、任务列表、表格', () => {
    const html = renderMarkdown('# 你好 世界\n\n`code`\n\n- [ ] 待办\n\n| a | b |\n| - | - |\n| 1 | 2 |')
    expect(html).toContain('<h1 id="你好-世界">')
    expect(html).toContain('<code>code</code>')
    expect(html).toContain('type="checkbox"')
    expect(html).toContain('<table>')
  })

  it('代码围栏带语言与行号属性', () => {
    const html = renderMarkdown('```ts\nconst a = 1\n```')
    expect(html).toContain('language-ts')
    expect(html).toContain('data-line-number')
  })

  it('URL 守卫：危险协议被剥掉、元素与文本保留', () => {
    const html = renderMarkdown('[x](javascript:alert(1)) [y](https://example.com) [z](/rel) [m](mailto:a@b.c) [e]()')
    expect(html).not.toContain('javascript:')
    expect(html).toContain('href="https://example.com"')
    expect(html).toContain('href="/rel"')
    expect(html).toContain('href="mailto:a@b.c"')
    expect(html).toContain('<a>x</a>')
  })

  it('图片 data: 源被剥掉', () => {
    const html = renderMarkdown('![alt](data:text/html;base64,AAAA)')
    expect(html).not.toContain('data:')
    expect(html).toContain('<img alt="alt">')
  })

  it('管线抛错时回退到转义后的纯段落', () => {
    expect(renderMarkdown('FORCE_PIPELINE_FAIL <b>&"\'')).toBe('<p>FORCE_PIPELINE_FAIL &lt;b&gt;&amp;&quot;&#39;</p>')
  })
})

describe('renderCommentMarkdown（评论管线）', () => {
  it('常规 markdown 渲染，行内 HTML 不透传', () => {
    const html = renderCommentMarkdown('**bold** <script>alert(1)</script>')
    expect(html).toContain('<strong>bold</strong>')
    expect(html).not.toContain('<script>')
  })

  it('管线抛错时回退到转义后的纯段落', () => {
    expect(renderCommentMarkdown('FORCE_PIPELINE_FAIL')).toBe('<p>FORCE_PIPELINE_FAIL</p>')
  })
})

describe('extractToc', () => {
  it('抽取层级、去重 slug、清理标点', () => {
    const toc = extractToc('# A B\n\n## A B\n\n### !@#\n\n#### a <b> c\n\n##### a--b\n\n## -x-\n\n####### 深度七')
    expect(toc.map((item) => [item.id, item.level])).toEqual([
      ['a-b', 1],
      ['a-b-2', 2],
      ['section', 3],
      ['a-c', 4],
      ['a-b-3', 5],
      ['x', 2],
    ])
  })

  it('空内容与管线失败返回空数组', () => {
    expect(extractToc('')).toEqual([])
    expect(extractToc('FORCE_PIPELINE_FAIL')).toEqual([])
  })
})

describe('nodeText（探针）', () => {
  it('null / 标量 / 文本 / 行内码 / 递归 / 未知', () => {
    expect(nodeText(null)).toBe('')
    expect(nodeText('str')).toBe('')
    expect(nodeText(42)).toBe('')
    expect(nodeText({ type: 'text', value: 'v' })).toBe('v')
    expect(nodeText({ type: 'inlineCode', value: 'c' })).toBe('c')
    expect(nodeText({ type: 'element', children: [{ type: 'text', value: 'a' }, { type: 'text', value: 'b' }] })).toBe('ab')
    expect(nodeText([null, { type: 'text', value: 'x' }, 'skip'])).toBe('x')
    expect(nodeText({ type: 'unknown' })).toBe('')
  })
})

describe('isSafeUrl（探针）', () => {
  it('非字符串一律不安全', () => {
    expect(isSafeUrl(42)).toBe(false)
    expect(isSafeUrl(null)).toBe(false)
  })

  it('空与相对引用安全', () => {
    expect(isSafeUrl('')).toBe(true)
    expect(isSafeUrl('#a')).toBe(true)
    expect(isSafeUrl('?q=1')).toBe(true)
    expect(isSafeUrl('/root')).toBe(true)
    expect(isSafeUrl('./a')).toBe(true)
    expect(isSafeUrl('../a')).toBe(true)
    expect(isSafeUrl('plain')).toBe(true)
  })

  it('白名单 scheme 大小写不敏感', () => {
    expect(isSafeUrl('HTTP://a')).toBe(true)
    expect(isSafeUrl('mailto:a@b')).toBe(true)
    expect(isSafeUrl('JavaScript:alert(1)')).toBe(false)
    expect(isSafeUrl('vbscript:x')).toBe(false)
    expect(isSafeUrl('data:text/html,x')).toBe(false)
  })

  it('浏览器会忽略的字符先剥掉再判 scheme', () => {
    expect(isSafeUrl('java\nscript:alert(1)')).toBe(false)
    expect(isSafeUrl('java\u0000script:alert(1)')).toBe(false)
    expect(isSafeUrl('jav\u007fascript:alert(1)')).toBe(false)
  })
})

describe('rehypeUrlGuard（探针）', () => {
  it('剥掉不安全 href/src，保留元素；非对象子节点跳过', () => {
    const guard = rehypeUrlGuard()
    const tree: HastNode = {
      type: 'root',
      children: [
        null,
        'str',
        el('a', { href: 'javascript:x' }, [{ type: 'text', value: 'keep' }]),
        el('img', { src: 'data:x' }),
        el('a', { href: 'https://ok' }),
        el('a', { href: 42 }),
        el('br'),
      ],
    }
    guard(tree)
    const elements = (tree.children as HastNode[]).filter((node) => node?.type === 'element')
    expect((elements[0].properties as Record<string, unknown>).href).toBeUndefined()
    expect(elements[0].children).toEqual([{ type: 'text', value: 'keep' }])
    expect((elements[1].properties as Record<string, unknown>).src).toBeUndefined()
    expect((elements[2].properties as Record<string, unknown>).href).toBe('https://ok')
    expect((elements[3].properties as Record<string, unknown>).href).toBeUndefined()
    expect(elements[4].properties).toBeUndefined()
  })

  it('无 children 的树安全通过', () => {
    const guard = rehypeUrlGuard()
    expect(() => guard({ type: 'root' })).not.toThrow()
  })
})

describe('rehypeHeadingIds（探针）', () => {
  it('标题加 id，非标题不动，非对象子节点跳过', () => {
    const plugin = rehypeHeadingIds()
    const tree: HastNode = {
      type: 'root',
      children: [
        null,
        'str',
        el('h1', undefined, [{ type: 'text', value: 'T' }]),
        el('h7', undefined, [{ type: 'text', value: 'skip' }]),
        el('p', undefined, [{ type: 'text', value: 'plain' }]),
        el('h2', { class: 'pre' }),
      ],
    }
    plugin(tree)
    const elements = (tree.children as HastNode[]).filter((node) => node?.type === 'element')
    expect((elements[0].properties as Record<string, unknown>).id).toBe('t')
    expect(elements[1].properties).toBeUndefined()
    expect(elements[2].properties).toBeUndefined()
    expect((elements[3].properties as Record<string, unknown>).class).toBe('pre')
  })

  it('无 children 的树安全通过', () => {
    const plugin = rehypeHeadingIds()
    expect(() => plugin({ type: 'root' })).not.toThrow()
  })
})

describe('sanitizeStyleValue（探针）', () => {
  it('非字符串返回 null', () => {
    expect(sanitizeStyleValue(42)).toBeNull()
    expect(sanitizeStyleValue(undefined)).toBeNull()
  })

  it('CSS 转义先解码再判定：\\75 rl() 等价 url()', () => {
    expect(sanitizeStyleValue('\\75 rl(x)')).toBeNull()
    expect(sanitizeStyleValue('\\70 osition:fixed')).toBeNull()
    expect(sanitizeStyleValue('\\63 ontent:x')).toBeNull()
  })

  it('解码出 NUL / 代理区 / 悬挂反斜杠时 fail closed', () => {
    expect(sanitizeStyleValue('\\0')).toBeNull()
    expect(sanitizeStyleValue('\\D800')).toBeNull()
    expect(sanitizeStyleValue('\\FFFFFF')).toBeNull()
    expect(sanitizeStyleValue('\\')).toBeNull()
  })

  it('字面转义还原成字符；合法 hex 正常解码', () => {
    expect(sanitizeStyleValue('\\(a\\)')).toBe('(a)')
    expect(sanitizeStyleValue('\\69 talic')).toBe('italic')
  })

  it('黑名单与 position/content 规则', () => {
    expect(sanitizeStyleValue('background:url(x)')).toBeNull()
    expect(sanitizeStyleValue('width:expression(alert(1))')).toBeNull()
    expect(sanitizeStyleValue('@import "x"')).toBeNull()
    expect(sanitizeStyleValue('background:javascript:alert(1)')).toBeNull()
    expect(sanitizeStyleValue('behavior:url(x)')).toBeNull()
    expect(sanitizeStyleValue('-moz-binding:url(x)')).toBeNull()
    expect(sanitizeStyleValue('position:fixed')).toBeNull()
    expect(sanitizeStyleValue('POSITION : ABSOLUTE')).toBeNull()
    expect(sanitizeStyleValue('position:relative')).toBe('position:relative')
    expect(sanitizeStyleValue(';content:"x"')).toBeNull()
    expect(sanitizeStyleValue('content :x')).toBeNull()
    expect(sanitizeStyleValue('color:red')).toBe('color:red')
  })

  it('控制字符在解码前剥掉', () => {
    expect(sanitizeStyleValue('color:\u0000red')).toBe('color:red')
  })
})

describe('rehypeCommentSanitize（探针，白名单纵深防御）', () => {
  const sanitize = rehypeCommentSanitize()

  function run(children: unknown[]): HastNode[] {
    const tree: HastNode = { type: 'root', children }
    sanitize(tree)
    return tree.children as HastNode[]
  }

  it('raw / comment / doctype 与非对象子节点被移除或保留', () => {
    const kept = run([
      null,
      'plain text is dropped',
      { type: 'text', value: 'plain' },
      { type: 'raw', value: '<script>' },
      { type: 'comment', value: 'x' },
      { type: 'doctype' },
      el('p', undefined, [{ type: 'text', value: 'ok' }]),
    ])
    expect(kept).toEqual([{ type: 'text', value: 'plain' }, el('p', undefined, [{ type: 'text', value: 'ok' }])])
  })

  it('丢弃名单整体移除；未知标签降级为保留清洗后的孩子', () => {
    const kept = run([
      el('script', undefined, [{ type: 'text', value: 'evil' }]),
      el('svg', undefined, [{ type: 'text', value: 'gone' }]),
      el('custom-el', { dangerous: 1 }, [el('strong', undefined, [{ type: 'text', value: 'keep' }])]),
      el('custom2', { dangerous: 1 }),
    ])
    expect(kept).toEqual([el('strong', undefined, [{ type: 'text', value: 'keep' }])])
  })

  it('标签名统一小写后再判白名单', () => {
    const kept = run([el('SCRIPT', undefined, [])])
    expect(kept).toEqual([])
  })

  it('URL 属性：危险剥掉、安全保留', () => {
    const kept = run([
      el('a', { href: 'javascript:alert(1)', title: 't' }),
      el('a', { href: 'https://ok' }),
      el('img', { src: 'data:x' }),
    ])
    expect(kept[0].properties).toEqual({ title: 't' })
    expect(kept[1].properties).toEqual({ href: 'https://ok' })
    expect(kept[2].properties).toEqual({})
  })

  it('style 只在白名单标签上保留，且值要过清洗', () => {
    const kept = run([
      el('span', { style: 'color:red' }),
      el('span', { style: 'url(x)' }),
      el('div', { style: 'color:red' }),
      el('span', { styleValue: 'position:fixed' }),
    ])
    expect(kept[0].properties).toEqual({ style: 'color:red' })
    expect(kept[1].properties).toEqual({})
    expect(kept[2].properties).toEqual({})
    expect(kept[3].properties).toEqual({})
  })

  it('id 必须匹配 SAFE_ID；class 保留；for 被剥掉', () => {
    const kept = run([
      el('p', { id: 'bad id' }),
      el('p', { id: 'good_1:ok' }),
      el('p', { className: ['x'], htmlFor: 'y' }),
    ])
    expect(kept[0].properties).toEqual({})
    expect(kept[1].properties).toEqual({ id: 'good_1:ok' })
    expect(kept[2].properties).toEqual({ className: ['x'] })
  })

  it('超长属性名与事件处理器被剥掉', () => {
    const kept = run([
      el('p', { ['x'.repeat(65)]: 1 }),
      el('p', { ONCLICK: 'x' }),
      el('p', { onclick: 'x' }),
    ])
    expect(kept.map((node) => node.properties)).toEqual([{}, {}, {}])
  })

  it('MathML 属性只在 MathML 标签上保留；aria-* 走 kebab 映射', () => {
    const kept = run([
      el('mrow', { mathvariant: 'bold', width: '10px' }),
      el('mrow', { className: ['x'] }),
      el('div', { mathvariant: 'bold' }),
      el('p', { ariaHidden: 'y' }),
    ])
    expect(kept[0].properties).toEqual({ mathvariant: 'bold', width: '10px' })
    expect(kept[1].properties).toEqual({ className: ['x'] })
    expect(kept[2].properties).toEqual({})
    expect(kept[3].properties).toEqual({ ariaHidden: 'y' })
  })

  it('input 只保留 checkbox；无属性的 input 也保留', () => {
    const kept = run([
      el('input', { type: 'text' }),
      el('input', { type: 'CHECKBOX', checked: true, disabled: true, 'data-x': 1 }),
      el('input'),
    ])
    expect(kept).toHaveLength(2)
    expect(kept[0].properties).toEqual({ type: 'CHECKBOX', checked: true, disabled: true })
  })

  it('标签专属属性按白名单；递归清洗孩子', () => {
    const kept = run([
      el('ol', { start: '3' }),
      el('ol', { align: 'x' }),
      el('a', { 'data-footnote-ref': 'x' }),
      el('section', { 'data-footnotes': 'x' }),
      el('p', undefined, [el('custom', { onX: 1 }, [{ type: 'text', value: 'deep' }])]),
    ])
    expect(kept[0].properties).toEqual({ start: '3' })
    expect(kept[1].properties).toEqual({})
    expect(kept[2].properties).toEqual({ 'data-footnote-ref': 'x' })
    expect(kept[3].properties).toEqual({ 'data-footnotes': 'x' })
    // 深层未知标签的孩子在递归里被清洗后原位保留
    expect(kept[4].children).toEqual([{ type: 'text', value: 'deep' }])
  })

  it('children 缺失的树安全通过', () => {
    expect(() => sanitize({ type: 'root' })).not.toThrow()
    expect(() => sanitize({ type: 'root', children: 'x' as unknown as HastNode[] })).not.toThrow()
  })
})
