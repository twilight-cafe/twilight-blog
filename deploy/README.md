# Linux 部署说明（twilight-blog）

本目录下的 `twilight-blog.service` 是给 systemd 用的单元文件。这份说明记录**为什么**
它是这个样子——其中每一条都对应一个在 Linux 上真实踩过或验证过的问题。

---

## 一、拿到二进制

### 用 build.py（推荐）

前端资源是用 `//go:embed` 嵌进 Go 二进制的，所以必须**先**构建前端、把
`twilight-web/dist` 拷进 `twilight-blog/internal/view/web/`，**再**编译 Go——
否则 `go build` 会因为 embed 目录为空而失败。`build.py` 把这两步串在一起：

```bash
python3 build.py
```

**在 Windows 上**不带参数运行会一次产出三个平台（这正是它的主要用途：

| 目标 | 产物 |
|---|---|
| windows/amd64 | `twilight-blog/dist/twilight-blog-windows-amd64.exe`（并复制一份到 `twilight-blog/main.exe` 以兼容旧文档） |
| linux/amd64 | `twilight-blog/dist/twilight-blog-linux-amd64` |
| linux/arm64 | `twilight-blog/dist/twilight-blog-linux-arm64` |

在其它宿主上默认只产出本机平台。想覆盖用 `--targets`：

```bash
python3 build.py --targets linux/amd64,linux/arm64   # 只要 Linux 包
python3 build.py --host-only                         # 只要本机平台
python3 build.py --skip-frontend                     # 嵌入资源已最新时跳过前端
python3 build.py --list                              # 只看看会构建哪些目标
```

所有目标都显式用 `CGO_ENABLED=0`，所以产出的 Linux 二进制是**静态 ELF**，
不依赖 glibc，可以直接丢进 `scratch`/`alpine` 容器或最小发行版。

每个产物落盘后都会**读文件头复核**（PE 的 `MZ` + machine 字段、ELF 的
`\x7fELF` + `e_machine`）——`go build` 在环境变量没传对时会静默编译出宿主平台的
二进制，文件名看着没问题，要到目标机器上才炸 `Exec format error`。

### 手工交叉编译

不想跑前端构建时（例如嵌入资源已经是最新的），也可以直接调 `go build`：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o blog ./twilight-blog
# ARM 服务器：
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o blog ./twilight-blog
```

注意这条路径**没有** build.py 的文件头复核，产物是否真的是目标架构要靠自己确认
（`file blog` 或 `readelf -h blog`）。

---

## 二、安装步骤

### 用 create-service.py（推荐）

`deploy/create-service.py` 把下面的手工步骤 1–4 串起来，并加上安全护栏：
安装前校验二进制是 ELF 且架构与本机匹配；覆盖旧二进制/单元文件前先落
带时间戳的备份；服务正在运行时先 stop 再换二进制、装完 restart 载入新代码；
`/var/lib/twilight-blog` 里的既有数据一律不碰，可安全重复执行。默认先展示
计划并请求确认，`--dry-run` 只看不做：

```bash
sudo python3 deploy/create-service.py            # 二进制自动从 dist/ 查找
sudo python3 deploy/create-service.py --dry-run  # 只打印计划
sudo python3 deploy/create-service.py --binary ./blog --env-file ./key.env --yes
```

不想用脚本时，手工步骤等价如下（脚本行为以本节为准）：

```bash
# 1) 服务账号与目录（StateDirectory 也会建，这里先建是为了放二进制）
sudo useradd --system --home /var/lib/twilight-blog --shell /usr/sbin/nologin twilight-blog
sudo install -d -o twilight-blog -g twilight-blog -m 0700 /var/lib/twilight-blog

# 2) 二进制放到**系统**目录（不要放数据目录，避免被误删/误改）
sudo install -o root -g root -m 0755 blog /usr/local/bin/twilight-blog

# 3) 发信密钥：只走环境变量，不要写进 settings.json
sudo install -m 0600 -o root -g root /dev/null /etc/twilight-blog.env
echo 'BLOG_MAIL_API_KEY=re_你的密钥' | sudo tee /etc/twilight-blog.env

# 4) systemd 单元
sudo install -m 0644 deploy/twilight-blog.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now twilight-blog

