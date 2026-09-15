import type { HttpClient } from '../http';
import type {
  BatchIdsInput,
  CreateMomentInput,
  Moment,
  Paginated,
  PaginationOptions,
  UpdateMomentInput,
} from '../types';
import { resolvePagination } from './pagination';

// 动态接口。listMoments / getMoment 走公开 /api/moments；其余增删改、回收站与
// 恢复/永久删除都走 /api/admin。回收站语义与文章一致：delete 软删、restore 救回、
// permanentlyDelete 才真删（之后不可恢复）。
export interface MomentsApi {
  listMoments(options?: PaginationOptions): Promise<Paginated<Moment>>;
  getMoment(id: number): Promise<Moment>;
  createMoment(input: CreateMomentInput): Promise<Moment>;
  updateMoment(id: number, input: UpdateMomentInput): Promise<Moment>;
  deleteMoment(id: number): Promise<void>;
  batchDeleteMoments(input: BatchIdsInput): Promise<void>;
  listTrashMoments(options?: PaginationOptions): Promise<Paginated<Moment>>;
  restoreMoment(id: number): Promise<void>;
  batchRestoreMoments(input: BatchIdsInput): Promise<void>;
  permanentlyDeleteMoment(id: number): Promise<void>;
  batchPermanentlyDeleteMoments(input: BatchIdsInput): Promise<void>;
}

export function createMomentsApi(http: HttpClient): MomentsApi {
  return {
    listMoments(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Moment>>({
        path: '/api/moments',
        query: pagination,
      });
    },

    getMoment(id: number) {
      return http.request<Moment>({
        path: `/api/moments/${id}`,
      });
    },

    createMoment(input: CreateMomentInput) {
      return http.request<Moment>({
        method: 'POST',
        path: '/api/admin/moments',
        json: {
          content: input.content,
          // 创建时省略 mediaIds 直接当成「无媒体」发送（创建没有「已关联媒体」可保留）。
          media_ids: input.mediaIds ?? [],
        },
      });
    },

    updateMoment(id: number, input: UpdateMomentInput) {
      const json: Record<string, unknown> = {
        content: input.content,
      };
      // 只有调用方显式给了 mediaIds 才下发。若把「省略」强制成 []，服务端会理解成
      // 「解除全部媒体关联」，从而误删动态现有的所有媒体。
      if (input.mediaIds !== undefined) {
        json.media_ids = input.mediaIds;
      }
      return http.request<Moment>({
        method: 'PUT',
        path: `/api/admin/moments/${id}`,
        json,
      });
    },

    deleteMoment(id: number) {
      return http.request<void>({
        method: 'DELETE',
        path: `/api/admin/moments/${id}`,
      });
    },

    batchDeleteMoments(input: BatchIdsInput) {
      return http.request<void>({
        method: 'DELETE',
        path: '/api/admin/moments',
        json: { ids: input.ids },
      });
    },

    listTrashMoments(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Moment>>({
        path: '/api/admin/trash/moments',
        query: pagination,
      });
    },

    restoreMoment(id: number) {
      return http.request<void>({
        method: 'POST',
        path: `/api/admin/moments/${id}/restore`,
      });
    },

    batchRestoreMoments(input: BatchIdsInput) {
      return http.request<void>({
        method: 'POST',
        path: '/api/admin/moments/batch-restore',
        json: { ids: input.ids },
      });
    },

    permanentlyDeleteMoment(id: number) {
      return http.request<void>({
        method: 'DELETE',
        path: `/api/admin/moments/${id}/permanent`,
      });
    },

    batchPermanentlyDeleteMoments(input: BatchIdsInput) {
      return http.request<void>({
        method: 'DELETE',
        path: '/api/admin/moments/batch-permanent',
        json: { ids: input.ids },
      });
    },
  };
}
