# 本地开发环境搭建与流程

面向「在一台新机器上把这个仓库跑起来，并知道每次改完该跑什么」。
所有命令、路径、版本都取自当前仓库的实际配置，不是通用模板。

---

## 一、这个仓库是什么

一个自托管的博客引擎。四个子项目，**互相之间有真实的构建依赖**：

| 目录 | 角色 | 技术栈 |
|---|---|---|
| `twilight-blog/` | 后端**与站点外壳**。Go module `blog-engine`。最终产物是单个二进制 | Go 1.23（实测 1.26.6）+ echo v4 + modernc sqlite（**纯 Go，无 cgo**） |
| `twilight-web/` | 前端 SPA | React 19 + TypeScript 5.8 + Vite 6，测试用 Vitest 5 |
| `twilight-blog-api/` | HTTP API 的 TypeScript 封装（还可对外发布成 npm 包） | TypeScript + Vite（lib 模式） |
| `InkUI/`（**在仓库外**） | UI 组件库 `ink-design` 的源码 | React + TypeScript |

### 两个最容易踩的结构事实

**1. `InkUI/` 不在本仓库里，而是一个 `file:` 依赖。**

```jsonc
// twilight-web/package.json
"ink-design": "file:../../InkUI"
```

从 `twilight-web/` 数**两级**上一级才是 `InkUI/`：

```
D:\Files\development\
├── InkUI\                 ← 组件库源码（独立仓库）
└── twilight-blog\         ← 本仓库
    └── twilight-web\      ← 从这里 ../../ 才到 InkUI
```

所以新机器上必须先把 `InkUI` 克隆到**同级目录**，否则 `pnpm install` 直接失败。
它有自己的构建步骤（见 §3.3）。

**2. `twilight-blog-api`（SDK）在仓库内，但生产构建**不**走它的 `dist/`。**

```jsonc
"twilight-blog-api": "file:../twilight-blog-api"
```

包自己的 `exports` 指向 `./dist/index.js`，而 `dist/` 是 **gitignore 掉、未被跟踪**的
构建产物。`twilight-web` 靠 `vite.config.ts` 里的 alias 绕过它、直引 `src/index.ts`：

```ts
{ find: 'twilight-blog-api', replacement: '../twilight-blog-api/src/index.ts' }
```

**后果**：改 SDK 源码后**不需要**重建它的 `dist/`，前端构建会立刻用上新源码。
但如果你把 `dist/` 交给别的消费者（发布 npm 包、或别的项目 `npm i` 这个目录），
就必须先重建，否则对方拿到的是陈旧的产物。这一点有守护测试
（`ink-design-alias.test.ts` 断言别名必须指向 `src`）和一段事故记录，见 §7。

---

## 二、环境准备

### 2.1 必需

| 依赖 | 版本要求 | 说明 |
|---|---|---|
| Go | `go.mod` 声明 1.23；实测 1.26.6 可用 | 需要能交叉编译（纯 Go 依赖，无需 CGO） |
| Node.js | SDK 声明 `>=18.18` | 仓库当前用 pnpm，实测 Node 22.22 可用 |
| pnpm | **v11 或更高**（见下方说明） | 不是 npm——有几处 pnpm 专属配置 |
| Python 3 | 3.x | 仅用于 `build.py`（无第三方依赖） |
| **cwebp** 或 **ffmpeg** | 任一 | ⚠️ **硬性依赖，缺了进程直接拒绝启动**，见 §2.3 |

### 2.2 关于 pnpm 的特别说明

`twilight-web/pnpm-workspace.yaml` 里有两条**非默认**设置，是为了绕开本机的安装故障：

```yaml
nodeLinker: hoisted
packageImportMethod: copy
```

文件里的注释说明了原因：isolated 布局（junction/symlink）会**静默产生空目录**
（症状是 `node_modules` 下某个包目录是空的、`tsc` 报 `TS2688`），
hardlink 导入会随机卡死。`hoisted + copy` 用真实文件复制，慢但确定可用。

> 文件里也写了「正常环境下可删除这些设置」。如果你在一台安装正常的机器上，
> 遇到诡异的解析问题时可以试试删掉这两行重装。

### 2.3 ⚠️ WebP 编码器是硬性依赖（最容易踩的坑）

`main.go` 在**打开数据库、启动 Meilisearch 之前**就探测 WebP 编码器，
探测不到直接 `Fatalf` 退出：

