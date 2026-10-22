## 本轮工作进度（2026-10-22）

> 范围：Moments 开关、内页页头压缩、文章头图高度、评论邮件通知、全站中英文切换、
> 文章/标签双语与 AI 生成标记。以下为完成状态与剩余事项。

### 已完成

- [x] **Moments 开关**：`settings.json` 新增 `moments_enabled`（键缺失 = 启用，向后兼容）；
      `GET/PUT /api/admin/site` 支持该字段；外壳经 `data-moments-enabled` 下发（不用内联脚本）；
      导航（buildNav）、页脚、侧栏、后台菜单、仪表盘快捷入口统一过滤；`/moments` 公开页
      给出「未启用」提示且不发请求；公开微语路由不再注册，管理员内容路由保留（重新开启即可恢复）；
      站点设置页提供 Switch。
- [x] **内页紧凑页头**：所有非首页内页使用 `.mast.slim.compact` + `.nav-slim.compact`
      （上内边距 28→16px、品牌字 32→26px、导航行 10→8px，移动端断点同步），首页大标题不变。
- [x] **文章头图高度**：`post_banner_height`（默认 400，合法区间 120–800，0 视为未设置）；
      外壳 `data-post-banner-height` → `--post-banner-height`，作用于 `.post-cover img`
      与 `.post-cover .img-frame`；站点设置页提供数字输入与越界拦截。
- [x] **评论邮件通知**：`mail.comment_notify_to`（空 = 关闭；非空按邮箱格式校验）；
      仅**访客**发表评论时发信，站长自己评论不通知；`service/comment_notifier.go`
      发送失败只记日志、绝不影响评论创建（201）；邮件照常落 `emails` 表、受每日配额约束；
      收件人只在 `GET /api/admin/site`（仅管理员）回显，公开 `GET /api/site` 不含该字段。
- [x] **全站中英文**：`lib/i18n.ts`（zh/en 词典内联在调用点、`t(zh, en)` 漏翻即编译错误、
      localStorage `tw_locale` 持久化、`<html lang>`/`data-locale` 同步、useSyncExternalStore 订阅）；
      `LanguageSwitcher` 替换内页精简头的 `TWILIGHT BLOG — INK & CODE`，并出现在首页大标题区、
      后台顶栏与登录/注册/找回密码/安装页；公开页、评论区、后台 14 个页面全部双语；
      `fmtRelative` 相对时间与校验/错误文案跟随语言。默认语言中文。
- [x] **文章双语 + AI 标记**：`articles` 表新增 `title_en` / `content_en` / `ai_generated`
      （建表语句 + `addedColumns` 迁移两条路径都覆盖）；创建/更新请求三态字段；
      编辑器提供「中文 / English」Tab（各自标题+正文）与「AI 生成」开关；
      前台按当前语言取标题/正文/标签（英文缺失回退中文）；AI 生成的文章在阅读页显示提示条；
      后台文章列表带 AI 徽章；搜索索引同步双语字段。
- [x] **标签双语**：`tags` 表新增 `name_en`；重命名接口与编辑界面同时改中英文名；
      前台标签云/标签页标题/相关文章按语言显示。
- [x] **测试与仓库卫生**：前端新增/更新 i18n、双语文章/标签、开关 UI、站点设置新字段、
      外壳属性通道、紧凑页头等测试；后端新增配置/viewmodel/服务单测与接口 e2e、
      「关闭数据库」的仓储错误路径测试、公开接口 400/500 分支 e2e；`.gitignore` 复核无需改动。

### 验证状态（2026-10-22 收尾复核）

- `tsc -b` 通过；`gofmt -l` 无输出；`go build` / `go vet` / `go test ./... -short` 全部通过。
- 前端 vitest 全量 **635 用例 / 45 个文件**全绿；覆盖率门禁**全局 100%（lines / statements /
  functions / branches 四项）**，报告在 `twilight-web/debug/cov-final/`。
- `python3 build.py --host-only` 已执行：前端重建、`internal/view/web` 嵌入资源同步、
  linux/amd64 产物编译成功（25.8 MiB）。
- Go 全仓覆盖率（`-coverpkg=./...`，合并各测试二进制）**93.4%**（8889 条语句中剩 588 条未覆盖），
  已达 100% 的部分：`internal/search`（含 manager / download / client / childproc）、
  根包 `main.go`（整条启动链重构为可注入的 `bootHooks` 后全部覆盖）、
  `internal/viewmodel/site.go`、`internal/config` 的新增字段。

