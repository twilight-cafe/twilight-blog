import type { HttpClient } from '../http';
import type { SiteSettingsInput, SiteSettingsResponse } from '../types';

/**
 * 站点级设置（网站描述、代码块行数、首页每页篇数、统一圆角）。
 *
 * 读是公开的（GET /api/site）——这几项本来就展示给所有读者；
 * 写仅管理员（PUT /api/admin/site）。settings.json 里的其他字段
 * （密码哈希、邮件 key 等）不属于「公开可见的设置」，
 * 永远不该出现在这个端点里。
 */
export interface SiteApi {
  /** 取站点设置；无需登录。description 为空串表示站长未设置，前端应回退到内置文案。 */
  getSite(): Promise<SiteSettingsResponse>;
  /**
   * 改站点设置；仅站长。返回保存后的最终值（服务端会折叠描述里的空白）。
   *
   * 四个字段同一次提交：行数、每页篇数、圆角都省略时服务端保持现值，
   * 描述是必填（传空串即清空并回退到内置文案）。
   */
  updateSite(input: SiteSettingsInput): Promise<SiteSettingsResponse>;
}

export function createSiteApi(http: HttpClient): SiteApi {
  return {
    getSite() {
      return http.request<SiteSettingsResponse>({
        path: '/api/site',
      });
    },

    updateSite(input: SiteSettingsInput) {
      return http.request<SiteSettingsResponse>({
        method: 'PUT',
        path: '/api/admin/site',
        // 三个数值项都是可选三态：省略 = 服务端保持现值。
        // undefined 字段被 JSON.stringify 自动丢弃，恰好就是「省略」。
        //
        // 必须逐个显式列出，不能偷懒写成 `json: { ...input }`：那样会把客户端
        // 未来多带的字段一并透传，而契约里「省略」与「显式传值」是两种语义，
        // 多透一个键就等于替调用方做了一个它没做的决定。
        json: {
          description: input.description,
          code_block_max_lines: input.code_block_max_lines,
          home_page_size: input.home_page_size,
          border_radius: input.border_radius,
        },
      });
    },
  };
}
