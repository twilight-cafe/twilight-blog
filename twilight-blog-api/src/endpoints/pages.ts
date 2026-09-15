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
      return http.request<Page>({
        method: 'POST',
        path: '/api/admin/pages',
        json: {
          title: input.title,
          content: input.content,
        },
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