### 剩余任务（Go 全仓 100% 的收尾，按文件从大到小）

> 下面每一项都是**错误分支**：正常路径早已覆盖，缺的是「仓储/依赖失败时会发生什么」。
> 手段已经跑通并验证可用：
>
> - 服务层：`internal/service/fakes_test.go` 里的「全部方法都失败」替身 + 逐方法可定制的 stub；
> - HTTP 层：`internal/view/handler_errors_e2e_test.go` 用 **SQLite 触发器**
>   （`BEFORE INSERT/UPDATE/DELETE … RAISE(ABORT)`）与「删表」制造故障，走真实 HTTP 栈；
> - 检索层：`internal/search` 的 `swap`/`hook` 间接层（OS 调用、HTTP、轮询时长都可替换）。

- [ ] `internal/service/user_service.go`（63 条）：注册 / 登录 / 改资料 / 改密 / 找回密码的
      全部错误分支（验证码仓库、账号仓库、发信器分别失败）。
- [ ] `internal/view/handler/auth.go`（56 条）：登录、登出、`/api/me`、邮箱验证码、注册、
      找回密码、改资料、改密、管理员资料各自的校验与错误映射。
- [ ] `internal/view/handler/comment.go`（43 条）：列表 / 点赞 / 取消点赞 / 创建 / 管理端
      评论管理的校验与错误映射。
- [ ] `internal/model/article_repo.go`（32）/ `moment_repo.go`（27）/ `comment_repo.go`（25）/
      `page_repo.go`（13）/ `user_repo.go`（11）：事务内每一步的失败（触发器 + 删表 + 关闭连接）。
- [ ] `admin_command.go`（28）/ `email_command.go`（15）/ `restore_command.go`（12）/
      `backup_command.go`（11）：命令行子命令的用法错误与运行失败分支。
- [ ] `internal/view/handler/moment.go`（23）/ `page.go`（23）/ `site.go`（20）/
      `media.go`（13）/ `install.go`（14）：各自的校验与错误映射。
- [ ] `internal/service/admin_auth.go`（21）/ `comment_service.go`（15）/ `media_service.go`（16）：
      剩下的是「媒体/管理员/评论」在具体前置条件下的分支。
- [ ] 每一批做完都重跑一次覆盖率：
      `go test ./... -short -count=1 -coverpkg=./... -coverprofile=debug/cover.out`，
      再用 `go tool cover -func=debug/cover.out | awk '$3 != "100.0%"'` 看剩余清单。

### 本轮做过的可测性重构（供下一轮参考）

- `main.go`：整条启动链拆成 `serve(bootHooks)`，`bootHooks` 逐项可替换
  （日志、锁文件、配置、编码器探测、数据库、凭据迁移、会话吊销、安装状态、
  检索启动、代理解析、路由注册、监听、退出码、信号通道）；`run(args, hooks, commands)`
  负责子命令派发；`exitFunc` 让 `main()` 能在测试里执行而不结束测试进程。
- `internal/search`：`osExecutable / osGetwd / absPath / statFile / readFile / writeFile /
  chmodFile / mkdirAll / randRead / execCommand / listenTCP / healthClient / fatalLog /
  requestShutdown / killGroup / configureChild / shutdownGrace / readyTimeout /
  readyInterval` 全部变成可替换的包级变量；`binaryNamesFor(goos, goarch)` 与
  `candidateIsSafelyOwnedFor(goos, info)` 参数化，Windows / macOS 分支在 Linux 上也能验证。
- `internal/search/download.go`：下载、解包、拷贝、校验各步骤都有间接层；
  `maxDownloadBytes` 由 const 改成 var（否则要写 512 MiB 才能验证体积上限）。
- `internal/search/client.go`：`taskPollTimeout` / `taskPollInterval` 可调。


---

- 增删查改文章时更新 melisearch
- [ ] 评论组件独立分块：把 Comments 从首屏包拆成 React.lazy chunk（文章页/独立页改 Suspense
      包裹），微语点击「评论」时才下载该 chunk。现在微语已经做到「点击前不发评论请求」
      （见 components/MomentComments.tsx），但 Comments 仍被 Post/PageDetail 静态引用，
      代码仍在主包里。
- [x] 微语显示日期时间（前台微语卡右上角，精确到分钟；前后台配图灯箱同时支持左右按钮＋滑动切换）
- [x] 全站 iOS 风格动效：动效令牌（app.css 的 --ease-*/--dur-*）、路由切换入场、卡片/缩略图/
      按钮的按压与悬停反馈、弹窗与轻提示的弹簧曲线、灯箱手势（横向拖动切换、下拉关闭）、
      文章/独立页面的阅读进度条（见 lib/motion.ts 与 components/media/MediaLightbox.tsx）。
