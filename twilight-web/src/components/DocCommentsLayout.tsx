// DocCommentsLayout.tsx —— 文章页 / 独立页面共用的「正文 + 评论」布局
//
// 为什么需要这一层：Comments 原本挂在 Post / PageDetail 里面。两者是两条不同的
// 路由，点击导航时 React Router 会卸载旧页面、挂载新页面，于是评论框也跟着
// 卸载重挂——新页面还在取数据的那段时间评论框整块消失，数据到了又突然出现。
//
// 把两条路由放进同一个无路径布局路由后，布局实例在 /post/:id 与 /pages/:id
// 之间切换时会被复用：评论区留在原地，只把 targetType / targetId 换成新页面。
// 评论列表会按新对象重新加载，但输入框与整个区块不卸载，也就没有「消失又出现」。
//
// 评论对象从地址解析，而不是等页面数据回来才告诉布局：页面还在加载时评论区就
// 已经可以做自己的请求，两者并行。页面加载失败时通过 setCommentUnavailable
// 把评论区收掉，避免 404 下面还挂着一条不存在的对象的评论区。
//
// 单独渲染 Post / PageDetail（测试、Storybook）时没有这一层；页面检测不到
// commentsHosted，仍会自己渲染评论区，行为与以前一致。

import { useCallback, useMemo, useState } from 'react'
import { Outlet, useLocation, useOutletContext } from 'react-router-dom'
import Comments from './Comments'
import { parseDocCommentTarget, type DocOutletContext } from '../lib/doc-comments'

export default function DocCommentsLayout() {
  const { pathname } = useLocation()
  // 外壳（PublicShell）把 setToc 放在 Outlet context 里；这里必须原样转发，
  // 否则正文页的目录会拿到 undefined，侧栏永远空着。
  const parent = useOutletContext<DocOutletContext | undefined>()
  const target = useMemo(() => parseDocCommentTarget(pathname), [pathname])
  // 记住「哪一个对象被页面判定为不存在」。按 key 存而不是布尔值：切换到另一篇
  // 文章/页面时，旧的失败状态自然失效，不需要额外的重置 effect。
  const [failedKey, setFailedKey] = useState<string | null>(null)
  const setCommentUnavailable = useCallback((unavailable: boolean) => {
    if (!target) return
    setFailedKey(unavailable ? target.key : null)
  }, [target])
  const context = useMemo<DocOutletContext>(() => ({
    ...(parent ?? {}),
    commentsHosted: true,
    setCommentUnavailable,
  }), [parent, setCommentUnavailable])

  const visibleTarget = target !== null && failedKey !== target.key ? target : null

  return (
    <>
      <Outlet context={context} />
      {visibleTarget ? (
        // 与页面自身的容器同名同宽：评论区的左右边缘和正文严格对齐，页脚也
        // 沿用原来的下边距。文章页/独立页面共用同一个外层元素，切换时 React
        // 不会因为外层标签变化而把里面的 Comments 一起重建。
        <div className={`wrap ${visibleTarget.type === 'article' ? 'post-wrap page-article' : 'gpage page-detail'}`}>
          <div style={{ marginTop: visibleTarget.type === 'article' ? 34 : 30 }}>
            <Comments targetType={visibleTarget.type} targetId={visibleTarget.id} enabled />
          </div>
          {visibleTarget.type === 'page' ? <div style={{ height: 70 }} /> : null}
        </div>
      ) : null}
    </>
  )
}
