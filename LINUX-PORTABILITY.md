# twilight-blog · Linux 适配性审查报告

**审查日期**：2026-09-12
**范围**：Go 后端在 Linux 上的可移植性、依赖可执行文件的获取链路、文件与目录权限、子进程生命周期
**方法**：源码审计 + 交叉编译实测 + 真实上游 release 元数据核验 + 隔离实例验证
**与上一轮的关系**：这是独立的一轮审查。结论与修复都写在这里；代码改动见 §4。

---

## 一、结论

**代码层面没有平台障碍**——`CGO_ENABLED=0` 交叉编译 linux/amd64 与 linux/arm64 均实测成功，产物是**静态 ELF**（不依赖 glibc），`filepath`/`path` 用法正确，`.exe` 假设全都被关在 Windows 分支里。这些我逐条验过。

**但「全新 Linux 部署」此前根本起不来**，而且有两处默认不安全：

| 类别 | 数量 | 最严重的一条 |
|---|---|---|
| **BLOCKER** | 3 | 自动下载 Meilisearch **必然失败**（上游不发布校验和），失败路径是 `Fatalf` → 进程起不到监听 |
| **SECURITY** | 3 | `blog.db` 按 **0644** 落盘：同机任何用户都能读走 bcrypt 密码哈希、会话/CSRF 哈希、全部邮箱 |
| **ROBUSTNESS** | 5 | 父进程被 SIGKILL 后 Meilisearch 成孤儿，占着 LMDB 锁导致重启失败 |
| 已核实正确 | 12 | 见 §5 |

**已全部修复并验证**（§4 列出改动清单）。剩下 4 条未修，都是需要你决策或超出本次范围的，见 §6。

---

## 二、BLOCKER

### B1. 自动下载必然失败：代码要求的校验和上游不存在 【已修复】

**这是本轮最重要的发现，而且我亲自证伪了「自动下载能用」这个前提。**

`internal/search/download.go` 的 `expectedDigest` 只认三类校验值来源：`<资产名>.sha256`、`.sha256.txt`、或名字含 `sum`/`checksum` 的汇总文件。找不到就 fail-closed。

我拉取了上游最新的 release 元数据（`/releases/latest`，**v1.53.2**），它的 **14 个资产**是：

```
meilisearch-enterprise-linux-aarch64      meilisearch-linux-aarch64
meilisearch-enterprise-linux-amd64        meilisearch-linux-amd64
meilisearch-enterprise-linux-riscv64      meilisearch-linux-riscv64
meilisearch-enterprise-macos-amd64        meilisearch-macos-apple-silicon
meilisearch-enterprise-macos-apple-silicon meilisearch-openapi.json
meilisearch-enterprise-windows-amd64.exe  meilisearch-windows-amd64.exe
meilisearch-error-codes.json              meilisearch.deb
```

**没有任何校验和资产。** 于是调用链是：

```
expectedDigest 找不到校验值
  → "refusing to execute an unverified binary"
  → downloadLatest 失败 → EnsureBinary 失败 → search.Start 失败
  → main.go: logging.Fatalf("start meilisearch: %v") → 进程退出
```

**结论：`bin/` 里那个手工放进去的 `meilisearch-windows-amd64.exe` 掩盖了一个死掉的特性。** 任何没有本地 Meilisearch 的机器（即全新 Linux 部署）都起不来。

**修复**：改读 GitHub 在 `assets[].digest` 里给出的 `sha256:<hex>`——那字段在真实响应里是存在的。

**我验证了这个字段可信**，而不是假定它可信：下载了发布里最小的资产（`meilisearch-error-codes.json`，51873 字节），实测其 SHA-256 与 API 声明**逐字符一致**：

```
api digest   : sha256:52d14334e92a8a41ff3633999a61765d914bfcd857c5f98f8a1216b6bdaa6b1b
actual bytes : 51873            （API 声明 51873 ✓）
actual sha256: 52d14334e92a8a41ff3633999a61765d914bfcd857c5f98f8a1216b6bdaa6b1b  ✓
```

> 验证脚本：`.pentest-hunt/verifydigest/main.go`（用 Go 而不是 curl/Invoke-WebRequest——沙箱里 schannel 拿不到 TLS 凭据，而 Go 自带 `crypto/tls`）。