```
media conversion is required: no WebP encoder is available: ...
```

**为什么这么严格**：Go 标准库和 `golang.org/x/image` 都只有 WebP **解码器**，
没有编码器。而站点承诺「图片落盘前一律转成 WebP」。早期实现把「找不到 cwebp」
当成可选能力缺失，于是上传退回原格式落盘——症状是「上传头图后磁盘上还是 jpg」，
**只在有人去看磁盘时才会发现**。

**怎么办**：装 `cwebp`（推荐，来自 libwebp）或 `ffmpeg`，放到 `PATH` 上；
或者在 `settings.json` 里把 `webp_tool` 指向二进制的绝对路径。

> 注意配置里**钉着另一台机器的绝对路径**是常见故障：拷了别人的 `settings.json`
> 过来，那条路径在你的机器上不存在。程序会分两种处境给出不同的错误信息
> （「配了但文件不在」vs「什么都没配」），照着提示改即可。

WebM（ffmpeg）**不算**错误——它缺失只意味着 WebM 能力关闭，并且**站点当前
并未使用它**（音视频一律原样落盘）。

### 2.4 目录约定

```
<repo>/
├── twilight-blog/          Go 后端
│   ├── internal/view/web/      ← 前端产物嵌入处（go:embed）
│   ├── dist/                   ← 编译产物（gitignore）
│   └── main.exe                ← 兼容旧文档的副本
├── twilight-web/           前端
│   ├── dist/                   ← vite 构建输出
│   └── debug/                  ← 调试现场（gitignore，任意层级生效）
├── twilight-blog-api/      SDK
└── debug/                  仓库级调试产出
```

**约定：所有调试产出统一扔进 `debug/`**（根级或子项目级都可，`.gitignore` 按
任意层级忽略）。截图、探针脚本、临时 sqlite、响应体转储都归这里，不要散在源码目录。

### 2.5 包管理器只有 pnpm

仓库里**没有** `.npmrc`、没有 `package-lock.json`、没有 `yarn.lock`，只有两个
`pnpm-lock.yaml`。请勿用 npm/yarn 安装——它们会各写一份自己的 lock 文件，
把一个干净的仓库搞成一团。

### 2.6 行尾与缩进由工具统一，不要手改

- **`.gitattributes`：`* text=auto eol=lf`** —— 全部文本文件统一为 LF。
  二进制类型（图片/字体/压缩包/`*.db`/`*.pem` 等）显式标为 `binary`，不做转换。
  这解决了 Windows / macOS / Linux 之间的「整文件重写」假 diff。
- **`.editorconfig`**：默认 2 空格；**Go 用制表符**（gofmt 强制）、
  Python 4 空格、Markdown 保留行尾空格（两个空格 = 换行）、`*.bat`/`*.cmd` 用 CRLF。

> 实践建议：**打补丁或写脚本改文件时按字节读写**。用 Python 的 `read_text`/`write_text`
> 会在 Windows 上把 LF 静默换成 CRLF，于是在你自己的 diff 里制造出整文件改动——
> 而 `.gitattributes` 的 `eol=lf` 又会在提交时把它转回来，两边互相打架。
> 正确做法是 `open(path, 'rb')` / `open(path, 'wb')`，需要时显式指定 `newline=''`。


---

## 三、首次搭建

### 3.1 克隆

```bash
# 两个仓库必须同级
cd D:/Files/development
git clone <twilight-blog-url>
git clone <InkUI-url>          # 目录名必须是 InkUI
```

### 3.2 安装前端依赖

```bash
cd twilight-blog/twilight-web
pnpm install
```

若之前装过 `ink-design` 且改过 InkUI 源码，**必须**先删掉再装：

```bash
rm -rf node_modules/ink-design && pnpm install
```

原因：`file:` + `copy` 导入方式下，pnpm **不会因为源目录内容变化而重新拷贝**。
不删的话你改的 InkUI 代码永远不生效，而且没有任何报错。

### 3.3 构建 InkUI

```bash
cd D:/Files/development/InkUI
npm run build          # = node scripts/build-vite.mjs library && tsc -p tsconfig.types.json
```

**这一步不能省。** `ink-design` 的 `exports` 同时指向 `dist/` 与 `src/`：

