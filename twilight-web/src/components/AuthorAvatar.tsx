// AuthorAvatar.tsx —— 作者卡里的站长头像
//
// 头像由站长在后台「账号」页设置（复用媒体库），经公开的 GET /api/site 下发
// （见 lib/site.ts）：它和站点描述一样，本来就是给每一个读者看的东西，
// 因此不需要登录、也不该单独为它发一次请求。
//
// 两处用到它：公开站点的侧栏作者卡（PublicSidebar）与站点设置页的作者卡预览
// （SiteSettings）。它们必须长得完全一样——一份预览要是另一套排版，它就只是在
// 描述一个不会发生的事情——所以头像的两种形态（有图 / 占位块）只在这里实现一次。
//
// 没设头像的站点是**常态**而不是异常：那时退回首字母占位块，而不是留一个破图、
// 一块空白或一句话。「T」取自作者卡上那个品牌名 Twilight。
//
// alt 是空串（装饰性）：作者卡里紧跟其后的 <b>Twilight</b> 已经把「这是谁」
// 说出去了，图片再报一次只会让读屏软件把同一个名字念两遍。

import { useSiteAvatar } from '../lib/site'
import ProgressiveImage from './ProgressiveImage'

export default function AuthorAvatar() {
  const avatarUrl = useSiteAvatar()

  if (!avatarUrl) {
    return <span className="avatar" aria-hidden="true">T</span>
  }
  // 头像是最容易「先空一块、再弹出一张脸」的位置：它固定 54px，占位动画不会
  // 改变任何布局，只是把等待画成骨架。round 让骨架与外框都跟着圆形走。
  return <ProgressiveImage className="avatar" frameClassName="round" src={avatarUrl} alt="" />
}