**信任模型没有被削弱**：`assets[].digest` 与原先依赖的 `.sha256` 资产处在**同一信任级别**（都来自同一次 GitHub API 响应），而且它由 **GitHub 服务端**计算存储，比发布方自己上传的校验和文件更难伪造。原有的 `downloadPrefix` 钉扎（防止把二进制引到 GitHub 之外）保持不变。

另外，失败时的错误信息从一句「拒绝执行未校验的二进制」改成了给出可执行的下一步（手工放置二进制的确切位置），因为这条路径在真实部署里意味着进程起不来。

### B2. 资产选择会挑中**企业版**二进制 【已修复】

修好 B1 之后我立刻在真实数据上跑了一遍，结果选中的是：

```
release=v1.53.2  asset=meilisearch-enterprise-windows-amd64.exe
```

**企业版。** 原因是资产按字母序返回，而 `meilisearch-enterprise-*` 永远排在 `meilisearch-*` 前面，原实现取「第一个匹配」。在 Linux/amd64 上同理，会自动下载 `meilisearch-enterprise-linux-amd64`。

企业版是商业产品：无授权运行既可能违反其许可条款，也不保证与社区版行为一致。这类「能跑但不是我们想要的东西」最难察觉。

**修复**：分两轮选择——第一轮跳过所有含 `enterprise` 的资产；只有在**没有**社区版可选时才退回企业版，并打明确告警（静默失败让人无从下手，静默使用会踩许可问题）。

因为没有 Linux 机器，我把平台做成了参数（`selectAssetFor(release, goos, goarch)`），用**真实的 14 个资产名做夹具**在任意平台上验证四个目标：`linux/amd64`、`linux/arm64`、`windows/amd64`、`darwin/arm64`。这条测试同时是一条金丝雀：上游哪天改了命名或不再提供 digest，它会先失败，而不是等用户在新机器上发现。

### B3. 未装 cwebp/ffmpeg 时**图片完全传不上去** 【已修复】

`internal/service/media_service.go` 在 `!s.conv.WebPEnabled` 时返回 `ErrUnsupportedMediaType` → HTTP **400**。这与代码自己的文档**直接矛盾**：

- `mediaconv` 包注释：「Detect 找不到工具不算错误，只是把对应能力关掉，上传的文件保持原格式」；
- `router.go` 的启动日志：「Images will be stored as-is」。

Linux 发行版默认不装 cwebp（也不装 ffmpeg），而 `settings.json` 里的 `webp_tool`/`webm_tool` 指向不存在的 Windows 路径后会回退到 `PATH`——两边都空。**结果：Linux 上任何 JPG/PNG 都会 400，博客没法配图。**

**修复**：转换器不可用时退化为「原样落盘」，与文档一致。代价是失去「重新编码抹掉多态载荷」这层纵深防御，但内容嗅探（不信任客户端 `Content-Type`）、`nosniff` 与 CSP 都还在——而「完全不能传图」显然更糟。

---

## 三、SECURITY

### S1. `blog.db` 按 0644 落盘 【已修复】

SQLite 是**它自己**用 `open(path, O_RDWR|O_CREAT, 0644)` 创建库文件的（0644 是它写死的模式，见其源码文档里的示例注释），再被进程 umask 掩一次。常见 umask 022 下就是 **0644**——**同机任何用户都能读**。

这个库里装着：

- `users.password_hash`：bcrypt cost 10，可离线爆破；
- `sessions.token_hash` / `csrf_hash`：拿到即可冒用会话（虽然还需原始 token，但哈希泄露让「会话不可离线枚举」这条保证失去意义）；
- 每一个账号的**邮箱地址**；
- 回收站里的文章与评论原文——它们被删，往往正是因为不该再被看到。

对比之下 `settings.json`（0600）与 `log.txt`（0600）都做对了，**唯独最敏感的那个文件没有**。

**修复**：`database.restrictDatabasePermissions`，在 `Open` 的 migrate 之后把 `blog.db` / `-wal` / `-shm` / `-journal` 收成 0600、数据目录收成 0700。放在 migrate 之后是因为 WAL 与 SHM 要到那时才存在；**每次启动都要做**，因为 SQLite 会在最后一个连接正常关闭时删掉 WAL、下次连接再重建——重建那一次权限又回到 0644。

失败只记 Warn 不阻断启动：在没有 POSIX 权限概念的文件系统（FAT/exFAT、部分网络挂载）上 chmod 本就无意义。因此 systemd 单元里另设了 `UMask=0077` 作为更底层的兜底。

### S2. `meilisearch-data/` 是 0755 【已修复】