```jsonc
"exports": {
  ".": { "types": "./dist/index.d.ts", "import": "./dist/index.mjs", "require": "./dist/index.cjs" },
  "./styles.css": "./dist/inkui.css"
}
"files": ["dist", "src", "README.md"]
```

前端通过 alias 直引 `src/`（为了 tree-shaking），但 **`styles.css` 子路径必须
继续走包 `exports`** → `dist/inkui.css`。所以 `dist/` 必须存在且有内容。

> 注意 `npm run build` 里 vite 的 `emptyOutDir` 会清掉 `dist/`，因此
> **`tsc` 必须排在 vite 之后**（脚本里已经是这个顺序）。顺序反了会丢掉 `.d.ts`。

### 3.4 构建 SDK（可选）

只在你要**发布** `twilight-blog-api` 时才需要：

```bash
cd twilight-blog/twilight-blog-api
npm run build          # = vite build && tsc -p tsconfig.build.json
```

日常开发**不需要**——前端 alias 直引 `src/`。

### 3.5 首次构建整个站点

```bash
cd twilight-blog
python build.py
```

见 §4.1 对这一步的详细说明。成功后打开 `http://127.0.0.1:8080/install` 设置管理员密码。

---

## 四、日常开发流程

### 4.1 构建：`python build.py`

仓库根的 `build.py` 是唯一的总入口，三步：

```
1. 构建前端（twilight-web）→ 产出 dist/ 与 .vite/manifest.json
2. 把产物拷进 twilight-blog/internal/view/web/     ← go:embed 的位置
3. 编译 Go 二进制（默认三个平台）
```

**第 2 步必须在第 3 步之前**，因为前端资源是靠 `//go:embed` 嵌进二进制的；
三个平台的产物共用同一份嵌入结果（前端只构建一次）。

常用参数：

```bash
python build.py --list               # 只看会构建哪些目标，不实际构建
python build.py                      # 默认：宿主是 Windows 时编三平台
python build.py --host-only          # 只编本机平台（开发时最快）
python build.py --skip-frontend      # 跳过前端（嵌入资源已是最新时）
python build.py --targets linux/amd64,linux/arm64
```

默认目标（在 Windows 上）：

```
windows/amd64    → twilight-blog/dist/twilight-blog-windows-amd64.exe（并复制到 twilight-blog/main.exe）
linux/amd64      → twilight-blog/dist/twilight-blog-linux-amd64
linux/arm64      → twilight-blog/dist/twilight-blog-linux-arm64
```

交叉编译不需要 CGO：SQLite 用纯 Go 的 `modernc.org/sqlite`，所有目标显式
`CGO_ENABLED=0`，产出的 Linux 二进制是**静态链接**的（可直接丢进 scratch/alpine）。

> 脚本编译完会读文件头校验架构（PE 的 `MZ`+COFF machine、ELF 的 `\x7fELF`+`e_machine`）。
> 理由是 `go build` 若因环境变量没传对而忽略了 `GOARCH`，会**静默**产出一个本机架构
> 的二进制——文件名看着对，拿到目标机器上才报 `Exec format error`。

**改前端后必须重跑 `build.py`**，否则二进制里还是旧的前端。

### 4.2 前端热更新开发（推荐）

日常调前端不必每次跑 `build.py`。开两个终端：

```bash
# 终端 1：后端（监听的 8080）
cd twilight-blog && ./main.exe

# 终端 2：Vite dev server（5173）
cd twilight-web && pnpm dev
```

访问 **http://127.0.0.1:5173**（不是 8080）。dev server 把这些前缀代理到后端：

```ts
proxy: {
  '/api':           'http://127.0.0.1:8080',
  '/install':       'http://127.0.0.1:8080',
  '/install_status':'http://127.0.0.1:8080',
  '/uploads':       'http://127.0.0.1:8080',
}
```

两个开发期特有的行为，别误判成 bug：

- **`index.html` 不在前端工程里。** 站点外壳由 Go 持有
  （`twilight-blog/internal/view/template/index.html`），因为 `<title>` 与
  `<meta name="description">` 要带服务端才知道的站点描述。dev server 直接读**同一份**
  Go 模板、替换占位符后交给 Vite 的 HTML 管线（见 `vite.config.ts` 的 `indexShellPlugin`）。
- **开发期功能开关全开**（`data-search-enabled="true"`），因为 dev server 够不到
  `settings.json`。要调试「搜索被禁用」的状态，改测试里的 mock，不要指望 dev server。

