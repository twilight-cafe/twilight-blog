# 交接说明（2026-09-12）

起点是用户的 7 项要求：

1. 更新 `api.md` 和 `document.title`
2. 每个用户拥有**用户名**与**昵称**两个不同字段：昵称用于前台显示，用户名用于登录，**不得混用**
3. 未登录时评论区展示登录控件（类似内嵌的登录页），登录后才能评论
4. 优化 `/admin/users` 与 `/admin/profile` 的空间，美观且符合页面风格
5. 新增管理员专属的站点设置路由，允许设置「网站描述」
6. 全面细致地排查安全漏洞
7. 以黑客视角尝试入侵并修复可利用漏洞

附加约束（用户原话）：**「有需要删除的文件尽量最后留着统一删除。」**——见文末第五节。

**七项要求全部完成。** 下面每一条都附了实测证据，以及本轮**推翻的两个错误结论**
（上一版交接文档里的 M1 与「限流数值不符」），推翻过程写清楚了，免得下一个人再去
「修」一个不存在的漏洞。

---

## 一、验证状态（全部实测，非声称）

| 检查项 | 命令 | 结果 |
|---|---|---|
| Go 格式 | `gofmt -l ./internal ./main.go` | 无输出 |
| Go 构建 | `go build ./...` | 通过 |
| Go 静态检查 | `go vet ./...` | 通过 |
| Go 测试 | `go test ./internal/...` | 全部 ok（config/database/middleware/model/search/service/session/view/viewmodel） |
| SDK 类型 | `twilight-blog-api` → `tsc --noEmit` | 通过 |
| 前端类型 | `twilight-web` → `tsc -b` | 通过 |
| InkUI 类型 | `InkUI` → `tsc --noEmit` | 通过 |
| 前端产物嵌入 | `vite build` + 复制进 `internal/view/web` | 通过（`index-BPo9__lx.js` / `index-C3S0CXd0.css`） |
| 二进制 | `go build -o main.exe .` | 通过（20.7 MB） |
| 端到端冒烟 | 真机启动 + HTTP 回归（见第四节） | 通过 |

> 环境坑：Go 默认 `GOCACHE` 在 `%LOCALAPPDATA%`，在沙箱模式（workspace-write）下写入被拒，
> 必须 `$env:GOCACHE = Join-Path $env:TEMP 'dsh-gocache'`。前端 `vite build` 会被沙箱拒绝
> （esbuild 以管道启动子进程 → `spawn EPERM`），需要一次性放行；`tsc` 不受影响。
> 不要用 pnpm（无 TTY 下会中断并损坏 node_modules）。

---

## 二、七项要求逐条落地

### 要求 #2：用户名 / 昵称彻底分离（承重项）

**数据层** `internal/database/`
- `users.username` 新列；`addedColumns` 登记，唯一索引放 `addedIndexes`（晚于补列执行）。
- 唯一索引**必须是 partial**（`WHERE username <> ''`）：迁移期老库所有行都是空串，
  非 partial 会让迁移直接失败。已用反证证明。
- `backfillUsernames` 从邮箱推导老账号用户名（`deriveUsername` + `uniqueUsername` 去重）。

**服务层** `internal/service/user_service.go`
- `Register(email, code, username, nickname, password)`、`LoginWithUsername(identifier, password)`
  （输入形似邮箱时回退按邮箱查找）、`UpdateOwnUsername` / `UpdateOwnNickname` / `AdminUpdateUser`。
- **用户名的形状规则本轮移到了 `internal/model/username.go`**——原来只有 service 一份，
  导致管理员登录名那条入口没人校验（见第三节 S1）。

**站长身份拆分** `internal/config/config.go`
- `admin_name` → `admin_username`（登录）+ `admin_nickname`（展示），`Load` 里一次性迁移。

**接口层**：`PATCH /api/me`（username / nickname 各自可选）、`PUT /api/admin/profile`
（`name` 必填为展示名，`username` 可选为登录名，**省略即不改**）。

**评论署名只有一个来源**：`CreateCommentRequest` 没有昵称字段；`CreateAsUser` 从账号资料读昵称；
删掉了「评论后回写账号昵称」的副作用。署名不得由请求指定，这一条有守护测试。

### 要求 #3：未登录时评论区展示登录控件

`twilight-web/src/components/Comments.tsx` 重写：未登录渲染 `InlineAuth`
（身份 Tabs + 用户名 + 密码 + 注册/找回链接），登录成功后 `setCurrentUser` 就地变成评论框，
**不跳转、不丢阅读位置**。

