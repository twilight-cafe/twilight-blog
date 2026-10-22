# ink-design 0.1.3 问题清单（0.1.4 已全部修复）

- 复核版本：`ink-design@0.1.3`（npmjs，2026-10-17T15:44:24Z 发布）
- 复核方式：解包注册表 tarball 与 `node_modules/ink-design` 逐文件比对；站点按
  `vite.config.ts` 的别名直接消费包内 `src/`，运行时行为与源码一致。
- 结论：**0.1.3 的发布产物没有重建**（`dist/` 与 0.1.2 逐字节相同），因此注册表
  消费者拿不到任何新能力；源码消费路径则被另外几处打包/类型问题绊住。下面按
  影响排序。

| # | 严重度 | 位置 | 一句话 |
| --- | --- | --- | --- |
| 1 | P0 | `dist/*` | 0.1.3 的 `dist/` 与 0.1.2 完全相同，新组件/新导出/新样式都没进产物 |
| 2 | P1 | `src/styles/parts/47-extend-motion.css` | 全局补间片段是孤儿，没有任何组件 import 它 |
| 3 | P1 | 12 个 `src/*.tsx` | 片段样式混用 `@/styles/parts/` 与相对路径，源码消费方必须自带 `@/` 别名 |
| 4 | P2 | `package.json` | `"type": "commonjs"` 与 `src/**` 的 ESM 语法矛盾，源码类型检查直接报 TS1286 |
| 5 | P3 | `README.md` | 文档写 `Skeleton.Card` / `Skeleton.List`，实际导出是 `SkeletonCard` / `SkeletonList` |
| 6 | P3 | `DatePicker.tsx` | 单选调用方的 `onChange` 类型仍被声明成 `Date \| Date[] \| null`，没有按 `multiple` 收窄 |
| 7 | P1 | `src/internal/TextCaret.tsx` | 自绘光标只在事件里重测，布局位移（滚动条出现等）后停在旧视口坐标，竖线偏离文字 |

## 0.1.4 复核（2026-10-18）

`ink-design@0.1.4`（2026-10-18T06:42Z 发布）逐条对照后的状态——**7 条全部修复**，
站点随之删掉了两处临时接线（见文末「站点侧的处置」）：

| # | 0.1.4 的处置 | 站点侧核对方式 |
| --- | --- | --- |
| 1 | `dist/` 重建，`BackTop.d.ts` / `Dialog.d.ts` / `Reveal` / `ScrollProgress` 等全部进产物 | `ink-design-alias.test.ts` 断言 dist 覆盖站点用到的全部具名导入 |
| 2 | `21-inkui-media.css` 用 `@import` 引入 `47-extend-motion.css` | `inkui-css-parts.test.ts` 的 `KNOWN_ORPHANS` 清空 |
| 3 | 12 个组件改用相对路径，源码里不再出现 `@/styles/parts/` | 既有的路径解析 / 深度一致性断言照常通过 |
| 4 | `package.json` 改为 `"type": "module"` | 源码消费的 TS1286 消失 |
| 5 | README 改为 `SkeletonCard` / `SkeletonList` 具名导出 | — |
| 6 | `DatePicker` 按 `multiple` 收窄为判别联合（单选只给 `Date \| null`） | `tsc -b` 通过 |
| 7 | `TextCaret` 用 `ResizeObserver`（target + `documentElement`）重测，补 `visualViewport` / `fonts.ready`，首次测量前保留原生光标，镜像补全 `text-indent` / `font-variant` 等 | 真浏览器复测：去掉站点槽位后，滚动条出现那一刻光标自动归位 |

复核方式：`diff -rq <0.1.3>/src <0.1.4>/src` 逐文件比对 + 站点全量测试 + 无头
Chromium 实测（见 `debug/search-caret/` 的复现脚本）。

---

## 1（P0）发布产物未重建：`dist/` 仍是 0.1.2

**现象**：0.1.3 的 tarball 里 `src/` 包含 `ErrorBoundary` `BackTop` `Reveal`
`ScrollProgress` `ThemeToggle` `DialogProvider` 等新实现，但 `dist/index.mjs`、
`dist/index.d.ts`、`dist/inkui.css` 与 0.1.2 的文件**逐字节相同**。

