// markdown-math-toc.test.ts —— 含数学公式的标题：目录 id 必须与正文标题 id 一致
//
// markdown.test.ts 把 remark-math 换成了失败桩，用来测管线异常分支，因此那边
// 无法出现真实公式。这里加载真实管线，专门守住「目录点不动」这条回归：
// KaTeX 会把公式展开成 MathML 注解 + 可见字形两份文本，如果标题 id 在 KaTeX
// 之后生成，目录里的 slug 就永远对不上正文。

import { describe, expect, it } from 'vitest'
import { extractToc, renderMarkdown } from '../lib/markdown'

function headingId(html: string, level = 1): string | null {
  const doc = new DOMParser().parseFromString(html, 'text/html')
  return doc.querySelector(`h${level}`)?.id ?? null
}

describe('含数学公式的目录锚点', () => {
  it('行内公式：目录文本保留公式，id 与正文标题一致', () => {
    const markdown = '# 公式 $E=mc^2$ 测试\n\n正文'
    const [item] = extractToc(markdown)

    expect(item.text).toBe('公式 E=mc^2 测试')
    expect(item.id).toBe('公式-emc2-测试')
    expect(headingId(renderMarkdown(markdown))).toBe(item.id)
  })

  it('纯公式标题不退化：id 取 LaTeX 本身，而不是重复的 KaTeX 文本', () => {
    const markdown = '## $x^2$'
    const [item] = extractToc(markdown)

    expect(item.text).toBe('x^2')
    expect(item.id).toBe('x2')
    expect(headingId(renderMarkdown(markdown), 2)).toBe(item.id)
  })

  it('两个同名公式标题：目录与正文使用同一套去重序号', () => {
    const markdown = '# $x$\n\n# $x$'
    const toc = extractToc(markdown)
    const doc = new DOMParser().parseFromString(renderMarkdown(markdown), 'text/html')
    const ids = [...doc.querySelectorAll('h1')].map((heading) => heading.id)

    expect(toc.map((item) => item.id)).toEqual(['x', 'x-2'])
    expect(ids).toEqual(toc.map((item) => item.id))
  })
})
