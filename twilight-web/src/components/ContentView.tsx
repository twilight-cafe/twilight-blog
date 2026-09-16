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
// enhanceCodeBlocks 是渲染后的 DOM 后处理：给每个 <pre> 包一层容器、加语言标签
// 和「复制」按钮。之所以用命令式 DOM 而非在管线里产出按钮，是因为复制交互
// （clipboard、定时器）难以在纯 HTML 字符串阶段实现。
// 注意：复制成功的提示用 setTimeout 还原，所有 timer 收进 ref 集合，卸载时统一
// clearTimeout，避免组件已卸载还去改按钮文字（内存/警告泄漏）。

import { useLayoutEffect, useMemo, useRef, type CSSProperties } from 'react'
import { renderCommentMarkdown, renderMarkdown } from '../lib/markdown'
import { useSiteCodeLines } from '../lib/site'

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
    copyButton.textContent = '复制'
    copyButton.title = '复制代码'
    copyButton.setAttribute('aria-label', '复制代码')

    copyButton.addEventListener('click', async () => {
      const originalText = code.textContent || ''
      try {
        await writeClipboard(originalText)
        copyButton.textContent = '已复制'
        copyButton.classList.add('copied')
      } catch {
        copyButton.textContent = '复制失败'
      }
      const timer = window.setTimeout(() => {
        timers.delete(timer)
        copyButton.textContent = '复制'
        copyButton.classList.remove('copied')
      }, 1600)
      timers.add(timer)
    })

    pre.parentNode?.insertBefore(wrapper, pre)
    wrapper.append(label, pre, copyButton)
  })
}

export default function ContentView({ text, className = '', trusted = false }: ContentViewProps) {
  const ref = useRef<HTMLDivElement>(null)
  const timers = useRef<Set<number>>(new Set())
  // 代码块行数上限来自站点设置（GET /api/site 公开下发）。通过 CSS 变量传给
  // 样式表而不是内联 max-height：具体怎么限（行高、内边距的合成）是 CSS 的
  // 事，这里只负责「多少行」这一个数字。
  const codeLines = useSiteCodeLines()
  const html = useMemo(
    // 注意极性与 prop 名一致：trusted 才走文章管线，默认走白名单管线。
    () => (trusted ? renderMarkdown(text || '') : renderCommentMarkdown(text || '')),
    [trusted, text],
  )

  // 为什么是「无依赖的 layout effect」而不是 useEffect(..., [html])：
  // React 19 在父级提交（如 TOC 注入引发的整页重渲染）会用**完全相同的内容**
  // 重新设置 dangerouslySetInnerHTML（真机实测：同一元素、同 len/同 hash 的
  // innerHTML 被连续设置两次），把 DOM 后处理包好的壳冲掉；而依赖 [html] 的
  // effect 不会在第二次提交后重跑——壳就永久丢失，代码块限行随之失效。
  // 所以必须在**每次**提交后重新检查一遍。enhanceCodeBlocks 幂等（已包壳的
  // pre 直接跳过），重复执行没有副作用；layout effect 保证重包发生在 paint
  // 之前，不会闪一下裸代码。
  useLayoutEffect(() => {
    const pending = timers.current
    enhanceCodeBlocks(ref.current, pending)
    return () => {
      for (const timer of pending) window.clearTimeout(timer)
      pending.clear()
    }
  })

  return (
    <div
      ref={ref}
      className={`plain-body ${className}`.trim()}
      style={{ '--code-max-lines': codeLines } as CSSProperties}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  )
}
