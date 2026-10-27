import { HttpClient } from './http';
// 各 endpoint 导出的 `*Api` 都是接口（纯类型），必须用 `import type` 引入，
// 否则消费端开启 `verbatimModuleSyntax` 时会因为「导入了只会作为类型的东西」而报错。
import { createInstallApi } from './endpoints/install';
import type { InstallApi } from './endpoints/install';
import { createAuthApi } from './endpoints/auth';
import type { AuthApi } from './endpoints/auth';
import { createUsersApi } from './endpoints/users';
import type { UsersApi } from './endpoints/users';
import { createArticlesApi } from './endpoints/articles';
import type { ArticlesApi } from './endpoints/articles';
import { createTagsApi } from './endpoints/tags';
import type { TagsApi } from './endpoints/tags';
import { createMomentsApi } from './endpoints/moments';
import type { MomentsApi } from './endpoints/moments';
import { createPagesApi } from './endpoints/pages';
import type { PagesApi } from './endpoints/pages';
import { createCommentsApi } from './endpoints/comments';
import type { CommentsApi } from './endpoints/comments';
import { createEmailsApi } from './endpoints/emails';
import type { EmailsApi } from './endpoints/emails';
import { createMediaApi } from './endpoints/media';
import type { MediaApi } from './endpoints/media';
import { createSearchApi } from './endpoints/search';
import type { SearchApi } from './endpoints/search';
import { createSiteApi } from './endpoints/site';
import type { SiteApi } from './endpoints/site';
import { createTranslateApi } from './endpoints/translate';
import type { TranslateApi } from './endpoints/translate';
import type {
  AdminSiteSettingsResponse,
  Article,
  BatchIdsInput,
  ChangePasswordInput,
  Comment,
  CountResponse,
  CreateArticleInput,
  CreateCommentInput,
  CreateMomentInput,
  CreatePageInput,
  Email,
  EmailCodeInput,
  InstallInput,
  InstallStatus,
  ListCommentsOptions,
  LoginInput,
  MeResponse,
  MediaContent,
  MediaInput,
  MediaItem,
  MessageResponse,
  Moment,
  Page,
  Paginated,
  PaginationOptions,
  RegisterInput,
  RenameMediaInput,
  RenameTagInput,
  ResetPasswordInput,
  SearchOptions,
  SearchResult,
  SiteSettingsInput,
  SiteSettingsResponse,
  Tag,
  TranslateArticleInput,
  TranslateArticleResult,
  TranslateConfig,
  TranslateModelList,
  TranslateModelQuery,
  UpdateAdminProfileInput,
  UpdateArticleInput,
  UpdateMomentInput,
  UpdatePageInput,
  UpdateProfileInput,
  UpdateTranslateConfigInput,
  UpdateUserInput,
  UserResponse,
} from './types';

/**
 * 仅传输层配置：鉴权不在这个客户端里，而是存在浏览器的 session cookie 中，
 * 因此没有 token 需要配置或持久化。整个客户端是无状态的（除了底层的 HttpClient）。
 */
export interface BlogClientOptions {
  baseUrl: string;
  fetch?: typeof fetch;
}

/**
 * 博客 API 的门面：聚合并暴露所有 endpoint（安装、鉴权、文章、标签、动态、页面、
 * 评论、邮件、媒体、搜索）。所有 api 工厂共享同一个 HttpClient，因此共享同一份
 * session cookie 与同一套超时/CSRF 逻辑——没有「每用户独立」的隔离状态。
 *
 * 关于 401：本客户端不自动刷新或重登。任何接口在会话失效时都会抛出
 * `BlogApiError`（status=401），是否跳登录页由调用方决定。
 */
export class BlogClient {
  private readonly http: HttpClient;
  private readonly installApi: InstallApi;
  private readonly authApi: AuthApi;
  private readonly usersApi: UsersApi;
  private readonly articlesApi: ArticlesApi;
  private readonly tagsApi: TagsApi;
  private readonly momentsApi: MomentsApi;
  private readonly pagesApi: PagesApi;
  private readonly commentsApi: CommentsApi;
  private readonly emailApi: EmailsApi;
  private readonly mediaApi: MediaApi;
  private readonly searchApi: SearchApi;
  private readonly siteApi: SiteApi;
  private readonly translateApi: TranslateApi;

