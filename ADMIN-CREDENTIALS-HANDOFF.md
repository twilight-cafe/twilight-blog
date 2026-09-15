# 交接：把管理员凭据并入 `users` 表（方案 B）

> **本文件是给"重开上下文后的新 agent"的完整施工说明。**
> 它自带全部背景、代码位置、陷阱与验收标准，**不依赖任何先前对话**。
> 本文件由上一轮（只做设计、未改代码）产出。**代码尚未动过。**

---

## 0. TL;DR

把站长（管理员）变成 `users` 表里 `is_admin = 1` 的那一行，让「用户名全站唯一」由**现成的
`idx_users_username` 索引**负责，从而删掉为绕过它而存在的整套变通代码（预留用户名判定、
启动时重名扫描、登录名的恒定时间比较、`settings.json` 里的密码哈希）。

**用户已拍板的设计决策：D-1 = 方案 b** —— 管理员也可以走 `PUT /api/me/password` 改自己的密码
（与访客同一条路）。这**顺带修掉一个线上 bug**（见 §2）。

预计改动：**约 3.5 天**，5 个可独立验收的提交（§11）。

---

## 1. 必读文件（按顺序）

| 顺序 | 文件 | 为什么 |
|---|---|---|
| 1 | `twilight-blog/internal/view/router.go` | 全部路由与依赖装配，是改动地图 |
| 2 | `twilight-blog/internal/config/config.go` | 要被大幅削弱的那个包 |
| 3 | `twilight-blog/internal/service/user_service.go` | 预留用户名机制就在这里 |
| 4 | `twilight-blog/internal/view/handler/auth.go` | 登录/安装/改密三条路径 |
| 5 | `twilight-blog/internal/session/session.go` | `UserID()` 的语义要变（§6） |
| 6 | `twilight-blog/internal/model/user_repo.go` | `userColumns` 常量 + `List` |
| 7 | `twilight-blog/internal/database/schema.go` `sqlite.go` | 迁移纪律（§5） |
| 8 | `twilight-blog/HANDOFF.md` | 项目的既有约定与历史坑（**环境坑在第六节**） |

---

## 2. 为什么选 b：它修掉一个线上 bug

**已验证的事实（不要怀疑，但可以复核）：**

- `twilight-web/src/pages/admin/Profile.tsx:23-39` 按角色分流，管理员走 `AdminProfile`。
- `Profile.tsx:146` 在 `AdminProfile` 里渲染 `<PasswordCard />`。
- `Profile.tsx:260` 那个卡片调用 `blog.changePassword({currentPassword, newPassword})`
  → `twilight-blog-api/src/endpoints/auth.ts:121` → `PUT /api/me/password`。
- 后端 `internal/view/handler/auth.go:333` 对管理员返回
  `403 {"message":"only visitor accounts have a password"}`。
- `twilight-blog-api/api.md:662` 还把这个 403 写成了规范：
  `| 403 Forbidden | The session belongs to the administrator |`

**结论：管理员点自己账号页上的「修改密码」永远失败，toast 显示「修改失败」。**
而 `Profile.tsx:5` 的注释还写着「控制台没有改管理员密码的入口」——注释与代码互相矛盾。

**因此：方案 b 的前端工作量是零。** 那个表单早就写好了，只是后端一直拒绝它。
实施后必须同步改掉的三处描述：
1. `Profile.tsx:5` 的注释（说没有入口）。
2. `twilight-blog-api/api.md:640`（"Changes the visitor's password" → 当前账号的密码）
   与 `:662`（删掉那一行 403）。
3. `PENTEST-REPORT.md` 授权矩阵里把 `PUT /api/me/password 管理员 403` 记为「正确」的那一行
   —— 它当时是从"契约一致"的角度判的，现在要标注为 bug。

---

## 3. 环境准备（照抄，否则第一步就卡住）

工作目录：`D:\Files\development\twilight-blog`

```powershell
Set-Location D:\Files\development\twilight-blog\twilight-blog
$env:GOPATH     = 'D:\Files\development\twilight-blog\.gopath'
$env:GOCACHE    = 'D:\Files\development\twilight-blog\twilight-blog\.gocache'
$env:GOMODCACHE = 'D:\Files\development\twilight-blog\twilight-blog\.modcache'
$env:GOPROXY    = 'off'          # 离线构建；若需新增依赖改成 https://goproxy.cn,direct
$env:GOTELEMETRY = 'off'
$env:GOTOOLCHAIN = 'local'
Remove-Item Env:\GOFLAGS -ErrorAction SilentlyContinue
```

**为什么必须这么设（踩过的坑）：**
- `GOCACHE` 默认在 `%LOCALAPPDATA%`，沙箱下写入被拒 → 必须指到工作区内。
- `GOPATH` 默认在 `C:\Users\<user>\go`，`go mod download` 会往 `$GOPATH/pkg/sumdb` 写 →
  沙箱拒绝（`Access is denied`）。**把 GOPATH 指到工作区内即可，不需要关掉 sumdb 校验。**
- `GOPROXY=off` 是离线；`go mod tidy` 需要联网（它会拉 `x/tools` 作为测试依赖），
  那时改成 `https://goproxy.cn,direct`。

