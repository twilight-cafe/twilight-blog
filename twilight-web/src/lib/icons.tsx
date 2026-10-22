// icons.tsx —— 内联 SVG 图标集
// 所有图标共用 24×24 视图框、stroke=currentColor，因此颜色随文字颜色走、
// 可用 CSS 控制尺寸。name 不匹配时返回 null（不会渲染空 svg）。
//
// 表里只放**确实被引用**的字形：多留一个没人用的图标就是一份没人维护的路径数据，
// 要新增时把 path 加回这张表即可。调用方一律传字面量 name（AdminLayout 的菜单表
// 是唯一的数据来源），所以这里没有「运行时才知道名字」的调用方。

import type { ReactNode } from 'react'

const paths: Record<string, ReactNode> = {
  doc: <><path d="M7 3h7l4 4v14H7zM14 3v4h4M10.5 12h4M10.5 16h4" /></>,
  page: <><path d="M8 4h11v13M5 8h11v12H5z" /></>,
  msg: <><path d="M4 6h16v11H9l-5 4z" /></>,
  img: <><path d="M4 5h16v14H4zM4 17l5-4 4 3 3-2 4 3" /><circle cx="9" cy="10" r="1.6" /></>,
  tag: <><path d="M4 12V5h7l9 9-7 7-9-9z" /><circle cx="8.3" cy="9" r="1.3" /></>,
  zap: <><path d="M13 2L5 13h6l-1 9 8-11h-6l1-9z" /></>,
  set: <><path d="M4 7h9M17.5 7H20M4 17h2.5M11 17h9" /><circle cx="15" cy="7" r="2.2" /><circle cx="8.5" cy="17" r="2.2" /></>,
  pal: <><path d="M12 3a9 9 0 000 18c1.6 0 2.1-1 1.6-2.1-.5-1 .4-2.4 2-2.4H18a3.5 3.5 0 003.5-3.5C21.5 7.5 17.2 3 12 3z" /><circle cx="8" cy="9" r="1.2" /><circle cx="12" cy="7" r="1.2" /><circle cx="16" cy="9" r="1.2" /><circle cx="7" cy="13.5" r="1.2" /></>,
  sun: <><circle cx="12" cy="12" r="4" /><path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5 5l1.4 1.4M17.6 17.6L19 19M19 5l-1.4 1.4M6.4 17.6L5 19" /></>,
  moon: <><path d="M20 14.5A8.5 8.5 0 119.5 4a7 7 0 0010.5 10.5z" /></>,
  copy: <><path d="M9 9h11v11H9zM5 15V4h11" /></>,
  x: <><path d="M5 5l14 14M19 5L5 19" /></>,
  eye: <><path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z" /><circle cx="12" cy="12" r="2.8" /></>,
  clock: <><circle cx="12" cy="12" r="8.5" /><path d="M12 7v5l3.5 2" /></>,
  out: <><path d="M10 4H4v16h6M14 8l4 4-4 4M18 12H9" /></>,
  // 上箭头：「回到顶部」按钮。从下往上收笔，和 out 的「向外走」相反。
  up: <><path d="M12 19V5M6 11l6-6 6 6" /></>,
  user: <><circle cx="12" cy="8" r="3.6" /><path d="M5 20a7 7 0 0114 0" /></>,
  // 铅笔：媒体库的「重命名」动作（改的是展示名，不是文件本身）。
  edit: <><path d="M4 20h4L20 8l-4-4L4 16zM14 6l4 4" /></>,
}

export default function Icon({
  name,
  size,
  className = 'ic',
}: {
  name: string
  size?: number
  className?: string
}) {
  const content = paths[name]
  if (!content) return null
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.7}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {content}
    </svg>
  )
}
