// localize.test.ts —— 双语字段的逐项回退规则
//
// 这里守的是「英文缺失时绝不能丢内容」：整篇回退（而不是逐字段回退）会让
// 只译了标题的文章在英文界面下显示空白正文；对旧后端（响应里没有 *_en 键）
// 不做兜底则会让标题渲染成 undefined。

import { describe, expect, it } from 'vitest'
import type { Article, Page } from 'twilight-blog-api'
import {
  articleAIGenerated,
  articleContent,
  articleTags,
  articleTitle,
  pageContent,
  pageTitle,
  tagName,
} from '../lib/localize'

function article(overrides: Partial<Article> = {}): Article {
  return {
    id: 1,
    title: '中文标题',
    content: '中文正文',
    title_en: 'English title',
    content_en: 'English body',
    ai_generated: false,
    ai_generated_en: false,
    created_at: 1_700_000_000,
    updated_at: 1_700_000_000,
    cover_media_id: null,
    tags: ['Go', '前端'],
    tags_en: ['Go', 'Frontend'],
    ...overrides,
  }
}

describe('articleTitle', () => {
  it('中文界面一律取中文，即使英文存在', () => {
    expect(articleTitle(article(), 'zh')).toBe('中文标题')
  })

  it('英文界面取英文', () => {
    expect(articleTitle(article(), 'en')).toBe('English title')
  })

  it('英文为空串 / 只有空白 / undefined（旧后端）时回退中文', () => {
    expect(articleTitle(article({ title_en: '' }), 'en')).toBe('中文标题')
    expect(articleTitle(article({ title_en: '   ' }), 'en')).toBe('中文标题')

    const legacy = article()
    delete (legacy as Partial<Article>).title_en
    expect(articleTitle(legacy, 'en')).toBe('中文标题')
  })
})

describe('articleContent', () => {
  it('中文界面取中文正文', () => {
    expect(articleContent(article(), 'zh')).toBe('中文正文')
  })

  it('英文界面取英文正文，并原样保留缩进与首尾空白', () => {
    expect(articleContent(article({ content_en: '  # Heading\n\n    indented' }), 'en')).toBe('  # Heading\n\n    indented')
  })

  it('英文缺省时回退中文（只译了标题的文章不能显示空白正文）', () => {
    expect(articleContent(article({ title_en: 'Only the title', content_en: '' }), 'en')).toBe('中文正文')
    expect(articleContent(article({ content_en: '   ' }), 'en')).toBe('中文正文')
  })
})

describe('articleTags', () => {
  it('中文界面取中文标签', () => {
    expect(articleTags(article(), 'zh')).toEqual(['Go', '前端'])
  })

  it('英文界面逐项取英文名：缺译名的标签单独回退，不影响其他项', () => {
    expect(articleTags(article({ tags_en: ['Go', ''] }), 'en')).toEqual(['Go', '前端'])
    expect(articleTags(article({ tags_en: ['Go', '  '] }), 'en')).toEqual(['Go', '前端'])
  })

  it('tags_en 比 tags 短（服务端异常 / 旧数据）：缺的位置回退中文', () => {
    expect(articleTags(article({ tags: ['Go', '前端'], tags_en: ['Go'] }), 'en')).toEqual(['Go', '前端'])
  })

  it('旧后端没有 tags_en：整份回退中文', () => {
    const legacy = article()
    delete (legacy as Partial<Article>).tags_en
    expect(articleTags(legacy, 'en')).toEqual(['Go', '前端'])
  })

  it('没有标签时返回空数组（不是 undefined）', () => {
    expect(articleTags(article({ tags: [], tags_en: [] }), 'en')).toEqual([])
  })
})

describe('articleAIGenerated', () => {
  it('只有英文标记：英文界面显示声明，中文界面不显示', () => {
    const value = article({ ai_generated: false, ai_generated_en: true })
    expect(articleAIGenerated(value, 'en')).toBe(true)
    expect(articleAIGenerated(value, 'zh')).toBe(false)
  })

  it('只有中文标记：中文界面显示声明，英文界面不显示', () => {
    const value = article({ ai_generated: true, ai_generated_en: false })
    expect(articleAIGenerated(value, 'zh')).toBe(true)
    expect(articleAIGenerated(value, 'en')).toBe(false)
  })

  it('两种语言都标记时两边都显示', () => {
    const value = article({ ai_generated: true, ai_generated_en: true })
    expect(articleAIGenerated(value, 'zh')).toBe(true)
    expect(articleAIGenerated(value, 'en')).toBe(true)
  })

  it('两份都未标记时不显示声明', () => {
    const value = article({ ai_generated: false, ai_generated_en: false })
    expect(articleAIGenerated(value, 'zh')).toBe(false)
    expect(articleAIGenerated(value, 'en')).toBe(false)
  })

  it('旧后端没有 ai_generated_en 时英文界面回退中文标记', () => {
    const legacy = article({ ai_generated: true })
    delete (legacy as Partial<Article>).ai_generated_en
    expect(articleAIGenerated(legacy, 'zh')).toBe(true)
    expect(articleAIGenerated(legacy, 'en')).toBe(true)

    const clean = article({ ai_generated: false })
    delete (clean as Partial<Article>).ai_generated_en
    expect(articleAIGenerated(clean, 'en')).toBe(false)
  })
})

describe('tagName', () => {
  it('中文界面取中文名', () => {
    expect(tagName({ name: '前端', name_en: 'Frontend' }, 'zh')).toBe('前端')
  })

  it('英文界面取英文名', () => {
    expect(tagName({ name: '前端', name_en: 'Frontend' }, 'en')).toBe('Frontend')
  })

  it('英文名缺失 / 空白时回退中文', () => {
    expect(tagName({ name: '前端', name_en: '' }, 'en')).toBe('前端')
    expect(tagName({ name: '前端', name_en: '   ' }, 'en')).toBe('前端')
  })
})

describe('页面双语回退', () => {
  function page(overrides: Partial<Page> = {}): Page {
    return {
      id: 1,
      title: '关于',
      content: '# 中文正文',
      title_en: 'About',
      content_en: '# English body',
      created_at: 1_700_000_000,
      updated_at: 1_700_000_000,
      ...overrides,
    }
  }

  it('中文界面一律取中文，即使英文存在', () => {
    expect(pageTitle(page(), 'zh')).toBe('关于')
    expect(pageContent(page(), 'zh')).toBe('# 中文正文')
  })

  it('英文界面标题与正文各自取英文', () => {
    expect(pageTitle(page(), 'en')).toBe('About')
    expect(pageContent(page(), 'en')).toBe('# English body')
  })

  it('只翻了标题的页面：正文单独回退中文，不显示空内容', () => {
    const partial = page({ content_en: '' })
    expect(pageTitle(partial, 'en')).toBe('About')
    expect(pageContent(partial, 'en')).toBe('# 中文正文')
  })

  it('旧后端没有英文字段时整页回退中文', () => {
    const legacy = page()
    delete (legacy as Partial<Page>).title_en
    delete (legacy as Partial<Page>).content_en
    expect(pageTitle(legacy, 'en')).toBe('关于')
    expect(pageContent(legacy, 'en')).toBe('# 中文正文')
  })
})