  constructor(options: BlogClientOptions) {
    this.http = new HttpClient(options.baseUrl, {
      fetch: options.fetch,
    });
    this.installApi = createInstallApi(this.http);
    this.authApi = createAuthApi(this.http);
    this.usersApi = createUsersApi(this.http);
    this.articlesApi = createArticlesApi(this.http);
    this.tagsApi = createTagsApi(this.http);
    this.momentsApi = createMomentsApi(this.http);
    this.pagesApi = createPagesApi(this.http);
    this.commentsApi = createCommentsApi(this.http);
    this.emailApi = createEmailsApi(this.http);
    this.mediaApi = createMediaApi(this.http);
    this.searchApi = createSearchApi(this.http);
    this.siteApi = createSiteApi(this.http);
    this.translateApi = createTranslateApi(this.http);
  }

  getBaseUrl(): string {
    return this.http.getBaseUrl();
  }

  // 安装流程（首次初始化，无需登录）
  getInstallStatus(): Promise<InstallStatus> {
    return this.installApi.getInstallStatus();
  }

  install(input: InstallInput): Promise<InstallStatus> {
    return this.installApi.install(input);
  }

  // 鉴权与会话自助（登录、注册、改密、登出、当前会话）
  login(input: LoginInput): Promise<MeResponse> {
    return this.authApi.login(input);
  }

  requestEmailCode(input: EmailCodeInput): Promise<MessageResponse> {
    return this.authApi.requestEmailCode(input);
  }

  register(input: RegisterInput): Promise<MeResponse> {
    return this.authApi.register(input);
  }

  resetPassword(input: ResetPasswordInput): Promise<MessageResponse> {
    return this.authApi.resetPassword(input);
  }

  logout(): Promise<void> {
    return this.authApi.logout();
  }

  me(): Promise<MeResponse> {
    return this.authApi.me();
  }

  updateProfile(input: UpdateProfileInput): Promise<UserResponse> {
    return this.authApi.updateProfile(input);
  }

  changePassword(input: ChangePasswordInput): Promise<void> {
    return this.authApi.changePassword(input);
  }

  updateAdminProfile(input: UpdateAdminProfileInput): Promise<MeResponse> {
    return this.authApi.updateAdminProfile(input);
  }

  // 访客账号管理（仅站长）
  listUsers(options?: PaginationOptions): Promise<Paginated<UserResponse>> {
    return this.usersApi.listUsers(options);
  }

  updateUser(id: number, input: UpdateUserInput): Promise<UserResponse> {
    return this.usersApi.updateUser(id, input);
  }

  // 站点设置（读公开；写仅站长）——整份快照进出，字段级的展开在 siteApi 里
  getSite(): Promise<SiteSettingsResponse> {
    return this.siteApi.getSite();
  }

  updateSite(input: SiteSettingsInput): Promise<SiteSettingsResponse> {
    return this.siteApi.updateSite(input);
  }

  // 管理端站点设置：比公开快照多一个评论通知收件地址（仅站长可读）。
  getAdminSite(): Promise<AdminSiteSettingsResponse> {
    return this.siteApi.getAdminSite();
  }

  // AI 翻译（仅站长）：执行翻译与读写配置。配置响应不含密钥。
  translateArticle(input: TranslateArticleInput): Promise<TranslateArticleResult> {
    return this.translateApi.translateArticle(input);
  }

  getTranslateConfig(): Promise<TranslateConfig> {
    return this.translateApi.getTranslateConfig();
  }

  updateTranslateConfig(input: UpdateTranslateConfigInput): Promise<TranslateConfig> {
    return this.translateApi.updateTranslateConfig(input);
  }

  listTranslateModels(input?: TranslateModelQuery): Promise<TranslateModelList> {
    return this.translateApi.listTranslateModels(input);
  }