- [x] 动效第二轮：按导航方向区分的推入/退回（PUSH 右、POP 左）、前进回顶/返回复位滚动位置、
      灯箱缩略图→全屏的共享元素放大与缩回、灯箱「第几张 / 共几张」、亮暗切换整页交叉淡入
      （View Transitions，不支持时直接切）、导航当前项常驻下划线、目录悬停位移、等待态呼吸。
- [x] 动效第三轮：图片占位加载（骨架扫光 → 图片淡入，失败态斜纹 + IMG 标记；头图、缩略图、
      头像、媒体库与 Markdown 正文图统一走 ProgressiveImage / enhanceImages）、视频首帧占位、
      列表骨架屏（首页/归档/标签/搜索/微语/评论/文章页）、评论与后台表格行错峰入场、
      目录滚动高亮当前小节、回到顶部按钮（离开一屏浮出，减少动态效果时直接跳）、
      点赞/复制/批量条/空态/侧栏盒子与媒体库的入场细节。
- [x] 动效第四轮（消除突变）：列表刷新不再整块清空——已有内容时旧数据留在原位、
      整块淡到 55%，新数据到达后原地替换（首页 / 微语 / 标签 / 搜索 / 评论，以及文章、
      页面、微语、评论、用户、媒体、标签六个后台列表），只有第一次加载才画骨架；
      骨架淡入；对话框关闭先播退场（140ms，期间移出可访问性树并禁用点击）再卸载；
      后台面包屑、设置页保存状态、编辑器同步状态换字时淡入；仪表盘计数卡与后台行式
      列表错峰落位；标签选中弹一下、标签云悬停撑开；分页器与字数计数器改为渐变。

---

## 移交 ink-design：组件上游清单（2026-10）

> 来源：`twilight-web`。只列与领域无关、其他应用也会重复写的部分；每条给出
> 站内实现、建议 API（参数与默认值）、必须保留的行为与验收点。
>
> 三条原则：① 只做增量 props，现有默认行为不变；② 不依赖博客 API 类型、
> react-router、SEO；③ 样式只用 ink-design 自己的 token 与 parts，站点 class
> （`.img-frame` / `.skl-*` / `.back-top` / `.rv` 等）不带走。
>
> 本仓库的 `ink-design` 是 `node_modules` 里的只读依赖；以下是给 ink-design
> 仓库的计划，不在本仓库实施。

### 来源速查

| 站内实现 | 行数 | 现有依赖 | 建议去向 |
| --- | --- | --- | --- |
| `components/ProgressiveImage.tsx` | 78 | React | 增强 `Image`（P0-1） |
| `components/BackToTop.tsx` | 43 | motion、icons | `BackTop` + `useScrolled`（P0-3） |
| `components/ErrorBoundary.tsx` | 61 | React | `ErrorBoundary`（P0-2） |
| `lib/dialog.tsx` | 130 | Modal、motion | `DialogProvider` + `useDialog`（P0-4） |
| `components/common.tsx` 的 `Reveal` | ~43 | React | `Reveal` / `useInView`（P1-1） |
| `common.tsx` 的 `ReadingProgress` + `lib/motion.ts` | ~35 | React | `ScrollProgress` + `useScrollProgress`（P1-2） |
| `lib/motion.ts` 的 `useActiveHeading` | 34 | — | `useScrollSpy` + `Anchor.spy`（P1-3） |
| `lib/motion.ts` 的 `useScrolled` | 22 | — | `useScrolled`（P0-3） |
| `components/ModeToggle.tsx` | 63 | IconButton、Icon、motion | `ThemeToggle` + `useColorMode`（P1-4） |
| `components/media/MediaLightbox.tsx` | 382 | `MediaItem`、motion | 升级 `ImagePreviewGroup`（P2-1） |

### 总览

- [ ] P0-0 动效令牌与 reduced-motion 工具
- [ ] P0-1 `Image` 增强（站内 `ProgressiveImage` 收敛）
- [ ] P0-2 `ErrorBoundary`
- [ ] P0-3 `BackTop` + `useScrolled`
- [ ] P0-4 `DialogProvider` + `useDialog`（含 `Modal` 退场）
- [ ] P1-1 `Reveal` / `useInView`
- [ ] P1-2 `ScrollProgress` + `useScrollProgress`
- [ ] P1-3 `useScrollSpy` + `Anchor.spy`
- [ ] P1-4 `ThemeToggle` + `useColorMode`
- [ ] P2-1 `ImagePreviewGroup` 升级（吸收 `MediaLightbox`）
- [ ] P2-2 Skeleton 预设（可选）
- [ ] P2-3 输入光标动效（方案记录，未实现）

