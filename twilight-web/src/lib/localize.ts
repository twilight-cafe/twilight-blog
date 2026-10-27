// localize.ts —— 按当前语言挑文章 / 标签的展示文本
//
// 双语数据在库里是「两列并存」而不是「一行一个语言」：中文是主列（NOT NULL），
// 英文是附加列（默认空串）。因此所有读取都必须遵守同一条回退规则：
//
//   英文界面 + 英文列非空 → 用英文
//   其余情况（中文界面、英文为空、旧后端根本没有这个字段）→ 用中文
//
// 为什么回退要逐项判断而不是「整篇有英文就用英文」：一篇文章可能只译了标题，
// 正文还没翻；标签可能只有一部分有译名。整篇切换会让读者看到「英文标题 +
// 空白正文」——比不翻译更糟。逐字段 / 逐标签回退是唯一不会丢内容的做法。
//
// 旧后端兼容：升级前端但没升级二进制时，响应里没有 title_en / tags_en 这些键，
// 读出来是 undefined。这里把它们与空串同等对待（回退中文），而不是让
// `<h1>` 渲染出 undefined。

import type { Article, Page, Tag } from 'twilight-blog-api'
import type { Locale } from './i18n'

/**
 * 文章里参与本地化的字段。
 *
 * 刻意用结构化子集而不是 Article 整体：搜索结果（ArticleSearchResult）与
 * 后台列表项都是 Article 的超集，传进来不该要求调用方先做类型断言。
 */
export type LocalizableArticle = Pick<
  Article,
  'title' | 'content' | 'title_en' | 'content_en' | 'tags' | 'tags_en' | 'ai_generated' | 'ai_generated_en'
>

/** 能取英文名的标签。name_en 在旧后端上缺席，所以是可选读取。 */
export type LocalizableTag = Pick<Tag, 'name'> & { name_en?: string }

/**
 * 挑一个值：英文非「空白」才用英文，否则回退中文。
 *
 * 判空用 trim：站长在英文框里敲了一个空格不等于翻译了；返回的是**原值**，
 * 不是 trim 后的值——正文里的首行缩进与代码块缩进必须原样保留。
 */
function pickEnglish(english: string | undefined, fallback: string): string {
  if (english === undefined || english.trim() === '') return fallback
  return english
}

/** 文章标题：英文缺失（或只有空白）时回退中文标题。 */
export function articleTitle(article: LocalizableArticle, locale: Locale): string {
  if (locale !== 'en') return article.title
  return pickEnglish(article.title_en, article.title)
}

/** 文章正文：规则与标题一致。 */
export function articleContent(article: LocalizableArticle, locale: Locale): string {
  if (locale !== 'en') return article.content
  return pickEnglish(article.content_en, article.content)
}

/**
 * 文章标签：逐项回退。
 *
 * tags_en 与 tags 由服务端按同一顺序装配（见 model.loadTagsForArticle），
 * 因此按下标对齐是安全的。旧后端没有 tags_en，整段回退到中文标签。
 */
export function articleTags(article: LocalizableArticle, locale: Locale): string[] {
  if (locale !== 'en') return article.tags
  const english = article.tags_en ?? []
  return article.tags.map((tag, index) => pickEnglish(english[index], tag))
}

/** 判断「是否 AI 生成」只需要这两个字段，与标题正文无关。 */
export type AIGeneratedArticle = Pick<Article, 'ai_generated' | 'ai_generated_en'>

/**
 * 文章是否带 AI 生成声明 —— **按语言**判断。
 *
 * 库里中英文各存一份标记，前台只读当前语言那一份：英文版是 AI 产物时，
 * 只有英文读者看到声明；中文仍由人撰写，就不该替它认领一个 AI 标记。
 *
 * 旧后端没有 ai_generated_en（读出来是 undefined）时，英文界面回退到中文标记，
 * 与升级前的行为一致；反之中文界面永远只看 ai_generated。
 */
export function articleAIGenerated(article: AIGeneratedArticle, locale: Locale): boolean {
  if (locale !== 'en') return article.ai_generated === true
  return (article.ai_generated_en ?? article.ai_generated) === true
}

/** 标签云 / 标签页标题里的标签名。 */
export function tagName(tag: LocalizableTag, locale: Locale): string {
  if (locale !== 'en') return tag.name
  return pickEnglish(tag.name_en, tag.name)
}

/**
 * 独立页面里参与本地化的字段。
 *
 * 与文章不同，页面只有标题和正文两份文本；旧后端没有 *_en 键，因此读成可选。
 */
export type LocalizablePage = Pick<Page, 'title' | 'content'> & {
  title_en?: string
  content_en?: string
}

/** 页面标题：英文缺失（或只有空白）时回退中文。 */
export function pageTitle(page: LocalizablePage, locale: Locale): string {
  if (locale !== 'en') return page.title
  return pickEnglish(page.title_en, page.title)
}

/** 页面正文：规则与标题一致。 */
export function pageContent(page: LocalizablePage, locale: Locale): string {
  if (locale !== 'en') return page.content
  return pickEnglish(page.content_en, page.content)
}