  // 文章（列表/详情公开；增删改与回收站在 /api/admin 下，需登录）
  listArticles(options?: PaginationOptions): Promise<Paginated<Article>> {
    return this.articlesApi.listArticles(options);
  }

  getArticle(id: number): Promise<Article> {
    return this.articlesApi.getArticle(id);
  }

  createArticle(input: CreateArticleInput): Promise<Article> {
    return this.articlesApi.createArticle(input);
  }

  updateArticle(id: number, input: UpdateArticleInput): Promise<Article> {
    return this.articlesApi.updateArticle(id, input);
  }

  deleteArticle(id: number): Promise<void> {
    return this.articlesApi.deleteArticle(id);
  }

  batchDeleteArticles(input: BatchIdsInput): Promise<void> {
    return this.articlesApi.batchDeleteArticles(input);
  }

  listTrashArticles(options?: PaginationOptions): Promise<Paginated<Article>> {
    return this.articlesApi.listTrashArticles(options);
  }

  restoreArticle(id: number): Promise<void> {
    return this.articlesApi.restoreArticle(id);
  }

  batchRestoreArticles(input: BatchIdsInput): Promise<void> {
    return this.articlesApi.batchRestoreArticles(input);
  }

  permanentlyDeleteArticle(id: number): Promise<void> {
    return this.articlesApi.permanentlyDeleteArticle(id);
  }

  batchPermanentlyDeleteArticles(input: BatchIdsInput): Promise<void> {
    return this.articlesApi.batchPermanentlyDeleteArticles(input);
  }

  // 标签（列表/文章/计数公开；改名在 /api/admin 下）
  listTags(options?: PaginationOptions): Promise<Paginated<Tag>> {
    return this.tagsApi.listTags(options);
  }

  getTagArticles(tagId: number, options?: PaginationOptions): Promise<Paginated<Article>> {
    return this.tagsApi.getTagArticles(tagId, options);
  }

  getTagCount(tagId: number): Promise<CountResponse> {
    return this.tagsApi.getTagCount(tagId);
  }

  renameTag(tagId: number, input: RenameTagInput): Promise<Tag> {
    return this.tagsApi.renameTag(tagId, input);
  }

  // 动态（列表/详情公开；增删改与回收站在 /api/admin 下）
  listMoments(options?: PaginationOptions): Promise<Paginated<Moment>> {
    return this.momentsApi.listMoments(options);
  }

  getMoment(id: number): Promise<Moment> {
    return this.momentsApi.getMoment(id);
  }

  createMoment(input: CreateMomentInput): Promise<Moment> {
    return this.momentsApi.createMoment(input);
  }

  updateMoment(id: number, input: UpdateMomentInput): Promise<Moment> {
    return this.momentsApi.updateMoment(id, input);
  }

  deleteMoment(id: number): Promise<void> {
    return this.momentsApi.deleteMoment(id);
  }

  batchDeleteMoments(input: BatchIdsInput): Promise<void> {
    return this.momentsApi.batchDeleteMoments(input);
  }

  listTrashMoments(options?: PaginationOptions): Promise<Paginated<Moment>> {
    return this.momentsApi.listTrashMoments(options);
  }

  restoreMoment(id: number): Promise<void> {
    return this.momentsApi.restoreMoment(id);
  }

  batchRestoreMoments(input: BatchIdsInput): Promise<void> {
    return this.momentsApi.batchRestoreMoments(input);
  }

  permanentlyDeleteMoment(id: number): Promise<void> {
    return this.momentsApi.permanentlyDeleteMoment(id);
  }

  batchPermanentlyDeleteMoments(input: BatchIdsInput): Promise<void> {
    return this.momentsApi.batchPermanentlyDeleteMoments(input);
  }

  // 页面（列表/详情公开；增删改与回收站在 /api/admin 下）
  listPages(options?: PaginationOptions): Promise<Paginated<Page>> {
    return this.pagesApi.listPages(options);
  }

  getPage(id: number): Promise<Page> {
    return this.pagesApi.getPage(id);
  }