**证据**（解包两份 tarball 后）：

```bash
$ diff -rq 0.1.2/dist 0.1.3/dist        # 无输出
$ grep -c ErrorBoundary node_modules/ink-design/dist/index.mjs       # 0
$ grep -c ErrorBoundary node_modules/ink-design/dist/index.d.ts      # 0
$ grep -c ink-scroll-progress node_modules/ink-design/dist/inkui.css # 0
```

而 `src/index.ts` 里这些导出都在：

```ts
export { ErrorBoundary } from './ErrorBoundary';
export { BackTop, useScrolled } from './BackTop';
export { Reveal, useInView } from './Reveal';
export { ScrollProgress, useScrollProgress } from './ScrollProgress';
export { DialogProvider, useDialog } from './Dialog';
export { ThemeToggle } from './ThemeToggle';
export { getInitialMode, useColorMode } from './useColorMode';
export { IOS_EASE_OUT, prefersReducedMotion, usePrefersReducedMotion } from './motion';
```

**影响**：

- 注册表消费者 `import { BackTop } from 'ink-design'` 得到 `undefined`（运行时抛错）；
- TS 用户看不到任何新类型，`dist/index.d.ts` 里没有 `DialogProvider` / `Reveal` /
  `useColorMode` 等；
- README 描述的能力与发布产物完全脱节，等于 0.1.3 对注册表用户不可用；
- 附带后果：0.1.3 对 `DatePicker` 等组件的类型改动也被旧的 `d.ts` 掩盖（见第 6 条）。

**建议**：发布流程里强制先 `npm run build`，并在 CI 增加一条「版本号变更后
`dist/index.d.ts` 必须包含本次新增导出」的断言；`files` 里的 `dist` 不应是
从旧版本残留的目录（清空后重建）。

**站点侧临时处置**：见文末「临时处置」——运行时走 `src/`，类型由
`src/types/ink-design.d.ts` 补齐。

## 2（P1）`47-extend-motion.css` 是孤儿片段

**现象**：`src/styles/parts/47-extend-motion.css` 只被整表 `src/styles/inkui.css`
`@import`，**没有任何组件 import 它**。而站点消费的是 `src/index.ts` 这条
「样式跟着组件走」的入口——于是这段「全局状态补间」不会进入站点 CSS。

**证据**：

```bash
$ grep -rn "47-extend-motion" node_modules/ink-design/src
node_modules/ink-design/src/styles/inkui.css:54:@import "./parts/47-extend-motion.css";
```

**影响**：README 的「全局状态补间（`*` 只补颜色/描边/阴影/不透明度）」在源码
消费模式下不生效；同一条动效在「整表样式」与「按组件样式」两条路径下表现不一致。
如果调用方为了行为一致而手动 `import 'ink-design/dist/inkui.css'`，又会把未使用组件
的样式全部带上（P2-4 拆分的目的正好被抵消）。

**建议**：让 `47-extend-motion.css` 被根级/基础片段引用（例如由
`01-extend-root.css` 或 `21-inkui-media.css` 带出），或提供
`ink-design/motion.css` 子路径并在 README 里说明。

**站点侧临时处置**：`inkui-css-parts.test.ts` 的 `KNOWN_ORPHANS` 白名单显式列出
这一项；上游补上 import 后，白名单的反向断言会变红，提醒回来删除。

## 3（P1）片段样式混用相对路径与 `@/` 别名

**现象**：0.1.3 新增的片段导入中，12 个文件写成 `@/styles/parts/*.css`，
其余组件（如 `Button.tsx`）用相对路径 `./styles/parts/*.css`。

**证据**：

```bash
$ grep -rln "import '@/styles/parts/" node_modules/ink-design/src --include=*.tsx
src/AutoComplete.tsx src/Avatar.tsx src/Calendar.tsx src/List.tsx
src/Progress.tsx src/Radio.tsx src/SegmentedControl.tsx src/Splitter.tsx
src/Steps.tsx src/Switch.tsx src/Timeline.tsx src/internal/TextCaret.tsx
```