该目录是**全文索引**（`title` + `content` 都是可搜索字段），原先用 `MkdirAll(..., 0o755)`，其中 LMDB 文件按 0644 落盘。改为 0700，并显式 `Chmod`——因为 `MkdirAll` 对**已存在**的目录不做任何改动，老部署那个 0755 的目录不会被自动收紧。

### S3. `/tmp` 的「创建→删除→交给子进程重建」竞态 【已修复】

`media_service.go` 的 `tempFilePath` 先用 `os.CreateTemp` 取一个随机名，**立刻 `os.Remove`**，再把裸路径交给 cwebp/ffmpeg 让它们自己创建。`O_EXCL` 提供的保护被完全抵消。

在 Linux 上 `/tmp` 是 **1777**（所有人可写**且可列举**），从删除到子进程 `open()` 之间隔着一整个 fork+exec（毫秒级）——本机任何其他用户都能用 `inotify` 盯住这个文件名，抢先用**符号链接**占位，转码器随后会跟随符号链接写入。后果是**以博客进程身份任意写文件**：覆盖 `settings.json` / `blog.db`，或往 `~/.ssh/authorized_keys` 追加一行。触发条件是管理员上传一张图片（`POST /api/admin/media`），而博客管理员上传图片是日常动作。

Windows 上这个问题轻得多（`%TEMP%` 是按用户隔离且带 ACL 的）——这是**典型的只在 Linux 上成立的缺口**。

**修复**：每次上传创建一个**私有**临时目录 `os.MkdirTemp("", "media-upload-")`（0700，属主是本进程），输入与转码产物都放进去，`defer os.RemoveAll` 收尾。别人无法在该目录里创建任何条目，竞态窗口**不是变小而是消失**。

---

## 四、ROBUSTNESS

### R1. 父进程被 SIGKILL 后 Meilisearch 成孤儿 【已修复】

`exec.Command(binary)` 原先既没有 `Setpgid` 也没有 Linux 的 `Pdeathsig`（全仓无任何 `SysProcAttr`）。父进程一旦被 **SIGKILL**（OOM killer、`docker stop` 超时后的 `-9`、`systemctl kill -9`），子进程会被改挂 PID 1 **继续常驻**，并持有数据目录的 LMDB 锁。下次启动时新旧实例争锁——症状是「服务莫名其妙起不来」，排查成本极高。

**修复**（`childproc_linux.go` / `childproc_other.go`，构建标签分平台）：

- `Setpgid: true` + `Pdeathsig: SIGTERM`（**仅 Linux**——`Pdeathsig` 字段只存在于 Linux 的 `SysProcAttr`，darwin/BSD 没有，Windows 更没有）；
- `Stop()` 改为「先 SIGTERM → 等 3 秒收尾 → 再 SIGKILL（发往整个进程组）」，取代原先的直接强杀。Meilisearch 基于 LMDB，数据不会坏，但硬杀会让它跳过正常的落盘与锁释放。

非 Linux 平台退化为原行为（`Process.Kill()`），并在代码里写明这是有意的取舍。`Manager` 新增一个 `done` 通道来观察子进程退出——`waitCh` 只能被读一次，而 `monitor` 已经需要那一次。

### R2. 失败时做 9 次 GitHub API 调用 【已修复】

落点是 3 个目录 × 3 个文件名 = 最多 9 次尝试，而原实现每次都重新拉一遍 release 元数据。没有外网的机器启动时要先卡满 **9 轮 60 秒超时（约 9 分钟）**才报错退出；未认证的 GitHub API 还有 60 次/小时/IP 的限流，9 次调用很容易撞满。

**修复**：把元数据解析（`latestAsset`）提到落点循环**之外**，只解析一次；落点写不进去是预期内的正常降级，逐个跳过并继续。

### R3. `buildDSN` 在 Linux 上会破坏合法路径 【已修复】

`strings.ReplaceAll(path, "\\", "/")` 是为 Windows 写的（URI 里反斜杠是转义字符），但在 Linux 上**反斜杠是合法的文件名字符**。`blog\prod.db` 会被静默改写成 `blog/prod.db`——写到另一个位置，而报错里显示的是改写后的路径，毫无线索。改为只在 `runtime.GOOS == "windows"` 时替换。默认的 `blog.db` 不受影响，所以这是个潜伏问题。

### R4. 没有 systemd 单元，却有一个**别的程序**的安装脚本 【已补】

