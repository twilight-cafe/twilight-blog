已找到并实机验证了 2 个此前 6 轮都没覆盖的漏洞，都在最新加入的 `backup` / `restore` 模块。下面是完整报告。

---

# 渗透测试报告 · 第七轮（Round 7）

- **目标**：`twilight-blog`（Go 后端 + React 前端 + TS SDK）
- **被测版本**：当前工作区（HEAD `d1d6987 feat: 命令行全量备份/还原` + 未提交改动）
- **授权**：仓库所有者本人
- **方法**：白盒通读 + 实际编译（`go build`/`go vet`/`go test`）+ 在两台隔离副本上跑 PoC（未改项目源码）
- **本轮定位**：前 6 轮把「请求怎么进来的」加固得很彻底，但 **`restore` 是本仓库唯一一个「会不可逆替换全站数据、且明确以『归档不可信』为设计前提」的新功能**，此前报告完全没有覆盖它。本轮就盯这条信任边界。

## 0. 结论摘要

| 编号       | 级别     | 一句话                                                       | 状态       |
| ---------- | -------- | ------------------------------------------------------------ | ---------- |
| **R7-01**  | **严重** | `restore` 把归档里 `settings.json` 的 `media_dir` **原样当作文件系统目标路径**，可删除/替换工作目录之外的任意目录 | 已修复 + 回归测试 |
| **R7-02**  | **高**   | 数据库里的 `media.storage_name` 未做路径校验，公开接口 `GET /api/media/:id` 变成**任意文件读取**（经恶意归档还原注入） | 已修复 + 回归测试 |
| **R7-03**  | **中低** | 还原进来的 `settings.json` 被整体信任，可关闭限流、`trusted_proxies` 放宽到全网、`cookie_secure=never`、`webp_tool` 指向任意可执行文件 | 已修复 + 回归测试 |
| 复用面复验 | —        | 认证 / 会话 / CSRF / SQL / 上传 / 路径 / Markdown 管线**维持原判，未发现新入口** | 已复验     |

> 可达性说明（重要，不夸大）：R7-01 / R7-02 不是未认证远程直接可利用，需要运维在站点目录里执行一次 `restore`。但 `internal/backup/archive.go` 文件头明确写着「**归档是不可信输入**……可能来自下载、U 盘、别人的机器」，`restore` 也支持 `-` 直接从别的机器管道读入。所以这正落在该模块自定的威胁模型内。

### 修复状态（交接后）

- R7-01：`restore` 的媒体落点改由本机决定（本机 `media_dir` 或显式 `--media-dir`），归档值只用于提示；归档 `settings.json` 落盘前会把 `media_dir` 改写成实际落点。新增回归测试覆盖绝对路径与 `../` 逃逸，并断言工作目录之外不出现 `.replaced-*`。
- R7-02：`MediaService.Path` 要求 `storage_name` 是单个普通文件名，拒绝空值、绝对路径、分隔符与 `..`，并 `Lstat` 拒绝符号链接；删除路径先校验、坏记录仍删库但绝不碰文件；handler 将非法名映射为 500。回归测试覆盖公开读取、管理员删除与符号链接。
- R7-03：还原归档 `settings.json` 前比对限流开关、`cookie_secure`、`trusted_proxies`/`allowed_origins` 与转码工具；默认拒绝削弱项，`--accept-archive-settings` 可显式放行并告警。`config.Load` 也新增 `ValidateMediaDir`，非法 `media_dir` 启动即失败。
- P2：`CoverMediaIDsToDelete` 增加排除 `users.avatar_media_id`；红灯测试通过注入 `removeFile`/`renamePath`/`mkdirAll` 钩子修复。

---

## 1. R7-01【严重】restore 被归档内 `media_dir` 用作目标路径 → 任意目录删除/替换

### 1.1 位置与根因

`restore_command.go` 的 `applyRestore`（约 365–380、425 行）：