**其它硬性环境约束：**
- **不要用 pnpm**（无 TTY 下会中断并损坏 `node_modules`）。前端类型检查用
  `node node_modules/typescript/bin/tsc -b`（在 `twilight-web/` 下）。
- **`vite build` 在受限模式下必然 `spawn EPERM`**（esbuild 经命名管道启动子进程）。
  需要构建前端产物时，按沙箱规则申请一次性放行 `danger-full-access`。
- **绝对不要用 PowerShell 的 `Get-Content -Raw` / `Set-Content` 去改源码文件。**
  PS 5.1 对无 BOM 文件按系统 ANSI（GBK）解码，会把 UTF-8 中文注释写坏且**不可逆**
  （上一轮就这么毁过一次 `media_service.go`，136 个字节）。源码只用 read/edit/write 工具。
- 测试实例放在工作区内（如 `.pentest-run/`），**不要动** `twilight-blog/settings.json`
  与 `blog.db`（那是用户的测试数据，但也不要覆盖）。
- 后台进程别用 `| Select-Object -First N` 截输出——管道一关进程就被杀。

---

## 4. 必须守住的三条不变量

| # | 不变量 | 由谁保证 |
|---|---|---|
| **I1** | 全站**最多一个**管理员 | 新增的**部分唯一索引** `idx_users_single_admin`（DB 层，不是应用层判断） |
| **I2** | **外部 API 形状完全不变** | `MeResponse` 在 admin 会话下仍只给 `role`/`name`/`username`，**不给** `user`；`/api/admin/users` 仍只列访客 |
| **I3** | 管理员会话仍是 `role=admin` + `AdminTTL`(24h)；访客仍是 `role=user` + `UserTTL`(30d) | 登录与改密后的会话签发都按 `user.IsAdmin` 选择角色与 TTL |

**I2 是这次重构最重要的验收标准**：所有既有 HTTP 响应体必须逐字节不变（唯一有意的例外是
`PUT /api/me/password` 对管理员从 403 变为 204，这正是 D-1=b 的目的）。

---

## 5. 数据模型改动

### 5.1 建表语句（`internal/database/schema.go`，`users` 表）

```sql
    is_admin      INTEGER NOT NULL DEFAULT 0,
```

### 5.2 必须同时登记到 `addedColumns`（**老库不会自动加列**）

`schema.go:240-254` 的注释解释了为什么：`CREATE TABLE IF NOT EXISTS` 对已存在的表不做任何改动，
所以新增一列要改两处——建表语句（只对新库生效）+ `addedColumns`（对老库生效）。

```go
// schema.go:255 起
var addedColumns = []columnAddition{
	{"comments", "like_count", "INTEGER NOT NULL DEFAULT 0"},
	{"comments", "nickname", "TEXT NOT NULL DEFAULT ''"},
	{"users", "nickname", "TEXT NOT NULL DEFAULT ''"},
	{"users", "password_hash", "TEXT NOT NULL DEFAULT ''"},
	{"users", "username", "TEXT NOT NULL DEFAULT ''"},
	{"users", "is_admin", "INTEGER NOT NULL DEFAULT 0"},   // ← 新增
	{"email_verification_codes", "attempts", "INTEGER NOT NULL DEFAULT 0"},
}
```

`definition` 必须带 `DEFAULT`：SQLite 的 `ALTER TABLE ADD COLUMN` 加 `NOT NULL` 列时要求给默认值。

### 5.3 「最多一个管理员」用部分唯一索引钉死（放进 `addedIndexes`）

```go
// schema.go:275 起
var addedIndexes = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username
	    ON users(username COLLATE NOCASE) WHERE username <> ''`,
	// 只索引 is_admin=1 的行，而它们的 is_admin 恒为 1 —— 于是"第二条管理员行"
	// 必然撞唯一约束。把 I1 变成数据库不变量，而不是"没人会那么写"的约定。
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_single_admin
	    ON users(is_admin) WHERE is_admin = 1`,
}
```

必须放 `addedIndexes` 而不是 `schemaSQL`：它引用了刚刚才补上的列，放前面会让整个批次
报 `no such column`，迁移失败、进程起不来（`schema.go:264-268` 有同样的说明）。

### 5.4 管理员的 `email` 用**空串**作哨兵（关键决策，别改）

`users.email` 是 `NOT NULL ... UNIQUE`，而 **SQLite 无法通过 ALTER 放宽列约束**，
所以管理员行必须有一个 email 值。选空串的理由：

- `''` 满足 `NOT NULL`（`NOT NULL` 只拒绝 NULL，不拒绝空串）；
- **没有任何注册路径能产生它**：`normalizeAndValidateEmail`（`user_service.go:604-614`）
  明确拒绝空值，`AdminUpdateUserRequest.Validate` 也要求非空；
- `UNIQUE` 保证不可能有第二行空 email。

于是它是天然的"不可注册地址"，**零新增守卫代码**。
（若改成 `admin@example.com` 之类的真实形状，就必须新增"禁止注册该地址"的守卫，否则用户
抢先注册就撞唯一约束——不要那么做。）

**代价（必须记入文档与运维说明）**：管理员**不能**通过「忘记密码」自助重置
（那条路要求邮箱存在且能收信）。这正是 §10 那个 CLI 存在的理由。

