// ContentView.tsx —— Markdown 内容渲染容器
//
// 默认走**评论白名单管线**（renderCommentMarkdown），只有显式声明 trusted 时才走
// 文章管线。两条管线最后都用 dangerouslySetInnerHTML 注入，这里不再额外 sanitize。
//
// 为什么默认必须是安全的那一条：`sanitize` 曾经是一个默认为 false 的可选开关，
// 于是「新页面忘了传这个 prop」等于「把访客提交的原始 HTML 直接注入 DOM」——
// 一个漏写就静默变成存储型 XSS，而且类型检查不会报错。把极性翻过来之后，
// 漏写只会让文章少渲染一点格式，不会变成漏洞。
//
// 渲染器本身是**懒加载**的（见 `lib/markdown-loader.ts`）：KaTeX 与 highlight.js
// 是首屏包里最重的两块，只有这里才需要它们。所以本组件有一个「等渲染器」的状态，
// 取不到时给占位、加载失败时给转义后的原文——**绝不**降级成不转义的注入。
//
// enhanceCodeBlocks 是渲染后的 DOM 后处理：给每个 <pre> 包一层容器、加语言标签
// 和「复制」按钮。之所以用命令式 DOM 而非在管线里产出按钮，是因为复制交互
// （clipboard、定时器）难以在纯 HTML 字符串阶段实现。
// 注意：复制成功的提示用 setTimeout 还原，所有 timer 收进 ref 集合，卸载时统一
// clearTimeout，避免组件已卸载还去改按钮文字（内存/警告泄漏）。

import { t } from '../lib/i18n'
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import { escapeHtml } from '../lib/format'
import { loadMarkdown, peekMarkdown } from '../lib/markdown-loader'
import { useSiteCodeLines } from '../lib/site'
import { Loading } from './common'
import { useDelayedLoading } from '../lib/loading'
import { useImageGroup, type ImageGroupHandle } from '../lib/imageGroup'

interface ContentViewProps {
  text: string
  className?: string
  /**
   * 声明这段内容是**站长自己撰写的**，可以走不做标签白名单的文章管线。
   *
   * 只有文章 / 独立页面 / 动态这三类「作者可信」的内容才该传它。访客提交的
   * 内容（评论）绝不能传——默认为 false 正是为了让漏写这件事fail closed。
   */
  trusted?: boolean
}

async function writeClipboard(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return
    } catch {
      // Fall through to the legacy textarea approach below.
    }
  }

  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.style.position = 'fixed'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)
  try {
    textarea.select()
    document.execCommand('copy')
  } finally {
    // Always detach, even when execCommand throws.
    textarea.remove()
  }
}

// 根级导出供测试直呼（与 markdown.ts 探针同一先例）：root 为空的守卫在渲染
// 路径不可达（effect 只在挂载后跑），直接调用才可测；pre 已包裹 / 无 code 子元素
// 的跳过分支也只能用手搓 DOM 触达。
export function enhanceCodeBlocks(root: HTMLElement | null, timers: Set<number>): void {
  if (!root) return
  const pres = root.querySelectorAll<HTMLPreElement>('pre')
  pres.forEach((pre) => {
    if (pre.parentElement?.classList.contains('code-block')) return

    const code = pre.querySelector('code')
    if (!code) return

    const languageClass = Array.from(code.classList).find((name) => name.startsWith('language-'))
    const language = languageClass ? languageClass.slice('language-'.length) : 'code'

    const wrapper = document.createElement('div')
    wrapper.className = 'code-block'

    const label = document.createElement('span')
    label.className = 'code-lang'
    label.textContent = language

    const copyButton = document.createElement('button')
    copyButton.type = 'button'
    copyButton.className = 'code-copy'
    copyButton.textContent = t('复制', 'Copy')
    copyButton.title = t('复制代码', 'Copy code')
    copyButton.setAttribute('aria-label', t('复制代码', 'Copy code'))

    copyButton.addEventListener('click', async () => {
      const originalText = code.textContent || ''
      try {
        await writeClipboard(originalText)
        copyButton.textContent = t('已复制', 'Copied')
        copyButton.classList.add('copied')
      } catch {
        copyButton.textContent = t('复制失败', 'Copy failed')
      }
      const timer = window.setTimeout(() => {
        timers.delete(timer)
        copyButton.textContent = t('复制', 'Copy')
        copyButton.classList.remove('copied')
      }, 1600)
      timers.add(timer)
    })

    pre.parentNode?.insertBefore(wrapper, pre)
    wrapper.append(label, pre, copyButton)
  })
}

