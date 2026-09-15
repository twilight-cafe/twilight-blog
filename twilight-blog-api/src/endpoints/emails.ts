import type { HttpClient } from '../http';
import type { Email, Paginated, PaginationOptions } from '../types';
import { resolvePagination } from './pagination';

// 出站邮件记录接口，仅站长可用（/api/admin/emails）。内容含收件人邮箱，属敏感数据。
export interface EmailsApi {
  listEmails(options?: PaginationOptions): Promise<Paginated<Email>>;
  getEmail(id: number): Promise<Email>;
}

export function createEmailsApi(http: HttpClient): EmailsApi {
  return {
    listEmails(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Email>>({
        path: '/api/admin/emails',
        query: pagination,
      });
    },

    getEmail(id: number) {
      return http.request<Email>({
        path: `/api/admin/emails/${id}`,
      });
    },
  };
}
