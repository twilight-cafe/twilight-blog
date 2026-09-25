// head-seo.test.tsx —— 页面级 head：纯函数、DOM 对账、各页接入
//
// 三个层次各守一件事：
//   1. 纯函数（描述摘要、JSON-LD 序列化）——分享卡片与结构化数据的**内容**正确；
//   2. applyHead 的对账——「这一页没声明的标签必须被回收」，否则从文章页回首页
//      会把 og:type=article 与 BlogPosting 一起带过去（这是最容易漏的一条）；
//   3. 页面接入——canonical 指向自身、首页 WebSite、搜索/后台 noindex。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

import {
  applyHead,
  articleJsonLd,
  articlePageHead,
  plainDescription,
  resetHeadForTests,
  serializeJsonLd,
  setSiteDescription,
  websiteJsonLd,
} from '../lib/head'
import { resetSiteCacheForTests } from '../lib/site'
import Post from '../pages/public/Post'
import Home from '../pages/public/Home'
import Search from '../pages/public/Search'
import Archive from '../pages/public/Archive'
import PageDetail from '../pages/public/PageDetail'
import { getSharedClientMock, makeArticle, makePage, resetClientMock } from './helpers'

vi.mock('../lib/client', async () => {
  const { getSharedClientMock } = await import('./helpers')
  return getSharedClientMock()
})
// 评论与编辑器都不是本文件的关注点，替换成探针以免它们各自的接口依赖把用例拖进无关分支。
vi.mock('../components/Comments', () => ({ default: () => <div>COMMENTS-STUB</div> }))

const client = getSharedClientMock()

function meta(attribute: 'property' | 'name', key: string): string | null {
  return document.head.querySelector(`meta[${attribute}="${key}"]`)?.getAttribute('content') ?? null
}

function canonicalHref(): string | null {
  return document.head.querySelector('link[rel="canonical"]')?.getAttribute('href') ?? null
}

function jsonLdScripts(): HTMLScriptElement[] {
  return Array.from(document.head.querySelectorAll('script[type="application/ld+json"][data-head="page"]'))
}

beforeEach(() => {
  resetClientMock(client)
  resetSiteCacheForTests()
  resetHeadForTests()
  client.blog.getSite.mockResolvedValue({ description: '测试站', avatar_url: '', code_block_max_lines: 20 })
  client.blog.getInstallStatus.mockResolvedValue({ installed: true })
})

describe('plainDescription', () => {
  it('摘掉 Markdown 噪声，只留下能给人读的纯文本', () => {
    const markdown = [
      '# 标题',
      '',
      '正文里有**粗体**、`行内代码` 与 ![图](/api/media/1)。',
      '',
      '```ts',
      'const secret = 1',
      '```',
      '',
      '还有 [链接文字](https://example.com/a) 要保留文字。',
    ].join('\n')

    const text = plainDescription(markdown)
    expect(text).toContain('标题')
    expect(text).toContain('粗体')
    expect(text).toContain('行内代码')
    expect(text).toContain('链接文字')
    expect(text).not.toContain('const secret')
    expect(text).not.toContain('#')
    expect(text).not.toContain('![')
    expect(text).not.toContain('https://example.com')
  })

  it('强调符号去掉而不是换成空格：中文摘要里不能凭空多出空格', () => {
    expect(plainDescription('正文**内容**')).toBe('正文内容')
    expect(plainDescription('~~删掉~~与*强调*')).toBe('删掉与强调')
    expect(plainDescription('a **b** c')).toBe('a b c')
  })

  it('空输入返回空串；超长正文按上限截断', () => {
    expect(plainDescription('')).toBe('')
    const long = 'a'.repeat(400)
    expect(plainDescription(long, 150)).toBe(`${'a'.repeat(150)}…`)
    expect(plainDescription(long, 1500)).toBe(long)
  })
})

describe('JSON-LD 序列化', () => {
  it('转义 < > &：标题里的 </script> 不能把脚本标签提前闭合', () => {
    const hostile = serializeJsonLd({ '@type': 'BlogPosting', headline: '</script><img src=x>&"quoted"' })
    expect(hostile).not.toContain('<')
    expect(hostile).not.toContain('>')
    expect(hostile).not.toContain('&')
    expect(hostile).toContain('\\u003c/script\\u003e')
    expect(hostile).toContain('\\u0026')
  })

  it('构造器：WebSite 与 BlogPosting（有/无分享图）', () => {
    expect(websiteJsonLd('Twilight', '描述', 'https://x.test/')).toMatchObject({
      '@context': 'https://schema.org',
      '@type': 'WebSite',
      name: 'Twilight',
      description: '描述',
      url: 'https://x.test/',
    })

    const base = { title: '标题', description: '摘要', publishedAt: 1_700_000_000_000, url: 'https://x.test/post/1' }
    expect(articleJsonLd(base)).toMatchObject({ '@type': 'BlogPosting', headline: '标题' })
    expect(articleJsonLd(base)).not.toHaveProperty('image')
    expect(articleJsonLd({ ...base, image: '/api/media/2' })).toHaveProperty('image', ['/api/media/2'])
  })

  it('articlePageHead：描述取自正文，url 用绝对地址', () => {
    const head = articlePageHead({
      path: '/post/1',
      title: '标题',
      content: '正文**内容**',
      publishedAt: 1_700_000_000_000,
      image: '/api/media/2',
    })
    expect(head.type).toBe('article')
    expect(head.description).toBe('正文内容')
    expect(head.jsonLd).toMatchObject({ '@type': 'BlogPosting', mainEntityOfPage: expect.stringContaining('/post/1') })
  })
})

