// common.tsx —— 公开站点的通用 UI 小组件
// Avatar / Empty / Loading / PageHero / 骨架：纯展示。
//
// 「进视口才淡入」（Reveal）与阅读进度条（ScrollProgress）已由 ink-design
// 提供（0.1.3 起），站点不再各维护一份 IntersectionObserver / 滚动监听：页面直接从
// `ink-design` 导入这两个组件，样式也由它们的 Style Part 自带。

import { t } from '../lib/i18n'
import type { ReactNode } from 'react'

/**
 * 整块的等待提示。
 *
 * visible 由调用方用 useDelayedLoading 决定（见 lib/loading.ts）：加载超过阈值
 * 才传 true。**元素本身始终渲染**，即使不可见也占住 height——否则「先空白，
 * 200ms 后突然出现一块 LOADING」又是一次布局跳动。
 */
export function Loading({ height = 160, visible = true }: { height?: number; visible?: boolean }) {
  return (
    <div
      className="loading-screen"
      data-visible={visible ? 'true' : 'false'}
      style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center' }}
    >
      {/* 慢呼吸而不是转圈：这里等的通常是懒加载的页面 chunk，多数时候一闪而过，
          转圈反而更像卡住；0.35 的最低不透明度让它「在等」但不抢注意力。 */}
      <span className="kicker loading-pulse">LOADING…</span>
    </div>
  )
}

export function Empty({ text }: { text: string }) {
  return (
    // empty-state 让空态也走一次入场：它是「查询有结果」的对立面，同一次状态变化。
    <div className="empty-state" style={{ padding: '40px 8px', color: 'var(--faint)', fontFamily: 'var(--mono)', fontSize: 12 }}>
      {text}
    </div>
  )
}

/**
 * 骨架的公共外壳。
 *
 * 两条约定，缺一条就会在加载与内容之间「跳一下」：
 *   1. 骨架**始终挂在树上**，在加载一开始就占住内容将要出现的高度。数据到达
 *      之前换掉骨架或干脆不画，页面都会先塌下去再长回来；
 *   2. 只有 data-visible 为 true 时才真正显示（见 lib/loading.ts）。快请求
 *      在 200ms 内就结束，骨架自始至终不可见——用户看到的是内容直接出现。
 *
 * aria-hidden 由这里统一给：骨架是纯视觉占位，读屏用户需要知道的是「在加载」，
 * 那由页面标题和 aria-busy 负责，不是一串没有语义的空 div。
 */
function SkeletonView({ visible, className, children }: { visible: boolean; className: string; children: ReactNode }) {
  return (
    <div className={`skl-view ${className}`} data-visible={visible ? 'true' : 'false'} aria-hidden="true">
      {children}
    </div>
  )
}

/**
 * 列表骨架：与真实卡片**同一套结构**（.acard.has-cover 的封面 + 正文），
 * 只把文字换成等高的灰条。这样骨架和内容的高度、间距、折行位置都对得上，
 * 数据到达时卡片不会跳版。
 *
 * 为什么直接复用内容类的类名而不是另写一套骨架专用尺寸：站点的排版随时会
 * 调整（标题字号、内边距、标签高度），复制一份尺寸就多一处会漂移的来源。
 * 复用类名后，改排版就是同时改骨架，不需要记得去同步一个「看起来无关」的文件。
 *
 * 行宽刻意不等：一排等宽的灰条看起来像表格，长短不一的才像被裁过的文字。
 */
export function SkeletonCards({ count = 3, visible = true }: { count?: number; visible?: boolean }) {
  return (
    <SkeletonView visible={visible} className="skl-cards">
      {Array.from({ length: count }, (_, index) => (
        <div className="acard has-cover" key={index}>
          <div className="cover">
            <span className="skl skl-cover" />
          </div>
          <div className="body">
            <div className="top">
              <span className="skl skl-t" style={{ width: 34 }} />
              <span className="skl skl-t" style={{ width: 54 }} />
              <span className="skl skl-t" style={{ width: 46, marginLeft: 'auto' }} />
            </div>
            <div className="skl-title">
              <span className="skl skl-t skl-t-title" style={{ width: `${62 + (index % 3) * 7}%` }} />
            </div>
            <p className="ex">
              <span className="skl skl-t" style={{ width: '96%' }} />
              <br />
              <span className="skl skl-t" style={{ width: `${58 + (index % 4) * 9}%` }} />
            </p>
            <div className="tags">
              <span className="skl skl-pill" style={{ width: 56 }} />
              <span className="skl skl-pill" style={{ width: 42 }} />
            </div>
            <div className="ft">
              <span className="skl skl-t" style={{ width: 62 }} />
              <span className="skl skl-t" style={{ width: 44 }} />
            </div>
          </div>
        </div>
      ))}
    </SkeletonView>
  )
}

// 正文骨架的行宽。段落之间用 .skl-line-gap 撑出与正文 p 间距相当的空隙。
const ARTICLE_LINES = ['100%', '96%', '92%', '88%', '0', '100%', '94%', '96%', '70%']

