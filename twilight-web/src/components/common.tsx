// common.tsx —— 公开站点的通用 UI 小组件
// Reveal：用 IntersectionObserver（阈值 0.06）做「进入视口才淡入」的一次性动画，
//   触发后 unobserve，不会重复播放；delay 通过 CSS 变量 --d 控制错峰。
// Avatar / Empty / Loading / PageHero：纯展示。

import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react'

// 观察逻辑拆出来：Reveal 的 ref 在 effect 执行时恒已挂载，「el 为空」的守卫
// 分支走渲染路径不可达，直接调用才可测（与 markdown.ts 的探针导出同一先例）。
export function observeReveal(el: Element | null, onEnter: () => void): () => void {
  if (!el) return () => {}
  const observer = new IntersectionObserver(
    (entries) => {
      entries.forEach((entry) => {
        if (entry.isIntersecting) {
          onEnter()
          observer.unobserve(entry.target)
        }
      })
    },
    { threshold: 0.06 },
  )
  observer.observe(el)
  return () => observer.disconnect()
}

export function Reveal({
  children,
  delay = 0,
  className = '',
  as: Tag = 'div',
}: {
  children: ReactNode
  delay?: number
  className?: string
  as?: 'div' | 'section' | 'article' | 'li' | 'span'
}) {
  const ref = useRef<HTMLElement | null>(null)
  const [visible, setVisible] = useState(false)

  useEffect(() => observeReveal(ref.current, () => setVisible(true)), [])

  return (
    <Tag
      ref={ref as never}
      className={`rv ${visible ? 'in' : ''} ${className}`.trim()}
      style={delay ? ({ '--d': `${delay}s` } as CSSProperties) : undefined}
    >
      {children}
    </Tag>
  )
}

export function Loading({ height = 160 }: { height?: number }) {
  return (
    <div style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      <span className="kicker">LOADING…</span>
    </div>
  )
}

export function Empty({ text }: { text: string }) {
  return (
    <div style={{ padding: '40px 8px', color: 'var(--faint)', fontFamily: 'var(--mono)', fontSize: 12 }}>
      {text}
    </div>
  )
}

export function Avatar({ letter, alt = false, size = 'cav' }: { letter: string; alt?: boolean; size?: string }) {
  return <span className={`${size}${alt ? ' alt' : ''}`}>{letter || '匿'}</span>
}

export function PageHero({ kicker, title, subtitle }: { kicker: string; title: ReactNode; subtitle?: ReactNode }) {
  return (
    <div className="phero rv">
      <span className="kicker">{kicker}</span>
      <h1>{title}</h1>
      {subtitle ? <p>{subtitle}</p> : null}
    </div>
  )
}