---

### P0-0 动效令牌与 reduced-motion 工具（所有动效组件的共同前提）

站内已经在用这套时序，ink-design 目前只有 `--ease` 一个动效 token。建议新增
（不要改旧的 `--ease`，它已经用在大量颜色过渡上）：

```css
:root {
  --ease-ios: cubic-bezier(0.32, 0.72, 0, 1);
  --ease-out-ios: cubic-bezier(0.22, 1, 0.36, 1);
  --ease-spring: cubic-bezier(0.34, 1.56, 0.64, 1);
  --ease-in-out-ios: cubic-bezier(0.65, 0, 0.35, 1);
  --dur-tap: 140ms;
  --dur-fast: 200ms;
  --dur: 320ms;
  --dur-slow: 520ms;
}
```

```ts
/** 同步判断；matchMedia 不存在时返回 false（老浏览器 / 测试环境不抛错）。 */
export function prefersReducedMotion(): boolean
/** 订阅版本：系统偏好变化时触发重渲染，供 hover/自动播放类组件使用。 */
export function usePrefersReducedMotion(): boolean
/** Web Animations API 里要用字符串曲线，与 --ease-out-ios 保持一致。 */
export const IOS_EASE_OUT = 'cubic-bezier(0.22, 1, 0.36, 1)'
```

- 库已有 `@media (prefers-reduced-motion: reduce)` 的全局兜底（0.01ms），新组件仍需
  在 JS 里判断一次：WAAPI、`scrollTo({ behavior })`、计时卸载这些 CSS 管不到。
- 验收：matchMedia 缺失/命中时都不抛错；`usePrefersReducedMotion` 能响应运行时变化。

---

### P0-1 `Image` 增强（不要新增 `ProgressiveImage`）

ink-design 的 `Image` 已有 `placeholder` / `fallback` / `preview` / `src` 变化重置。
站内另写了 `ProgressiveImage`，缺的只是下面这些行为；补进 `Image` 后站点删掉本地实现。

```ts
export interface ImageProps extends Omit<React.ComponentPropsWithoutRef<'img'>, 'placeholder' | 'onError'> {
  src?: string
  /** 已有：备用图 URL（第一次 onError 时切换） */
  fallback?: string
  /** 已有：占位内容 */
  placeholder?: React.ReactNode
  /** 已有：点击预览 */
  preview?: boolean | ImagePreviewConfig
  /** 现有实现会调用 onError，但类型被 Omit 掉了；建议补回并转给 img */
  onError?: (event: React.SyntheticEvent<HTMLImageElement>) => void

  /* ===== 新增 ===== */
  /** 加载前的宽高比（number 或 CSS 字符串，如 16 / 9）；加载完成后交还图片。 */
  aspectRatio?: number | string
  /** 块级显示（文章头图 / 正文图）；默认 false，保持现有 inline-flex。 */
  block?: boolean
  /** 加载完成淡入；默认 true，需要完全兼容旧观感可传 false。 */
  fade?: boolean
  /** 用内置扫光骨架作为占位；默认 false，与 placeholder 二选一。 */
  skeleton?: boolean
  /** src 与 fallback 都失败时渲染的内容；默认沿用 placeholder。 */
  failedContent?: React.ReactNode
  /** 状态变化回调。 */
  onLoadStateChange?: (state: 'loading' | 'loaded' | 'failed') => void
}
```

**必须保留的行为（每条都是站内踩过的坑）：**

- `src` 变化时在**渲染期**重置状态，不能等 `useEffect`：否则会闪一帧旧图 / 旧骨架。
- 用 ref 回调读 `img.complete` 与 `naturalWidth`，缓存命中直接落终态，**不闪骨架**；
  SSR / 无 `window` 时按 `loading` 处理，不访问 `complete`。
- 骨架必须是 `<img>` 的**兄弟节点**：替换元素没有可用的伪元素盒子；外框同时负责
  定位与裁切（`overflow: hidden`）。
- 淡入用 `animation`（keyframes `to { opacity: 1 }`），不要用 `opacity` 的 transition：
  调用方常在同一元素上有自己的 `transform` 过渡（悬停放大），transition 会互相覆盖。