**影响**：源码消费方必须在自己的构建器里定义 `@/` → `ink-design/src/` 的别名，
否则构建期 `UNRESOLVED_IMPORT`（样式整段丢失或构建失败）。这是隐式契约：包的
`package.json` / README 都没有提到它；而包**没有** `sideEffects` 字段、也没有
子路径导出，调用方只能通过阅读源码发现。

**建议**：统一改回相对路径（与 `Button.tsx` 一致）；或者在 README 顶部把
「源码消费需要 `@/` 别名」写成正式契约，并在 `exports` 里提供对应入口。

**站点侧临时处置**：`vite.config.ts` / `vitest.config.ts` 已把 `@/` 映射到
`node_modules/ink-design/src`（P2-4 时就这么配的），`inkui-css-parts.test.ts`
的片段可解析性断言会同时接受两种前缀。

## 4（P2）`"type": "commonjs"` 与 ESM 源码矛盾

**现象**：`package.json` 仍是 `"type": "commonjs"`，而 `src/**` 全是 ESM
（`import` / `export`）。把 `src` 纳入 TypeScript 检查时逐文件报：

```text
error TS1286: ESM syntax is not allowed in a CommonJS module when 'verbatimModuleSyntax' is enabled.
error TS1287: A top-level 'export' modifier cannot be used on value declarations in a CommonJS module ...
```

**影响**：源码消费方的 `tsc` 无法直接检查 `src/`（必须关掉 `verbatimModuleSyntax`
或改用 `dist` 类型——而 `dist` 又落后，见第 1 条）。这正是 0.1.3 的新 API
「运行时可 import、类型不可用」的根因之一。

**建议**：`"type": "module"`（`dist/index.cjs` 与 `dist/index.mjs` 是显式扩展名，
不受该字段影响），或把 `src` 拆成独立子包/子路径并单独声明模块类型。

**站点侧临时处置**：不修改第三方包，改为站内类型补丁（见文末）。

## 5（P3）README 的 `Skeleton.Card` 与实际导出不一致

**现象**：README 写「`Skeleton.Card` / `Skeleton.List`：文章卡片与列表骨架预设」，
但 `Skeleton.tsx` 只导出 `SkeletonCard` / `SkeletonList`（`Skeleton.Card` 只是
`displayName`，并未挂到 `Skeleton` 对象上）。

**影响**：照文档写 `Skeleton.Card` 会在编译期报错（或运行期 `undefined`）。

**建议**：README 改为具名导出；或真的给 `Skeleton` 挂静态属性并写类型。

## 6（P3）`DatePicker` 的 `onChange` 类型未按 `multiple` 收窄

**现象**：`DatePickerProps.onChange?: (date: Date | Date[] | null, dateString: string | string[]) => void`，
但 `multiple=false`（默认）时运行时只会回传 `Date | null`。同理 `value` 也是
`DatePickerValue = Date | readonly Date[] | null`。

**影响**：单选调用方被迫处理 `Date[]` 分支（或断言）；服务端返回的数据结构也
无法从类型上区分。站点的 `Editor.tsx` 正是这种写法：

```tsx
<DatePicker value={publishedAt} onChange={(date) => setPublishedAt(date)} ... />
// publishedAt: Date | null —— 用 src 类型检查时这里会报 Date[] 不可赋值
```

**建议**：给 `DatePicker` 加 `multiple` 的判别联合（重载或泛型），至少让
`onChange` 与 `multiple` 联动。

## 7（P1）自绘光标只在事件里重测，布局位移后停在旧坐标

**现象**：输入框聚焦后，ink-design 把原生光标设为透明
（`.ix-caret-target { caret-color: transparent }`），改由 portal 到 `body` 的
`position: fixed` 竖线跟随 `selectionStart`。这根竖线用的是**视口坐标**，只在
`focus / keydown / keyup / mouseup / click / input / selectionchange` 以及
`window` 的 `scroll / resize` 里重新测量。任何不伴随这些事件的布局位移，都会
让它停在原地。

**复现**（站点搜索页，真浏览器，headless chromium 156）：