仓库里搜不到任何 systemd 单元、Dockerfile 或 compose。而 `twilight-blog/bin/install_pmail_service.py` 是一个 **Linux/Ubuntu** 的服务安装脚本（`import pwd`、`os.geteuid()`、写 `/etc/systemd/system/pmail.service`、`CAP_NET_BIND_SERVICE`）——**它装的是 PMail，与本站无关**，而且被 git 跟踪着。

这个缺口很实际：应用把 `settings.json` / `blog.db` / `log.txt` / `media/` / `meilisearch-data/` 全部写成**相对当前工作目录**的路径，而 systemd 默认 CWD 是 `/`。不设 `WorkingDirectory` 的后果不是「起不来」，而是更糟的**静默新建一套空状态**——数据库里没有管理员行，于是 **`/install` 重新开放**，任何能访问站点的人都能设一个新的管理员密码。

**已补**：`deploy/twilight-blog.service` + `deploy/README.md`。单元文件里逐条注明了每个设置对应哪个问题，包括 `WorkingDirectory` / `StateDirectory` / `UMask=0077` / `EnvironmentFile`（注入 `BLOG_MAIL_API_KEY`）/ `NoNewPrivileges` / `ProtectSystem=strict` / `PrivateTmp` 等，以及「为什么 `ProtectSystem=strict` 下 `ReadWritePaths` 必须包含数据目录」。

### R5. macOS/arm64 匹配不到任何资产 【已修复】

顺带发现：macOS 的 arm64 资产叫 `meilisearch-macos-apple-silicon`，名字里**既没有 `arm64` 也没有 `aarch64`**，只有 `apple-silicon`，所以 darwin/arm64 永远匹配不到。补了别名。这条超出 Linux 范围，但它在同一个函数里、同一个 bug 类，留着不如一起修。

---

## 五、已核实正确（无需改动）