---

## 6. ⚠️ 四个静默失败陷阱（这一节是全文最重要的部分）

这四个坑都不会报错、不会有编译失败，只会让行为悄悄错掉。

### T1 — `userColumns` 漏加 `is_admin` ⇒ 管理员被降级为访客

`internal/model/user_repo.go:46` 有一个**共用的 SELECT 列清单**：

```go
const userColumns = `id, email, COALESCE(username, ''), COALESCE(nickname, ''), COALESCE(password_hash, ''), created_at, updated_at`
```

`FindByUsername` / `FindByEmail` / `GetByID` / `List` 四条路径全用它（`:64, :85, :103, :213`）。

**漏加 `is_admin` 的后果**：`model.User.IsAdmin` 恒为 `false` → 管理员登录时拿到
`role=user` 会话 → 进不了 `/api/admin/*` → **站点彻底锁死**。而所有编译与既有测试都是绿的。

改动清单：
```go
const userColumns = `id, email, COALESCE(username, ''), COALESCE(nickname, ''), COALESCE(password_hash, ''), COALESCE(is_admin, 0), created_at, updated_at`
```
`scanUser`（`:48`）同步加一个 `&user.IsAdmin` 扫描目标（注意 SQLite 里 `INTEGER` → Go 里用
`bool` 需要 `sql` 驱动支持；`modernc.org/sqlite` 支持 `*bool` 扫描 0/1，但**要写个测试确认**，
不确定就扫进 `int64` 再转换）。

### T2 — `ChangePassword` 里的 `startUserSession` 会把管理员降级

`internal/view/handler/auth.go:331-361` 现在是：

```go
if err := h.sessions.RevokeUserSessions(ctx, *sess.UserID); err != nil { ... }
if err := h.startUserSession(c, *sess.UserID); err != nil { return err }   // ← 恒发 RoleUser
return c.NoContent(http.StatusNoContent)
```

`startUserSession`（`:426-435`）硬编码 `session.RoleUser`。D-1=b 放开管理员之后，
管理员改完密码会被**当场降级成访客会话**——等于自己把自己踢出后台，而请求返回 204。

必须改成按角色签发：

```go
// reissueSession 按账号的真实角色重新签发会话。
//
// 不能直接复用 startUserSession：它硬编码 RoleUser，管理员改完密码会被降级。
func (h *AuthHandler) reissueSession(c echo.Context, user *model.User) error {
	if user.IsAdmin {
		return h.startAdminSession(c, user.ID)
	}
	return h.startUserSession(c, user.ID)
}
```

这要求 `ChangePassword` 能拿到 `*model.User`。建议把
`UserService.ChangePassword`（`user_service.go:335-360`）的签名从 `error` 改为
`(*model.User, error)`——它内部本来就 `GetByID` 了一次，直接返回即可，不必多查一次库。

同时新增一个 `startAdminSession(c, adminUserID)`：现在 `loginAdmin`（`:125`）是内联
`h.sessions.Create(ctx, session.RoleAdmin, nil, session.AdminTTL)`，**注意 `userID` 参数
要从 `nil` 改成 `&adminUserID`**，否则 `RevokeUserSessions(adminID)` 杀不掉管理员会话、
"改密后其他设备下线"这条保证就断了。

### T3 — `List` 漏加 `WHERE is_admin = 0` ⇒ 管理员出现在后台用户列表

`user_repo.go:201-217`：**两处**都要加过滤，否则列表与 `total` 会不一致：

```go
if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 0`).Scan(&total); err != nil { ... }