### 4.3 后端单独编译

```bash
cd twilight-blog
go build ./...
go vet ./...
go test ./...
```

Go 环境变量（受限沙箱 / 离线环境）：

```bash
export GOPROXY=off GOFLAGS=-mod=mod GOTELEMETRY=off
export GOMODCACHE='<repo>/twilight-blog/.modcache'
export GOCACHE='<repo>/twilight-blog/.modcache/gocache'
```

这两个缓存目录已在 `.gitignore` 里，指到仓库内是为了避免写到默认 `$GOPATH`
（受限沙箱下会被拒绝）。**注意用盘符路径，不要用 `/d/...` 的 MSYS 写法。**

### 4.4 启动与子命令

```bash
./main.exe                 # 无参数 = 启动 HTTP 服务（默认 127.0.0.1:8080，见 settings.json）
```

`main.go` 在启动 HTTP **之前**分发子命令。⚠️ **所有子命令都必须在站点的运行目录
（即 `settings.json` / `blog.db` 所在目录）下执行**，否则会报
`settings.json not found: run this from the site's working directory`。

| 子命令 | 用法 | 说明 |
|---|---|---|
| `admin` | `admin set-password [--username <name>] [--nickname <name>]` | 设置/创建管理员密码。**密码从 stdin 读两次**，不接受命令行参数 |
| `backup` | `backup [-o <file>] [--with-index] [--with-logs] [--force] [--quiet]` | 打一个 tar.gz，含数据库 + `settings.json` + 媒体目录 |
| `restore` | `restore <archive\|-> [--yes] [--keep-settings] [--keep-sessions] [--no-safety-backup] [--dry-run] [--force] [--media-dir <dir>] [--accept-archive-settings]` | 用备份替换站点数据 |
| `upgrade-media` | `upgrade-media [--dry-run] [--all] [--quiet]` | 把媒体库里还不是 WebP 的图片重新编码 |
| `email` | `email test -target <recipient>` | 通过配置的 Resend 账号发一封测试邮件 |

**退出码**：`admin` / `backup` / `restore` / `upgrade-media` 用 **2 = 用法或校验失败、
1 = 运行失败**；`upgrade-media` 额外规定「全转成功或无事可做 = 0，至少一张失败 = 1」。

几条容易踩的细节：

- **`set-password` 的密码走 stdin，不能写在命令行上**——避免进 shell 历史与进程列表。
  这是忘记密码时的**唯一**恢复路径（管理员账号没有可达邮箱，没有邮件重置）。
- **`backup` 可以边服务边跑**：数据库用 `VACUUM INTO` 取一致快照，不阻塞写入。
  但 **`restore` 必须先停站**——它会替换 `blog.db`、删掉 `-wal`/`-shm`、整个换掉
  媒体目录，而还在跑的进程会继续往旧文件句柄里写。它靠 `blog.pid` 判断实例是否在运行
  （`--force` 可跳过）。
- **`restore` 的 `--yes` 是硬要求**：stdin 不是终端时必须显式给，程序**不会**把
  「没人能回答」当成同意。
- **`--` 读 stdin**：`backup -o -` 与 `restore -` 都支持管道，可以直接在两台机器
  之间传备份。
- **`restore` 的媒体落点只由本机决定**：没有 `--media-dir` 时用本机
  `settings.json` 的 `media_dir`，有则用显式值。归档里的 `media_dir` **绝不会**
  被当成目标路径（R7-01），只会在两边不一致时打印提示；若归档 `settings.json`
  会被还原，它的 `media_dir` 会被改写成实际落点，保证文件与配置一致。
- **归档 `settings.json` 的安全字段会先比对**：`rate_limit.enabled` 不允许被关，
  `cookie_secure` 只接受 `auto`/`always`，`trusted_proxies`/`allowed_origins`
  只能收窄，`webp_tool`/`webm_tool` 不得改指本机之外的可执行文件。发现削弱默认
  拒绝；已复核时可加 `--accept-archive-settings` 放行并打印警告（R7-03）。
- **`--media-dir` 不能和 `--keep-settings` 同用**，也要求归档里含有
  `settings.json`；否则站点配置仍指向旧目录而媒体被放到了新目录，表现为「文章都在、
  图全挂了」，命令会在动数据之前失败。