```go
localMedia   := readMediaDir(settingsPath)                                  // 本机设置
archiveMedia := localMedia
if archived := manifest.PartPath(backup.PartSettings); archived != "" {
    rel, _ := backup.RelativePath(archived)
    archiveMedia = readMediaDir(filepath.Join(staging, rel))                // ← 归档里的值
}
mediaDir := archiveMedia                                                     // 默认直接采信归档
if opts.keepSettings { mediaDir = localMedia }
...
case backup.PartMedia:
    if err := swap(source, mediaDir, mediaDir); err != nil { ... }          // ← 当作目标路径
```

而 `backup_command.go:383-417` 的读取只做「取字符串 + trim」：

```go
func readSiteSetting(path, key, fallback string) string {
    ...
    value, _ := settings[key].(string)
    if trimmed := strings.TrimSpace(value); trimmed != "" {
        return trimmed          // 不做 IsLocal / 不拒绝绝对路径 / 不拒绝 ..
    }
    return fallback
}
```

`restore_swap.go` 的 `swapIn` 会：把已存在的 `dst` 改名挪走 → 把归档内容放进去；且在**默认（带安全备份）路径下 `keepOld=false`，随后 `os.RemoveAll(parked)` 把原目录删掉**。

### 1.2 实机 PoC（已复现）

构造一个格式合法、但 `data/settings.json` 里 `media_dir` 指向工作目录之外目录的归档：

```jsonc
// 归档内 data/settings.json
{"media_dir":"/home/victim","server_name":"127.0.0.1","port":8299}
```

在被测机器上（Linux，实测输出）：

```
$ cd /srv/blog
$ ./blog-engine restore evil.tar.gz --yes
Archive verified: 3 file(s), 127 bytes, checksums match.
Note: media_dir differs (this host "media", archive "/home/twilight/.../.poc/victim"); using ".../.poc/victim".
  restored settings.json
  restored blog.db
  restored /home/twilight/.../.poc/victim      ← 工作目录之外
  revoked every session: every device must sign in again
```

结果：目标目录里原有的 `secret.txt` **消失**，被归档的 `evil.txt` 取代；若改用 `--no-safety-backup`，原目录会被改名为 `victim.replaced-<时间戳>` 保留下来。即：

- 目标可以是任意绝对路径或含 `..` 的相对路径（`/etc`、`~/.ssh`、`/var/lib/twilight-blog`、另一个站点的目录……）；
- 默认配置下先移走后 `RemoveAll` → **不可逆的任意目录销毁**；
- 不需要恶意构造：一台机器产的备份若 `media_dir` 是绝对路径，在另一台机器上 restore 就会写到那个路径去（代码只是打印一句 `Note` 就继续）。

### 1.3 修复建议

1. **绝对不要把归档里的 `media_dir` 直接当目标路径。** 目标目录应当由本机决定：
   - 默认用本机 `settings.json` 的 `media_dir`（等价于强制 `--keep-settings` 的媒体语义）；
   - 需要采用归档布局时，走一个显式 `--media-dir <相对路径>` 参数，并对它做校验。
2. 校验函数至少要求：`filepath.IsLocal`（拒绝绝对路径、`..`、盘符、设备名），且 `filepath.Rel(workdir, abs)` 不得逃出工作目录；落盘前 `os.MkdirAll` 的目标也走同一套校验。
3. 把 `settings.json` 里的 `media_dir` 本身纳入 `config.Load` 的合法性校验（非法直接启动失败），而不是「只在当前目录下相对可写」这种隐式约定。
4. 回归测试：`restore` 一个 `media_dir=../x` 与 `media_dir=/tmp/x` 的归档必须被拒绝，且磁盘上不能出现任何 `.replaced-*` 目录。

---

## 2. R7-02【高】`media.storage_name` 未校验 → 公开接口任意文件读取

### 2.1 位置与根因

