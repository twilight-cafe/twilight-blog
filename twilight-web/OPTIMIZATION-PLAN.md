# twilight-web 加载速度与 SEO 优化计划

> 状态：**计划稿，未改动任何代码**。
> 全部数字为本次在 `twilight-web` 内的实测值（方法见 §0.3），非估算。
> 访问范围：仅 `twilight-web`。凡需越界的条目集中列在 §6，等你授权后再动。

---

## 0. 基线测量

### 0.1 现有产物

| 产物 | 原始 | gzip | bz2 |
|---|---|---|---|
| `dist/assets/main-DYtURx3d.js` | 1254.8 KB | 382.9 KB | 318.0 KB |
| `dist/assets/main-C6AXH6ia.css` | 136.0 KB | 27.2 KB | 24.0 KB |
| 字体资源（woff2+woff+ttf，共 62 个文件） | 1111.1 KB | — | — |

字体只列进表是因为**部署体积**：浏览器只会取 `woff2`（约 4–6 个文件、~100 KB），`woff`/`ttf` 是磁盘上的死重量，对运行时零影响。

### 0.2 JS 体积归因（总量 1271.0 KB）

| 贡献方 | 体积 | 说明 |
|---|---|---|
| `ink-design/dist/index.mjs` | **328.6 KB** | 全站第一大项，且是**可消除**的 |
| `katex` | **262.4 KB** | 只在正文含公式时才需要 |
| `react-dom` | 176.9 KB | 框架税，见 §5 |
| `lowlight` + 内嵌 `highlight.js` | **169.0 KB** | 默认 `common` = 37 种语言 |
| `react-router` | 42.4 KB | 框架税 |
| `micromark-core-commonmark` | 27.0 KB | Markdown 解析必需 |
| `property-information` / `mdast-util-to-markdown` / `mdast-util-to-hast` | 17.7 / 10.6 / 10.5 KB | `remark-gfm` 传递依赖，不可拆 |
| 其余 remark/rehype/micromark 链 | ≈ 43 KB | Markdown 管线必需 |
| `react` | 7.9 KB | |
| **应用自有代码** | **≈ 110 KB** | 其中 **admin 专属 ≈ 63 KB** |

### 0.3 测法（可复现）

- 归因用 esbuild 0.25 `metafile` **内存打包**（`write:false`），入口 `src/main.tsx`，`minify` + `define NODE_ENV=production`，逐包累加 `bytesInOutput`。
- esbuild 口径 1271.0 KB vs vite 真实产物 1254.8 KB，差 1.3%——**以下所有结论都取同一口径下的差值**，不做跨口径比较。
- gzip 用 Python `gzip.compress(raw, 9)` 对真实产物压缩测得。
- 受控实验：同一入口分别绑定不同 alias，只改一个变量，比对总量。

---

## 1. 三个根因

### 1.1 `ink-design` 的预打包单文件（−292 KB，且零行为变化）

`node_modules/ink-design/dist/index.mjs` 是 1993.5 KB 源码、**145 个导出、只有 1 条 `export {}` 语句**的单文件产物。后果是任何一次 `import { Button } from 'ink-design'` 都会把整包拖进来，Rollup 无从裁剪——包 `package.json` 里**没有 `sideEffects` 字段**，模块被当成有副作用。

受控实验（esbuild，同入口）：

| 实验 | 总量 | `ink-design` 贡献 |
|---|---|---|
| A 现状（`dist/index.mjs`） | 1271.0 KB | 328.6 KB / 13 files |
| B 单独 `import { Button }` | 335.5 KB | **318.1 KB** |
| C 单独 `import { Modal }` | 335.5 KB | **318.1 KB** |
| D 裸 `import 'ink-design'` | 335.5 KB | **318.1 KB** |
| E 完全不引 | 0.0 KB | 0.0 KB |

B/C/D 三条完全同值，即**引 1 个组件和引 145 个组件代价相同**。应用实际只用到 21 个（Button/Input/Chip/Badge/Status/IconButton/Select/Checkbox/Modal/DatePicker/Pagination/CopyButton/Toolbar/TimePicker/message/AdminNav/NavRow + hooks + Portal/NativeSelect）。