rows, err := q.QueryContext(ctx, `
SELECT `+userColumns+`
FROM users
WHERE is_admin = 0
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?
`, p.Limit(), p.Offset())
```

已 grep 确认 `List` 是**唯一**枚举账号的仓储方法（`user_repo.go:208,214`）。
`backfillUsernames`（`database/sqlite.go:246,268`）会扫 `users`，但管理员行有用户名所以会被跳过；
而 `:268` 那条 `SELECT username FROM users WHERE username <> ''` 把管理员的名字也算进去是**对的**
——它能防止回填出来的用户名撞上管理员的名字。

### T4 — 迁移必须早于 `view.Register` ⇒ 否则 `/install` 短暂开放

详见 §9。

---

## 7. `session.UserID == nil` 的语义审计（逐点处理）

**现状**：`session.UserID(c)` 对管理员返回 `nil`，多处代码把「`nil`」当作「不是访客账号」用
（`session.go:317-325` 的注释明确写了这个约定）。管理员有了 `user_id` 之后这些判断会**静默翻转**。

| 位置 | 现在 | 迁移后 | 处置 |
|---|---|---|---|
| `handler/auth.go:291` `UpdateOwnProfile`（`PATCH /api/me`） | `!ok \|\| sess.UserID == nil` → 403 | **管理员会通过** | **保持 403**：改为 `if !ok \|\| sess.IsAdmin() \|\| sess.UserID == nil`。**这是明确的不做项**，见 §13 |
| `handler/auth.go:333` `ChangePassword`（`PUT /api/me/password`） | `!ok \|\| sess.UserID == nil` → 403 | **管理员通过** | **D-1=b：有意放开**。删掉 `sess.UserID == nil` 这个条件，并**必须**同时修 T2 |
| `handler/comment.go:87-93` `Like` | `userID == nil` → 401/403 | **管理员能点赞** | **保持 403**：加 `if session.IsAdmin(c) { return 403 "only visitor accounts can like comments" }` 前置 |
| `handler/comment.go:116-122` `Unlike` | 同上 | 同上 | 同上 |
| `handler/auth.go:196` `Me` | `if sess.UserID == nil { 401 }` | 不触发（在 `!IsAdmin()` 分支内） | 无需改，**加一行注释说明为何安全** |
| `handler/comment.go:162-169` `Create` | `if sess.IsAdmin() { CreateAsAdmin } else if sess.UserID != nil { CreateAsUser }` | 顺序正确 | 无需改 |
| `handler/comment.go:66` `List` → `ListByTarget(currentUserID)` | 管理员传 `nil` | 管理员传其 id → 多一个 `comment_likes` 连接，`liked_by_me` 恒 `false` | 无害，写进 PR 描述 |
| `session.go:317-325` `UserID()` 文档 | 「管理员与未登录都返回 nil」 | **文档变错** | 必须改写 |
| `session/session_test.go:72` | 断言 `sess.UserID == nil` | **测试失败** | 改为断言 `*sess.UserID == adminID` |

**为什么 `Like`/`Unlike` 要保持 403**：既有语义是「点赞的主体是访客账号」，而
`CommentResponse` 的管理员分支用 `AuthorType: "admin"` 渲染，让管理员点赞会往
`comment_likes` 里塞一行指向管理员 user 的记录，与「访客互动」的语义混在一起。
这属于范围外的行为变更，**不要顺手做**。

---

## 8. 代码搬移清单

### 8.1 新增

| 文件 | 内容 |
|---|---|
| `internal/model/admin.go` | 哨兵错误 + `AdminRepository` 接口：`Create(ctx, *User) error`、`Get(ctx) (*User, error)`、`Exists(ctx) (bool, error)`、`SetPassword(ctx, id, hash, updatedAt) error`、`UpdateIdentity(ctx, id, username, nickname, updatedAt) error` |
| `internal/model/admin_repo.go` | SQLite 实现。`Get` 用 `SELECT ... FROM users WHERE is_admin = 1 LIMIT 1`（复用 `userColumns`/`scanUser`） |
| `internal/service/admin_auth.go` | `AdminAuth`：`Exists()`、`Verify(password) bool`（bcrypt）、`SetPassword(password) error`（内含 `config.ValidatePasswordStrength`）、`Identity() (username, nickname string, err error)` |
| `internal/database/admin_migration.go` | `MigrateAdminCredentials(ctx, db, cfg) error`（§9） |
| `admin_command.go`（根目录，仿 `email_command.go`） | `main.exe admin set-password`（§10） |

### 8.2 修改

| 文件:行 | 改动 |
|---|---|
| `internal/database/schema.go:107` | `users` 加 `is_admin` 列 |
| `internal/database/schema.go:255` | `addedColumns` 加 `users.is_admin` |
| `internal/database/schema.go:275` | `addedIndexes` 加 `idx_users_single_admin` |
| `internal/model/user.go:14` | `User` 结构体加 `IsAdmin bool` |
| `internal/model/user_repo.go:46` | `userColumns` 加 `COALESCE(is_admin, 0)` —— **T1** |
| `internal/model/user_repo.go:48` | `scanUser` 加扫描目标 —— **T1** |
| `internal/model/user_repo.go:122-124` | `Create` 的 INSERT 加 `is_admin` 列与占位符 |
| `internal/model/user_repo.go:208,212-217` | `List` 两处加 `WHERE is_admin = 0` —— **T3** |
| `internal/config/config.go` | **删除** `IsInstalled`（`:338`）、`VerifyPassword`（`:619`）、`SetPassword`（`:555`）、`SetPasswordHash`（`:428`）、`PasswordHashValue`（`:435`）；`PasswordHash`/`AdminUsername`/`AdminNickname` 降级为「一次性迁移来源」（`Save` 时清空，同 `Password`/`AdminName` 范式）；删除 `:309-322` 的明文密码迁移分支 |
| `internal/view/handler/install.go:42` | `Status` → `adminRepo.Exists()` |
| `internal/view/handler/install.go:55-99` | `Install`：校验密码 + 用户名形状 → `adminRepo.Create(...)`。并发双装由唯一索引兜住；插入前没有任何写入，**不再需要**「写盘失败回滚内存哈希」那段 |
| `internal/view/handler/auth.go:77-93` | `Login` 改为单一查找（§8.4） |
| `internal/view/handler/auth.go:95-134` | **删除** `isAdminLoginName` 与 `loginAdmin` 整支 |
| `internal/view/handler/auth.go:47-49` | `AdminName()` → 读管理员行的 nickname（缓存或每请求查一次，二选一，见 §12 开放问题） |
| `internal/view/handler/auth.go:141-143` | `adminMe()` → 读管理员行的 username/nickname，**wire 形状不变** |
| `internal/view/handler/auth.go:289-326` | `UpdateOwnProfile` 加 `sess.IsAdmin()` 守卫（保持 403） |
| `internal/view/handler/auth.go:331-361` | `ChangePassword` 放开管理员 + **新增 `reissueSession`** + `startAdminSession` 传真实 userID —— **T2** |
| `internal/view/handler/auth.go:377-420` | `UpdateAdminProfile` → 写管理员行（`UpdateIdentity`）；删除 `UsernameTakenByAccount` 预检（唯一索引负责） |
| `internal/view/handler/comment.go:87-93,116-122` | `Like`/`Unlike` 加 `IsAdmin()` 前置 403 |
| `internal/view/router.go:118` | `NewUserService` 去掉 `cfg.AdminLoginName` 参数 |
| `internal/view/router.go:121,335-364` | **删除** `warnOnReservedUsernameCollision` 及调用 |
| `main.go` | `database.Open` 之后、`view.Register` 之前插入 `database.MigrateAdminCredentials(...)` —— **T4**；`os.Args[1] == "admin"` 分发 |
| `email_command.go:100-122` | `loadInstalledConfig` 需要 DB（§10.2） |
| `internal/session/session.go:317-325` | `UserID()` 文档改写 |
| `internal/session/session_test.go:72` | 断言改为 `*sess.UserID == adminID` |

### 8.3 删除（净减量，这是本方案的主要收益）

| 内容 | 位置 | 约行数 |
|---|---|---|
| `warnOnReservedUsernameCollision` + 调用 | `router.go:121,335-364` | 35 |
| `isReservedUsername` / `reservedUsernameError` / `UsernameTakenByAccount` | `user_service.go:492-543` | 50 |
| `AdminLoginNameSource` 类型 + `adminName` 字段 + 构造参数 + 三处预留名判断 | `user_service.go:44-49,67,74-76,407-409,461-463,516-518` | 30 |
| `isAdminLoginName` + `loginAdmin` | `handler/auth.go:95-134` | 40 |
| `Config.IsInstalled` / `VerifyPassword` / `SetPassword` / `SetPasswordHash` / `PasswordHashValue` / 明文迁移分支 | `config.go` | 90 |
| `config_test.go` 的相关用例 | `config_test.go:15,108,208,211` | 30 |

**净效果：新增约 400 行（含 CLI 与测试），删除约 275 行，并消掉一整类「两个命名空间」的推理负担。**

### 8.4 登录路径合一

**现状**（`handler/auth.go:77-93`）：先 `isAdminLoginName`（恒定时间字符串比较）分流，两条分支
各做一次 bcrypt，靠 `equalizeLoginTiming` + `dummyPasswordHash` 对齐耗时。

**目标**：

```go
user, err := h.users.Authenticate(c.Request().Context(), req.Identifier(), req.Password)
if err != nil {
	// 文案必须与现状逐字相同："invalid username or password"
	return echo.NewHTTPError(http.StatusUnauthorized, "invalid username or password")
}
if user.IsAdmin {
	return h.startAdminSession(c, user.ID)
}
return h.startUserSession(c, user.ID)
```

- `dummyPasswordHash` 与 `equalizeLoginTiming` **保留** —— 它们对付的是「账号不存在 / 无密码
  vs 密码错」，与分支问题无关，依然必要（`user_service.go:243-250` 的硬约束注释要看）。
- **一处刻意的能力变化，必须写进 PR**：`isAdminLoginName` 的恒定时间比较消失了。理由：改为
  `WHERE username = ? COLLATE NOCASE` 的索引查找后，耗时取决于索引结构而非"匹配了多少字节"，
  不再构成逐字节预言机；而且这条路径对所有用户一致，不再有"管理员特殊"。
- `LoginWithUsername` 里「输入形似邮箱时回退按邮箱查」的兼容分支**保留**。

---

## 9. 一次性迁移的时序（**必读**）

### 为什么不能跳过

用户说「不需要向前兼容」，但**这一次迁移必须做**：如果直接删掉 `settings.json` 里的
`password_hash` 而不搬进库，升级后第一个启动瞬间 DB 里没有管理员行 → `IsInstalled` 为假
→ **未鉴权的 `/install` 重新开放** → 任何能访问站点的人都能设一个新的管理员密码。
**这是一个真实的接管窗口。**

### 时序

```
main.go
  ├─ logging.Configure("")
  ├─ cfg = config.Load("settings.json")        ← 读到旧 password_hash / admin_username / admin_nickname
  ├─ cfg.Save("settings.json")                 ← 与现在一样写回完整文件
  ├─ db = database.Open("blog.db")             ← schemaSQL + addedColumns + addedIndexes
  ├─ database.MigrateAdminCredentials(ctx, db, cfg)      ★ 新增，必须在这一步
  │     ├─ 若 users 里已有 is_admin=1 的行 → 什么都不做（并清空 cfg 里的旧字段）
  │     └─ 否则用 cfg.PasswordHash / AdminUsername / AdminNickname 插入管理员行
  │           （PasswordHash 为空则视为"未安装"，不插入）
  ├─ cfg.Save("settings.json")                 ← 把已搬走的密钥字段从文件里抹掉
  ├─ search.Start(...)
  └─ view.Register(e, cfg, db, ...)            ← 从这里起才对外服务