- `aspectRatio` 只在未 `loaded` 时生效；失败态也保留占位比例，避免高度跳变。
- `alt` 缺省为 `''`（装饰图）；骨架 `aria-hidden`。
- 站内参数：文章头图 `16 / 9`；扫光 1.5s linear；淡入 320ms；失败态斜纹 + `IMG` 标记。

**验收：** 缓存命中不闪；失败切换 `fallback`；`src` 替换重置；`fade=false` 保持旧行为；
`aspectRatio` 加载前后高度正确；SSR 不报错；与 `preview` 组合正常。

---

### P0-2 `ErrorBoundary`

```ts
export interface ErrorBoundaryProps {
  children?: React.ReactNode
  /** 回退内容；函数形式拿到 error 与 reset */
  fallback?: React.ReactNode | ((error: Error, reset: () => void) => React.ReactNode)
  /** 错误上报；库不强制打印 console */
  onError?: (error: Error, info: React.ErrorInfo) => void
  /** 任一值变化时自动 reset */
  resetKeys?: readonly unknown[]
}
```

- 只能用 class（`getDerivedStateFromError` / `componentDidCatch` 没有函数式等价物）。
- 回退界面必须能在 Router / Provider 故障时渲染：**不依赖任何 hook / context**，
  只接收 `error` 与 `reset` 两个参数。站点当前的品牌版回退由调用方通过 `fallback` 传入。
- 默认回退保持极简（一行文案 + 「重试」），不要绑定具体应用的样式。
- 验收：抛错后显示 fallback；`resetKeys` 变化恢复 children；`onError` 收到 error 与
  `ErrorInfo`；重复渲染不重复上报。

---

### P0-3 `BackTop` + `useScrolled`

```ts
export interface BackTopProps {
  /** 离开页首多少 px 出现；默认 480 */
  threshold?: number
  /** 滚动容器；默认 window */
  target?: () => HTMLElement | Window | null
  /** 回到顶部的滚动行为；默认 'smooth' */
  behavior?: ScrollBehavior
  icon?: React.ReactNode
  /** aria-label / title；默认「回到顶部」 */
  label?: string
  /** 受控覆盖；不传由 useScrolled 决定 */
  visible?: boolean
  onClick?: () => void
  className?: string
  style?: React.CSSProperties
}

export function useScrolled(
  threshold?: number,
  target?: () => HTMLElement | Window | null,
): boolean
```

- 不可见时同时 `aria-hidden` 与 `tabIndex={-1}`：看不见的按钮不进 Tab 顺序。
- `prefersReducedMotion()` 时用 `behavior: 'auto'`，不要 smooth。
- `useScrolled` 初值直接读当前滚动位置（刷新后停在中段时首帧就在）；
  scroll 监听 passive；threshold 变化时立刻按新阈值对齐。
- 站内参数：480px；44 × 44；右下 26px（窄屏 16px）；半透明底 + `backdrop-filter: blur(10px)`。
- 验收：刷新停在中段首帧可见；减少动态效果直接跳；隐藏时读屏与 Tab 都跳过。

---

### P0-4 `DialogProvider` + `useDialog`（建议同时给 `Modal` 加退场）

```ts
export interface DialogProviderProps {
  children: React.ReactNode
  okText?: string          // 默认「确定」
  cancelText?: string      // 默认「取消」
  /** 退场时长；默认 140ms，prefers-reduced-motion 时为 0 */
  closeDuration?: number
  className?: string
}

export interface DialogOptions {
  okText?: string
  cancelText?: string
  danger?: boolean
  closable?: boolean
  maskClosable?: boolean
}

export interface PromptOptions extends DialogOptions {
  placeholder?: string
  initialValue?: string
  maxLength?: number
  /** 打开时全选初值；默认 true */
  selectAll?: boolean
  /** 返回错误文案则不关闭；返回 null 表示通过 */
  validate?: (value: string) => string | null
}

export interface DialogApi {
  confirm: (title: string, message?: string, options?: DialogOptions) => Promise<boolean>
  prompt: (title: string, options?: PromptOptions) => Promise<string | null>
}

export function DialogProvider(props: DialogProviderProps): React.ReactElement
export function useDialog(): DialogApi
```

**行为要求：**

- 关闭时**先 resolve**（调用方 `await` 立即继续），界面再留 `closeDuration` 播退场，
  然后卸载。否则按「确定」的瞬间整块浮层消失，像被切断。
- 退场期间：浮层 `aria-hidden`、遮罩 `pointer-events: none`、`closing` 守卫挡住重复
  `close`（同一个 promise 不能 resolve 两次）。
