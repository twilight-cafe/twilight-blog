// 统一错误载体。所有非 2xx、网络异常、JSON 解析失败都被归一化成这一个类，
// 调用方只需 try/catch 这一种错误即可，靠 status 区分业务含义。
export interface BlogApiErrorOptions {
  // HTTP 状态码；网络层错误（超时/断网）时可能为空。
  status?: number;
  method: string;
  path: string;
  // 服务端返回的原始错误体（已尽量取出 message），用于精细判断或调试。
  body?: unknown;
  // 底层异常（如 AbortError、fetch 抛出的网络错误），便于排查根因。
  cause?: unknown;
}

export class BlogApiError extends Error {
  // 出错请求的 HTTP 状态；网络层错误时为 undefined。
  readonly status?: number;
  readonly method: string;
  readonly path: string;
  readonly body: unknown;

  constructor(message: string, options: BlogApiErrorOptions) {
    super(message);
    this.name = 'BlogApiError';
    this.status = options.status;
    this.method = options.method;
    this.path = options.path;
    this.body = options.body;

    if (options.cause !== undefined) {
      (this as Error & { cause?: unknown }).cause = options.cause;
    }
  }
}

// 类型守卫：catch 里用 `if (isBlogApiError(e))` 收窄，再读 e.status 做分支处理。
export function isBlogApiError(error: unknown): error is BlogApiError {
  return error instanceof BlogApiError;
}
