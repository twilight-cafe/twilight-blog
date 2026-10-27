package database

// schemaSQL 是完整的数据库结构定义。
//
// 它写成幂等的，并且**每次 Open 都会重放**——对全新的库和已有的库一视同仁：
// 没有 migrations 表，也没有版本号，所以每条语句都必须能安全地重复执行。
// SQLite 会把整串当作一个批次执行，因此任何一处语法错误都会让整个调用失败。
//
// 务必记住这条不变量：CREATE TABLE IF NOT EXISTS 对**已存在**的表不做任何改动。
// 所以这里新增的列只会出现在「从零创建」的库里；初始版本之后新增的列，
// 必须同时登记到下面的 addedColumns，否则老库永远拿不到这一列。
//
// 表与索引速览：
//   - articles / moments / pages：三张内容表，结构极简，都靠 deleted_at 做软删。
//     idx_*_active_created 是带 WHERE deleted_at IS NULL 的**部分索引**，
//     正好匹配列表查询「只取未删除、按时间倒序」，比全表索引更小也更快。
//     articles 是三者里唯一带外键的一张：cover_media_id 指向头图那张媒体。
//     每篇最多一张头图，靠「一个列」而不是「一张关联表」表达，于是「多于一张」
//     在这种结构下根本无法表示。该外键用 ON DELETE SET NULL —— 媒体库允许直接
//     删任意一条媒体，删掉正被当作头图的那张时文章自动摘掉头图，而不是留下一个
//     指向不存在行的 id。
//     注意 CREATE TABLE 里它写在 media 之前：SQLite 的外键目标是**惰性解析**的，
//     建表时父表尚不存在并不报错，真正会报错的是「子表写入时父表仍不存在」。
//   - tags：name 用 COLLATE NOCASE UNIQUE，因此 "Go" 与 "go" 是同一个标签。
//   - article_tags / moment_media / comment_likes：关联表，主键都是复合的，
//     天然去重（重复点赞因此是幂等的），并靠外键 ON DELETE CASCADE 随主体清理
//     ——前提是 foreign_keys pragma 真的开了，见 sqlite.go 的 verifyPragmas。
//   - users：email 与 username 都是 NOCASE。两者职责严格分开：
//     **username 是登录标识，nickname 只是前台展示名**，不得互相顶替
//     （昵称可以重复、可以随时改；用户名必须唯一）。email 不参与登录，
//     只用于验证码与找回密码。注意**没有 deleted_at**，账号不做软删。
//     站长（管理员）也住在这张表里，是 is_admin = 1 的那一行——于是「用户名
//     全站唯一」由 idx_users_username 一个索引统一负责，不再需要「预留用户名」
//     那套跨命名空间的推理。管理员行的 email 恒为空串（见 addedIndexes 的说明）。
//     avatar_media_id 是指向 media 的可空引用（ON DELETE SET NULL），只有
//     管理员那一行会用到：站长的头像是他唯一可配置的对外形象，因此它由
//     PUT /api/admin/profile 写入、由公开的 GET /api/site 下发。
//   - comments：author_type 区分管理员与访客，user_id 可空（管理员没有账号行）；
//     idx_comments_roots 是给「根评论分页」用的覆盖索引。
//   - sessions：统一存放两种角色的会话，存的是 session token 的 SHA-256 与
//     配套 CSRF token 的哈希；user_id 对两种角色都是真实账号 id。
//   - admin_sessions：**已废弃，不再创建**。它是 sessions 之前的旧管理员
//     会话表，自 sessions 统一承载两种角色之后就再没有任何代码读写它
//     （一度只剩 PruneExpired 还在清理、以及测试在建行验证）。
//     老库里可能仍残留这张表：留着无害，因为没有任何语句会碰它；
//     确实想清掉可手动执行 DROP TABLE admin_sessions（本代码不会替你做，
//     删表是不可逆操作，不该埋在每次启动都会跑的迁移里）。
//   - emails：出站邮件的审计留痕，只增不改，没有 deleted_at。
//   - media：有 deleted_at 列，但当前媒体是**硬删**（库里记录与磁盘文件一起删），
//     该列实际未被使用，属于预留。
const schemaSQL = `
CREATE TABLE IF NOT EXISTS articles (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    title          TEXT    NOT NULL,
    content        TEXT    NOT NULL DEFAULT '',
    -- 英文版本；空串表示没有英文版，前台回退中文。
    title_en       TEXT    NOT NULL DEFAULT '',
    content_en     TEXT    NOT NULL DEFAULT '',
    -- 1 = 站长标记为 AI 生成；前台会显示提示。中文与英文各记一份：
    -- 「手写中文 + AI 翻译英文」只会让英文版带声明，中文原文不该被误标。
    ai_generated    INTEGER NOT NULL DEFAULT 0,
    ai_generated_en INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    -- 头图。NULL 表示这篇文章没有头图；ON DELETE SET NULL 见文件头「表与索引速览」。
    -- 指向的 media 行本身可能属于别的对象（服务层会拒绝非图片，但不去重），
    -- 详见 service.ArticleService 里对「专属头图」的说明。
    cover_media_id INTEGER REFERENCES media(id) ON DELETE SET NULL,
    deleted_at     INTEGER
);

CREATE INDEX IF NOT EXISTS idx_articles_deleted_at
    ON articles(deleted_at);

CREATE INDEX IF NOT EXISTS idx_articles_active_created
    ON articles(created_at DESC, id DESC) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS tags (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    name    TEXT NOT NULL COLLATE NOCASE UNIQUE,
    -- 英文名；空串表示未提供，前台回退到 name。不参与唯一约束。
    name_en TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS article_tags (
    article_id INTEGER NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    tag_id     INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (article_id, tag_id)
);

CREATE INDEX IF NOT EXISTS idx_article_tags_tag_id
    ON article_tags(tag_id);

CREATE TABLE IF NOT EXISTS moments (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    content     TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    deleted_at  INTEGER
);

CREATE INDEX IF NOT EXISTS idx_moments_deleted_at
    ON moments(deleted_at);

CREATE INDEX IF NOT EXISTS idx_moments_active_created
    ON moments(created_at DESC, id DESC) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS moment_media (
    moment_id  INTEGER NOT NULL REFERENCES moments(id) ON DELETE CASCADE,
    media_id   INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (moment_id, media_id)
);

CREATE INDEX IF NOT EXISTS idx_moment_media_media_id
    ON moment_media(media_id);

CREATE TABLE IF NOT EXISTS pages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT    NOT NULL,
    content     TEXT    NOT NULL DEFAULT '',
    -- 与文章同构的英文版本；两列都可为空，前台按当前语言取用并逐字段回退中文。
    title_en    TEXT    NOT NULL DEFAULT '',
    content_en  TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    deleted_at  INTEGER
);

CREATE INDEX IF NOT EXISTS idx_pages_deleted_at
    ON pages(deleted_at);

CREATE INDEX IF NOT EXISTS idx_pages_active_created
    ON pages(created_at DESC, id DESC) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    -- email 的 UNIQUE 是全站唯一的「不可注册地址」哨兵：管理员行的 email 恒为
    -- 空串。SQLite 无法通过 ALTER 放宽列约束，所以管理员行必须有一个 email 值，
    -- 而 '' 是天然的不可注册地址——normalizeAndValidateEmail 与
    -- AdminUpdateUserRequest.Validate 都明确拒绝空值，没有任何注册路径能产生它。
    -- UNIQUE 又保证不可能有第二行空 email。代价是管理员不能自助找回密码。
    email         TEXT    NOT NULL COLLATE NOCASE UNIQUE,
    username      TEXT    NOT NULL DEFAULT '',
    nickname      TEXT    NOT NULL DEFAULT '',
    password_hash TEXT    NOT NULL DEFAULT '',
    is_admin      INTEGER NOT NULL DEFAULT 0,
    -- 站长的头像。只有 is_admin = 1 的那一行会用到它（访客头像没有需求），
    -- 但列开在这里而不是另起一张表：它就是「这一行账号的一个可空引用」。
    -- ON DELETE SET NULL 与 articles.cover_media_id 同理——媒体库允许硬删任意
    -- 一条媒体，删掉正被当作头像的那张时这一列自动变回 NULL，而不是留下一个
    -- 指向不存在行的 id。avatar_media_id 的补列见 addedColumns（带 REFERENCES
    -- 的补列不能写 DEFAULT）。
    avatar_media_id INTEGER REFERENCES media(id) ON DELETE SET NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- username 的唯一性、以及「最多一个管理员」的唯一性都在**补列之后**建立，
-- 见 addedIndexes：老库在这一刻还没有 username 与 is_admin 列，把
-- CREATE UNIQUE INDEX 写在上面这串建表语句里会让整个批次直接报
-- "no such column" 而迁移失败。

CREATE TABLE IF NOT EXISTS email_verification_codes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    email       TEXT    NOT NULL COLLATE NOCASE,
    code_hash   TEXT    NOT NULL,
    expires_at  INTEGER NOT NULL,
    consumed_at INTEGER,
    attempts    INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_email_verification_codes_email
    ON email_verification_codes(email);

CREATE INDEX IF NOT EXISTS idx_email_verification_codes_expires_at
    ON email_verification_codes(expires_at);

CREATE TABLE IF NOT EXISTS comment_tokens (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT    NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_comment_tokens_user_id
    ON comment_tokens(user_id);

-- Unified session store for both roles. The raw session token lives only in an
-- httpOnly cookie; the row holds its SHA-256 hash plus the hash of the matching
-- CSRF token. user_id is the real account id for both roles — the administrator
-- owns an is_admin = 1 row in users like everybody else.
CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT    PRIMARY KEY,
    role       TEXT    NOT NULL,
    user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
    csrf_hash  TEXT    NOT NULL,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires_at
    ON sessions(expires_at);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id
    ON sessions(user_id);

CREATE TABLE IF NOT EXISTS comments (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    author_type TEXT    NOT NULL DEFAULT 'user',
    user_id     INTEGER REFERENCES users(id) ON DELETE CASCADE,
    admin_name  TEXT,
    nickname    TEXT    NOT NULL DEFAULT '',
    target_type TEXT    NOT NULL,
    target_id   INTEGER NOT NULL,
    parent_id   INTEGER,
    content     TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    deleted_at  INTEGER,
    like_count  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_comments_target
    ON comments(target_type, target_id, deleted_at);

CREATE INDEX IF NOT EXISTS idx_comments_parent
    ON comments(parent_id);

CREATE INDEX IF NOT EXISTS idx_comments_roots
    ON comments(target_type, target_id, deleted_at, parent_id, created_at, id);

CREATE TABLE IF NOT EXISTS comment_likes (
    comment_id INTEGER NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (comment_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_comment_likes_user_id
    ON comment_likes(user_id);

CREATE TABLE IF NOT EXISTS media (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    storage_name  TEXT    NOT NULL UNIQUE,
    original_name TEXT    NOT NULL,
    media_type    TEXT    NOT NULL,
    mime_type     TEXT    NOT NULL,
    size          INTEGER NOT NULL,
    converted     INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    deleted_at    INTEGER
);

CREATE INDEX IF NOT EXISTS idx_media_deleted_at
    ON media(deleted_at);

CREATE INDEX IF NOT EXISTS idx_media_active_created
    ON media(created_at DESC, id DESC) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS emails (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    from_email  TEXT    NOT NULL DEFAULT '',
    to_email    TEXT    NOT NULL,
    subject     TEXT    NOT NULL,
    body        TEXT    NOT NULL,
    sent_at     INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_emails_sent_at
    ON emails(sent_at DESC);
`