### 要求 #4：`/admin/users` 与 `/admin/profile` 的版面

此前 `.hint` 这个类名**根本没有全局样式**（只有 `.cform .hint` 一条），
加上卡片正文没有 `.bd2` 内边距、字段直接贴着卡片边缘，所以看起来又挤又散。本轮：

- 新增全局 `.hint` 与 `.act-row`（卡片底部操作行，按钮靠右、间距统一）。
- `Profile.tsx`：四张卡片全部套 `.bd2` 正文；保存按钮旁加**改动状态提示**
  （「显示名称有改动 / 登录名有改动 / 尚未修改」），改登录名这种会影响下次登录的事先说清楚。
- `UsersAdmin.tsx`：列表改为 `.ulist` / `.urow`（头像 + 昵称 + `@用户名` 徽标 + 元信息一行），
  行分隔线最后一行不再画（避免与卡片下边框叠成双线），悬停高亮。
- `app.css` 删掉了 `.cform .hint` 的 `cursor: pointer`——那个类名现在只用在静态说明上，
  指针手型是「使用我的昵称」旧交互的残留。

### 要求 #5：站点设置路由

- 后端（上一轮已就绪）：`GET /api/site`（公开）、`PUT /api/admin/site`（仅管理员，
  200 字上限、折叠空白、落盘失败回滚内存）。
- **新增 `twilight-web/src/pages/admin/SiteSettings.tsx`**：左侧写、右侧实时预览
  （首页标语 / 页脚简介 / 首页标签页标题三处实际排版），字数按**折叠空白后**统计，
  与后端一致；带清空、未保存提示、保存后回填服务端最终值。
- `App.tsx` 加 `/admin/site-settings`；`AdminLayout.tsx` 侧栏加「站点」（pal 图标）；
  非管理员被 AdminLayout 守卫挡在门外，后端再兜 403。
- **描述真的可见了**：`PublicShell.tsx` 的首页标语与页脚简介改用 `useSiteDescription()`；
  `index.html` 补上 `<meta name="description">`，描述变更时同步覆盖它
  （见 `lib/site.ts` 的 `applyMetaDescription`）。否则「后台能存、前台看不到」等于没做。

### 要求 #1：`document.title` 与 `api.md`

**标题**（`lib/site.ts` 的 `useDocumentTitle`）
- 首页：`Twilight · <站点描述>`（`useHomeDocumentTitle`，品牌在前）。
- 归档 / 标签（选中标签时带标签名）/ 搜索（带关键词）/ 微语 / 独立页面 / 404：`<页面名> · Twilight`。
- 文章页与独立页面用内容自己的标题；**后台由 `AdminLayout` 统一设置**（`<菜单标签> · Twilight`），
  后台子页面不要再自行设置——React 子 effect 先于父 effect 执行，父组件会把它覆盖掉。

**`twilight-blog-api/api.md`** 以 `internal/view/router.go` 的路由表逐条核对后重写相关章节：
- 新增「username 与 nickname 是两个字段」小节、`GET /api/site`、`PUT /api/admin/site`；
- `/api/auth/login`（站长用登录名）、`/api/auth/user/login`（用户名，email 为废弃兼容字段）、
  `POST /api/auth/register`（+username）、`PATCH /api/me`（username / nickname 双字段）、
  `PUT /api/admin/profile`（name + 可选 username）、`GET|PUT /api/admin/users`（+username）、
  `POST /api/comments`（**无** nickname）、搜索引擎 **ZincSearch → Meilisearch**；
- 新增「限流与客户端 IP 判定」小节（从右往左取第一个不可信地址 + nginx 配置指引）；
- 邮件日志正文的验证码打码（`[redacted]`）、按收件地址的限流、验证码 5 次尝试上限均已写入；
- 8 处内部锚点全部校验可跳转。

### 要求 #6 / #7：安全审计与修复

见第三节。

---

## 三、安全：结论、修复与**两处更正**

### 3.1 先更正上一版交接文档的两个错误结论

**更正 A：M1「默认信任回环代理 → echo 取 XFF 最左值 → 可伪造 IP 绕过限流」——不成立。**

`echo` v4.12.0 的 `ExtractIPFromXFFHeader` 是**从右往左取第一个不可信地址**
（源码 `ip.go`：`for i := len(ips)-1; i >= 0; i--`，遇到 `!truster.trust(ip)` 才返回），
不是取最左值。实测证据有两层：

