import type { HttpClient } from '../http';
import type {
  BatchIdsInput,
  CreatePageInput,
  Page,
  Paginated,
  PaginationOptions,
  UpdatePageInput,
} from '../types';
import { resolvePagination } from './pagination';

// 页面接口。listPages / getPage 走公开 /api/pages；增删改、回收站与恢复/永久删除都走
// /api/admin。回收站语义同文章：delete 软删、restore 救回、permanentlyDelete 才真删。
export interface PagesApi {
  listPages(options?: PaginationOptions): Promise<Paginated<Page>>;
  getPage(id: number): Promise<Page>;
  createPage(input: CreatePageInput): Promise<Page>;
  updatePage(id: number, input: UpdatePageInput): Promise<Page>;
  deletePage(id: number): Promise<void>;
  batchDeletePages(input: BatchIdsInput): Promise<void>;
  listTrashPages(options?: PaginationOptions): Promise<Paginated<Page>>;
  restorePage(id: number): Promise<void>;
  batchRestorePages(input: BatchIdsInput): Promise<void>;
  permanentlyDeletePage(id: number): Promise<void>;
  batchPermanentlyDeletePages(input: BatchIdsInput): Promise<void>;
}

export function createPagesApi(http: HttpClient): PagesApi {
  return {
    listPages(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Page>>({
        path: '/api/pages',
        query: pagination,
      });
    },

    getPage(id: number) {
      return http.request<Page>({
        path: `/api/pages/${id}`,
      });
    },

    createPage(input: CreatePageInput) {
      const json: Record<string, unknown> = {
        title: input.title,
        content: input.content,
      };
      // 英文版本省略就是「没有」，服务端按空串写入（与文章创建一致）。
      if (input.titleEn !== undefined) json.title_en = input.titleEn;
      if (input.contentEn !== undefined) json.content_en = input.contentEn;
      return http.request<Page>({
        method: 'POST',
        path: '/api/admin/pages',
        json,
      });
    },

    updatePage(id: number, input: UpdatePageInput) {
      const json: Record<string, unknown> = {
        title: input.title,
      };
      // 同 updateArticle：省略正文即「不改」，不要把它变成空串发出去。
      if (input.content !== undefined) {
        json.content = input.content;
      }
      // 英文标题 / 正文同样是三态：省略 = 不改，空串 = 清空。
      if (input.titleEn !== undefined) {
        json.title_en = input.titleEn;
      }
      if (input.contentEn !== undefined) {
        json.content_en = input.contentEn;
      }
      return http.request<Page>({
        method: 'PUT',
        path: `/api/admin/pages/${id}`,
        json,
      });
    },

    deletePage(id: number) {
      return http.request<void>({
        method: 'DELETE',
        path: `/api/admin/pages/${id}`,
      });
    },

    batchDeletePages(input: BatchIdsInput) {
      return http.request<void>({
        method: 'DELETE',
        path: '/api/admin/pages',
        json: { ids: input.ids },
      });
    },

    listTrashPages(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Page>>({
        path: '/api/admin/trash/pages',
        query: pagination,
      });
    },

    restorePage(id: number) {
      return http.request<void>({
        method: 'POST',
        path: `/api/admin/pages/${id}/restore`,
      });
    },

    batchRestorePages(input: BatchIdsInput) {
      return http.request<void>({
        method: 'POST',
        path: '/api/admin/pages/batch-restore',
        json: { ids: input.ids },
      });
    },

    permanentlyDeletePage(id: number) {
      return http.request<void>({
        method: 'DELETE',
        path: `/api/admin/pages/${id}/permanent`,
      });
    },

    batchPermanentlyDeletePages(input: BatchIdsInput) {
      return http.request<void>({
        method: 'DELETE',
        path: '/api/admin/pages/batch-permanent',
        json: { ids: input.ids },
      });
    },
  };
}