- **备份包里有所有人的密码哈希**，按数据库同等对待（`0600`，绝不入库）。

**启动顺序是硬约束**（`main.go` 的包注释与 §7 都提到）：日志 → 配置 → 数据库迁移 →
凭据迁移 → 启动 Meilisearch → 注册路由 → 监听 HTTP。其中「凭据迁移」的位置尤其
重要：必须在 `view.Register` **之前**，否则第一个到达的请求会看到「未安装」，
于是未鉴权的 `/install` 重新开放——那是一个真实的接管窗口。


### 4.5 「未安装」与管理员密码

- **是否已安装**的权威答案是**数据库里有没有 `is_admin = 1` 的用户行**，
  不是 `settings.json` 有没有密码哈希。只有 `settings.json` 而没有哈希，只是默认模板。
- 未安装时进程**照常启动**（不 `Fatalf`），以便提供 `/install` 引导页。
- 管理员凭据在首次启动时从 `settings.json` 迁进 `users` 表，然后从配置里抹掉；
  同时会**吊销迁移前的旧会话**（旧 cookie 的 `user_id` 为 NULL，`RevokeUserSessions`
  够不到它，不主动吊销的话改密码也杀不掉）。
- **忘记密码只能走 CLI**（`./main.exe admin set-password`），没有邮件重置。

---

## 五、验证清单

改完之后按需跑。全部命令都在对应目录下执行。

### 5.1 前端

```bash
cd twilight-web
export npm_config_verify_deps_before_run=false

# 类型检查
node node_modules/typescript/bin/tsc -b

# 全量测试 + 覆盖率（硬阈值 100%，四个指标）
node node_modules/vitest/vitest.mjs run --coverage --coverage.reportsDirectory=debug/cov-<新目录>
```

**覆盖率阈值是 100%（lines / statements / functions / branches 四项）**，不是
「大致很高」。新增纯函数文件即新增一道 100% 门槛。

### 5.2 后端

```bash
cd twilight-blog
gofmt -l ./internal ./main.go      # 应无输出
go build ./...
go vet ./...
go test ./...                       # 见下方关于 -short 的说明
```

> **`go test ./...` 不加 `-short` 会跑一条联网测试**
> （`internal/search` 的 `TestAgainstRealGitHubRelease`，拉真实 GitHub release
> 元数据）。它设计上「连不上就 Skip」，但在网络受限的沙箱里可能拿到**空资产列表的
> 成功响应**，于是走到 `Fatalf` 而不是 Skip。**离线环境下用 `go test ./... -short`**，
> 它会正确跳过。

### 5.3 端到端

```bash
cd <repo>
python build.py
bash .workbuddy/scripts/smoke-e2e.sh
```

`smoke-e2e.sh` 在临时目录里跑一份完整实例，从「未安装」走到
「安装 → 登录 → 建文章 → 搜索 → 改站点描述 → 登出」，并核对前端依赖的响应体形状。
前置是先跑 `build.py`。

```bash
PORT=8123 bash .workbuddy/scripts/smoke-e2e.sh   # 换端口
KEEP=1 bash .workbuddy/scripts/smoke-e2e.sh      # 保留实例目录以便排查
```

缺 Meilisearch 时它退回到「直接启二进制 + 手动 HTTP」。

### 5.4 几何 / 布局

没有自动门禁，只能手跑：

```bash
bash .workbuddy/scripts/visual-measure.sh
```

**必须一次调用跑完，输出不要走管道**（脚本对输出方式敏感）。

---

## 六、settings.json 速查

首次启动会回写一份完整的 `settings.json`（缺的键补默认值），所以直接看运行后
生成的那份最省事。默认值：