关键线索：包内**带着 `src/`**，而 `src/index.ts` 顶部注释写着「twilight-web 通过 vite alias 直接指向本文件（见 twilight-web/vite.config.ts 与 tsconfig.app.json 的 paths）」——**但当前 `vite.config.ts` 里没有这条 alias**，实际走的是 `dist/index.mjs`。这条注释与代码不一致，正是 318 KB 的来源。

进一步实验（alias 到 `src/`，并补上它内部用到的 `@/` 别名）：

| 实验 | 总量 | `ink-design` 贡献 |
|---|---|---|
| A 现状 | 1271.0 KB | 328.6 KB |
| B alias → `src/`（无 `sideEffects`） | 1096.7 KB | 156.2 KB |
| **C alias → `src/` + `sideEffects:false`** | **976.9 KB** | **36.8 KB** |

C 恰好只拉进 21 个组件 + hooks，与"实际用到的"完全吻合。**收益 291.8 KB**。

两个必须补的前提，已核实：
- `ink-design/src/**` **没有任何 CSS 副作用导入**（`import './x.css'` 一个都没有），样式全部来自 `dist/inkui.css` → 丢掉未使用组件不会丢样式。
- 那些 `vitest` / `@testing-library` 导入都在 `*.test.tsx` 里，不在 `index.ts` 的引用链上，不会进包。

### 1.2 全部路由静态导入 + 首屏被 `/api/me` 挡住

`src/App.tsx` 头部**静态导入全部 25 个页面组件**（公开区 8 个 + admin 区 15 个 + Setup），没有任何 `React.lazy`。实测「仅公开区」与「全部路由」的增量是 **81.7 KB**（784.1 → 702.4 KB，同条件下比对），这部分是每个匿名访客都在下载的 admin 代码。

更要紧的是渲染时序。`App.tsx` 的现有逻辑：

```
GET /install_status  →  GET /api/me  →  才渲染第一个字节的内容
```

`if (state === 'installed' && !authReady) return <loading>` 把**公开页**也拦在登录态解析之后。于是任何访客看首页都要等 3 次串行往返（两次网关 + 一次文章接口）才看到内容。这是 LCP 与 SEO 双重损失，而匿名访客根本不需要这次 `/api/me`。

### 1.3 公式与高亮无条件进首屏

`katex`（262.4 KB）+ `lowlight/common`（169.0 KB）= **431.4 KB，占首屏的 34%**，全部通过 `lib/markdown.ts` 的模块级单例被静态拉进主 chunk。而它们只在「正文含公式」或「代码块标了语言」时才有用——对绝大多数页面是纯浪费。

---

## 2. 目标（实测可达，不是拍脑袋）

首屏（公开区、匿名访客）JS：

| 阶段 | 首屏 JS | 依据 |
|---|---|---|
| 现状 | 1254.8 KB | 真实产物 |
| + `ink-design` tree-shaking | ≈ 963 KB | −291.8 KB（实验 C） |
| + admin 路由分包 | ≈ 881 KB | −81.7 KB（同入口比对） |
| + KaTeX 懒加载 | ≈ 619 KB | −262.4 KB |
| + 高亮瘦身（白名单） | ≈ 490 KB | −130 KB 量级 |
| **合计** | **≈ 450–500 KB（−60%）** | gzip 约 382.9 → **≈ 150 KB** |

推导链路：`G 702.4`（公开区，含 shaking）+ `react-dom/client 173.1` + 壳层 ≈ 7 = 882.5；再减 katex 262.4 → 620.1；再减 lowlight 169.0 → **451.1 KB（理论上界）**。

CSS：136.0 → ≈ 114 KB（`katex.min.css` 21.4 KB 随公式 chunk 走）；gzip 27.2 → ≈ 24 KB。

> 声明：以上是**加法估算**，每项都有独立实测支撑，但组合后的实际值要等改完量。不把估算当结果写进验收。

---

## 3. 分阶段计划

### P0 —— 零行为变化，先做（预计 −370 KB 首屏）