- Esc / 点遮罩 / 右上角 × 都走 cancel；prompt 内 Enter 提交；打开即全选初值。
- `validate` 返回非 null 时保持打开（错误文案交给调用方显示）。
- 单实例限制：并发调用后者覆盖前者（站点现状），请在文档写明；若要队列化可另行设计。
- 建议先把退场做进 `Modal` 本身，这样所有 Modal 都受益，`DialogProvider` 只负责
  Promise 与内容。给 `Modal` 增加的参数：

  ```ts
  export interface ModalProps {
    /** open 变 false 后的退场时长；默认 0 = 立即卸载（保持现状） */
    closeDuration?: number
  }
  ```

  `afterClose` 的触发时机相应改为退场结束之后。
- 验收：退场窗口内重复点击只 resolve 一次；减少动态效果不等退场；Esc/遮罩取消；
  prompt 校验不通过不关闭。

---

### P1-1 `Reveal` / `useInView`

```ts
export interface RevealProps {
  children?: React.ReactNode
  /** 渲染的标签，默认 'div' */
  as?: React.ElementType
  /** 错峰延迟（毫秒），默认 0 */
  delay?: number
  /** IntersectionObserver 阈值，默认 0.06 */
  threshold?: number
  /** 默认 '0px' */
  rootMargin?: string
  /** 只播一次，默认 true */
  once?: boolean
  /** true 时直接显示，不观察 */
  disabled?: boolean
  className?: string
  style?: React.CSSProperties
}

export function useInView<T extends Element>(options?: {
  threshold?: number
  rootMargin?: string
  once?: boolean
  root?: Element | null
}): [React.RefObject<T | null>, boolean]
```

- `IntersectionObserver` 不存在（SSR / 老浏览器）时直接可见；
  `prefers-reduced-motion` 时也直接可见，不播动画。
- `once` 命中后 unobserve；延迟只通过 CSS 变量 `--ink-reveal-delay` 表达。
- 站内参数：threshold 0.06；时长 520ms（`--dur-slow`）；调用方把错峰 clamp 在 200ms 内。
- 验收：进入视口前不可见、命中后可见且只一次；无 IO 时不报错直接显示；减少动态效果直接显示。

---

### P1-2 `ScrollProgress` + `useScrollProgress`

```ts
export interface ScrollProgressProps {
  /** 自定义滚动容器；默认文档 */
  target?: React.RefObject<HTMLElement | null> | (() => HTMLElement | null)
  /** 默认 'top' */
  position?: 'top' | 'bottom'
  /** 默认 2（px） */
  thickness?: number
  /** 默认 100 */
  zIndex?: number
  className?: string
}

/** 0–1；内容不足一屏时恒为 0。 */
export function useScrollProgress(target?: ScrollProgressProps['target']): number
```

- 用 `transform: scaleX()` 而不是 width；**不要加 transition**（进度必须跟手）。
- 组件 portal 到 body：任何带 transform 的祖先都会让 `position: fixed` 失效。
- 初值先算一次；scroll/resize 都挂 passive；Safari 橡皮筋要 clamp 到 [0,1]。
- `aria-hidden` 恒为 true（纯视觉反馈）。
- 站内参数：top、2px、z-index 100。
- 验收：内容不足一屏为 0；滚动到底为 1；不触发额外布局。

---

### P1-3 `useScrollSpy` + `Anchor.spy`

```ts
export function useScrollSpy(
  ids: readonly string[],
  options?: {
    /** 视口上方多少 px 视为「已经越过」；默认 96 */
    offset?: number
    root?: Element | null
  },
): string | null
```

`Anchor` 新增（或导出配套 hook）：

```ts
export interface AnchorProps {
  // ...现有 props
  /** 打开内置滚动观测，自动维护 activeHref */
  spy?: boolean
  /** 默认 96 */
  spyOffset?: number
}
```

- 每次 scroll/resize 读一遍标题的 `getBoundingClientRect().top`，不要用
  IntersectionObserver：两节同时可见时观察器说不出「当前是哪节」。
- 按文档顺序遍历：缺失的 id `continue`（跳过而不是中断），第一个仍在 offset 以下的
  标题之后 `break`；空数组 → null。
- 初值先算一次（从目录点进来时首帧就高亮）；监听 passive。
- 站内参数：offset 96。
- 验收：顶部时为 null；滚到某小节高亮该项；缺失 id 不影响后续；空数组安全。

---

### P1-4 `ThemeToggle` + `useColorMode`