// columnAddition 表示一个在初始版本之后才加入的列。
type columnAddition struct {
	table      string
	name       string
	definition string
}

// addedColumns 是需要补加到**已有库**上的列。
//
// 为什么必须有它：CREATE TABLE IF NOT EXISTS 已经让老表保持着最初的形状，
// 新列永远不会被自动加上。于是每次启动都要逐列查一遍 PRAGMA table_info，
// 再决定是否 ALTER TABLE ADD COLUMN。
//
// 因此新增一列的完整流程是两步：
//  1. 改上面的建表语句（只对新建的库生效）；
//  2. 把同一列登记到这里（对老库生效）。
//
// 只做第一步，老库会在运行时抛 "no such column"。这个坑很难在开发机上发现，
// 因为本地数据库往往是删掉重建的——那正好绕开了老库路径。
//
// definition 必须带 DEFAULT：SQLite 的 ALTER TABLE ADD COLUMN 加一个
// NOT NULL 列时，要求给出默认值才能填满已有行。
//
// cover_media_id 上有 REFERENCES 却**不能**写 DEFAULT：SQLite 明确规定
// 「外键已启用时，带 REFERENCES 子句的补列必须以 NULL 为默认值」。
// 省略 DEFAULT 正好就是 NULL，这不是漏写。
var addedColumns = []columnAddition{
	{"comments", "like_count", "INTEGER NOT NULL DEFAULT 0"},
	{"comments", "nickname", "TEXT NOT NULL DEFAULT ''"},
	{"users", "nickname", "TEXT NOT NULL DEFAULT ''"},
	{"users", "password_hash", "TEXT NOT NULL DEFAULT ''"},
	{"users", "username", "TEXT NOT NULL DEFAULT ''"},
	{"users", "is_admin", "INTEGER NOT NULL DEFAULT 0"},
	{"email_verification_codes", "attempts", "INTEGER NOT NULL DEFAULT 0"},
	{"articles", "cover_media_id", "INTEGER REFERENCES media(id) ON DELETE SET NULL"},
	{"articles", "title_en", "TEXT NOT NULL DEFAULT ''"},
	{"articles", "content_en", "TEXT NOT NULL DEFAULT ''"},
	{"articles", "ai_generated", "INTEGER NOT NULL DEFAULT 0"},
	{"articles", "ai_generated_en", "INTEGER NOT NULL DEFAULT 0"},
	{"pages", "title_en", "TEXT NOT NULL DEFAULT ''"},
	{"pages", "content_en", "TEXT NOT NULL DEFAULT ''"},
	{"tags", "name_en", "TEXT NOT NULL DEFAULT ''"},
	{"users", "avatar_media_id", "INTEGER REFERENCES media(id) ON DELETE SET NULL"},
}