describe('applyHead 的标签写入与对账', () => {
  it('写出完整一套；再写「这一页没有的」时把它们回收掉', () => {
    applyHead({
      path: '/post/7',
      title: '文章标题',
      description: '文章摘要',
      type: 'article',
      image: '/api/media/3',
      publishedAt: 1_700_000_000_000,
      jsonLd: { '@type': 'BlogPosting' },
    })

    expect(canonicalHref()).toContain('/post/7')
    expect(meta('property', 'og:type')).toBe('article')
    expect(meta('property', 'og:title')).toBe('文章标题')
    expect(meta('property', 'og:description')).toBe('文章摘要')
    expect(meta('property', 'og:image')).toContain('/api/media/3')
    expect(meta('property', 'article:published_time')).toBe('2023-11-14T22:13:20.000Z')
    expect(meta('name', 'twitter:card')).toBe('summary_large_image')
    expect(meta('name', 'twitter:image')).toContain('/api/media/3')
    expect(jsonLdScripts()).toHaveLength(1)

    // 首页：没有发布时间、没有分享图、没有结构化数据 → 这三样必须消失
    applyHead({ path: '/', title: 'Twilight', description: '站点描述' })

    expect(meta('property', 'og:type')).toBe('website')
    expect(meta('property', 'article:published_time')).toBeNull()
    expect(meta('property', 'og:image')).toBeNull()
    expect(meta('name', 'twitter:card')).toBe('summary')
    expect(meta('name', 'twitter:image')).toBeNull()
    expect(jsonLdScripts()).toHaveLength(0)
    expect(canonicalHref()).not.toContain('/post/7')
  })

  it('canonical 复用外壳已有的 <link rel="canonical">，不新增一个', () => {
    const preexisting = document.createElement('link')
    preexisting.setAttribute('rel', 'canonical')
    preexisting.setAttribute('href', 'https://shell.test/')
    document.head.appendChild(preexisting)

    applyHead({ path: '/archive', title: '归档' })
    expect(document.head.querySelectorAll('link[rel="canonical"]')).toHaveLength(1)
    expect(canonicalHref()).toContain('/archive')
  })

  it('robots：给了就写，不给就删掉（避免后台的 noindex 留在公开页上）', () => {
    applyHead({ path: '/admin', title: '概览', robots: 'noindex, nofollow' })
    expect(meta('name', 'robots')).toBe('noindex, nofollow')

    applyHead({ path: '/', title: 'Twilight' })
    expect(meta('name', 'robots')).toBeNull()
  })

  it('description 页面级优先于站点级，与两者的到达顺序无关', () => {
    // 顺序 A：先站点级、后页面级 → 页面级赢
    setSiteDescription('站点描述')
    applyHead({ path: '/post/1', title: '标题', description: '文章摘要' })
    expect(meta('name', 'description')).toBe('文章摘要')
    expect(meta('property', 'og:description')).toBe('文章摘要')

    // 顺序 B：先页面级、后站点级（就是这里以前会翻车的那种顺序）→ 页面级仍然赢
    resetHeadForTests()
    applyHead({ path: '/post/1', title: '标题', description: '文章摘要' })
    setSiteDescription('站点描述')
    expect(meta('name', 'description')).toBe('文章摘要')

    // 页面没有自己的描述 → 回落站点级
    applyHead({ path: '/archive', title: '归档' })
    expect(meta('name', 'description')).toBe('站点描述')
  })

  it('两级描述都没有时保持外壳自带的那一份，不写空串抹掉它', () => {
    const shell = document.createElement('meta')
    shell.setAttribute('name', 'description')
    shell.setAttribute('content', '外壳里的默认描述')
    document.head.appendChild(shell)

    applyHead({ path: '/moments', title: '微语' })
    expect(meta('name', 'description')).toBe('外壳里的默认描述')
  })

  it('resetHeadForTests 清空受管标签与模块状态', () => {
    applyHead({
      path: '/post/1',
      title: '标题',
      description: '摘要',
      type: 'article',
      image: '/api/media/1',
      publishedAt: 1_700_000_000_000,
      robots: 'noindex, nofollow',
      jsonLd: { '@type': 'BlogPosting' },
    })
    resetHeadForTests()
    expect(canonicalHref()).toBeNull()
    expect(jsonLdScripts()).toHaveLength(0)
    expect(meta('property', 'og:title')).toBeNull()
    // twitter:* 挂在 name 上（不是 property），og:*/article:* 挂在 property 上：
    // 清理用的选择器必须与写入同规，否则这两族会各留下一半残标签。
    expect(meta('name', 'twitter:card')).toBeNull()
    expect(meta('name', 'twitter:image')).toBeNull()
    expect(meta('property', 'article:published_time')).toBeNull()
    expect(meta('name', 'robots')).toBeNull()
    expect(meta('name', 'description')).toBeNull()
    // 状态也清了：此后再 apply 一个没有描述的页面，就只剩空站点描述这一条出路
    applyHead({ path: '/', title: 'Twilight' })
    expect(meta('name', 'description')).toBeNull()
  })
})