# 5) 首次安装管理员：打开 http://127.0.0.1:8080/install
#    （server_name 默认 127.0.0.1，公网部署请在前面放反向代理，见第四节）
```

---

## 三、几个必须知道的 Linux 行为

### 3.1 工作目录决定一切

`settings.json`、`blog.db`、`log.txt`、`media/`、`meilisearch-data/` **全部是相对路径**。
工作目录不对的后果不是「起不来」，而是更糟的**静默新建一套空状态**：

- `blog.db` 会被新建 → 看起来「数据全丢了」；
- 数据库里没有管理员行 → **`/install` 重新开放**，任何能访问站点的人都能设一个新的管理员密码。

所以单元文件里的 `WorkingDirectory=` 不是可选项。若在容器里跑，等价物是 `WORKDIR`
加一个挂载到该路径的卷。

### 3.2 文件权限

应用会主动收紧：

| 文件 | 权限 | 由谁设置 |
|---|---|---|
| `settings.json` | 0600 | `config.writeFileAtomic` 显式 chmod（不受 umask 影响） |
| `log.txt` | 0600 | `logging.openLocked` |
| `blog.db` / `-wal` / `-shm` | 0600 | `database.restrictDatabasePermissions`（启动时） |
| 数据目录 | 0700 | 同上 |
| `meilisearch-data/` | 0700 | `search.Start` |
| `media/` | 0700（随数据目录） | — |

**为什么不能只靠应用**：SQLite 是**它自己**用 `open(path, O_RDWR|O_CREAT, 0644)` 创建库文件的，
在常见 umask 022 下就是 **0644**——同机任何用户都能读走管理员 bcrypt 哈希、会话/CSRF
哈希与全部邮箱。应用在启动时会 chmod 回来，但那一步在没有 POSIX 权限概念的文件系统上
会失败（只记 Warn）。**`UMask=0077` 是更底层的一道**，且能覆盖 SQLite 之后重建的
`-wal`/`-shm` 与 Meilisearch 子进程写出的所有文件。

**Windows 例外（上表一行都不成立）**：Go 的 `os.Chmod` 在 Windows 上只能切换只读属性，
**不能限制 ACL**，而 `config_test.go` 的权限守护测试在 Windows 上被 `t.Skip`，
所以这条假设从未被验证过。实测结果是继承自卷根的宽权限：

```
NT AUTHORITY\Authenticated Users   Allow   Modify, Synchronize
BUILTIN\Users                      Allow   ReadAndExecute, Synchronize
```

对 `settings.json` / `blog.db` / `blog.db-wal` / `log.txt` 均成立——本机任意已认证用户
既能读走凭据哈希离线爆破，也能**直接改写 `blog.db` 替换 `password_hash`**。

代码侧无法修（`os.Chmod` 在 Windows 上做不到），因此这是一条部署动作：

```powershell
# 以管理员身份运行；-WhatIf 先看会改什么
pwsh -File deploy\harden-windows-acl.ps1 -WhatIf
pwsh -File deploy\harden-windows-acl.ps1
# 回滚（恢复为继承父目录权限）
pwsh -File deploy\harden-windows-acl.ps1 -Revert
```

脚本对**目录**设置可继承的 ACE 而不只是逐个文件：否则日志轮转产生的 `log.txt.1`、
上传产生的 `media/*.webp`、SQLite 重建的 `-wal`/`-shm` 都会重新继承宽松权限。

### 3.3 依赖可执行文件从哪来

**Meilisearch**（必需）：

- 首次启动时若本地找不到，会自动从 GitHub 下载并**校验 SHA-256**。
  校验值来自 GitHub 服务端计算的 `assets[].digest` 字段——因为上游 release
  **不发布任何校验和文件**（v1.53.2 的 16 个资产全是裸二进制、`.deb` 与 `.json`）。
- 查找顺序：`<二进制所在目录>/bin` → `<工作目录>/bin` → `<工作目录>`。
  按上面的安装方式，下载会落在 **`/var/lib/twilight-blog/bin/`**（`/usr/local/bin/bin`
  通常不可写，会自动跳过）。该路径必须可写，否则请预置二进制。
- **推荐预置**（避免生产环境依赖 GitHub 可达性）：

  ```bash
  # 下载后手工校验，再放进数据目录
  curl -fLO https://github.com/meilisearch/meilisearch/releases/download/v1.53.2/meilisearch-linux-amd64
  sha256sum -c <<< "6c00019a887813ccbce54eec52718c9343bf4234383871146c67c76b1d2646cd  meilisearch-linux-amd64"
  sudo install -o twilight-blog -g twilight-blog -m 0750 meilisearch-linux-amd64 \
       /var/lib/twilight-blog/bin/meilisearch
  ```

  注意上游同时发布社区版与企业版，且资产按字母序返回——自动下载已显式跳过
  `enterprise` 资产，预置时也请选社区版。

**cwebp / ffmpeg**（可选）：

- 未安装时**图片与音频按原格式存储**，上传不受影响（这是刻意行为——早期版本会直接
  拒绝没有转换器的图片上传，在默认不装 cwebp 的 Linux 上是致命的）。
- 想启用 WebP 转换：`sudo apt install webp`（提供 `cwebp`）；`ffmpeg` 同时提供
  WebP 与 WebM 能力。
- `settings.json` 里的 `webp_tool` / `webm_tool` 若指向不存在的路径，会自动回退到 `PATH`。

### 3.4 子进程与信号

- 应用会用 `Setpgid` 让 Meilisearch 自成进程组，并设 `Pdeathsig=SIGTERM`：
  父进程即使被 **SIGKILL**（OOM killer、`systemctl kill -9`、容器强杀），
  子进程也会收到内核发的 SIGTERM，不会变成占着 LMDB 锁的孤儿。
- 关站时 `Stop()` 是「先 SIGTERM、给 3 秒收尾、再 SIGKILL」。
- 单元里 `KillSignal=SIGTERM` 与应用的信号处理对齐；`TimeoutStopSec=20` 给足余量。

### 3.5 监听地址与反向代理

`server_name` 默认是 `127.0.0.1`。公网部署请在前面放 nginx/Caddy，并注意两点：

1. 把代理地址写进 `trusted_proxies`，否则限流会把所有请求算在代理这一个 IP 上；
   写进去之后，客户端的 `X-Forwarded-For` 才会被采信（**必须用
   `$proxy_add_x_forwarded_for` 追加，而不是透传客户端原值**，否则限流可被伪造绕过）。
2. `X-Forwarded-Proto: https` 由代理下发，应用据此给会话 cookie 加 `Secure`
   （`cookie_secure: "auto"` 时）。

### 3.6 没有任何 systemd 单元是此前的真实缺口

`twilight-blog/bin/install_pmail_service.py` 是一个 **Linux/Ubuntu** 的服务安装脚本
（用了 `pwd`、`geteuid`、写 `/etc/systemd/system/`），但它装的是**另一个程序（PMail）**，
与本站无关，而且它在 `bin/` 里被 git 跟踪着。本目录的单元文件才是本站的。

---

## 四、备份与还原

凭据现在**只住在数据库里**（`settings.json` 不再含任何密码哈希）。
备份清单因此变成：

| 要备 | 不必备 |
|---|---|
| `blog.db`（**必需**，丢了等于丢凭据与全部内容） | `bin/`、`meilisearch-data/`、`dumps/`（可重建） |
| `media/` | `log.txt` |
| `settings.json`（站点描述等；**不含密钥**） | |

发信密钥在 `/etc/twilight-blog.env` 里，单独备份（或存进密钥管理器）。

上面这张表由 `backup` 子命令直接实现，不必手工拷文件：

```bash
cd /var/lib/twilight-blog && sudo -u twilight-blog /usr/local/bin/twilight-blog backup
# → twilight-backup-20260918-120000.tar.gz（+ 同名 .sha256 旁文件）
```

### 4.1 `backup`

```
twilight-blog backup [-o <file>|-] [--with-index] [--with-logs] [--force] [--quiet]
```

- `-o -` 把归档写到 stdout，于是 `ssh host 'twilight-blog backup -o -' > local.tar.gz`
  可以直接把它拉下来；此时内容清单改走 stderr，避免混进归档。
- **默认不打包 `meilisearch-data/`**：索引每次启动都会从数据库全量重建，打包只是
  让归档更大；`--with-index` 会把它和主密钥（`meilisearch-data.master.key`）**一起**
  带上——两者必须成对，换了钥匙的旧索引读不出来。
- `--with-logs` 会带上 `log.txt` 与全部轮转代（`log.txt.1`…）。
- 目标已存在时默认拒绝覆盖，`--force` 才允许。
- **可以在服务运行中执行**：数据库走 `VACUUM INTO`，拿的是一致快照，不阻塞写入。
  这也是「不要直接 `cp blog.db`」的原因——WAL 模式下最近提交的事务还在
  `blog.db-wal` 里，只拷主文件会**静默丢掉最近几条写入**。

### 4.2 `restore`

```
twilight-blog restore <archive|-> [--yes] [--keep-settings] [--keep-sessions]
                              [--no-safety-backup] [--dry-run] [--force]
```

```bash
sudo systemctl stop twilight-blog
cd /var/lib/twilight-blog
sudo -u twilight-blog /usr/local/bin/twilight-blog restore --dry-run /root/backup.tar.gz
sudo -u twilight-blog /usr/local/bin/twilight-blog restore --yes /root/backup.tar.gz
sudo systemctl start twilight-blog
```

**必须先停服务**。还原会替换 `blog.db`、删掉它的 `-wal`/`-shm`、把整个 `media/`
换掉，而正在运行的进程持有旧文件的句柄——它随后写下的东西会落到已经被替换掉的
inode 上，**不报错**，只是数据慢慢对不上。服务在跑时命令会拒绝执行；判据是工作
目录里的 `blog.pid`（服务启动时写、退出时删）加上「那个进程是否真的还活着」，
所以崩溃后留下的陈旧锁只会得到一条提示，不会把人永久挡在门外。`--force` 可以越过。

其余选项与安全性质：

- **动数据之前先自动打一份「还原前安全备份」**（`twilight-backup-pre-restore-*.tar.gz`），
  它就是回滚点。`--no-safety-backup` 跳过它——此时旧数据会被改名保留而不是删除，
  因为删掉等于把退路堵死。
- 归档先**完整解包并逐项校验**（路径守卫、类型白名单、体积封顶、sha256 对账），
  全部通过之后才问用户，所以「解包成功」这件事本身是有信息量的。
- **默认吊销全部会话**。还原会把 `sessions` 表回滚到备份那一刻，于是备份之后被
  踢下线的设备会重新拿到有效凭证。`--keep-sessions` 才保留归档里的那份。
- `--keep-settings` 保留本机 `settings.json`（端口、代理、媒体目录布局），只换内容
  ——把 A 站的归档还原到 B 站时用。注意媒体会按**本机**的 `media_dir` 落位。
- 非终端（systemd、cron、管道）不会把「没人能回答」当成同意，必须显式 `--yes`。
- 还原后**索引会在下次启动时从数据库重新同步**，`--with-index` 带回来的索引只是
  省一次全量重建。

### 4.3 归档是敏感文件

归档里有全部账号的 bcrypt 哈希、会话 token 摘要、回收站原文与每张图片的**唯一副本**
（媒体是硬删、没有回收站）。应用写出的一切都是 0600，但传输与存放要自己负责：

```bash
# 传输后核对旁文件（一份没校验过的备份只能算「很可能完整」）
sha256sum -c twilight-backup-20260918-120000.tar.gz.sha256
```

定时备份交给 cron 或 systemd timer，注意**用服务自己的账号**跑，否则归档的属主
会变成 root 且权限取决于 umask。`-o` 是普通路径、不做时间格式化，要按日期归档
就包一层 shell：

```ini
# /etc/systemd/system/twilight-backup.service
[Service]
Type=oneshot
User=twilight-blog
WorkingDirectory=/var/lib/twilight-blog
ExecStart=/bin/sh -c 'exec /usr/local/bin/twilight-blog backup --quiet \
  -o /var/backups/twilight/backup-$(date -u +%%Y%%m%%dT%%H%%M%%S).tar.gz'
```

（保留份额、异地存放、定期**真的还原一次**——这些都在这个命令之外，但它们才是
备份有没有用的决定性因素。）

---

## 五、容器部署注意

单元文件不适用于容器，但同样的约束成立：

```dockerfile
# 必须固定工作目录，并让它是可写的
WORKDIR /data
VOLUME /data
# 密钥走环境变量，不要烤进镜像
# docker run -e BLOG_MAIL_API_KEY=... -v blogdata:/data ...
```

- 只读根文件系统（`--read-only`）可以，但 `/data` 必须是可写卷，否则 Meilisearch
  无处落盘，第一次启动就会失败。
- 容器强杀（`docker stop` 超时后的 `-9`）有 `Pdeathsig` 兜底，但**前提是**
  应用是 PID 1 或至少是子进程的直接父进程；若中间隔了一层 shell，
  请用 `exec` 形式启动（`CMD ["/usr/local/bin/twilight-blog"]` 而非 shell 形式）。
- `/tmp` 挂 `noexec` 不影响本站：临时目录只用于转码中间产物，不会被当作程序执行。