1. 新增守护测试 `TestBuildIPExtractorResistsSpoofedForwardedChain`
   （`internal/middleware/security_test.go`）：攻击者自带 `X-Forwarded-For: 1.2.3.4, 198.51.100.9`
   时解析结果恒为 `198.51.100.9`（真实跳数），左侧伪造值被忽略；伪造可信地址、
   链尾畸形、直连客户端自带头等情况一并覆盖。**反证**：把 extractor 换成朴素取最左值后，
   4 个子用例全部失败并打印出被伪造的值（`client IP = "1.2.3.4"`）——证明这组测试是承重的。
2. 真机回归：5 次登录请求分别伪造 `X-Forwarded-For: 1.2.3.N, 203.0.113.9`，
   结果 `401|401|401|429|429`——**五个不同的伪造值落在同一个桶里**；
   服务端日志里记录的也全是 `remote=203.0.113.9`（真实跳数），从未出现 `1.2.3.N`。

结论：默认 `trusted_proxies = {127.0.0.1/32, ::1/128}` 配上
`$proxy_add_x_forwarded_for` 是**安全**的，不需要自定义 IPExtractor。
本轮只补了守护测试与文档（把「从右往左」这条规则写进 `api.md`），
**没有**改动 `BuildIPExtractor`（`git diff` 对该文件为空）。

**更正 B：`api.md` 的限流数值本来就是对的**（login 3/min、email 3/min+10/h、verify 10/min、
comment 5/min、install 5/min，与 `config.go` 的 `Default*` 常量逐项一致），
只有搜索用户名（ZincSearch → Meilisearch）确实错了。上一版交接文档说「文档 login 10/min」有误。

### 3.2 本轮新发现并修复的三个问题

| 级别 | 问题 | 修复 | 位置 |
|---|---|---|---|
| **中** | **六位验证码可在 10 分钟有效期内被穷举**：校验端点的限流是按 IP 的，分布式来源换 IP 就换额度；而一枚码的有效期固定、失败不计数 | 新增 `attempts` 列 + `RecordFailedAttempt`（**递增与作废写在同一条 UPDATE**，否则并发会一起放行）；同一枚码错 5 次即作废。留足手滑余地的对照测试同时存在 | `database/schema.go`、`model/email_verification_repo.go`、`service/user_service.go` |
| **低** | **管理员登录名没有服务端形状校验**：只查非空与长度，直接调接口就能存进含零宽空格或西里尔字母的名字（前端表单有校验，但服务端才是权威） | 把用户名形状规则移到 `model.NormalizeAndValidateUsername`，service / viewmodel / 后台资料三处共用；`AdminProfileRequest.Validate` 现在会拒绝 `ab`、`1abc`、`abc def`、`admin<ZWSP>`、`аdmin`（西里尔 а） | `model/username.go`（新增）、`viewmodel/auth.go`、`service/user_service.go` |
| **低** | **验证码明文进邮件留痕表**：`GET /api/admin/emails/:id` 能读到验证码，可拿去重置任意用户密码 | 新增 `EmailSender.SendVerification(to, subject, body, code)`：正文照原样发出，**落库前把验证码换成 `[redacted]`**；正文与密钥对不上时整段作废而不是原样保存（fail closed） | `service/email_service.go`、`service/user_service.go` |

新增/修改的守护测试均做了反证（改坏 → 确认失败 → 精确还原）：

- 验证码尝试上限：去掉 `RecordFailedAttempt` 调用 → `after 5 failed attempts the correct code was still accepted`。
- 邮件打码：`SendVerification` 改回存原文 → `stored body "Your verification code is 483920" still contains the verification code`。
- 管理员登录名：还原成只查非空 → `Validate() accepted the login name "ab"`。
- 按地址限流：注释掉判断 → `request beyond the cap error = <nil>`。

### 3.3 上一轮已修、本轮复核仍在的项

- 邮件日志验证码打码之外的 L2（未鉴权发信）：本轮补上**按收件地址 10 封/小时**
  （`emailCodesPerHourPerAddress`）。按 IP 的额度挡不住分布式刷同一个收件箱；
  计数按**签发次数**而非「仍然有效的码」，所以「等它过期再要一枚」这条绕路也被堵住。
  真要做图形验证码需要第三方依赖并拖慢注册，暂不值得——被滥用时再上。
