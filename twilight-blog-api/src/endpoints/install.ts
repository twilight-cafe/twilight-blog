import type { HttpClient } from '../http';
import type { InstallInput, InstallStatus } from '../types';

// 安装相关接口。注意这两个路径不带 /api 前缀，且无需登录——是初始化前唯一能访问的端点。
// install 是「一次性」操作：仅在 installed=false 时有效；已安装后再调用应被服务端拒绝。
export interface InstallApi {
  getInstallStatus(): Promise<InstallStatus>;
  install(input: InstallInput): Promise<InstallStatus>;
}

export function createInstallApi(http: HttpClient): InstallApi {
  return {
    getInstallStatus() {
      return http.request<InstallStatus>({
        path: '/install_status',
      });
    },
    install(input: InstallInput) {
      // 只需管理员密码；成功后服务端会建立首个账号与配置，并置 installed=true。
      return http.request<InstallStatus>({
        method: 'POST',
        path: '/install',
        json: { password: input.password },
      });
    },
  };
}
