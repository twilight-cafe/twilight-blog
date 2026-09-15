import type { HttpClient } from '../http';
import type {
  BatchIdsInput,
  Comment,
  CreateCommentInput,
  ListCommentsOptions,
  Paginated,
  PaginationOptions,
} from '../types';
import { resolvePagination } from './pagination';

// 评论接口。listComments（按 target 列某对象下的评论）与 createComment、like/unlike 公开；
// 其余列表/管理/回收站/恢复/永久删除都走 /api/admin。回收站语义同文章：delete 软删、
// restore 救回、permanentlyDelete 才真删。
export interface CommentsApi {
  // 列某文章/动态/页面下的评论；必须带 targetType + targetId（见 ListCommentsOptions）。
  listComments(options: ListCommentsOptions): Promise<Paginated<Comment>>;
  /** 发评论需要会话 cookie（站长或访客账号都行）；署名由服务端取自作者身份。 */
  createComment(input: CreateCommentInput): Promise<Comment>;
  // 点赞。操作基于当前会话，重复点赞仍是「已赞」状态（幂等），返回最新 like_count 与 liked_by_me。
  likeComment(id: number): Promise<Comment>;
  // 取消点赞，同样基于当前会话。
  unlikeComment(id: number): Promise<Comment>;
  // 站长视角：跨全部对象的评论列表（不受 target 限制）。
  listAllComments(options?: PaginationOptions): Promise<Paginated<Comment>>;
  listTrashComments(options?: PaginationOptions): Promise<Paginated<Comment>>;
  deleteComment(id: number): Promise<void>;
  batchDeleteComments(input: BatchIdsInput): Promise<void>;
  restoreComment(id: number): Promise<void>;
  batchRestoreComments(input: BatchIdsInput): Promise<void>;
  permanentlyDeleteComment(id: number): Promise<void>;
  batchPermanentlyDeleteComments(input: BatchIdsInput): Promise<void>;
}

export function createCommentsApi(http: HttpClient): CommentsApi {
  return {
    listComments(options: ListCommentsOptions) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Comment>>({
        path: '/api/comments',
        query: {
          target_type: options.targetType,
          target_id: options.targetId,
          page: pagination.page,
          page_size: pagination.page_size,
        },
      });
    },

    createComment(input: CreateCommentInput) {
      // 请求体里没有署名字段：评论显示名取自作者身份，客户端无权指定。
      return http.request<Comment>({
        method: 'POST',
        path: '/api/comments',
        json: {
          target_type: input.targetType,
          target_id: input.targetId,
          parent_id: input.parentId ?? null,
          content: input.content,
        },
      });
    },

    likeComment(id: number) {
      return http.request<Comment>({
        method: 'POST',
        path: `/api/comments/${id}/like`,
      });
    },

    unlikeComment(id: number) {
      return http.request<Comment>({
        method: 'DELETE',
        path: `/api/comments/${id}/like`,
      });
    },

    listAllComments(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Comment>>({
        path: '/api/admin/comments',
        query: pagination,
      });
    },

    listTrashComments(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Comment>>({
        path: '/api/admin/trash/comments',
        query: pagination,
      });
    },

    deleteComment(id: number) {
      return http.request<void>({
        method: 'DELETE',
        path: `/api/admin/comments/${id}`,
      });
    },

    batchDeleteComments(input: BatchIdsInput) {
      return http.request<void>({
        method: 'DELETE',
        path: '/api/admin/comments',
        json: { ids: input.ids },
      });
    },

    restoreComment(id: number) {
      return http.request<void>({
        method: 'POST',
        path: `/api/admin/comments/${id}/restore`,
      });
    },

    batchRestoreComments(input: BatchIdsInput) {
      return http.request<void>({
        method: 'POST',
        path: '/api/admin/comments/batch-restore',
        json: { ids: input.ids },
      });
    },

    permanentlyDeleteComment(id: number) {
      return http.request<void>({
        method: 'DELETE',
        path: `/api/admin/comments/${id}/permanent`,
      });
    },

    batchPermanentlyDeleteComments(input: BatchIdsInput) {
      return http.request<void>({
        method: 'DELETE',
        path: '/api/admin/comments/batch-permanent',
        json: { ids: input.ids },
      });
    },
  };
}
