import type { PaginationOptions } from '../types';

// 规范化后的分页参数，直接喂给查询串（snake_case 字段名与服务端一致）。
export type ResolvedPagination = {
  page: number;
  page_size: number;
};

// 把调用方分页入参规范成服务端要的 page/page_size。
// 注意：校验失败会【同步】抛 RangeError（不是 BlogApiError），发生在发请求之前，
// 调用方若只 catch BlogApiError 会漏掉它。page_size 上限 12 是与服务端保持一致的限制。
export function resolvePagination(options: PaginationOptions = {}): ResolvedPagination {
  const page = options.page ?? 1;
  const pageSize = options.pageSize ?? 12;

  if (!Number.isInteger(page) || page < 1) {
    throw new RangeError('page must be a positive integer');
  }
  if (!Number.isInteger(pageSize) || pageSize < 1 || pageSize > 12) {
    throw new RangeError('page_size must be between 1 and 12');
  }

  return { page, page_size: pageSize };
}