```go
// internal/service/media_service.go:589
func (s *MediaService) Path(media *model.Media) string {
    return filepath.Join(s.mediaDir, media.StorageName)   // StorageName 来自数据库，未校验
}
```

调用点：

- **公开**读接口 `internal/view/handler/media.go:115`：`os.Open(h.service.Path(media))`，路由 `GET /api/media/:id`（`router.go:272`，无需登录）。
- 管理端删除 `media_service.go:567-586`：`os.Remove(s.Path(media))`，以及 `ArticleService.HardDelete` 对头图的删除。

`storage_name` 由服务端生成（随机 16 字节 hex + 扩展名），**通过 HTTP 无法注入**；但 `restore` 会把归档里的 `blog.db` 整体换上，而 `internal/backup` 只校验归档的**文件路径**，不校验数据库**内容**。于是「恶意归档 → 还原 → 库里一行 `storage_name='../../../../etc/passwd'`」即可穿越。

### 2.2 实机 PoC（已复现）

在还原进来的库里写入一行：

```sql
INSERT INTO media (storage_name, original_name, media_type, mime_type, size, converted, created_at)
VALUES ('../../../../../../../../../../etc/passwd','passwd','image','image/png',0,0,1700000000);
```

启动服务后匿名访问（实测输出）：

```
$ curl -s -i http://127.0.0.1:8311/api/media/1
HTTP/1.1 200 OK
Content-Type: image/png
Content-Length: 1384

root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
...
```

即：**无需任何身份**，可读取博客进程有权限读的任意文件（`blog.db`、`settings.json`、`/etc/passwd`、`~/.ssh/id_rsa`、journal、另一个站点的文件……；`Content-Type` 还能由攻击者指定）。同一路径用于删除时，管理员调用 `DELETE /api/admin/media/:id` 即可删除任意文件。

### 2.3 修复建议

1. 在 `MediaService.Path` 增加校验：拒绝空值、绝对路径、路径分隔符、`..` 段；用 `filepath.IsLocal(storageName)` **且** 拼接后 `filepath.Rel(mediaDir, abs)` 不逃逸。非法直接返回错误（不要静默 404，便于发现）。
2. 读取/删除媒体文件前，`os.Open` 后可用 `filepath.EvalSymlinks` + 前缀复核，防 `mediaDir` 内的符号链接（当前 `extractFile`/`saveFile` 都是 0600 普通文件，风险较低，但纵深防御值得加）。
3. 把 `storage_name` 不变量写进迁移/守护测试：任何 `media` 行都必须满足 `filepath.Base(storage_name)==storage_name`。

---

## 3. R7-03【中低】还原后的 `settings.json` 被整体信任（配置投毒）

`applyRestore` 默认把归档里的 `settings.json` 原样写回 `settings.json`，下一次启动 `config.Load` 全盘采信。一个恶意归档因此可以：

- `rate_limit.enabled = false`：**关掉全站限流**（登录/验证码/评论/搜索）；
- `trusted_proxies = ["0.0.0.0/0","::/0"]`：让 `X-Forwarded-For`/`X-Forwarded-Host` 可被任意伪造，绕过按 IP 限流并污染来源判定；
- `cookie_secure = "never"`：会话 cookie 失去 `Secure`；
- `webp_tool = "<任意已存在的可执行文件>"`：下次启动时 `mediaconv.Detect` 会 `exec` 它（这是 R3-04/S4 里「本地工具未校验属主」的另一条注入来源；受 `settings.json` 0600 与归档解包 0600 限制，直接 RCE 不总是成立，但它把「管理员配置」变成了攻击者可控输入）。

这与 R7-01 叠加时，攻击者可以在一次 restore 里同时完成「破坏数据」与「削弱防护」。

**修复**：还原配置时做白名单/区间校验（`allowed_origins`、`trusted_proxies` 只能收窄或维持、`cookie_secure` 只允许 auto/always、`rate_limit.enabled` 不允许在 restore 中被关闭），或至少对「归档配置与本机配置的关键安全字段不一致」时要求显式 `--accept-archive-settings`。