// addedIndexes 是**必须在补列之后**才能执行的索引语句。
//
// 为什么不跟建表语句放在一起：建表语句是在补列之前整批执行的，而这里的索引
// 引用了刚刚才补上的列。老库在那一刻还没有 username，放进 schemaSQL 会让
// 整个批次报 "no such column"——迁移直接失败，进程起不来。
//
// username 用**部分唯一索引**而不是列上的 UNIQUE，理由有两层：
//   - SQLite 的 ALTER TABLE ADD COLUMN 无法为已有行逐个算出不同的值，
//     列级 UNIQUE 会让多行历史空值 "" 立刻互相冲突；
//   - 空串在本表里的语义是「该行的用户名尚未回填」，本就不该参与唯一性约束。
//     回填见 sqlite.go 的 backfillUsernames，它会把每一行都填满。
//
// idx_users_single_admin 把「全站最多一个管理员」变成**数据库不变量**，而不是
// 「没人会那么写」的约定：它只索引 is_admin = 1 的行，而那些行的 is_admin 恒为 1，
// 于是插入第二行管理员必然撞唯一约束。应用层因此不需要任何「是不是已经有一个了」
// 的预检——那类判断在并发下本来也拦不住。
var addedIndexes = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username
	    ON users(username COLLATE NOCASE) WHERE username <> ''`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_single_admin
	    ON users(is_admin) WHERE is_admin = 1`,
}