#### P0-1 `ink-design` tree-shaking
- **改什么**：`vite.config.ts` 增加 `resolve.alias`：`/^ink-design$/` → `node_modules/ink-design/src/index.ts`（**必须用正则精确匹配**，否则 `ink-design/styles.css` 会被前缀改写击穿，实验中已踩到）；再补 `{ find: /^@\//, replacement: <ink-src>/ }`；`build.rollupOptions.treeshake.moduleSideEffects` 对 `ink-design/src/**` 返回 `false`（其余返回 `null` 走默认）。`tsconfig.app.json` 的 `paths` 要同步补 `@/*`，否则 `tsc -b` 解析不了组件内部的 `@/utils/cx`。
- **同步清单（易漏）**：`vitest.config.ts` 的别名清单与 `vite.config.ts` 是**两处手工同步**的（注释已声明这一约定）。三处（vite / vitest / tsconfig）任一漏改，就会出现「测试绿、构建红」或反过来的分叉。
- **收益**：−291.8 KB。
- **风险**：① `tsc -b` 从此会类型检查 `ink-design/src` 的 143 个文件，其中的写法可能触发 `noUnusedLocals` 等既有严格开关；② ink-design 升级到新版本时，`src/` 是否继续随包发布、内部别名是否仍叫 `@/` 都是隐含契约 → 需要一条守护测试；③ 源码路径比 dist 更脆弱（依赖 `files: ["dist","src"]` 这个事实）。
- **测试/守护**：新增 `ink-design-alias.test.ts`，断言①应用实际用到的 21 个导出全部可用；②打包产物中不存在未使用组件的特征串（文本断言，不是快照）；③`vite.config.ts` 与 `vitest.config.ts` 的 alias 键集合一致。**三条都要先故意改坏验证会红。**

#### P0-2 admin 区路由分包
- **改什么**：`App.tsx` 里 `/admin/*` 页面改 `React.lazy` + `<Suspense>`；`AdminLayout` 的登录/角色守卫保持不动。`/install`（Setup）单独判断是否值得拆（1.6 KB，建议不拆）。
- **收益**：−81.7 KB 首屏。
- **风险**：Suspense fallback 会引入一个新的中间态，可能和 `AdminLayout` 现有的守卫时序叠加出"闪一下加载页再跳登录页"（与 `authReady` 那个已修的坑同类）。
- **测试/守护**：必须覆盖 ①挂起中的 fallback 渲染；②chunk 加载失败（mock 动态 import reject）时的降级；③守卫仍先于内容生效（未登录不渲染 console）。**覆盖率 100% 的最大难点就在这里**——`lazy` 的 pending/rejected 分支在 jsdom 下要主动构造。

#### P0-3 不再用 `/api/me` 阻塞公开页
- **改什么**：`authReady` 只门控 admin 区与页脚账号入口；公开区在 `install_status` 之后立刻渲染。`authReady` 未解析时页脚账号入口保持"登录/注册"以外不做乐观猜测（沿用现有 `useCurrentUser()` 的 null 语义）。
- **收益**：首屏少一次串行往返（这才是 LCP 上真正的大头，比几十 KB 更值钱）。
- **风险**：页脚账号入口在已登录时可能先显示"登录/注册"再变成"控制台"——这是**可接受的**，但必须写进注释并锁进测试，避免以后有人当成回归又改回去（那会重新引入对公开页的阻塞）。
- **测试/守护**：①`/api/me` 永久 pending 时公开页已可见；②已登录访客刷新后不闪回登录页（这条是原设计要保护的不变量，**必须保留且继续通过**）；③admin 区在 `authReady` 前不渲染。

#### P0-4 SEO 基础件（纯前端可做的部分）
- **改什么**：新增 `src/lib/head.ts` 统一管理 head（canonical / `og:*` / `twitter:*` / `robots` / JSON-LD），提供 `setHeadTags(partial)` 与 `usePageMeta()`。接入点：
  - 文章页：`og:type=article`、`article:published_time`、独立 `description`（正文取前 ~150 字纯文本）、`JSON-LD BlogPosting`、canonical 指向 `/post/:id`；
  - 首页：`JSON-LD WebSite`、canonical 指向 `/`；
  - `/search`、`/admin/*`：`<meta name="robots" content="noindex">`；
  - 列表页 `?page=N`：`rel=prev/next`，并把 canonical 指向无参数或当前页（**需要你拍板策略**，见 §6）。