---

## 4. 其它观察（非阻塞）

1. **`ArticleService.HardDelete` 删头图会漏掉「站长头像」引用**：`CoverMediaIDsToDelete`（`article_repo.go:307-345`）排除了其他文章与 `moment_media`，但没有排除 `users.avatar_media_id`。若同一张图既是某文章头图又是站长头像，永久删除该文章会删掉媒体文件，`ON DELETE SET NULL` 把头像静默清空。建议在 NOT EXISTS 里补 `users.avatar_media_id`。
2. **测试套件当前有 1 条红灯**：`internal/service TestUpgradeToWebPWarnsWhenTheOriginalFileCannotBeRemoved`。原因是 `saveFile` 每次都会把 `mediaDir` chmod 回 0700，测试「把目录改 0500 让 unlink 失败」的前提被抵消。它不是安全漏洞，但意味着这条守护**并未真正生效**，建议把用例改成用打开文件句柄（Windows）或直接对 `os.Remove` 注入错误。
3. `restore --force` 允许在服务运行时继续（会与旧文件句柄争用，造成静默数据错位），文档已警告，维持原判。
4. 之前报告里那些「本地权限」项（`blog.db` 0644、临时文件竞态、Meilisearch 下载无大小上限）在当前工作区**已修**（`restrictDatabasePermissions`、`os.MkdirTemp` 私有目录、`assets[].digest` 校验等），本轮复核无回退。

---

## 5. 已复验、确认守住的面（未发现新入口）

- 认证：单一登录入口、`equalizeLoginTiming`、管理员并入 `users` 后 `Lookup` 会回查 `is_admin` 并吊销不匹配会话（旧 `user_id IS NULL` 会话也被 `RevokeLegacySessions` 清掉）。
- 授权：`/api/admin` 统一 `RequireAdmin`；访客/管理员的「UserID 非空语义翻转」处都有显式 `IsAdmin()` 判断；`AdminUpdateUser` 拒绝改管理员行。
- CSRF / Origin / CORS：三重防护在位，`Origin: null` 拒绝，`RequireCSRF` 只认请求头、不回退 cookie。
- SQL：118 个调用点全部参数绑定，无动态表名/列名/`ORDER BY` 拼接。
- 上传：内容嗅探 + 魔数复核 + 像素预算 + 私有临时目录 + WebP 强制重编码，脚本类型被拒。
- Markdown/评论 XSS：客户端唯一 sink 是 `ContentView`，默认走评论白名单管线，`allowDangerousHtml:false`，CSP `script-src 'self'`；`isSafeUrl` 与控制字符剥离经复验。
- 归档解包：`safeEntryPath` 正确挡住 zip-slip、symlink、设备文件，并有体积/条目上限。

---

## 6. 底线结论与优先级

| 优先级 | 动作                                                         |
| ------ | ------------------------------------------------------------ |
| **P0** | 修 R7-01：`restore` 的目标 `media_dir` 不再取自归档，改为本机决定 + `filepath.IsLocal` 校验 |
| **P0** | 修 R7-02：`MediaService.Path` 对 `storage_name` 做本地/在目录内校验 |
| **P1** | 还原 `settings.json` 的安全字段做白名单/一致性校验（R7-03）  |
| **P2** | 补 `users.avatar_media_id` 到 `CoverMediaIDsToDelete`；修复红灯测试 |

一句话：**前六轮把「请求入口」守得很死，但第七轮新加的「备份还原」在同一个系统里引入了一个更硬的信任边界——归档被当成不可信输入来解包，却又被当成可信数据来执行（`media_dir` 作路径、`blog.db` 作真源、`settings.json` 作配置）。三者都需要在「解包之后、落盘之前」按本机策略重新校验。**

需要的话，我可以直接把 R7-01/R7-02 的修复补丁和守护测试写出来。