| 键 | 默认值 | 约束 |
|---|---|---|
| `server_name` | `""` | 空 = 监听所有网卡（会打 Warning；开发建议填 `127.0.0.1`） |
| `port` | `8080` | |
| `media_dir` | `"media"` | 必须是工作目录内的相对路径；拒绝绝对路径、`..` 与 `.` |
| `log_level` | `"normal"` | |
| `max_upload_size` | `50` | 单位 MB（第一道闸；媒体类型与内容校验在 service 层） |
| `webp_tool` | `""` | 空 = 去 `PATH` 找；**必须能找到一个 WebP 编码器** |
| `webm_tool` | `""` | 可选（站点当前不用 WebM） |
| `code_block_max_lines` | `20` | 3..500 |
| `home_page_size` | `12` | 1..12 |
| `border_radius` | `4` | 0..24（**0 是合法值**，表示直角） |
| `search_enabled` | — | `false` 时三处一起消失：子进程、`/api/search` 路由、前端入口 |
| `cookie_secure` | — | 生产（HTTPS）应为 `true` |
| `trusted_proxies` | — | 反向代理场景必填，否则 RealIP 采信可伪造的转发头 |
| `allowed_origins` | — | |
| 限流组 | — | `login_per_minute` 3、`email_per_minute` 3、`email_per_hour` 10、`comment_per_minute` 5、`verify_per_minute` 10、`install_per_minute` 5、`search_per_minute` 60 |

> `settings.json` **不入库**（`.gitignore` 里有 `/twilight-blog/settings.json`）。
> 它含密码哈希与可能的邮件 API key。
>
> 注：管理员凭据在首次启动后会被迁进数据库并从该文件抹掉，所以运行一段时间后
> 那份文件里**不应**再有 `password_hash`。若还在，说明迁移没成功，去看日志。

---

## 七、必须知道的坑（都踩过）

### 7.1 `script-src 'self'`：内联 `<script>` 永不执行

站点的 CSP **没有** `unsafe-inline`，也没有 hash/nonce。所以：

```html
<!-- 这段永远不会执行，而且服务端看不出任何异常 -->
<script>window.__SITE__ = { searchEnabled: true };</script>
```

这条曾导致「关闭搜索」的开关在线上从未生效过：前端读不到 `false`，走
「属性缺失=全开」兜底照样渲染搜索入口，点进去 404。而所有测试都是绿的——
它们断言的是「HTML 里有这个字符串」，对「浏览器肯不肯执行」没有任何约束。

**规矩**：
- 传数据只用 HTML 属性（`<html data-*>` → `flags.ts` 读 `dataset`），
  或走 `getSite` API + `lib/site.ts` 单例 → CSS 变量。
- **不要用内联脚本，不要放宽 CSP。**
- 守护测试在 `shell-flags.test.ts`，断言打在**通道**上而不是「文本里有这个串」。

### 7.2 dataset 的值全是字符串

```ts
Boolean('false') === true      // ← 坑
dataset.searchEnabled !== 'false'   // ← 正确写法
```

数字型参数**不能用** `Number(raw ?? '')`（缺失时是 `0`，会被当合法值发出去）。

### 7.3 「构建成功」≠「产物完整」

vite 会产出「CSS 引用了 N 个资源、而目录里只有 M < N 个」这种状态，**全程不报错**。
所以嵌入完成后要**逐文件比对指纹**，不能只看命令返回 0。

### 7.4 「单测全绿」≠「打出来的包是对的」

修完 SDK 源码、测试全绿，但浏览器里跑的还是旧逻辑——因为嵌入的 bundle 早于修复。
排查方式是比对 `twilight-web/dist/assets` 与 `twilight-blog/internal/view/web/assets`
（看 `main-*.js` 的文件名最快）。**改前端/SDK 后一定要重跑 `build.py` 并复核产物。**

### 7.5 `lib/head.ts` 是 `<head>` 的唯一写入口

每次 `applyHead` 会**对账**、删掉本页未声明的受管标签。所以**每个路由页面都必须
调 `usePageHead`**，否则上一个页面的 meta 会残留。JSON-LD 必须转义 `<>&`。

### 7.6 分页参数是必填的

所有列表端点共用 handler 的 `maxPageSize = 12`，且 `page` / `page_size` **必填**。
缺失会返回 `400 {"message": "page is required"}`。站点级「首页每页篇数」的上界
必须等于这个 handler 上限，否则首次请求 400 → **整站白屏**，且只在站长改过设置后
才出现（开发时看不见）。

### 7.7 Windows 上的工具坑

- **bash 环境可能起手就是坏的**（找不到 `dirname` / `ls` / `cat` / `head`），
  而且会**制造假结论**——比如 `cat x || echo missing` 在 `cat` 本身不存在时也会
  输出 `missing`。这种情况改用 Python 直接操作文件。
