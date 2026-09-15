import type { HttpClient } from '../http';
import type { Paginated, PaginationOptions, UpdateUserInput, UserResponse } from '../types';
import { resolvePagination } from './pagination';

/** 站长视角下的访客账号列表与编辑（仅站长可用）。 */
export interface UsersApi {
  listUsers(options?: PaginationOptions): Promise<Paginated<UserResponse>>;
  /** 改某个访客账号的登录标识（username）、展示名（nickname）与联系方式（email）。 */
  updateUser(id: number, input: UpdateUserInput): Promise<UserResponse>;
}

export function createUsersApi(http: HttpClient): UsersApi {
  return {
    listUsers(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<UserResponse>>({
        path: '/api/admin/users',
        query: pagination,
      });
    },

    updateUser(id: number, input: UpdateUserInput) {
      return http.request<UserResponse>({
        method: 'PUT',
        path: `/api/admin/users/${id}`,
        json: { username: input.username, nickname: input.nickname, email: input.email },
      });
    },
  };
}