- **收益**：不是 KB，是收录与分享卡片质量。
- **风险**：`lib/site.ts` 现在直接在 `publish()` 里写 `document.querySelector('meta[name=description]')`。新增 head 层后**必须二选一**：把这段搬进 `head.ts`，或明确约定"站点级描述由 site.ts 管、页面级由 head.ts 管"。两套并存 = 竞态（路由切换与 `/api/site` 返回顺序不定），最后一个写入者赢。
- **测试/守护**：`head.ts` 的所有分支（标签不存在需创建 / 已存在需更新 / 路由切换需清理上一页的 `og:type` 与 JSON-LD）；JSON-LD 必须断言**序列化后的字符串**（`<` `>` `&` 的转义），否则一个含引号的文章标题就能把 `<script type="application/ld+json">` 打坏。

### P1 —— 体积攻坚（要你先拍板，见 §6）

#### P1-1 KaTeX 懒加载（−262.4 KB）
- **改什么**：`lib/markdown.ts` 拆成「轻管线」（remark-parse/gfm + rehype-stringify）与「数学管线」（+ remark-math/rehype-katex）。文章页/页面页/微语在**数据 fetch 的同时**并行 `import()` 数学管线，只在正文含 `$` 时 await 它，拿到后再一次性同步渲染。
- **为什么这样设计**：这三个页面本来就是异步取数，懒加载与取数并行，**不引入额外 RTT、不引入渲染闪烁**；含公式的文章多等一次本地 chunk，不含公式的文章一个字节都不下载。比"先渲染纯文本再升级"干净得多。
- **风险**：`ContentView` 的同步渲染契约要动，而它踩着项目里最硬的坑之一——**React 19 会用完全相同的内容重设 `dangerouslySetInnerHTML`，DOM 后处理必须用无依赖 `useLayoutEffect`**。任何改动这个时序的方案都可能让代码块外壳（语言标签/复制按钮/限高）静默失效，而 jsdom 复现不出来。
- **测试/守护**：①含公式 / 不含公式两条路径；②懒加载 chunk 失败时降级为纯文本渲染而不是白屏；③**保留现有的源码形态文本断言 + 真机截图**（这是该项目已验证过的、唯一能抓住上面那个坑的手段）。

#### P1-2 代码高亮瘦身（−130 ~ −169 KB）
- **改什么**：`rehypeHighlight` 默认用 lowlight 的 `common`（37 种）。改为**显式语言白名单**（≈10 种）或**整体懒加载**。
- **这是产品决策，不是技术决策**：白名单之外的语言将不再着色（代码块本身照常渲染、语言标签照常显示、复制按钮照常工作）。方案与取舍见 §6。
- **顺带可解**：`highlight.js` 顶层包（11.12.0）与 lowlight 内嵌的那份**并存**，而顶层那份**只被用来 import 主题 CSS**。把 `github-dark.min.css` 落到 `src/styles/` 自持即可删掉这个依赖；同时现有注释里记着的「亮色模式下代码块仍强制深色」也能顺手按 `prefers-color-scheme` 修掉。
- **测试/守护**：白名单内每种语言一段真实代码的着色断言 + 白名单外语言"无 hljs 类但内容完整"的断言。

#### P1-3 CSS 瘦身
- **改什么**：`katex.min.css`（21.4 KB）随数学 chunk 懒加载；`inkui.css`（73.2 KB）与 `app.css`（32.1 KB）**暂不动**——按路由切分 CSS 需要 InkUI 侧改为按组件出 CSS，属于 §6 范围。
- **收益**：−21.4 KB 阻塞 CSS。
- **风险**：从 `main.tsx` 移除 katex CSS 后，公式 chunk 加载完成前公式会**裸显一帧**——需要与 P1-1 一起验证，必要时给公式容器一个 `visibility:hidden` 直到样式就位。

### P2 —— 需要你授权（越界，见 §6）

