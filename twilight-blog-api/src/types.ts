// 评论/动态/页面三类可被评论或聚合的目标类型；也是部分接口按 target 过滤的依据。
export type TargetType = 'article' | 'moment' | 'page';
// 媒体粗略分类，用于前端选择渲染组件（图片/音频/视频），比 mime_type 更粗。
export type MediaType = 'image' | 'audio' | 'video';

// 分页结果统一结构。page 从 1 开始；total 是命中总数，total_pages 由服务端算好，调用方无需自算。
export interface Paginated<T> {
  items: T[];
  page: number;
  page_size: number;
  total: number;
  total_pages: number;
}

export interface AdminAuthor {
  type: 'admin';
  name: string;
}

export interface UserAuthor {
  type: 'user';
  id: number;
  /** 评论者自选的公开昵称，任何人可见，可安全展示。 */
  nickname: string;
  /**
   * 仅当以管理员身份请求时才会出现。公开评论接口故意不返回它，
   * 以免访客邮箱被枚举抓取。
   */
  email?: string;
}

export type CommentAuthor = AdminAuthor | UserAuthor;

// 文章。所有 *_at 字段为数值型 Unix 时间戳，单位以服务端为准（前端对秒/毫秒做了兼容，
// 不要假设固定单位）。deleted_at 存在即表示已软删、目前在回收站；公开接口不会返回它。
export interface Article {
  id: number;
  title: string;
  content: string;
  created_at: number;
  updated_at: number;
  deleted_at?: number;
  /**
   * 头图那张媒体的 id；没有头图时是 **null**（不是省略）。
   *
   * 这个字段恒出现，是刻意的：服务端把它声明为必填，所以判断方式就是
   * `cover_media_id !== null`，不需要再判 undefined。渲染地址用
   * `mediaUrl(article.cover_media_id)`。
   */
  cover_media_id: number | null;
  tags: string[];
}

// 标签。article_count 是该标签下（公开）文章数，便于前端直接展示热度，无需另发计数请求。
export interface Tag {
  id: number;
  name: string;
  article_count: number;
}

// 动态（朋友圈式短内容）。media_items 是已关联、已上传的媒体；创建/更新时用 mediaIds 关联，见下。
// *_at 时间戳语义与 Article 相同。deleted_at 存在即软删（在回收站）。
export interface Moment {
  id: number;
  content: string;
  media_items: MediaItem[];
  created_at: number;
  updated_at: number;
  deleted_at?: number;
}

// 独立页面（如「关于」「友链」），非信息流内容。deleted_at 存在即软删（在回收站）。
export interface Page {
  id: number;
  title: string;
  content: string;
  created_at: number;
  updated_at: number;
  deleted_at?: number;
}

// 评论。target_type + target_id 指向被评论的对象（文章/动态/页面）。
// parent_id 为 null/省略表示顶层评论，填数字表示对另一条评论的回复（楼中楼）。
// like_count 是累计点赞数，liked_by_me 是当前会话是否已点赞（同一评论随登录态变化）。
// deleted_at 存在即软删（在回收站，仅管理员可见）。
export interface Comment {
  id: number;
  author: CommentAuthor;
  target_type: TargetType;
  target_id: number;
  parent_id?: number | null;
  content: string;
  like_count: number;
  liked_by_me: boolean;
  created_at: number;
  updated_at: number;
  deleted_at?: number;
}

// 出站邮件记录（如注册验证码、密码重置）。仅管理员可读取。sent_at 为发出时间，created_at 为落库时间。
export interface Email {
  id: number;
  from_email: string;
  to_email: string;
  subject: string;
  body: string;
  sent_at: number;
  created_at: number;
}

// 媒体库条目。media_type 是粗分类（image/audio/video），mime_type 是精确类型（如 image/webp）。
// size 为字节数。converted 表示服务端是否已转码（例如视频转成标准格式/缩略图）。
// url 是访问该媒体的地址；deleted_at 存在即软删（在回收站）。
export interface MediaItem {
  id: number;
  url: string;
  original_name: string;
  media_type: MediaType;
  mime_type: string;
  size: number;
  converted: boolean;
  created_at: number;
  deleted_at?: number;
}

// 安装状态。installed=false 表示尚未完成首次初始化（设置管理员密码），
// 此时除 /install 与 /install_status 外的大部分接口应被服务端拒掉。
export interface InstallStatus {
  installed: boolean;
}

/* -------------------------------------------------------------------------- */
/* Identity                                                                    */
/* -------------------------------------------------------------------------- */

/** 会话归属角色：admin 为站长，user 为注册访客。 */
export type Role = 'admin' | 'user';

/** 访客账号视图，只有本人与站长能看到完整字段。 */
export interface UserResponse {
  id: number;
  /**
   * 登录标识：登录时填的就是它。与 nickname 是两个独立字段——
   * username 用于认证，nickname 用于一切对外的展示位。改其中一个
   * 绝不会影响另一个。
   */
  username: string;
  /** 公开展示名：评论署名、后台列表等面向读者的地方都用它。 */
  nickname: string;
  /** 已验证的联系方式；不参与登录（登录标识是 username）。 */
  email: string;
  // 访客可能仅用验证码注册而从未设密码；false 时改密码流程需要走「重置」而非「修改」。
  has_password: boolean;
  created_at: number;
  updated_at: number;
}