/**
 * 文章 / 独立页面的骨架。
 *
 * 与旧版最大的不同是**带上了 .phead**：真实页面在数据到达时会多出一个标题区
 * （标签、大标题、日期那三行，合计约 150px）。骨架里没有它，内容一出现就会把
 * 整页往下推——这正是「从加载到渲染排版变化」最明显的一处。
 */
export function SkeletonArticle({ visible = true }: { visible?: boolean }) {
  return (
    <SkeletonView visible={visible} className="skl-article">
      <div className="phead">
        <div className="skl-tags">
          <span className="skl skl-pill" style={{ width: 54 }} />
          <span className="skl skl-pill" style={{ width: 42 }} />
        </div>
        <h1>
          <span className="skl skl-t skl-t-hero" />
        </h1>
        <div className="meta">
          <span className="skl skl-t" style={{ width: 92 }} />
          <span className="skl skl-t" style={{ width: 76 }} />
        </div>
      </div>
      <div className="skl-lines">
        {ARTICLE_LINES.map((width, index) =>
          width === '0'
            ? <span className="skl-line-gap" key={index} />
            : <span className="skl skl-line" key={index} style={{ width }} />,
        )}
      </div>
    </SkeletonView>
  )
}

/** 骨架的排版变体：一个名字对应内容里那一种列表结构。 */
export type SkeletonVariant = 'comments' | 'moments' | 'results' | 'archive' | 'cloud'

/**
 * 行式骨架：评论、搜索结果、微语、归档、标签云各用**内容自己的类名**铺行，
 * 所以每种列表的骨架都落在真实条目的位置与高度上（理由同 SkeletonCards）。
 */
export function SkeletonRows({
  variant,
  count = 3,
  visible = true,
}: {
  variant: SkeletonVariant
  count?: number
  visible?: boolean
}) {
  if (variant === 'comments') {
    return (
      <SkeletonView visible={visible} className="cmt-list">
        {Array.from({ length: count }, (_, index) => (
          <div className="cmt" key={index}>
            <span className="skl skl-avatar" />
            <div className="skl-grow">
              <div className="nm">
                <span className="skl skl-t" style={{ width: 66 }} />
                <span className="skl skl-t" style={{ width: 44 }} />
              </div>
              <div className="bd">
                <span className="skl skl-t" style={{ width: `${70 + (index % 3) * 9}%` }} />
              </div>
              <div className="ops">
                <span className="skl skl-t" style={{ width: 30 }} />
                <span className="skl skl-t" style={{ width: 30 }} />
              </div>
            </div>
          </div>
        ))}
      </SkeletonView>
    )
  }

  if (variant === 'moments') {
    return (
      <SkeletonView visible={visible} className="skl-moments">
        {Array.from({ length: count }, (_, index) => (
          <div className="mom" key={index}>
            <div className="mom-meta"><span className="skl skl-t" style={{ width: 90 }} /></div>
            <div className="mom-content plain-body">
              <span className="skl skl-t" style={{ width: '96%' }} />
              <br />
              <span className="skl skl-t" style={{ width: `${54 + (index % 3) * 12}%` }} />
            </div>
            <div className="mom-ft"><span className="skl skl-t" style={{ width: 40 }} /></div>
          </div>
        ))}
      </SkeletonView>
    )
  }

  if (variant === 'archive') {
    return (
      <SkeletonView visible={visible} className="skl-archive">
        <div className="tl-y"><span className="skl skl-t skl-t-year" /></div>
        {Array.from({ length: count }, (_, index) => (
          <div className="tl-row" key={index}>
            <span className="skl skl-t" style={{ width: 48 }} />
            <span className="skl skl-t" style={{ width: `${52 + (index % 4) * 8}%` }} />
            <span className="skl skl-t" style={{ width: 38 }} />
          </div>
        ))}
      </SkeletonView>
    )
  }

  if (variant === 'cloud') {
    return (
      <SkeletonView visible={visible} className="cloud big skl-cloud">
        {Array.from({ length: count }, (_, index) => (
          <span className="skl skl-pill skl-chip" key={index} style={{ width: 58 + (index % 4) * 22 }} />
        ))}
      </SkeletonView>
    )
  }

  return (
    <SkeletonView visible={visible} className="skl-results">
      {Array.from({ length: count }, (_, index) => (
        <div className="sres" key={index}>
          <span className="skl skl-t" style={{ width: 128 }} />
          <h3><span className="skl skl-t" style={{ width: `${42 + (index % 3) * 9}%` }} /></h3>
          <p><span className="skl skl-t" style={{ width: '92%' }} /></p>
        </div>
      ))}
    </SkeletonView>
  )
}

export function Avatar({ letter, alt = false, size = 'cav' }: { letter: string; alt?: boolean; size?: string }) {
  return <span className={`${size}${alt ? ' alt' : ''}`}>{letter || t('匿', '?')}</span>
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