1. 窗口高到「搜索前页面不滚动」（1920×1080、1440×1200 均可）；
2. 在搜索框输入关键词、回车；结果把页面撑高，经典滚动条出现，占去 10px 宽；
3. 居中的内容整体左移 5px；`window.innerWidth` 没有变化，浏览器**不派发
   `resize`**，也没有 `scroll` 事件；
4. 竖线留在旧位置：文字右端实测 432.83px，竖线 437.84px —— 5px 的缝
   （1280×1150 下是 2px，位移量等于滚动条宽度的一半）。

```text
输入后： input.left 350  expected 437.83  drawn 437.84   off 0.01px
回车后： input.left 345  expected 432.83  drawn 437.84   off 5.01px
```

**根因**：光标与输入框不在同一个坐标系（`fixed` 视口坐标 vs 文档流），却缺少
「布局变了就重测」的机制。`position: fixed` 还意味着祖先的 transform、祖先容器
的滚动、视觉视口缩放（`visualViewport`）都不会自动反映到它身上。

同一处还有精度缺口：`measureCaret` 的镜像元素只复制了
`font / letterSpacing / textTransform / wordBreak / overflowWrap`，没有复制
`text-indent`、`word-spacing`、`font-variant` / `font-feature-settings`
（例如 `tabular-nums`）——这些都会让镜像算出的字宽与真实字宽不等。

**影响**：凡是「输入框聚焦时页面或容器尺寸变化」的场景都会留下偏位的竖线，
直到用户下一次按键才复位；而原生光标此时已经被隐藏，用户没有正确的兜底。
站点上最典型的就是搜索页：提交后结果把页面撑成可滚动，光标立刻偏到文字右侧。

**建议**：

1. 用 `ResizeObserver` 观察 target 与 `document.documentElement`（滚动条槽位
   变化只改布局视口宽度，不改 `innerWidth`），在 rAF 里重测；`document.fonts.ready`
   后再测一次；
2. 更彻底的做法是不要再跨坐标系：把光标渲染进输入框自己的定位容器
   （`position: absolute`，原点取输入框的 padding 边），祖先位移与滚动自然跟随，
   事件表也可以整段删掉；
3. 若保留视口坐标，至少补上 `visualViewport` 的 `resize` / `scroll`，并在测不到
   位置时**隐藏**而不是保留旧坐标；
4. 自绘光标第一次成功测量之前先留着原生光标，避免「假光标错了、真光标又没了」。

---

## 站点侧的处置

1. ~~**类型补丁** `src/types/ink-design.d.ts`~~：0.1.4 重建 dist 后**已整份删除**，
   类型重新来自包自身。`ink-design-alias.test.ts` 现在守两件事：dist 必须覆盖
   站点用到的每个具名导入；补丁文件不得再出现（一旦回来，说明类型层面又分叉了
   一个 API）。
2. ~~**孤儿白名单** `inkui-css-parts.test.ts` 的 `KNOWN_ORPHANS`~~：0.1.4 起由
   `21-inkui-media.css` 用 CSS `@import` 带上 `47-extend-motion.css`，白名单清空；
   扫描逻辑同时把 CSS `@import` 计为「有导入方」（Vite 会内联它），否则这条永远
   删不掉。
3. **滚动条槽位** `app.css` 的 `html { scrollbar-gutter: stable }`：**长期保留**。
   它不是绕上游缺陷的补丁，而是站点自己的布局稳定性处置——页面在「不滚动 ↔
   可滚动」之间切换时，内容不再横移几个像素（上游第 7 条修好的是光标错位，
   跳动的还是内容本身）。

## 复核命令

```bash
cd twilight-web
npm view ink-design versions --registry=https://registry.npmjs.org/
diff -rq <0.1.3>/src <0.1.4>/src                     # 0.1.4 的改动面
diff -rq <0.1.2>/dist <0.1.3>/dist                   # 为空 = 0.1.3 产物未重建
grep -rn "47-extend-motion" node_modules/ink-design/src   # 只有 inkui.css 一处
grep -rln "import '@/styles/parts/" node_modules/ink-design/src --include=*.tsx
grep -n '"type"' node_modules/ink-design/package.json
npx tsc -b                                          # 站点：100% 通过（靠本地类型补丁）
npx vitest run --coverage --maxWorkers=8            # 566 用例、覆盖率 100%
```