/**
 * 描述当前登录会话，SPA 据此决定展示「管理后台」还是「个人中心」。
 *
 * role=admin 时有 name（站长展示名）与 username（站长登录名），没有 user；
 * role=user 时只有 user。二者互斥，靠 role 区分。
 * name 与 username 在 admin 会话下是两个不同的值——展示与登录分离，
 * 界面上务必分成两个输入框呈现，不要互相回填。
 *
 * ⚠️ 关于 `user` 缺席：站长**现在确实有账号行了**（users 表里 is_admin = 1 的
 * 那一行，凭据就存在那里），但它的 e-mail 是空串哨兵，而且这个响应形状是
 * **刻意保持不变**的——把它塞进 `user` 会改变既有客户端看到的载荷。
 * 所以「role=admin 时 user 为 undefined」依然是正确用法，只是原因从
 * 「站长没有账号行」变成了「形状刻意不暴露它」。需要改资料请用
 * PUT /api/admin/profile。
 */
export interface MeResponse {
  role: Role;
  /** 站长的展示名（前台署名、后台侧栏）。仅 role=admin 时出现。 */
  name?: string;
  /** 站长的登录名。仅 role=admin 时出现。 */
  username?: string;
  user?: UserResponse;
}

/** Generic acknowledgement body for endpoints that return no entity. */
export interface MessageResponse {
  message: string;
}

export interface CountResponse {
  count: number;
}

// 分页入参。page 默认 1；pageSize 默认且上限 12（见 pagination.ts 的校验），不要传超过 12 的值。
export interface PaginationOptions {
  page?: number;
  pageSize?: number;
}

export interface SearchOptions extends PaginationOptions {
  // q 为必填搜索关键词；空串会被服务端拒绝。
  q: string;
}

// 搜索结果项。type 字段用于前端区分渲染（文章 vs 页面），二者结构不同故用联合类型。
export interface ArticleSearchResult extends Article {
  type: 'article';
}

export interface PageSearchResult extends Page {
  type: 'page';
}

export type SearchResult = ArticleSearchResult | PageSearchResult;

// 列某对象下的评论：targetType + targetId 必填，指定「评论挂在哪个文章/动态/页面上」。
export interface ListCommentsOptions extends PaginationOptions {
  targetType: TargetType;
  targetId: number;
}

export interface CreateArticleInput {
  title: string;
  content: string;
  // 创建时省略 tags 视为「无标签」；想显式不带标签就传空数组。注意与更新时语义不同。
  tags?: string[];
  /**
   * 发布时间（Unix 秒）。省略表示「用服务器当前时间」。
   *
   * 服务端把它写进 created_at，而列表正是按那一列排序——所以这就是「补录旧文 /
   * 校正日期」的入口。必须是**秒**：传毫秒会被服务端拒绝（400）而不是静默存错，
   * 判据是 1e12（约公元 33658 年），见服务端 viewmodel.MaxPublishedAt。
   */
  publishedAt?: number;
  /**
   * 头图媒体的 id（先用 uploadMedia 拿到）。省略或传 null 都表示「没有头图」——
   * 创建时不存在「原头图」，「不指定」与「置空」是同一件事。
   */
  coverMediaId?: number | null;
}

/// 更新时省略 tags 表示「保留现有标签不变」；传空数组才会清空全部标签。
/// 这个区分是关键契约——把省略误当成空数组会误删所有标签。
export interface UpdateArticleInput {
  title: string;
  // 省略 content 表示「本次不改正文」，服务端保留原文；传字符串（含空串）才会替换。
  // 这里的可选性与 tags 同源：字段缺失与显式传值在服务端是两种语义，
  // 类型上就必须能表达「我这次不提这个字段」。
  content?: string;
  tags?: string[];
  /**
   * 发布时间（Unix 秒）。省略表示「不改」，传值才会改。
   *
   * 「置空」没有意义（文章必须有一个发布时间），所以这里只有两态，与 content 相同。
   */
  publishedAt?: number;
  /**
   * 头图媒体 id，**三态**，与 content / tags 的两态不同：
   *   - 省略（undefined）→ 别动头图；
   *   - `null` → 摘掉头图（只解除引用，那张图仍留在媒体库里）；
   *   - 数字 → 换成这张。
   *
   * 为什么必须三态：塌成两态的方向两边都是数据损坏——要么用户摘掉的图在保存后
   * 复活，要么一次只改标题的保存静默删掉头图。服务端用 OptionalID 接收这三态。
   * 注意 `??` 或 `||` 会把它压成两态，封装里用的是 `!== undefined` 判断。
   */
  coverMediaId?: number | null;
}

export interface CreateMomentInput {
  content: string;
  // 创建时关联的已上传媒体 id；省略表示不带媒体。
  mediaIds?: number[];
}

