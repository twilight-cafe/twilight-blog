import type { HttpClient } from '../http';
import type { Paginated, SearchOptions, SearchResult } from '../types';
import { resolvePagination } from './pagination';

// 搜索接口（公开，/api/search）。按关键词 q 跨文章与页面检索，结果以联合类型返回，
// 每项带 type 区分是文章还是页面。
export interface SearchApi {
  search(options: SearchOptions): Promise<Paginated<SearchResult>>;
}

export function createSearchApi(http: HttpClient): SearchApi {
  return {
    search(options: SearchOptions) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<SearchResult>>({
        path: '/api/search',
        query: {
          q: options.q,
          page: pagination.page,
          page_size: pagination.page_size,
          lang: options.lang,
        },
      });
    },
  };
}