  createPage(input: CreatePageInput): Promise<Page> {
    return this.pagesApi.createPage(input);
  }

  updatePage(id: number, input: UpdatePageInput): Promise<Page> {
    return this.pagesApi.updatePage(id, input);
  }

  deletePage(id: number): Promise<void> {
    return this.pagesApi.deletePage(id);
  }

  batchDeletePages(input: BatchIdsInput): Promise<void> {
    return this.pagesApi.batchDeletePages(input);
  }

  listTrashPages(options?: PaginationOptions): Promise<Paginated<Page>> {
    return this.pagesApi.listTrashPages(options);
  }

  restorePage(id: number): Promise<void> {
    return this.pagesApi.restorePage(id);
  }

  batchRestorePages(input: BatchIdsInput): Promise<void> {
    return this.pagesApi.batchRestorePages(input);
  }

  permanentlyDeletePage(id: number): Promise<void> {
    return this.pagesApi.permanentlyDeletePage(id);
  }

  batchPermanentlyDeletePages(input: BatchIdsInput): Promise<void> {
    return this.pagesApi.batchPermanentlyDeletePages(input);
  }

  // 搜索（公开，按关键词跨文章/页面）
  search(options: SearchOptions): Promise<Paginated<SearchResult>> {
    return this.searchApi.search(options);
  }

  // 评论（列评论公开；点赞/管理/回收站在 /api/admin 下）
  listComments(options: ListCommentsOptions): Promise<Paginated<Comment>> {
    return this.commentsApi.listComments(options);
  }

  createComment(input: CreateCommentInput): Promise<Comment> {
    return this.commentsApi.createComment(input);
  }

  likeComment(id: number): Promise<Comment> {
    return this.commentsApi.likeComment(id);
  }

  unlikeComment(id: number): Promise<Comment> {
    return this.commentsApi.unlikeComment(id);
  }

  listAllComments(options?: PaginationOptions): Promise<Paginated<Comment>> {
    return this.commentsApi.listAllComments(options);
  }

  listTrashComments(options?: PaginationOptions): Promise<Paginated<Comment>> {
    return this.commentsApi.listTrashComments(options);
  }

  deleteComment(id: number): Promise<void> {
    return this.commentsApi.deleteComment(id);
  }

  batchDeleteComments(input: BatchIdsInput): Promise<void> {
    return this.commentsApi.batchDeleteComments(input);
  }

  restoreComment(id: number): Promise<void> {
    return this.commentsApi.restoreComment(id);
  }

  batchRestoreComments(input: BatchIdsInput): Promise<void> {
    return this.commentsApi.batchRestoreComments(input);
  }

  permanentlyDeleteComment(id: number): Promise<void> {
    return this.commentsApi.permanentlyDeleteComment(id);
  }

  batchPermanentlyDeleteComments(input: BatchIdsInput): Promise<void> {
    return this.commentsApi.batchPermanentlyDeleteComments(input);
  }

  // 邮件（仅站长，出站邮件记录）
  listEmails(options?: PaginationOptions): Promise<Paginated<Email>> {
    return this.emailApi.listEmails(options);
  }

  getEmail(id: number): Promise<Email> {
    return this.emailApi.getEmail(id);
  }

  // 媒体（列表/下载公开；上传/改展示名/删除在 /api/admin 下）
  uploadMedia(files: MediaInput | MediaInput[]): Promise<MediaItem[]> {
    return this.mediaApi.uploadMedia(files);
  }

  getMedia(id: number): Promise<MediaContent> {
    return this.mediaApi.getMedia(id);
  }

  listMedia(options?: PaginationOptions): Promise<Paginated<MediaItem>> {
    return this.mediaApi.listMedia(options);
  }

  renameMedia(id: number, input: RenameMediaInput): Promise<MediaItem> {
    return this.mediaApi.renameMedia(id, input);
  }

  deleteMedia(id: number): Promise<void> {
    return this.mediaApi.deleteMedia(id);
  }
}

export function createClient(options: BlogClientOptions): BlogClient {
  return new BlogClient(options);
}
