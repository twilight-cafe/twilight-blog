import type { HttpClient } from '../http';
import type {
  Article,
  BatchIdsInput,
  CreateArticleInput,
  Paginated,
  PaginationOptions,
  UpdateArticleInput,
} from '../types';
import { resolvePagination } from './pagination';

// 文章接口。listArticles / getArticle 走公开 /api/articles，无需登录；
// 其余增删改、回收站与恢复/永久删除都走 /api/admin，需要登录。
// 回收站语义：delete 是软删（进回收站），permanentlyDelete 才真正从库里抹掉，
// restore 把软删的文章救回——顺序上必须先 restore 才能「保留」，永久删除后无法恢复。
export interface ArticlesApi {
  listArticles(options?: PaginationOptions): Promise<Paginated<Article>>;
  getArticle(id: number): Promise<Article>;
  createArticle(input: CreateArticleInput): Promise<Article>;
  updateArticle(id: number, input: UpdateArticleInput): Promise<Article>;
  deleteArticle(id: number): Promise<void>;
  // 一次性批量软删多个 id（走 DELETE + body）。失败不会回滚其中已删的部分，调用方需自行处理部分成功。
  batchDeleteArticles(input: BatchIdsInput): Promise<void>;
  listTrashArticles(options?: PaginationOptions): Promise<Paginated<Article>>;
  restoreArticle(id: number): Promise<void>;
  batchRestoreArticles(input: BatchIdsInput): Promise<void>;
  permanentlyDeleteArticle(id: number): Promise<void>;
  batchPermanentlyDeleteArticles(input: BatchIdsInput): Promise<void>;
}

export function createArticlesApi(http: HttpClient): ArticlesApi {
  return {
    listArticles(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Article>>({
        path: '/api/articles',
        query: pagination,
      });
    },

    getArticle(id: number) {
      return http.request<Article>({
        path: `/api/articles/${id}`,
      });
    },

    createArticle(input: CreateArticleInput) {
      const json: Record<string, unknown> = {
        title: input.title,
        content: input.content,
        // 创建时省略 tags 直接当成「空标签」发送（与更新不同：创建没有「旧标签」可保留）。
        tags: input.tags ?? [],
      };
      // 英文版本与 AI 标记：省略就是「没有」，服务端按空串 / false 处理。
      if (input.titleEn !== undefined) json.title_en = input.titleEn;
      if (input.contentEn !== undefined) json.content_en = input.contentEn;
      if (input.aiGenerated !== undefined) json.ai_generated = input.aiGenerated;
      // publishedAt 与 coverMediaId 在这里都不用 `??` 兜底成一个默认值：
      // 省略就该让字段**消失**，由服务端决定「用当前时间 / 没有头图」，
      // 而不是由客户端替它猜一个（比如 0）。
      if (input.publishedAt !== undefined) {
        json.published_at = input.publishedAt;
      }
      if (input.coverMediaId !== undefined) {
        json.cover_media_id = input.coverMediaId;
      }
      return http.request<Article>({
        method: 'POST',
        path: '/api/admin/articles',
        json,
      });
    },

    updateArticle(id: number, input: UpdateArticleInput) {
      const json: Record<string, unknown> = {
        title: input.title,
      };
      // 与 tags 同一条规则：只有调用方显式给了正文才附上该字段。
      //
      // 注意这句话的边界：JSON.stringify 本来就会丢弃值为 undefined 的属性，
      // 所以「省略 → 不发该字段」即使去掉这个判断也成立——真正提供该行为的是序列化，
      // 保留这个判断是为了把这个意图**写在代码里**，避免日后换成别的序列化方式
      // （或改成先展开再序列化）时语义悄悄改变。
      // 它挡的是真正的回归形态：把省略当成清空（如 content ?? ''），
      // 而那会让服务端把「我没提正文」理解成「把正文清空」。
      if (input.content !== undefined) {
        json.content = input.content;
      }
      // 英文标题 / 正文与 AI 标记同样是「省略 = 不改」。
      if (input.titleEn !== undefined) {
        json.title_en = input.titleEn;
      }
      if (input.contentEn !== undefined) {
        json.content_en = input.contentEn;
      }
      if (input.aiGenerated !== undefined) {
        json.ai_generated = input.aiGenerated;
      }
      // 只有当调用方显式给了 tags 才下发该字段。若把「省略」也强制成 []，
      // 服务端会理解为「清空标签」，从而误删文章现有的全部标签。
      if (input.tags !== undefined) {
        json.tags = input.tags;
      }
      if (input.publishedAt !== undefined) {
        json.published_at = input.publishedAt;
      }
      // 头图必须是 `!== undefined` 而不是 `!= null` / `??`：
      // null 是「摘掉头图」这条**指令**，把它一起挡掉，用户就再也删不掉头图了。
      if (input.coverMediaId !== undefined) {
        json.cover_media_id = input.coverMediaId;
      }
      return http.request<Article>({
        method: 'PUT',
        path: `/api/admin/articles/${id}`,
        json,
      });
    },

    deleteArticle(id: number) {
      return http.request<void>({
        method: 'DELETE',
        path: `/api/admin/articles/${id}`,
      });
    },

    batchDeleteArticles(input: BatchIdsInput) {
      return http.request<void>({
        method: 'DELETE',
        path: '/api/admin/articles',
        json: { ids: input.ids },
      });
    },

    listTrashArticles(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Article>>({
        path: '/api/admin/trash/articles',
        query: pagination,
      });
    },

    restoreArticle(id: number) {
      return http.request<void>({
        method: 'POST',
        path: `/api/admin/articles/${id}/restore`,
      });
    },

    batchRestoreArticles(input: BatchIdsInput) {
      return http.request<void>({
        method: 'POST',
        path: '/api/admin/articles/batch-restore',
        json: { ids: input.ids },
      });
    },

    permanentlyDeleteArticle(id: number) {
      return http.request<void>({
        method: 'DELETE',
        path: `/api/admin/articles/${id}/permanent`,
      });
    },

    batchPermanentlyDeleteArticles(input: BatchIdsInput) {
      return http.request<void>({
        method: 'DELETE',
        path: '/api/admin/articles/batch-permanent',
        json: { ids: input.ids },
      });
    },
  };
}
