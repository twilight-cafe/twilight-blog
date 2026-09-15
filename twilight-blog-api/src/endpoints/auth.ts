import type { HttpClient } from '../http';
import type {
  ChangePasswordInput,
  EmailCodeInput,
  LoginInput,
  MeResponse,
  MessageResponse,
  RegisterInput,
  ResetPasswordInput,
  UpdateAdminProfileInput,
  UpdateProfileInput,
  UserResponse,
} from '../types';

/**
 * 会话生命周期与账号自助接口。
 *
 * 这些接口都不在请求体或请求头里带 token：登录成功后由服务端下发
 * `tw_session`（httpOnly）与 `tw_csrf`（可读）两个 cookie，之后 HTTP 层会在每个
 * 非安全方法上回传 CSRF token。因此「是否已登录」完全看 cookie，本客户端不保存任何凭据。
 *
 * 命名约定：所有 username 都指**登录标识**，所有 nickname / name 都指**展示名**，
 * 两者在类型层就是不同字段，任何「顺手把昵称传进 username」的写法都算 bug。
 *
 * 登录只有一个方法：`login`。站长的登录名与访客的用户名共用一个命名空间，
 * 服务端据此判定身份，客户端无从（也无需）指定。
 */
export interface AuthApi {
  /** 登录（站长或访客，由用户名决定）。 */
  login(input: LoginInput): Promise<MeResponse>;
  /** 发验证码邮件，注册与重置密码共用。 */
  requestEmailCode(input: EmailCodeInput): Promise<MessageResponse>;
  /** 校验验证码、创建账号并自动登录。 */
  register(input: RegisterInput): Promise<MeResponse>;
  /** 用验证码设新密码，并撤销该账号的所有其它会话。 */
  resetPassword(input: ResetPasswordInput): Promise<MessageResponse>;
  /** 撤销当前会话并清除 cookie。 */
  logout(): Promise<void>;
  /** 取当前会话信息；未登录时以 401 拒绝（调用方据此判断要不要跳登录）。 */
  me(): Promise<MeResponse>;
  /** 改当前登录访客的资料（用户名和/或昵称），返回更新后的账号。 */
  updateProfile(input: UpdateProfileInput): Promise<UserResponse>;
  /** 改当前登录访客的密码，并轮换会话（旧 session 失效）。 */
  changePassword(input: ChangePasswordInput): Promise<void>;
  /** 改站长的展示名与（可选的）登录名。仅站长。 */
  updateAdminProfile(input: UpdateAdminProfileInput): Promise<MeResponse>;
}

export function createAuthApi(http: HttpClient): AuthApi {
  return {
    login(input: LoginInput) {
      // 此时还没有会话，服务端不会对登录接口做 CSRF 校验，因此这里不必带 token。
      // 用户名原样发出去：服务端发现它是邮箱形态时会自动按邮箱回退查找
      // （兼容 username 列出现之前注册的老账号）。
      return http.request<MeResponse>({
        method: 'POST',
        path: '/api/auth/login',
        json: { username: input.username, password: input.password },
      });
    },

    requestEmailCode(input: EmailCodeInput) {
      return http.request<MessageResponse>({
        method: 'POST',
        path: '/api/auth/email-code',
        json: { email: input.email },
      });
    },

    register(input: RegisterInput) {
      return http.request<MeResponse>({
        method: 'POST',
        path: '/api/auth/register',
        json: {
          email: input.email,
          code: input.code,
          username: input.username,
          nickname: input.nickname,
          password: input.password,
        },
      });
    },

    resetPassword(input: ResetPasswordInput) {
      return http.request<MessageResponse>({
        method: 'POST',
        path: '/api/auth/reset-password',
        json: { email: input.email, code: input.code, password: input.password },
      });
    },

    logout() {
      return http.request<void>({
        method: 'POST',
        path: '/api/auth/logout',
      });
    },

    me() {
      return http.request<MeResponse>({
        path: '/api/me',
      });
    },

    updateProfile(input: UpdateProfileInput) {
      // 未传的字段不进请求体：服务端用「键是否存在」区分「不改」与「清空」，
      // 客户端这边 undefined 即省略，二者语义对齐。
      const json: Record<string, string> = {};
      if (input.username !== undefined) json.username = input.username;
      if (input.nickname !== undefined) json.nickname = input.nickname;
      return http.request<UserResponse>({
        method: 'PATCH',
        path: '/api/me',
        json,
      });
    },

    changePassword(input: ChangePasswordInput) {
      return http.request<void>({
        method: 'PUT',
        path: '/api/me/password',
        json: {
          current_password: input.currentPassword,
          new_password: input.newPassword,
        },
      });
    },

    updateAdminProfile(input: UpdateAdminProfileInput) {
      // username 不传即保持不变：改登录名是影响下次登录方式的操作，
      // 不能被「碰巧没带这个键」误触发。
      const json: Record<string, string> = { name: input.name };
      if (input.username !== undefined) json.username = input.username;
      return http.request<MeResponse>({
        method: 'PUT',
        path: '/api/admin/profile',
        json,
      });
    },
  };
}