- **curl 是原生 Windows 程序，不认 MSYS 的 `/c/...` 路径**。重定向输出与 cookie jar
  一律用带盘符的路径（`D:/...`）。路径写错时 curl 仍然打印状态码、仍然返回 0，
  只是文件根本没落盘，于是所有依赖它的断言静默失真。
- **变更类请求必须双提交 CSRF token**：把 `tw_csrf` cookie 的值放进
  `X-CSRF-Token` 头。浏览器里由前端脚本自动做，裸 curl 必须手动做——
  这是安全机制正常工作，不是接口坏了。（`tw_session` 是 HttpOnly，脚本读不到；
  `tw_csrf` 是脚本可读的那个。）
- **写脚本/打补丁时按字节读写**（`read_bytes` / `write_bytes`）。用 `read_text`
  会把 LF 静默换成 CRLF，在只有你一个人的 diff 里制造出整文件改动。

### 7.8 测试自身的两个反模式

- **给源码写文本断言前，先剥注释。** 说明性注释必然引用被禁的写法
  （「不要写成 `json: { ...input }`」），不剥的话正则会先命中注释——断言红了，
  却红在你写的说明文字上；反过来注释里若恰好写全了，断言又会凭空变绿。
- **不要为「不可达 / 时序依赖」的分支编造测试。** 先判断那个分支是真需求还是
  防御残渣。若是残渣，**删掉分支**比造测试干净。另有一种情况：测试文件自身在
  收集期就挂（比如它 import 的模块连带加载了缺失的文件），此时断言在其目标场景下
  **永不执行**——那种断言应该删掉。

---

## 八、常用命令速查

```bash
# ---- 首次搭建 ----
cd D:/Files/development && git clone <blog> && git clone <InkUI>
cd twilight-blog/twilight-web && pnpm install
cd ../../InkUI && npm run build
cd ../twilight-blog && python build.py && ./main.exe     # → /install 设密码

# ---- 日常前端（热更新）----
cd twilight-blog && ./main.exe                            # 终端 1
cd twilight-web && pnpm dev                               # 终端 2 → :5173

# ---- 改完前端要出完整产物 ----
python build.py                                           # 仓库根

# ---- 前端验证 ----
cd twilight-web
export npm_config_verify_deps_before_run=false
node node_modules/typescript/bin/tsc -b
node node_modules/vitest/vitest.mjs run --coverage --coverage.reportsDirectory=debug/cov-1

# ---- 后端验证 ----
cd twilight-blog
gofmt -l ./internal ./main.go && go build ./... && go vet ./... && go test ./... -short

# ---- 端到端 ----
python build.py && bash .workbuddy/scripts/smoke-e2e.sh

# ---- 改了 InkUI 源码之后 ----
cd D:/Files/development/InkUI && npm run build
cd ../twilight-blog/twilight-web && rm -rf node_modules/ink-design && pnpm install
```

---

## 九、改一处要看哪里的对照表

| 你改了什么 | 必须跟着做的事 |
|---|---|
| 前端任意代码 | 重跑 `python build.py`（否则二进制里是旧的） |
| SDK（`twilight-blog-api/src`） | 无需重建 `dist/`（alias 直引源码）；要发布才 `npm run build` |
| InkUI 组件源码 | InkUI 里 `npm run build`；然后删 `node_modules/ink-design` 重装 |
| Go 代码 | `go build` / `vet` / `test`；若涉及前端嵌入则重跑 `build.py` |
| 站点设置新增字段 | 照 §六 的表加 config/viewmodel/handler/模板/flags/SDK 六处，细则见 `.workbuddy/memory/MEMORY.md` |
| `.workbuddy/scripts/*` | 脚本本身不入库（`.workbuddy/` 已 gitignore），只在本地生效 |

---

## 十、相关文档

仓库根目录的其他文档（各有侧重，本文件只覆盖「怎么搭、怎么跑」）：

- `HANDOFF.md` — 交接说明，含验证状态与历史要求逐条落地
- `LINUX-PORTABILITY.md` — Linux 可移植性
- `ADMIN-CREDENTIALS-HANDOFF.md` — 管理员凭据迁移的细节
- `deploy/README.md` — 部署（systemd、服务创建、Windows ACL 加固）
- `twilight-blog-api/api.md` — HTTP API 契约（**注意**：路由的唯一事实来源是
  `twilight-blog/internal/view/router.go`，`api.md` 有时滞后）
- `PENTEST-REPORT-ROUND*.md` — 历次安全审计