- L3（Meilisearch 校验和与二进制同源）：新增 `releaseSource{metadataURL, downloadPrefix}`，
  下载地址（二进制与校验和**都**）必须落在解析出它的那个来源的前缀内，
  被拒的地址在发起请求**之前**就退出——否则篡改元数据的人能把两个地址一起指向自己的主机，
  校验变成自我确认。守护测试全部离线（反证时还真去 `dial evil.test` 了，正说明这道闸在挡事）。
  挡不住「GitHub 与上游同时被攻陷」，那只能靠内置哈希，与「自动跟随 latest」互斥，已在注释里写明取舍。
- L4（评论 sanitizer 保留 `style`）：实测探针确认整条管线里**只有 KaTeX** 会产出内联样式，
  且只落在 `span` / `mstyle` / `mpadded` 三个标签上（`position` 只出现 `relative`）。
  于是把 `style` 从全局属性白名单里拿出来，改成只对这三个标签放行，
  并额外拒绝 `position` 非 relative、`content:`。
  反证：把标签集合清空 → 输出里的 style 属性从 34 个变成 0 个（证明这道闸生效），
  还原后 34 个仍在（证明没有误伤公式渲染）。原始 HTML 本来就是关的
  （`allowDangerousHtml: false`），`\htmlStyle` 之类需要 `trust: true`，实测全部退化成字面文本。

### 3.4 本轮实测确认安全的既有防线

CSRF（缺头 → 403）、OriginCheck、会话与 Cookie 标志、SQL 注入、IDOR、路径穿越、
SSRF、命令注入、XSS（文章正文是站长独占写权限、评论走白名单）、上传校验、
安装重放（→ 409）、账号枚举（等时 bcrypt）、列表契约（空数组而非 null）、
`/api/me` 越权（管理员 → 403）、`/api/admin/*` 未登录 → 401。

---

## 四、端到端回归（真机，非单元测试）

在临时目录（`%TEMP%`，不动仓库里的 `settings.json`/`blog.db`）启动 `main.exe`，
用真实 HTTP 请求跑了一遍：

| 场景 | 结果 |
|---|---|
| `GET /` | 200，含 `<meta name="description">` 与新产物 `/assets/index-BPo9__lx.js` |
| 产物内含新代码 | 命中「站点设置」「未保存的修改」「登录后即可发言」「utag」「.set-grid」「.urow」「.hint{…}」 |
| 深链 `/admin/site-settings`、`/admin/users`、`/admin/profile`、`/moments`、未知路径 | 全部 200 且返回 SPA 外壳 |
| `POST /install` 重放 | 409 |
| `GET /api/site`（未设置） | `{"description":""}` |
| `PUT /api/admin/site` 不带 CSRF | 403 |
| `PUT /api/admin/site`（含换行与缩进） | 200，折叠成单个空格；中文精确 round-trip（`以代码为笔， 以时间为墨。`） |
| `PUT /api/admin/site` 201 字 | 400 |
| `PUT /api/admin/site` 匿名 | 401 |
| 管理员登录名 `ab` / `1abc` / `abc def` / `admin<ZWSP>` / 西里尔 `аdmin` | 全部 400 |
| 只改展示名（不传 username） | 200，登录名保持 `admin` |
| 改登录名为 `twilight` | 200，**当前会话不失效**；随后用 `twilight` 登录 200、用旧名 `admin` 登录 401 |
| 带 `nickname: "IMPOSTOR"` 发评论 | 201，署名取账号展示名，`nickname` 字段被完全忽略 |
| `PATCH /api/me`（管理员） | 403 |
| `GET /api/admin/users`、`/api/admin/emails` | `{"items":[]…}`（空数组，非 null） |
| 伪造 `X-Forwarded-For` × 5 | `401|401|401|429|429`（同一桶），日志记录真实跳数 |

服务端日志除预期的配置类 WARN（未配 Resend key、无 ffmpeg、`server_name` 为空）
与被拒绝请求的 WARN 外无异常，无 panic。

---

## 五、删除动作（已按你确认执行）

**已删除 `twilight-web/frontend-refer/`**（重构前的冻结快照，183 个文件 / 5.6 MB，
其中 59 个被 git 跟踪、约 124 个是未跟踪的构建残留）。你确认后执行：

- `git rm -r twilight-web/frontend-refer` → 59 个文件进入暂存区的删除（可从历史回滚）；
- 未跟踪残留用 `Remove-Item -Recurse -Force` 清掉；
- 全仓 grep `frontend-refer` 已只剩本文档的这句说明，没有任何构建配置
  （`build.py` / `vite.config.ts` / `tsconfig` / `.gitignore`）引用它，删除不影响构建。

