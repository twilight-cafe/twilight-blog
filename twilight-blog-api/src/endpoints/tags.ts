import type { HttpClient } from '../http';
import type {
  Article,
  CountResponse,
  Paginated,
  PaginationOptions,
  RenameTagInput,
  Tag,
} from '../types';
import { resolvePagination } from './pagination';

// 标签接口。列表/文章/计数均公开（/api/tags）；只有改名走 /api/admin/tags，需要登录。
export interface TagsApi {
  listTags(options?: PaginationOptions): Promise<Paginated<Tag>>;
  // 取某标签下的文章列表（公开）。注意是软删之外的全部，含未公开？以服务端为准。
  getTagArticles(tagId: number, options?: PaginationOptions): Promise<Paginated<Article>>;
  // 取某标签当前文章数（公开），等价于 Tag.article_count。
  getTagCount(tagId: number): Promise<CountResponse>;
  // 整标签改名（仅站长）。文章只存标签名字符串，改名会一并影响所有关联文章。
  renameTag(tagId: number, input: RenameTagInput): Promise<Tag>;
}

export function createTagsApi(http: HttpClient): TagsApi {
  return {
    listTags(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Tag>>({
        path: '/api/tags',
        query: pagination,
      });
    },

    getTagArticles(tagId: number, options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<Article>>({
        path: `/api/tags/${tagId}/articles`,
        query: pagination,
      });
    },

    getTagCount(tagId: number) {
      return http.request<CountResponse>({
        path: `/api/tags/${tagId}/count`,
      });
    },

    renameTag(tagId: number, input: RenameTagInput) {
      return http.request<Tag>({
        method: 'PUT',
        path: `/api/admin/tags/${tagId}`,
        json: { name: input.name },
      });
    },
  };
}