| # | 项 | 依据 |
|---|---|---|
| 1 | **交叉编译可行** | 实测 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64\|arm64 go build` 均成功（20,756,532 / 19,680,791 字节），ELF magic `7F 45 4C 46`、64 位、小端、机器码 0x3E/0xB7、**静态链接无 `ld-linux`**。原因是 `modernc.org/sqlite` 是纯 Go 实现，其余依赖也全是纯 Go |
| 2 | `filepath` vs `path` | 字节路径一律 `filepath`；唯一用 `path` 的是 `embed.FS` 的 SPA 路由（那里**必须**用斜杠） |
| 3 | `.exe` 假设被正确限定 | `binaryNames()` / `execNames()` 的 `.exe` 变体都在 `GOOS=="windows"` 分支内；`bin/` 那个 128MB 的 Windows Meilisearch 在 Linux 上不会被误选（可执行位检查拦住） |
| 4 | zip/tar 路径穿越检查 | `filepath.Join` + `HasPrefix(target, Clean(dest)+os.PathSeparator)`，Linux 语义下成立 |
| 5 | `settings.json` 是 0600 | `writeFileAtomic` 对**新建的临时文件**显式 `Chmod(0600)`——chmod 不受 umask 影响 |
| 6 | `log.txt` 是 0600 | `OpenFile(..., 0o600)`（仅创建时生效，配合上面的 umask 兜底） |
| 7 | `_pragma` DSN 在 Linux 生效 | 驱动以 `SQLITE_OPEN_URI` 打开并执行 `pragma <value>`，`verifyPragmas` 的断言成立 |
| 8 | `cleanMeiliEnv` 正确 | 剔除全部 `MEILI_*` 与 `BLOG_MAIL_API_KEY`，再用显式值覆盖监听地址为 `127.0.0.1:<随机端口>`——Linux 上常见的 `MEILI_*` 环境变量无法把监听改成外部可达 |
| 9 | 信号处理覆盖正常停机 | `signal.Notify(os.Interrupt, syscall.SIGTERM)`，收到后显式停子进程再退出 |
| 10 | 无 Windows 专属 API | 全仓唯一 `syscall` 引用就是上面那个 `SIGTERM`；无注册表、无服务控制、无盘符逻辑 |
| 11 | 下载临时目录本身是安全的 | `download.go` 用 `os.MkdirTemp`（0700）——**与 S3 的 media 路径不是一回事**，不要混为一谈 |
| 12 | 原子写（上一轮引入）在 Linux 上更优 | 同目录临时文件 + 显式 chmod + `Sync` + `Close` + `Rename`；`rename(2)` 覆盖已存在目标是原子的 |

---

## 六、未修复（需要你决策或超出范围）

1. **`webp_tool` / `webm_tool` 没有可信性校验**（`mediaconv.go`）。`FindExecutable` 有 `candidateIsSafelyOwned`（拒绝组内/其他人可写的文件），但配置里的转换器路径只做 `exists()` + 按名字猜类型就 `exec`。定级中低（能改配置的人通常已经能改别的），但校验口径不一致本身值得统一。**我没改**是因为它需要把校验逻辑抽成公共函数并决定「配错了是拒绝启动还是打告警」，属产品决策。
2. **350MB 下载没有体积上限**，只有 10 分钟总超时；`/tmp` 需同时容纳归档与解包结果（约 670MB，tmpfs 上就是内存）。建议钉版本 + 加大小上限。**我没改**因为钉版本会牺牲「跟随 latest」，而那是刻意的设计取舍。
3. **`verifyPragmas` 不校验 `journal_mode`**。在只读文件系统或 NFS/CIFS 上，SQLite 无法写 `-shm` 时会**静默降级**为 `delete` 模式，而启动检查发现不了——`busy_timeout` 与 `foreign_keys` 都断言了，唯独 `journal_mode` 没有。这是子代理标出的最值得复核的一条，我确认属实。
4. **`install_pmail_service.py` 仍被 git 跟踪**，且与本站无关。我**没有删**——它是被跟踪的文件，删除属于破坏性操作，而我不清楚你是否刻意保留它。建议 `git rm`。
5. ~~**`build.py` 只构建宿主平台**~~ —— **已修**。`build.py` 现在在 Windows 上默认一次产出
   windows/amd64 + linux/amd64 + linux/arm64 三份产物（统一 `CGO_ENABLED=0`），
   并在每个产物落盘后读文件头复核平台与架构（见 §7.1）。

---

## 七、本次改动清单

**新增**
`deploy/twilight-blog.service` · `deploy/README.md` · `internal/search/childproc_linux.go` · `internal/search/childproc_other.go`

**修改**
`internal/search/download.go`（digest 来源、企业版排除、macOS 别名、元数据提到循环外、错误信息）
`internal/search/manager.go`（`Setpgid`/`Pdeathsig`、优雅关闭、`done` 通道、数据目录 0700、`EnsureBinary` 结构）
`internal/service/media_service.go`（私有临时目录、无转换器时降级存储）
`internal/database/sqlite.go`（`restrictDatabasePermissions`、DSN 反斜杠改写限 Windows）
`internal/search/download_test.go`（新增 3 条守护测试 + 1 条联网金丝雀）

**验证**
`gofmt` 干净 · `go build ./...` 通过 · `go vet ./...` 通过 · `go test ./...` **全绿**
`linux/amd64` 与 `linux/arm64` 交叉编译通过，产物为静态 ELF
对真实上游 release 的 `TestAgainstRealGitHubRelease` 通过（选中社区版 + 拿到 digest）

**关键测试的反证**
- 去掉 `parseAssetDigest` 那一步 → `TestExpectedDigestUsesGitHubAssetDigest` 失败
- 去掉 `selectAssetFor` 的 enterprise 过滤 → `TestSelectAssetForPrefersTheCommunityBuild` 的 linux/amd64 与 windows/amd64 断言立刻失败

---

## 七点一、`build.py` 多平台构建（后续补充）

原先 `build.py` 只构建宿主平台，交叉编译要手工敲命令——而手敲很容易漏掉
`CGO_ENABLED=0` 或 `GOARCH`，漏掉的后果又很隐蔽。现在：

**在 Windows 上不带参数运行**即产出三个平台：

| 目标 | 产物 |
|---|---|
| windows/amd64 | `twilight-blog/dist/twilight-blog-windows-amd64.exe`，并复制到 `twilight-blog/main.exe`（仓库多份文档按 `main.exe admin set-password` 引用它，不能丢） |
| linux/amd64 | `twilight-blog/dist/twilight-blog-linux-amd64` |
| linux/arm64 | `twilight-blog/dist/twilight-blog-linux-arm64` |

在其它宿主上默认只产出本机平台（保持原行为）。新增开关：
`--targets <goos>/<goarch>[,...]`、`--host-only`、`--skip-frontend`、`--list`。

设计上的几个要点：

1. **所有目标统一 `CGO_ENABLED=0`。** 交叉编译必须如此，而本项目没有任何 cgo 依赖
   （SQLite 走 `modernc.org/sqlite` 的纯 Go 实现）。统一之后本机产物与交叉产物是
   同一种形态——静态链接、不依赖 glibc。
2. **前端只构建一次**，三个平台嵌同一份资源（嵌入发生在编译期，不可能出现
   「Windows 包和 Linux 包前端不一致」）。
3. **产物落盘后读文件头复核**：PE 查 `MZ` + COFF machine，ELF 查 `\x7fELF` +
   `e_machine`。这一步挡的是「`GOOS`/`GOARCH` 没传进 `go build` 时它会**静默**
   编译出宿主平台二进制」——文件名看着没问题，部署到目标机器上才炸
   `Exec format error`，而那时排查会怀疑编译器、怀疑工具链，就是不会怀疑构建脚本。
4. **不扩大前端构建失败的回退范围。** 原脚本有一条很窄的 EPERM 回退（受限环境下
   Node 起不了子进程时用已有产物），保留；但真正的编译错误必须硬失败，否则会
   安安静静地打出一个前端是旧版本的包。失败时会提示两个可执行的下一步
   （`pnpm install` 或 `--skip-frontend`）。
5. **脚本的运行输出保持 ASCII，注释与文档字符串用中文。** 这不是随意规定：
   Python 在 Windows 上按控制台代码页（通常是 936）编码 stdout，同一段中文写进
   CI 日志或重定向到文件后，换个 UTF-8 查看器就是乱码。注释只在编辑器里读，
   不受影响；输出要经得起管道与日志。（这个问题是在实测中被逐字节确认的：
   输出字节是 `C7 B0 B6 CB...`，按 CP936 解码才是正确的中文。）

**验证（全部实测）**

```
windows/amd64   twilight-blog-windows-amd64.exe   20.2 MiB   PE  machine=0x8664  OK
linux/amd64     twilight-blog-linux-amd64         19.8 MiB   ELF machine=0x3E    OK
linux/arm64     twilight-blog-linux-arm64         18.8 MiB   ELF machine=0xB7    OK
main.exe                                           20.2 MiB   PE  machine=0x8664  OK  （与 windows 产物逐字节一致）
```

产物校验本身做了反证（`.pentest-hunt/verify-artifact-check.py`，7/7 通过）：

| 用例 | 结果 |
|---|---|
| ELF 产物被声明为 windows/amd64 | 拒绝：`not a PE binary (missing MZ signature)` |
| PE 产物被声明为 linux/amd64 | 拒绝：`not an ELF binary` |
| linux/amd64 产物被声明为 linux/arm64 | 拒绝：`ELF e_machine is 0x3E (expected 0xB7)` |
| linux/arm64 产物被声明为 linux/amd64 | 拒绝：`ELF e_machine is 0xB7 (expected 0x3E)` |

---

## 八、诚实声明

1. **没有在真实 Linux 机器上跑过。** 所有验证手段是：交叉编译 + ELF 头解析、参数化的平台分支单测（用真实资产名做夹具）、以及把代码路径读到 `filepath`/`syscall` 一级。**没有实测 `Pdeathsig` 在 OOM 场景下的行为，也没有实测 systemd 单元的语法**（`systemd-analyze verify` 需要 Linux）。单元文件请在上线前用 `sudo systemd-analyze verify` 过一遍。
2. **`assets[].digest` 的可信性只验证了一个资产**（`meilisearch-error-codes.json`，51873 字节）。它证明了该字段是真实哈希而不是占位值；没有逐个验证 14 个资产，也没有验证历史 release 里该字段的可用性（老 release 可能没有——那时会回退到校验和文件路径）。
3. **`/tmp` 竞态的严重性依赖一个我未实测的前提**：攻击者能用 `inotify` 在 `os.Remove` 与子进程 `open()` 之间赢得竞争。窗口是 fork+exec 级（毫秒），我认为这是可赢的，但没有实际构造 PoC。
4. **S2（Meilisearch 数据目录里 LMDB 文件的确切权限）是按「MkdirAll 0755 + LMDB 默认 0644」推算的**，没有实测 Meilisearch 自己创建文件时用的模式。目录 0700 的修复本身不依赖这个推算。
5. **没有为 `settings.json` 的 umask 行为做 Linux 实测**——`writeFileAtomic` 的逻辑（显式 chmod 新建的临时文件）在 Linux 上是标准语义，我按代码判定。
