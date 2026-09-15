import type { HttpClient } from '../http';
import type { SiteSettingsInput, SiteSettingsResponse } from '../types';

/**
 * 站点级文案设置（目前只有「网站描述」）。
 *
 * 读是公开的（GET /api/site）——描述本来就展示给所有读者；
 * 写仅管理员（PUT /api/admin/site）。这里只有描述一项：
 * settings.json 里的其他字段（密码哈希、邮件 key 等）不属于「公开可见的文案」，
 * 永远不该出现在这个端点里。
 */
export interface SiteApi {
  /** 取站点描述；无需登录。空串表示站长未设置，前端应回退到内置文案。 */
  getSite(): Promise<SiteSettingsResponse>;
  /** 改站点描述；仅站长。返回保存后的最终值（服务端会折叠空白）。 */
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
        json: { description: input.description },
      });
    },
  };
}