/** 正文图的终态：清掉占位比例，把高度交还图片本身。 */
function applyImageStatus(frame: HTMLElement, status: 'loaded' | 'failed'): void {
  frame.dataset.status = status
  frame.classList.remove('loading')
  frame.classList.add(status)
  frame.style.aspectRatio = ''
}

/**
 * 已经登记进图片组的正文图。React 19 有时会用相同内容重设 innerHTML，把包好的
 * 外框连同图片一起换掉——旧元素的等待若没人销账，整组就再也开不了闸。这里按
 * 根元素记账，下一次增强时把已经不在文档里的登记释放掉。
 */
const tracked = new WeakMap<HTMLElement, Set<{ img: HTMLImageElement; release: () => void }>>()

function forgetDetached(root: HTMLElement): void {
  const entries = tracked.get(root)
  if (!entries) return
  for (const entry of entries) {
    if (entry.img.isConnected) continue
    entry.release()
    entries.delete(entry)
  }
}

/**
 * 正文图片的占位动画（与 ProgressiveImage 共用 .img-frame / .img-skeleton）。
 *
 * 正文是 dangerouslySetInnerHTML 注进来的，图片不是 React 元素，没法套组件——
 * 只能在渲染之后包一层外框并挂 load / error。外框用 <span> 而不是 <div>：
 * 正文里的图片常与文字同处一个 <p>，块级元素会被浏览器提前闭合掉。
 *
 * 幂等：已经包过的直接跳过。内联的 aspect-ratio 只是「没加载完时先占一块」，
 * 图到了就清掉，把高度交还给图片本身——与 ProgressiveImage 的规则一致。
 *
 * 传了 group 时这张图也进组：到齐之前只画骨架，结果先记在外框的 data-status
 * 上；组开闸后 ContentView 会因为 context 变化重渲染，这个函数再跑一遍把它们
 * 一次性补上——于是正文图与同一组的头图/配图一起出现。
 */
export function enhanceImages(root: HTMLElement | null, group?: ImageGroupHandle | null): void {
  if (!root) return
  const open = group ? group.open : true
  forgetDetached(root)

  root.querySelectorAll<HTMLImageElement>('img').forEach((img) => {
    const wrapped = img.closest<HTMLElement>('.img-frame')
    if (wrapped) {
      // 已经包过：组开闸之后，把「早就解码完、一直在等组」的那张补上。
      const status = wrapped.dataset.status
      if (open && status && wrapped.classList.contains('loading')) {
        applyImageStatus(wrapped, status === 'failed' ? 'failed' : 'loaded')
      }
      return
    }

    const frame = document.createElement('span')
    frame.className = 'img-frame loading'
    frame.style.aspectRatio = '16 / 9'

    const skeleton = document.createElement('span')
    skeleton.className = 'img-skeleton'
    skeleton.setAttribute('aria-hidden', 'true')

    img.before(frame)
    frame.append(skeleton, img)

    const release = group?.track()
    if (release) {
      let entries = tracked.get(root)
      if (!entries) {
        entries = new Set()
        tracked.set(root, entries)
      }
      entries.add({ img, release })
    }

    let settled = false
    const settle = (status: 'loaded' | 'failed') => {
      if (settled) return
      settled = true
      release?.()
      // 组还没齐：先记住结果，等开闸那一轮重渲染再上屏。
      if (!open) {
        frame.dataset.status = status
        return
      }
      applyImageStatus(frame, status)
    }

    // 与 ProgressiveImage 同一口径：字节到齐之后等 decode，避免「解码一半」的
    // 画面被画出来。没有 decode 的环境（jsdom）退化成 load 即显示。
    const reveal = (node: HTMLImageElement) => {
      const decoded = typeof node.decode === 'function' ? node.decode() : null
      if (!decoded) {
        settle('loaded')
        return
      }
      decoded.then(() => settle('loaded'), () => settle('failed'))
    }

    img.addEventListener('load', () => reveal(img))
    img.addEventListener('error', () => settle('failed'))
    // 已缓存过的图片不会再触发 load：包完立刻查一次（坏图在这里就判掉）。
    if (img.complete) {
      if (img.naturalWidth > 0) reveal(img)
      else settle('failed')
    }
  })
}


