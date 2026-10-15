// MomentComments.tsx —— 微语卡的评论区（点击「评论」按钮后才挂载）
//
// 为什么不做成「卡片一渲染就挂评论区」：Comments 在挂载时就会请求
// GET /api/comments，而微语流一页有 12 条——无条件挂载等于一进页面就打 12 个
// 评论请求，绝大多数条目的评论区根本没人展开。所以评论组件只在**用户明确要看**
// 的那一刻才被挂载，未点击的微语一个请求都不发。
//
// 按钮固定在卡片右下方（.mom-ft 右对齐），两种文案与 aria-expanded 同步，
// 键盘与读屏用户据此就知道评论区的开合状态。
//
// 注意这里只是「点击后才挂载」的条件渲染，**不是** React.lazy 的按需下载：
// Comments 仍被文章页/独立页面静态引用，仍在首屏包里。把评论组件拆成独立
// chunk 是另一件事（代价是文章页的评论区也要改成 Suspense 包裹），已记在
// 仓库根目录的 todo.md 里，不要在这里顺手改。

import { useState } from 'react'
import Comments from './Comments'

interface MomentCommentsProps {
  /** 被评论微语的 id（targetType 恒为 moment，无需调用方重复传）。 */
  momentId: number
}

export default function MomentComments({ momentId }: MomentCommentsProps) {
  const [open, setOpen] = useState(false)

  return (
    <>
      <div className="mom-ft">
        <button
          type="button"
          className="mom-cmt-btn"
          aria-expanded={open}
          onClick={() => setOpen((value) => !value)}
        >
          {open ? '收起评论' : '评论'}
        </button>
      </div>
      {open ? <Comments targetType="moment" targetId={momentId} enabled /> : null}
    </>
  )
}