```

**两条硬约束：**

1. **`MigrateAdminCredentials` 必须严格早于 `view.Register`**（T4）。否则第一个到达的请求会看到
   「未安装」并打开 `/install`。
2. **幂等：管理员行已存在时绝不覆盖。** 否则一份陈旧的 `settings.json` 备份会把已经轮换掉的
   旧密码复活。

**实现要点**：`config` 包不该认识数据库，所以「读取旧字段」留在 `config.Load`（沿用现有
`password`/`admin_name` 的迁移范式），「搬进库」放在 `database` 包里的独立函数，由 `main.go` 接线。

**回滚方案（写进 PR）**：代码回滚后 `settings.json` 已不含 `password_hash`，需要手工把
`users` 表里那一行的 `password_hash` 拷回，或临时用 CLI 导出。**实施前先备份 `settings.json`
与 `blog.db`。**

---

## 10. CLI 与子命令

### 10.1 `main.exe admin set-password`（新增 `admin_command.go`）

`main.go:31` 已有子命令分发范式（`os.Args[1] == "email"`），照抄。

```
main.exe admin set-password [--username <name>] [--nickname <name>]
```

行为：
1. 打开 DB（顺带完成迁移）；
2. 从 **stdin** 读新密码两次并要求一致 —— **不要**从 argv 读（会进 shell history 与进程列表）；
3. 用 `config.ValidatePasswordStrength` 校验 —— `settings.json` 不再是权威，但强度规则必须复用同一份；
4. `bcrypt.GenerateFromPassword`，`DefaultCost`，与访客一致；
5. 行存在 → `SetPassword`；不存在 → 创建（`--username` 缺省 `admin`，昵称缺省等于登录名，
   email 用空串哨兵）；
6. **`session.RevokeAll`** —— 密码变了，所有既有会话必须失效；
7. 退出码：成功 0，校验失败 2，DB 失败 1。

**不要**提供「打印密码哈希」这类子命令（会把凭据写进终端与日志）。

### 10.2 `email` 子命令的影响

`email_command.go:100-122` 的 `loadInstalledConfig` 依赖 `cfg.IsInstalled()`。改动后需要 DB：

```go
db, err := database.Open(databasePath)     // ← 新增，会顺带迁移
...
if ok, _ := model.NewSQLiteAdminRepository(db).Exists(ctx); !ok {
	return errors.New("the blog is not installed; run /install first")
}
```

可接受的副作用（写进文档）：`main.exe email ...` 从此也会执行一次数据库迁移。

---

## 11. 提交顺序（5 个可独立验收的提交）

| # | 提交信息 | 内容 | 验收 |
|---|---|---|---|
| 1 | `feat(database): 加 users.is_admin 与单管理员唯一索引` | §5 全部 + `AdminRepository` + 3 条 repo 测试 | `go test ./internal/database ./internal/model` 绿；**不改变任何行为**（无调用方） |
| 2 | `feat(database): 把 settings.json 里的管理员凭据迁进 users` | §9 + 幂等测试 | 手工验证：旧 settings.json → 首次启动建行且文件里哈希被抹掉；二次启动不覆盖 |
| 3 | `refactor(auth): 登录路径合一，管理员凭据改从 users 读` | §8.4 + `InstallHandler` + `AdminAuth` + 删 `Config.*Password*` + T1 | §14 的响应体 diff **逐字节相同** |
| 4 | `refactor(account): 管理员可自助改密；删掉预留用户名机制` | §7 的三处守卫 + T2（`reissueSession`）+ §8.3 的全部删除 + 测试简化 | 新增 `PUT /api/me/password` 管理员的 e2e；全量 `go test ./...` 绿 |
| 5 | `feat(cli): main.exe admin set-password` + 文档 | §10 + `session.RevokeAll` + §13 的文档更新 | CLI 测试 + 文档锚点校验 |

**每个提交都必须跑**（见 §14 的确切命令）。提交 3、4 额外跑隔离实例的响应 diff。

---

## 12. 测试计划

### 12.1 新增守护测试（**每条都必须做反证**）

项目约定：新增守护测试必须**改坏 → 确认失败 → 精确还原**，一次只改一处。
（`HANDOFF.md` 第六节最后一条。）

| 测试 | 断言 | 反证方式 |
|---|---|---|
| `TestOnlyOneAdministratorCanExist` | 插第二行 `is_admin=1` → 唯一约束失败 | 删掉 `idx_users_single_admin` → 失败 |
| `TestAdminRowIsInvisibleToTheVisitorList` | `userRepo.List` 不含管理员行，`total` 也不算它 | 去掉 `WHERE is_admin = 0` → 失败（**同时验证 COUNT 与 SELECT 两处**） |
| `TestAdminEmailSentinelIsUnreachable` | `FindByEmail` 用任意合法地址都找不到管理员；`Register` 无法产生空 email | 把哨兵改成 `admin@example.com` → 断言"不可注册"失败 |
| `TestUserColumnsCarryIsAdmin` | `FindByUsername`/`GetByID` 读出的管理员行 `IsAdmin == true` | 从 `userColumns` 去掉 `is_admin` → 失败（**这是 T1 的直接守卫**） |
| `TestLoginFromTheAdminRowYieldsAnAdminSession` | 管理员行 + 正确密码 → `Role=admin`、TTL=`AdminTTL`、`UserID != nil` | 把 role 判断改成恒 `RoleUser` → 失败 |
| `TestAdminCanChangeOwnPassword`（D-1=b） | `PUT /api/me/password` 管理员 → 204；旧密码 401、新密码能登录；**响应后的会话仍是 `role=admin`** | ① 恢复 `sess.UserID == nil` 守卫 → 403 失败；② 把 `reissueSession` 换回 `startUserSession` → **会话角色变 user，失败**（这是 T2 的守卫） |
| `TestAdminPasswordChangeRevokesOtherAdminSessions` | 改密后另一个管理员 token 失效 | 不调 `RevokeUserSessions` → 失败 |
| `TestAdminCannotUseVisitorOnlyEndpoints` | `PATCH /api/me` → 403；`POST /api/comments/:id/like` → 403；`DELETE .../like` → 403 | 逐个移除 `IsAdmin()` 守卫 → 各自失败 |
| `TestAdminUsernameIsProtectedByTheDatabaseIndex` | 访客注册 `admin` → `ErrUsernameTaken`；管理员改名到某访客名 → `ErrUsernameTaken` | ——（由现有 `TestAdministratorLoginNameIsReserved` 简化而来） |
| `TestAdminRenameKeepsTheOldNameFree` | 管理员 `admin`→`root` 后：`root` 能登录、`admin` 401、访客可注册 `admin` | —— |
| `TestLegacySettingsHashIsMigratedOnce` | settings 有 `password_hash` + 库空 → 建行且哈希可验证；改库里的密码后重跑 → **不覆盖** | 去掉"已存在则跳过" → 第二条失败 |
| `TestMigrateAdminCredentialsRunsBeforeServing` | 迁移后 `adminRepo.Exists()` 为真 | 把调用挪到 `view.Register` 之后 → e2e 里 `/install_status` 返回 `installed:false` 且 `/install` 可再次执行 |
| `TestAdminSetPasswordRevokesSessions`（CLI） | `set-password` 后旧 token 查不到 | 不调 `RevokeAll` → 失败 |

### 12.2 需要改写的既有测试

- `TestAdministratorLoginNameIsReserved`（`user_service_test.go:246`）→ 大幅简化：不再需要
  注入 `testAdminLoginName`，改为「插一行 `is_admin=1` 再验证唯一性」。
  注意 `testAdminLoginName` 与 `NewUserService` 的第三个参数（`:55-57,73`）一起收拾。
- `config_test.go:15,108,208,211` → 随 `PasswordHashValue` 一起删。
- `session_test.go:72` → 改为断言 `UserID` 非 nil。
- `auth_e2e_test.go:523-528`（访客改密）必须**继续通过且响应不变**。

### 12.3 明确不必改的

`auth_e2e_test.go` 里安装/登录/`/api/me` 的断言应**保持逐字节不变** —— 那是 I2 的验收点。
如果它们需要改，说明改错了。

---

## 13. 文档更新清单

| 文件:行 | 改什么 |
|---|---|
| `twilight-web/src/pages/admin/Profile.tsx:5` | 注释说「控制台没有改管理员密码的入口」——**已过时**，改成"管理员与访客共用 `PUT /api/me/password`" |
| `twilight-blog-api/api.md:640` | "Changes the visitor's password" → 当前账号的密码 |
| `twilight-blog-api/api.md:662` | **删掉** `| 403 Forbidden | The session belongs to the administrator |` |
| `twilight-blog-api/src/types.ts:153-155` | 「role=admin 时…user 为 undefined（站长没有账号行）」→ 说明管理员现在**有**账号行，但 API 形状刻意不变（I2） |
| `twilight-blog/api.md` | 该文件已过时（见 `PENTEST-REPORT.md` 第 5.4 节），建议直接删除而不是追加修改 |
| `PENTEST-REPORT.md` | ① 授权矩阵里 `PUT /api/me/password 管理员 403` 标注为 bug 并改为 204；② 第〇节修复状态表加一行 |
| `twilight-blog/HANDOFF.md` | 或新建交接（本文件即上一轮的交接）。至少记下 §6 的四个陷阱与 §9 的时序约束 |

---

## 14. 验证命令（每个提交结束都跑）

```powershell
Set-Location D:\Files\development\twilight-blog\twilight-blog
# 环境变量见 §3