describe('页面接入', () => {
  // path 必须给成**真实的**路由形状：用 '*' 兜底时 useParams() 拿不到 id，
  // Post/PageDetail 会走「id 不合法」分支，测出来的是 404 页的 head 而不是文章页的。
  function renderAt(ui: React.ReactElement, route: string, path = '*') {
    return render(
      <MemoryRouter initialEntries={[route]}>
        <Routes>
          <Route path={path} element={ui} />
        </Routes>
      </MemoryRouter>,
    )
  }

  it('文章页：canonical 指向自身、og:type=article、头图进分享图与 JSON-LD', async () => {
    client.blog.getArticle.mockResolvedValue(makeArticle({ id: 12, title: '一篇好文章', content: '# 开头\n正文内容', cover_media_id: 5 }))
    renderAt(<Post />, '/post/12', '/post/:id')

    await waitFor(() => expect(meta('property', 'og:title')).toBe('一篇好文章'))
    expect(canonicalHref()).toContain('/post/12')
    expect(meta('property', 'og:type')).toBe('article')
    expect(meta('property', 'og:image')).toContain('/api/media/5')
    expect(meta('name', 'description')).toBe('开头 正文内容')
    expect(jsonLdScripts()[0].textContent).toContain('BlogPosting')
  })

  it('文章取不到：整页 noindex（地址栏里还是一个看起来正常的 /post/N）', async () => {
    client.blog.getArticle.mockRejectedValue(new Error('文章不存在'))
    renderAt(<Post />, '/post/999', '/post/:id')

    await waitFor(() => expect(screen.getByText('文章不存在')).toBeTruthy())
    expect(meta('name', 'robots')).toBe('noindex, nofollow')
    // 不残留上一页的分享图与结构化数据
    expect(meta('property', 'og:image')).toBeNull()
    expect(jsonLdScripts()).toHaveLength(0)
  })

  it('首页：WebSite 结构化数据 + 站点描述；从文章页切过来不会带走 article 标签', async () => {
    client.blog.getArticle.mockResolvedValue(makeArticle({ id: 3, cover_media_id: 9 }))
    const first = renderAt(<Post />, '/post/3', '/post/:id')
    await waitFor(() => expect(meta('property', 'og:type')).toBe('article'))
    first.unmount()

    client.blog.listArticles.mockResolvedValue({ items: [], page: 1, page_size: 12, total: 0, total_pages: 1 })
    renderAt(<Home />, '/')

    await waitFor(() => expect(meta('property', 'og:title')).toBe('Twilight'))
    expect(jsonLdScripts()[0].textContent).toContain('WebSite')
    expect(jsonLdScripts()[0].textContent).toContain('测试站')
    expect(meta('property', 'og:type')).toBe('website')
    expect(meta('property', 'article:published_time')).toBeNull()
    expect(meta('property', 'og:image')).toBeNull()
  })

  it('搜索页 noindex, follow；归档页是普通可收录页', async () => {
    client.blog.search.mockResolvedValue({ items: [], page: 1, page_size: 12, total: 0, total_pages: 1 })
    const search = renderAt(<Search />, '/search?q=go')
    await waitFor(() => expect(meta('name', 'robots')).toBe('noindex, follow'))
    expect(canonicalHref()).toContain('/search?q=go')
    search.unmount()

    client.blog.listArticles.mockResolvedValue({ items: [], page: 1, page_size: 12, total: 0, total_pages: 1 })
    renderAt(<Archive />, '/archive')
    await waitFor(() => expect(meta('property', 'og:title')).toBe('归档'))
    expect(meta('name', 'robots')).toBeNull()
  })

  it('独立页面：与文章同样产出文章性 head（后台编辑页不写，它归 AdminLayout 的 noindex）', async () => {
    client.blog.getPage.mockResolvedValue(makePage({ id: 4, title: '关于我', content: '这里是关于。' }))
    renderAt(<PageDetail />, '/pages/4', '/pages/:id')
    await waitFor(() => expect(meta('property', 'og:title')).toBe('关于我'))
    expect(meta('property', 'og:type')).toBe('article')
    expect(meta('name', 'robots')).toBeNull()
    expect(canonicalHref()).toContain('/pages/4')
    expect(jsonLdScripts()[0].textContent).toContain('关于我')
  })
})