**除此之外没有待删文件**：`internal/view/web/assets/` 里的旧产物随重建自动替换
（`index-6z039m2O.js`、`index-CGeOqNHW.css` → `index-BPo9__lx.js`、`index-C3S0CXd0.css`），
`twilight-web/dist/` 与 `main.exe` 本来就在 `.gitignore` 里。

### 可选后续

- 验证码端点若真被滥用，再上图形验证码（代价是第三方依赖 + 注册变慢）。
- Meilisearch 若要更强的完整性保证，改成内置已知哈希——代价是不再自动跟随 latest，
  与本项目当前的取舍互斥，已在 `internal/search/download.go` 注释里写明。

---

## 六、环境与踩过的坑（必读）

- **Go 沙箱**：`GOCACHE` 必须指到可写目录，否则 `Access is denied`。
  离线构建用 `GOPROXY=off GOFLAGS=-mod=mod GOTELEMETRY=off`。
  只对 `./internal ./main.go` 跑 `gofmt`，别扫全目录（会命中只读 `.modcache`）。
- **前端**：不要用 pnpm；类型检查直接 `node node_modules/typescript/bin/tsc`；
  `vite build` 在沙箱下必然 `spawn EPERM`（esbuild 子进程），需要一次性放行。
- **PowerShell 客户端坑**：`Invoke-WebRequest -Body <string>` 在 Windows PowerShell 5.1 下
  会把中文编成 `?`（要传 `[Text.Encoding]::UTF8.GetBytes(...)`），
  读响应也要自己按 UTF-8 解码 `RawContentStream`；`Invoke-WebRequest` 需要 `-UseBasicParsing`。
  别用 `| Select-Object -First N` 截后台进程的输出——管道一关，进程就被杀掉。
  **不要删 `%TEMP%\dsh-*` 这类目录**：它是会话运行器的临时根目录，删掉之后所有命令都会
  因为「temp 目录不存在」而无法启动（本次踩过，用写入一个占位文件恢复）。
- **换行符**：`.gitattributes` 是 `* text=auto eol=lf`，而 `api.md` 与 `app.css` 的工作副本
  历史上是 CRLF（早期 Windows 工具留下的）。本轮新增的行是 LF，`git diff --check` 干净、
  提交时会被统一成 LF，**不要**为了「看起来一致」去整文件转换，那只会制造整文件重写的假 diff。
- **项目约定**：单连接池 `SetMaxOpenConns(1)`，**绝不能在遍历 rows 时写库**（静默死锁）；
  新增列必须同时改建表语句与 `addedColumns`，引用新列的索引放 `addedIndexes` 且必须 partial；
  `ON DELETE CASCADE` 是承重的，仓储层不要再显式删关联；
  无关联对象装配后必须是非 nil 空切片；改 API 形状前先看 `twilight-blog-api` 的类型声明；
  TS 侧必填字段 Go 侧必须恒输出。
- **新增守护测试必须做反证**（改坏 → 确认失败 → 精确还原），一次只改一处，
  多处同时改坏会互相掩蔽。改完跑：`gofmt -l`（无输出）→ `go build ./...` → `go vet ./...` → `go test ./...`。

---

## 七、管理员凭据已并入 `users` 表（2026-09-12 后续改动）

`ADMIN-CREDENTIALS-HANDOFF.md` 描述的重构已经完成。这一节只记**下一个 agent 必须
知道、否则会踩坑**的部分；完整的背景与理由在那个文件里。

### 新的数据模型

站长（管理员）**也是 `users` 表里的一行**，靠 `is_admin = 1` 标识。

| 不变量 | 由谁保证 |
|---|---|
| 全站**最多一个**管理员 | 部分唯一索引 `idx_users_single_admin (is_admin) WHERE is_admin = 1`——**数据库层**，不是应用层判断 |
| 用户名全站唯一（站长与访客共用一个命名空间） | 现成的 `idx_users_username`。**「预留用户名」那套机制已全部删除** |
| 最多一行空 email | `users.email` 的 `NOT NULL UNIQUE` + 空串哨兵（见下） |

管理员行的 `email` 恒为**空串**。这是关键决策：`users.email` 是 `NOT NULL UNIQUE`，
而 SQLite 无法通过 `ALTER` 放宽列约束，所以那一行必须有值。选 `''` 是因为**没有任何
注册路径能产生它**（`normalizeAndValidateEmail` 与 `AdminUpdateUserRequest.Validate`
都拒绝空值），于是它是天然的「不可注册地址」，零新增守卫代码。
**不要**把它改成 `admin@example.com` 之类：那样就必须再加一条「禁止注册该地址」的
守卫，否则用户抢先注册就会撞唯一约束。代价见「忘记密码」。