```ts
export type ColorMode = 'light' | 'dark'

export interface UseColorModeOptions {
  /** 默认 'inkui-mode' */
  storageKey?: string
  /** 写到 <html> 的属性；默认 'data-mode' */
  attribute?: `data-${string}`
  /** 默认 'system' */
  defaultMode?: ColorMode | 'system'
  /** 关闭 View Transitions 交叉淡入 */
  disableTransition?: boolean
}

export interface UseColorModeResult {
  mode: ColorMode
  setMode: (mode: ColorMode) => void
  toggle: () => void
}

export function useColorMode(options?: UseColorModeOptions): UseColorModeResult

export interface ThemeToggleProps {
  storageKey?: string
  attribute?: `data-${string}`
  icons?: { light?: React.ReactNode; dark?: React.ReactNode }
  labels?: { light?: string; dark?: string }
  onModeChange?: (mode: ColorMode) => void
  className?: string
}
```

- 切换整页配色用 View Transitions：`document.startViewTransition(() => flushSync(apply))`，
  回调里必须 `flushSync`，否则晚一帧就变成「先切完再截两帧」。
- 不支持该 API 或 `prefersReducedMotion()` 时直接切，不做半截动画。
- localStorage 读写要 try/catch（隐私模式）。
- 若用于 SSR，建议额外导出 `getInitialMode(options)` 与 `colorModeScript(options)`
  （内联脚本字符串），避免首屏白闪；站点目前只做了客户端。
- 站内参数：storageKey `tw_mode`；attribute `data-mode`；交叉淡入 320ms。
- 验收：切换后 `<html>` 属性与存储一致；系统兜底；无 VT 时不报错；减少动态效果直接切。

---

### P2-1 `ImagePreviewGroup` 升级（吸收站内 `MediaLightbox`）

这是这批里最大的一件，建议单独一个 minor。先把 item 类型泛化，去掉对
`twilight-blog-api` 的 `MediaItem` 依赖。

```ts
export interface ImagePreviewItem {
  id?: string | number
  src: string
  alt?: string
  /** 默认 'image' */
  type?: 'image' | 'video'
  /** 视频封面 */
  poster?: string
}

export interface ImagePreviewOptions {
  open?: boolean
  index?: number
  onIndexChange?: (index: number) => void
  onClose?: () => void
  /** 'none' | 'fade' | 'shared'（共享元素）；默认 'fade' */
  zoom?: 'none' | 'fade' | 'shared'
  /** 横向拖动换图 + 下拉关闭；默认 true */
  gestures?: boolean
  /** ← → Esc；默认 true */
  keyboard?: boolean
  /** 默认 false */
  loop?: boolean
  /** 计数；默认 true */
  counter?: boolean | ((index: number, total: number) => React.ReactNode)
  /** 默认 true */
  closeOnMask?: boolean
  /** 默认 260ms */
  transitionDuration?: number
}

export interface ImagePreviewGroupProps {
  items: ImagePreviewItem[]
  preview?: ImagePreviewOptions
  children?: React.ReactNode
}
```

**行为与参数：**

- 共享元素：调用方在点击时把缩略图的 `getBoundingClientRect()` 传进来（或由
  `Image`/`ImagePreviewGroup` 自己记录触发器），用 WAAPI 从「缩略图矩形」放到
  「灯箱矩形」；任意一边量不出尺寸（图片未解码 / display:none / jsdom）就退化成 fade。
- 手势常量（站内实测）：`DRAG_SLOP = 8`、`SWITCH_THRESHOLD = 60`、
  `DISMISS_THRESHOLD = 120`、`EXIT_DURATION = 260`、`ZOOM_EXIT_DURATION = 240`（ms）。
- 视频：当前张 `controls` + `autoPlay`，相邻张只 `preload="metadata"`；首帧前用骨架占位，
  `loadeddata`/`error` 后收起。
- 灯箱只挂当前张与相邻两张，避免几十张图同时解码。
- a11y：`role="dialog"` + `aria-modal`、焦点陷阱、关闭后焦点回到触发器、body 滚动锁、
  每张图 `alt`；计数给读屏。
- 减少动态效果：不播缩放 / 手势回弹，功能保留。
- 现有 `ImagePreview` 的行为保持；新能力通过 `preview` 配置增量开启。
- 验收：单张不显示计数；首尾边界按钮与键盘不越界；拖动与点击不冲突；视频能播；
  缩放起点与缩略图重合；关闭动画结束才卸载。

---

### P2-2 Skeleton 预设（可选）

站内三种成品构图可以直接用 `Skeleton` 原语拼，不急着上游。若要做成预设：