- **P2-1** Go 侧是否为静态资源开了 gzip/brotli。若没开：1254.8 KB 会原样上线，而 gzip 后只有 382.9 KB —— **这一条的性价比高于本计划里任何前端改动**。
- **P2-2** 真正的 SEO：`canonical`/`og`/`title`/`description` 必须写在**服务端渲染的 HTML** 里。理由项目里已经写清楚了（`vite.config.ts` 的注释解释了 shell 为什么在 Go 那边）——社交平台与微信的抓取器**不执行 JS**，运行时注入对它们等于没做。同理 `robots.txt` 与 `sitemap.xml` 应由后端按库中文章生成。
- **P2-3** 若要内容进首屏 HTML（对 SEO 与 LCP 都是最有效的一步），需要在服务端预渲染 Markdown。
- **P2-4** 在 InkUI 侧按 `preserveModules` + `sideEffects` 重新打包 `ink-design`，并改为按组件出 CSS。这比 P0-1 的 alias 绕路**更稳**，长期应该走这条。

---

## 4. 明确不做（审过之后认为不该动）

| 项 | 体积 | 不做的理由 |
|---|---|---|
| `react-dom` 换 `preact/compat` | 176.9 KB | 收益诱人，但要同时满足 React 19 语义 + `ink-design` 的 `peer: react>=18` + 现有 100% 覆盖率的导出行为，风险与收益不成比例 |
| `micromark`/`mdast` 链重构 | ≈ 108 KB | 全是 Markdown 语义必需，条目多但职责内聚，砍任何一段都是有损的 |
| `app.css` 32.1 KB | 32.1 KB | 一个文件、职责单一，gzip 后占比很小，不值得为体积做有损拆分 |
| 剥离 `woff`/`ttf` 字体 | 1111.1 KB（磁盘） | 运行时零影响（浏览器只取 woff2）；要做也只能靠改写 `katex.min.css`，性价比低 |
| 引入 `rollup-plugin-visualizer` 常驻依赖 | — | 一次性分析用 esbuild `metafile` 即可，不必往工程里加依赖 |

---

## 5. 覆盖率与验收

**基线**：`coverage/coverage-summary.json` 显示 lines / branches / functions / statements **全 100%**，且 **0 个文件低于 100%**。阈值已硬编码在 `vitest.config.ts`（`all: true`，`include: ['src/**/*.{ts,tsx}']`）。

因此**每个新增文件从诞生第一刻就被要求 100%**，包括分支。这条约束会直接影响实现方式：

1. **不要写防御性分支**。`all: true` 意味着"我加个 `?? fallback` 保险一下"会立刻变成一条需要构造出来才能覆盖的分支。宁可让不可达分支不写（项目里已有先例：`isAllowedCommentAttribute` 里那条被删除的冗余 `on-` 检查，注释写明了"因不可达而被移除"）。
2. **纯逻辑与副作用分离**。head 计算、canonical 推导、description 截断写成纯函数（可直接测），DOM 写入单独薄封装。
3. **每条守护测试先证明它会失败**。按项目既有作风，新增守护（alias 未漂移 / 未使用组件不入包 / 懒加载失败降级 / 公开页不被 `/api/me` 阻塞）都要**故意把被测对象改坏一次确认报警**，再改回来。不会失败的测试比没有测试更糟。
4. **验证清单**（每个阶段结束跑）：

```bash
cd twilight-web
node node_modules/typescript/bin/tsc -b
node node_modules/vitest/vitest.mjs run --coverage
node scripts/coverage-gaps.mjs          # 若有未覆盖位置，逐条列出
```

5. **体积要量，不要只看退出码**。每阶段记录 `dist/assets/main-*.js` 的真实字节数并与上一阶段比对。注意：构建成功 ≠ 产物完整（历史上出现过"CSS 引用了 N 个资源、目录里只有 M<N 个"且全程不报错的情况），所以要独立核对产物，别把退出码当结论。

---

## 6. 需要你拍板的事

1. **授权范围**：是否允许我读 `twilight-blog`（Go 模板 shell / 静态资源压缩）以完成 P2-1、P2-2？这两项是**整个计划里性价比最高**的，但在 `twilight-web` 之外。
2. **代码高亮策略**（P1-2）：白名单（≈10 种语言常驻，其余不着色）/ 整体懒加载（首访零高亮，滚动到代码块再加载）/ 维持现状？
3. **分页 canonical 策略**（P0-4）：`/?page=2` 这类列表页 canonical 指向自身（保留分页收录）还是指向第一页（收敛权重）？