/// 更新时省略 mediaIds 表示「保留现有媒体关联不变」；传空数组才会解除全部关联。
export interface UpdateMomentInput {
  content: string;
  mediaIds?: number[];
}

export interface CreatePageInput {
  title: string;
  content: string;
}

export interface UpdatePageInput {
  title: string;
  // 与 UpdateArticleInput.content 同一套语义：省略即不改。
  content?: string;
}

export interface CreateCommentInput {
  targetType: TargetType;
  targetId: number;
  parentId?: number | null;
  content: string;
  // 注意：没有昵称字段。评论署名由服务端取自作者身份（访客账号的 nickname
  // 或站长的展示名），客户端无权替作者自取署名——曾经放开过，结果是同一个人
  // 可以顶着不同名字发言。此类型刻意不提供 nickname，加回来之前请先想清楚。
}

// 批量操作的 id 集合。传入的 id 在同一次请求内被一并处理，属「一次性」语义：失败不会自动重试其中某条。
export interface BatchIdsInput {
  ids: number[];
}

export interface RenameTagInput {
  // 新名称。改名是整标签重命名，会影响所有关联文章（文章只存标签名字符串）。
  name: string;
}

export interface InstallInput {
  // 首次初始化时设置的管理员密码；仅在 installed=false 时有效。
  password: string;
}

/* -------------------------------------------------------------------------- */
/* Auth input                                                                  */
/* -------------------------------------------------------------------------- */

/**
 * 登录：用户名 + 密码。**没有**「管理员 / 普通用户」这个选择，身份由用户名决定：
 * 服务端先看这个名字是否落在站长的登录名上，否则去账号表里找。
 *
 * 调用方不需要、也不应该自己判断身份——曾经的两个方法（loginAdmin / loginUser）
 * 让调用方替服务端做了一个它注定做不对的判断：两边的凭据形状一模一样，选错了
 * 只会得到一个与「密码错了」无法区分的 401。
 *
 * 用户名是一个**全局唯一**的命名空间：站长的登录名与访客的用户名不许重名
 * （注册与改名都由服务端拒绝），否则「用用户名区分身份」这件事本身就不成立。
 */
export interface LoginInput {
  /** 用户名；填注册邮箱也能登（老账号的用户名由邮箱推导而来，本人未必知道）。 */
  username: string;
  password: string;
}

/** 申请验证码；同一验证码既用于注册也用于重置密码，靠后续接口区分用途。 */
export interface EmailCodeInput {
  email: string;
}

export interface RegisterInput {
  /** 收验证码的邮箱，用来证明「确实是本人」；它不是登录名。 */
  email: string;
  code: string;
  /** 今后登录时要填的用户名，全站唯一；与展示用 nickname 是两回事。 */
  username: string;
  password: string;
  /** 前台展示名，评论署名等场合用它；与登录无关。 */
  nickname: string;
}

export interface ResetPasswordInput {
  email: string;
  code: string;
  password: string;
}

/**
 * 当前登录访客自助改资料。两个字段都是可选的（undefined 表示「不改」），
 * 一次可以只改其中一项：改 nickname 不影响登录，改 username 不影响已登录的会话。
 */
export interface UpdateProfileInput {
  username?: string;
  nickname?: string;
}

export interface ChangePasswordInput {
  // 注意字段名是 camelCase，但发往服务端会被重命名为 current_password / new_password（见 auth.ts）。
  currentPassword: string;
  newPassword: string;
}

/**
 * 改站长资料。name 是展示名（必传），username 是登录名（可选——
 * 改登录名会影响下次登录方式，不该由「碰巧没传」触发）。
 */
export interface UpdateAdminProfileInput {
  name: string;
  username?: string;
}

/** 站长修改其他访客账号。username 是登录标识，email 是联系方式，两者分别受唯一约束。 */
export interface UpdateUserInput {
  username: string;
  nickname: string;
  email: string;
}

/* -------------------------------------------------------------------------- */
/* Site settings                                                               */
/* -------------------------------------------------------------------------- */

/** 站点设置的公开视图：任何人可读（内容本来就是展示给所有读者的）。 */
export interface SiteSettingsResponse {
  description: string;
}

/** 修改站点描述。允许空串（清空是合法意图，前台回退内置文案）；上限 200 字符。 */
export interface SiteSettingsInput {
  description: string;
}

// 媒体源：浏览器里传 Blob/ArrayBuffer/Uint8Array，Node 里也可传文件路径字符串（由 media.ts 读盘）。
export type MediaSource = string | Uint8Array | ArrayBuffer | Blob;

// 带元信息的媒体描述符。name 缺省时按路径 basename 推断；contentType 缺省时按扩展名猜测。
export interface MediaInputDescriptor {
  data: MediaSource;
  name?: string;
  contentType?: string;
}

// uploadMedia 接受的入参：既可直接给裸数据/Blob/路径，也可给上面的描述符。数组表示一次传多个。
export type MediaInput = MediaSource | MediaInputDescriptor;

export interface MediaContent {
  /** 原始字节。Buffer 是 Uint8Array 子类，因此 Node 调用方也能直接用。 */
  data: Uint8Array;
  contentType: string;
  url: string;
}
