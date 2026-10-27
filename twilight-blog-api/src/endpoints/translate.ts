import type { HttpClient } from '../http';
import type {
  TranslateArticleInput,
  TranslateArticleResult,
  TranslateConfig,
  TranslateModelList,
  TranslateModelQuery,
  UpdateTranslateConfigInput,
} from '../types';

/**
 * AI 翻译（仅站长）。
 *
 * 三个端点都在 `/api/admin` 下：翻译会消耗站长配置的上游额度，配置里还有
 * 密钥，任何一项都不该对普通访客开放。
 */
export interface TranslateApi {
  /** 把标题与正文从 `from` 翻译成 `to`。未配置时服务端返回 503。 */
  translateArticle(input: TranslateArticleInput): Promise<TranslateArticleResult>;
  /** 读取脱敏配置；响应里没有密钥，只有 `api_key_configured`。 */
  getTranslateConfig(): Promise<TranslateConfig>;
  /** 修改配置；字段省略表示保持现值，`api_key` 留空同样保持现值。 */
  updateTranslateConfig(input: UpdateTranslateConfigInput): Promise<TranslateConfig>;
  /** 查询上游支持的模型 id；url/api_key 省略时沿用已保存配置。 */
  listTranslateModels(input?: TranslateModelQuery): Promise<TranslateModelList>;
}

export function createTranslateApi(http: HttpClient): TranslateApi {
  return {
    translateArticle(input: TranslateArticleInput) {
      return http.request<TranslateArticleResult>({
        method: 'POST',
        path: '/api/admin/translate',
        // 逐个显式列出，不用 `{ ...input }`：契约里只有这四个字段，
        // 展开会把调用方将来多带的键一并透传出去。
        json: {
          title: input.title,
          content: input.content,
          from: input.from,
          to: input.to,
        },
      });
    },

    getTranslateConfig() {
      return http.request<TranslateConfig>({
        path: '/api/admin/translate/config',
      });
    },

    updateTranslateConfig(input: UpdateTranslateConfigInput) {
      return http.request<TranslateConfig>({
        method: 'PUT',
        path: '/api/admin/translate/config',
        // 每个字段都必须显式出现；undefined 由 JSON.stringify 丢弃，
        // 恰好就是服务端约定的「省略 = 保持现值」。
        json: {
          enabled: input.enabled,
          url: input.url,
          model: input.model,
          api_key: input.api_key,
          timeout_seconds: input.timeout_seconds,
          max_tokens: input.max_tokens,
          prompt_template: input.prompt_template,
          thinking_effort: input.thinking_effort,
        },
      });
    },

    listTranslateModels(input = {}) {
      return http.request<TranslateModelList>({
        method: 'POST',
        path: '/api/admin/translate/models',
        json: {
          url: input.url,
          api_key: input.api_key,
        },
      });
    },
  };
}