```ts
export interface SkeletonCardProps {
  /** 头图宽高比；默认 16 / 9 */
  coverRatio?: number
  /** 默认 3 */
  lines?: number
  count?: number
}

export interface SkeletonListProps {
  /** 默认 3 */
  count?: number
  /** 每行高度；默认 64 */
  height?: number
}
```

站内 `SkeletonArticle` 是文章页专用构图，建议留在站点，不做成库预设。

---

### P2-3 输入光标动效（仅记录方案，未实现）

站内评估过，暂时不做；若 ink-design 要做，落点是 `Input` 而不是新组件。

```ts
export interface InputProps {
  /** 默认 'native'；'animated' 才启用自绘光标 */
  caret?: 'native' | 'animated'
  /** 闪烁周期；默认 1060ms */
  caretBlink?: number
  /** 输入时的轻微回弹；默认 100ms */
  caretPop?: number
}

export function useCaret(
  ref: React.RefObject<HTMLInputElement | HTMLTextAreaElement | null>,
  options?: { blink?: number; enabled?: boolean },
): void
```

- 需要：同字体/内边距/宽度的镜像元素量位置、`selectionchange`、内部滚动、输入法
  composition、密码圆点数宽；`type="number"` 的 `selectionStart` 不可靠，自动退回原生。
- iOS 的原生光标/选择手柄是系统 UI，`caret-color: transparent` 不总是可靠 → 移动端退回原生。
- 默认必须是 `native`；`prefers-reduced-motion` 时常亮或极慢呼吸。
- 验收：输入、方向键、点击换位、换行、滚动、中文候选、删除、粘贴都不漂移；
  关闭该 prop 时与现在完全一致。

---

### 已有组件的收敛（不新增 API）

| 组件 | 结论 |
| --- | --- |
| `Image` | 按 P0-1 增强；站点 `ProgressiveImage` 删除 |
| `Empty` | API 不变；站点改用 `Empty`（`description`），入场动画用 `className` 覆盖 |
| `Avatar` | API 不变；站点首字母头像用 `children` + `size`，删掉本地 `Avatar` |
| `Skeleton` | API 不变；站点三种构图可保留，或按 P2-2 做预设 |
| `Anchor` | 增加 `spy` / `spyOffset`（P1-3） |
| `Spin` | 站点保留「LOADING…」呼吸文案，不强行改用 Spin |
| `Modal` | 增加关闭退场（P0-4），所有 Modal 受益 |

### 明确不上游（留在站点或独立包）

- `Comments` / `MomentComments`：登录、点赞、回复、评论 API。
- `ContentView` + `lib/markdown.ts` + `enhanceCodeBlocks` / `enhanceImages`：
  Markdown 管线（hljs/KaTeX/主题），适合独立包，不进 UI 库。
- `MediaThumb` / `MediaGallery` / `MediaPicker` / `CoverPicker` / `AvatarPicker` /
  `AuthorAvatar`：依赖 `twilight-blog-api` 的 `MediaItem` / 用户模型。
- `PublicShell` / `PublicSidebar` / `AdminLayout` / `PageHero` / Footer：站点版式。
- `lib/head.ts`、`site.ts`、`nav.ts`、`flags.ts`、`validate.ts`、`format.ts`、
  `password.ts`：SEO 与工具，归共享工具包。
- `icons.tsx` / `toast.tsx`：站点图标集与十行 message 包装，没有必要。
- `lib/motion.ts` 的 `useRouteMotion`：绑定 react-router 的 push/pop 方向与滚动恢复，
  放独立的 `@twilight/router-motion` 更合适。
- 列表刷新的 `[data-busy]` 模式：属于应用的数据层（旧内容变暗 + 首次才骨架），
  不是组件；若要做，做成 `useRefresh` 之类的 hook 再单独评估。

### 交付验收（每条都适用于 P0–P2）

- [ ] 增量 props；现有默认行为不变；semver 只升 minor。
- [ ] 不新增对 react-router / 领域类型 / SEO 的依赖。
- [ ] SSR 安全：无 `window` / 无 `IntersectionObserver` / 无 `matchMedia` 时不抛错。
- [ ] 减少动态效果分支有用例；计时类组件用 fake timers 测试。
- [ ] 导出到 `index.ts`；样式进 `styles/parts/*.css`；补文档与 demo。
- [ ] ink-design 仓库自身覆盖率达标（新组件 100%）。
- [ ] 发布后站点替换本地实现、删除本地测试；站点覆盖率范围随之变化。