gofmt -l ./internal ./main.go          # 必须无输出；只扫这两个，别扫全目录（会命中只读 .modcache）
go build ./...                          # exit 0
go vet ./...                            # exit 0
go test ./internal/...                  # 全 ok
```

前端（只有 §13 的注释改动时才需要）：

```powershell
Set-Location D:\Files\development\twilight-blog\twilight-web
node .\node_modules\typescript\bin\tsc -b     # exit 0
```

### 14.1 I2 的验收：响应体 diff（提交 3 与 4 必做）

在隔离实例上（**不要动用户的 `settings.json` / `blog.db`**；复制一份二进制与空目录，
参考上一轮的 `.pentest-run/` 做法），把下面每个请求的**状态码 + 响应体**落到文件，
**改动前后各跑一次并 diff**：

```
POST /install                            → 200 {"installed":true}
GET  /install_status                     → {"installed":true}
POST /api/auth/login        (admin)      → {"role":"admin","name":…,"username":…}   无 user 字段
GET  /api/me                (admin 会话)  → 同上
PATCH /api/me               (admin 会话)  → 403 "only visitor accounts can change their profile"
PUT  /api/me/password       (admin 会话)  → ★ 唯一有意变化：403 → 204
POST /api/comments/1/like   (admin 会话)  → 403 "only visitor accounts can like comments"
GET  /api/admin/users       (admin 会话)  → 只列访客
POST /api/auth/register username=admin    → 409 "username is already taken"
```

除 `PUT /api/me/password` 一行外，**必须逐字节相同**。

---

## 15. 风险、回滚、运维影响

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| T1 漏改 `userColumns` | **中** | 管理员登录后被降级 → **站点锁死**，且编译与既有测试全绿 | `TestUserColumnsCarryIsAdmin` 直接守它 |
| T2 漏改 `reissueSession` | **中** | 管理员改完密码当场被降级，请求却返回 204 | `TestAdminCanChangeOwnPassword` 断言改密后会话仍是 `role=admin` |
| `UserID == nil` 语义翻转被漏掉 | 中 | 管理员意外获得点赞能力 | §7 逐项加测试；§14.1 的 diff 覆盖全部 4 个端点 |
| T4 迁移晚于 `view.Register` | 低 | `/install` 短暂开放 = 接管窗口 | 顺序写进代码注释 + 专门的测试 |
| 迁移覆盖已轮换的密码 | 低 | 旧密码复活 | 幂等（已存在则跳过）+ 测试；**上线前备份 `blog.db` 与 `settings.json`** |
| 回滚后 settings.json 无哈希 | — | 回不到旧结构 | 备份；或手工把 `users` 里的哈希拷回 |
| 第二个管理员行被创建 | 极低 | 身份歧义 | 部分唯一索引（DB 层） |
| 管理员无法自助重置密码 | 确定 | 忘记密码需登服务器 | 设计取舍；CLI + 文档写明 |

### 运维影响（必须写进文档）

1. `settings.json` 不再含任何密码哈希 → **备份清单变了**：`blog.db` 现在是必须备份的那一个，
   丢它就等于丢管理员凭据。
2. 恢复旧 `blog.db` 会一并恢复旧的管理员密码。
3. 忘记密码的标准动作从「编辑 JSON」变成「跑 `main.exe admin set-password`」。

---

## 16. 明确不做的事（别"顺手"做）

- **不**让管理员用 `PATCH /api/me` 改用户名/昵称（保持 403）。管理员继续用
  `PUT /api/admin/profile`。前端也是这么分工的，不要改。
  （这是 D-1 的边界：**b 只覆盖密码**。把 `PATCH /api/me` 也统一是自然的后续，
  但要单独一个提交，因为它会牵动前端 `IdentityCard`。）
- **不**让管理员点赞（保持 403）。
- **不**改 `MeResponse` 的形状（admin 仍不给 `user` 对象）—— 这是 I2。
- **不**改 `AdminTTL`(24h) / `UserTTL`(30d) 的取值。
- **不**改 `sessions` 表结构（`role` 列继续区分身份；只是 `user_id` 对管理员从 `NULL` 变成真实 id）。
- **不**为管理员引入可自助重置的真实邮箱（会需要新的不可注册地址守卫，收益不足）。
- **不**顺手把 `twilight-blog/api.md` 改一遍——它整体过时，建议直接删（见 §13）。

---

## 17. 开工前的收尾事项

1. **`.gopath/` 未被 `.gitignore` 覆盖**（上一轮为绕过沙箱的 sumdb 写入限制而创建的工作区目录）。
   开工前把它加进根 `.gitignore`，或直接删掉（下次设 `GOPATH` 时会重建）。
   已确认 `.pentest-run*/`、`**/.pentest/`、`*.mdb` 已在 `.gitignore` 里。
2. **备份 `twilight-blog/settings.json` 与 `twilight-blog/blog.db`**，因为 §9 的迁移会改动前者、
   写入后者。
3. 确认 `.pentest-run/` 里的测试实例**没有在运行**（`Get-Process main`），否则它会持有
   `blog.db` 的写锁，干扰迁移测试。

---

## 18. 开放问题（实施时需自行决定）

1. **`AdminName()`（评论署名用的展示名）是缓存还是每请求查库？**
   现在是 `h.config.AdminDisplayName()`，读内存、零成本。改成读库后，`CommentHandler.Create`
   每个评论请求会多一次查询。推荐：在 `AdminAuth` 里做**每次写评论时查一次**（评论写入频率低，
   且单连接池下多一次本地查询可忽略）；不要引入缓存失效逻辑——那正是"改了昵称前台没变"这类
   bug 的来源。
2. **`has_password` 对管理员**：管理员行的 `password_hash` 恒非空（迁移只在哈希非空时建行），
   所以 `ChangePassword` 的 `HasPassword()` 检查恒通过。建议加一条断言测试，防止将来有人
   建出一个无密码的管理员行。
3. **`scanUser` 扫 `bool` 还是 `int64`**：`modernc.org/sqlite` 对 `*bool` 扫描 `INTEGER 0/1`
   应当支持，但**写个测试确认**；若不确定就扫进 `int64` 再 `!= 0` 转换，别赌。