export default function ContentView({ text, className = '', trusted = false }: ContentViewProps) {
  const ref = useRef<HTMLDivElement>(null)
  const timers = useRef<Set<number>>(new Set())
  // 代码块行数上限来自站点设置（GET /api/site 公开下发）。通过 CSS 变量传给
  // 样式表而不是内联 max-height：具体怎么限（行高、内边距的合成）是 CSS 的
  // 事，这里只负责「多少行」这一个数字。
  const codeLines = useSiteCodeLines()
  // 渲染器是懒加载块。**用 peek 当初始值而不是从 null 起步**：已经加载过之后，
  // 后续挂载（翻页、切文章、列表里每条评论）必须在首次渲染就拿到它，否则每次
  // 都要先空一帧再填内容。
  const [renderer, setRenderer] = useState(peekMarkdown)
  // 正文图与页面上的其它图（头图、配图）同属一组：组开闸前正文图也只画骨架，
  // 开闸那一轮重渲染把它们的终态一次性补上（见 enhanceImages）。
  const imageGroup = useImageGroup()
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (renderer) return
    let alive = true
    loadMarkdown()
      .then((module) => {
        if (alive) setRenderer(module)
      })
      .catch(() => {
        // 只有「连渲染器都拿不到」才算失败。降级不在这里做，见下面的 failed 分支。
        if (alive) setFailed(true)
      })
    return () => {
      alive = false
    }
  }, [renderer])

  const html = useMemo(
    () => (renderer
      // 注意极性与 prop 名一致：trusted 才走文章管线，默认走白名单管线。
      ? (trusted ? renderer.renderMarkdown(text || '') : renderer.renderCommentMarkdown(text || ''))
      : null),
    [renderer, trusted, text],
  )
  // 渲染器是一块懒加载 chunk，通常几十毫秒就到。LOADING 占位同样延迟出现：
  // 否则每条微语 / 评论正文都会先闪一下“LOADING”再变成文字。
  const pending = useDelayedLoading(html === null)

  // 为什么是「无依赖的 layout effect」而不是 useEffect(..., [html])：
  // React 19 在父级提交（如 TOC 注入引发的整页重渲染）会用**完全相同的内容**
  // 重新设置 dangerouslySetInnerHTML（真机实测：同一元素、同 len/同 hash 的
  // innerHTML 被连续设置两次），把 DOM 后处理包好的壳冲掉；而依赖 [html] 的
  // effect 不会在第二次提交后重跑——壳就永久丢失，代码块限行随之失效。
  // 所以必须在**每次**提交后重新检查一遍。enhanceCodeBlocks 幂等（已包壳的
  // pre 直接跳过），重复执行没有副作用；layout effect 保证重包发生在 paint
  // 之前，不会闪一下裸代码。
  // 还没拿到渲染器时 ref.current 是 null（此时渲染的是占位），enhanceCodeBlocks
  // / enhanceImages 的空 root 守卫正好接住这一步。
  useLayoutEffect(() => {
    const pending = timers.current
    enhanceCodeBlocks(ref.current, pending)
    enhanceImages(ref.current, imageGroup)
    return () => {
      for (const timer of pending) window.clearTimeout(timer)
      pending.clear()
    }
  })

  const shellProps = {
    className: `plain-body ${className}`.trim(),
    style: { '--code-max-lines': codeLines } as CSSProperties,
  }

  if (failed) {
    // 降级成**转义后的原文**：宁可让人看到没排版的 Markdown，也不要白屏。
    // 转义这一步不能省——评论是不可信输入，这条路径绝不能把原文当 HTML 注进去。
    return (
      <div {...shellProps} dangerouslySetInnerHTML={{ __html: `<p>${escapeHtml(text || '')}</p>` }} />
    )
  }

  if (html === null) return <Loading height={160} visible={pending} />

  return <div ref={ref} {...shellProps} dangerouslySetInnerHTML={{ __html: html }} />
}