### 四条静默失败陷阱（都不会报错、不会失败编译）

1. **`userColumns` 漏加 `is_admin`** → `model.User.IsAdmin` 恒 false → 管理员登录拿到
   `role=user` 的会话 → **进不了 `/api/admin`，站点锁死**，而既有测试全绿。
   守卫：`TestUserColumnsCarryIsAdmin`。
2. **改密后按错误的角色重签会话** → 管理员改完自己的密码当场被降级成访客，请求却返回
   `204`。守卫：`TestAdminCanChangeOwnPassword` 断言改密后会话仍是 `role=admin`。
3. **`UserRepository.List` 漏 `WHERE is_admin = 0`** → 管理员出现在后台用户列表里。
   **COUNT 与 SELECT 两处都要加**，否则 `total` 与当页不一致。
4. **迁移晚于 `view.Register`** → `/install` 短暂开放 = 接管窗口。见下面的时序。

除此之外还有一类**语义翻转**：管理员会话的 `user_id` 以前是 `NULL`，现在指向真实
账号行，于是「`session.UserID == nil` 即不是访客」这个隐含约定失效了。
`PATCH /api/me`、点赞、取消点赞三处因此显式补了 `IsAdmin()` 守卫
（这三处是**有意的「不做项」**：管理员继续走 `PUT /api/admin/profile`，且不参与点赞）。

### 启动时序（硬约束，改动前先读 `main.go` 的注释）

```
config.Load → cfg.Save → database.Open
  → model.MigrateAdminCredentials   ★ 必须在这里
  → cfg.ClearLegacyCredentials + cfg.Save
  → search.Start → view.Register    ← 从这里起才对外服务
```

管理员凭据此前**只**存在于 `settings.json`。若直接把它删掉而不搬进库，升级后第一个
启动瞬间库里没有管理员行 → 「已安装」为假 → **未鉴权的 `/install` 重新开放**。
迁移必须**幂等**：管理员行已存在时绝不覆盖，否则一份陈旧的 `settings.json` 备份会把
已经轮换掉的旧密码复活。守卫：`TestLegacySettingsHashIsMigratedOnce`、
`TestMigrateAdminCredentialsRunsBeforeServing`。

> 诚实说明：`TestMigrateAdminCredentialsRunsBeforeServing` 验证的是**整条引导路径的
> 最终状态**，它**测不出**「迁移被放进 `view.Register` 内部」这个位置
> （真实架构里 Register 不可能 import model 的迁移，会构成 import cycle）。
> 真正的权威是 `main.go` 里那段带 ⚠️ 的注释与调用次序。

### 「已安装」的判据变了

不再是「`settings.json` 里有非空 `password_hash`」，而是
**`model.AdminRepository.Exists()`**（users 表里有没有 `is_admin = 1` 的行）。
`config.IsInstalled()` / `VerifyPassword` / `SetPassword` / `SetPasswordHash` /
`PasswordHashValue` 已全部删除。

### 忘记密码：只能跑 CLI

管理员**不能**走「忘记密码」自助重置（那条路要求邮箱存在且能收信）。标准动作是：

```powershell
.\main.exe admin set-password            # 密码从 stdin 读两次，绝不从 argv
.\main.exe admin set-password --username root --nickname 站长   # 仅当还没有管理员行
```

退出码：成功 0、用法/校验失败 2、DB 失败 1。改完会 `session.RevokeAll`（**包括管理员
自己的会话**）。刻意**不**提供「打印密码哈希」这类子命令。

### 运维影响（必须写进部署文档）

1. `settings.json` 不再含任何密码哈希、也不再含 `admin_username` / `admin_nickname`
   （这三个键已标 `json:"-"`）。**备份清单变了：`blog.db` 现在是必须备份的那一个，
   丢它就等于丢管理员凭据。**
2. 恢复旧 `blog.db` 会一并恢复旧的管理员密码。
3. 回滚到旧代码时 `settings.json` 已无哈希，需要手工把 `users` 表那一行的
   `password_hash` 拷回，或先用 CLI 导出。**升级前先备份 `settings.json` 与 `blog.db`。**
4. `main.exe email ...` 从此也会执行一次数据库迁移（它需要读库判断「装没装过」）。
