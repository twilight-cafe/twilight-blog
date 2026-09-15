import { BlogApiError } from './errors';

// 拼到 URL 上的查询参数值。注意 null/undefined 会被 buildUrl 直接跳过（不出现在查询串里）。
export type QueryParams = Record<string, string | number | boolean | null | undefined>;

/**
 * JSON 请求的默认超时（毫秒）。若不设上限，连接一旦卡死会让 Promise 永远
 * pending，调用方无法 abort、也无法重试。
 */
export const DEFAULT_REQUEST_TIMEOUT_MS = 30_000;

/**
 * 上传（multipart）的默认超时（毫秒）。
 *
 * 上传此前刻意设为 0（不超时），理由是大文件慢速传输不该在半途被中断。但
 * 「不中断」不等于「永不放弃」：服务端 accept 之后若不返回响应，`uploadMedia()`
 * 会永久 pending，界面一直卡在「上传中」，既没有错误提示也无法重试
 * （第三轮渗透测试 R3-16c）。
 *
 * 取 5 分钟：与服务端的 `Server.ReadTimeout`（同样是 5 分钟）对齐——
 * 客户端放弃的时刻正是服务端也会放弃的时刻，不会出现「客户端还在等、
 * 服务端早已断开」的空转。需要更长的部署可以显式传 timeoutMs 覆盖。
 */
export const DEFAULT_UPLOAD_TIMEOUT_MS = 5 * 60_000;

/** 存放 double-submit CSRF token 的「可读」cookie 名（非 httpOnly，脚本才读得到）。 */
export const CSRF_COOKIE_NAME = 'tw_csrf';

/** 必须回传 {@link CSRF_COOKIE_NAME} 里 token 的请求头名称。 */
export const CSRF_HEADER_NAME = 'X-CSRF-Token';

export interface HttpRequestOptions {
  method?: string;
  path: string;
  query?: QueryParams;
  json?: unknown;
  formData?: FormData;
  /**
   * 单次请求超时（毫秒）。上传（formData）默认不超时，避免大文件慢速传输在半途被中断。
   */
  timeoutMs?: number;
}

// 判断普通对象（排除 null 与数组）。错误体用普通对象承载 message/error 字段，数组要排除。
function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

// 仅靠 name 字符串判断是否为中止错误：AbortSignal.timeout 抛出的就是标准的 AbortError。
function isAbortError(error: unknown): boolean {
  return (
    typeof error === 'object' &&
    error !== null &&
    'name' in error &&
    (error as { name?: unknown }).name === 'AbortError'
  );
}

/**
 * 会改变状态的方法需要附带 CSRF token。GET/HEAD/OPTIONS 属于安全方法：
 * 浏览器不会把它们变成跨站变更请求，因此无需校验。
 */
function isUnsafeMethod(method: string): boolean {
  switch (method.toUpperCase()) {
    case 'POST':
    case 'PUT':
    case 'PATCH':
    case 'DELETE':
      return true;
    default:
      return false;
  }
}

/**
 * 构造一个到点即中止的 AbortSignal。
 *
 * 优先用 `AbortSignal.timeout`（语义最清晰，且中止原因是标准的 AbortError）。
 * 但它并非处处可用：老一些的运行时没有这个静态方法，而**原先的实现遇到这种情况
 * 会静默返回 undefined**——超时保护就此消失，请求与不设超时无异，且没有任何迹象
 * 表明它没有生效（第三轮渗透测试 R3-16c 的附带项）。
 *
 * 因此这里补一条基于 AbortController 的回退路径。中止时抛出的仍是名为
 * AbortError 的错误，isAbortError 判断与错误文案都不受影响。
 */
function buildTimeoutSignal(timeoutMs: number): AbortSignal | undefined {
  if (timeoutMs <= 0 || typeof AbortSignal === 'undefined') {
    return undefined;
  }
  if ('timeout' in AbortSignal) {
    return AbortSignal.timeout(timeoutMs);
  }
  if (typeof AbortController === 'undefined') {
    return undefined;
  }

  const controller = new AbortController();
  const timer = setTimeout(() => {
    controller.abort(new DOMException(`Timed out after ${timeoutMs}ms`, 'AbortError'));
  }, timeoutMs);
  // Node 下不要让这个定时器拖住事件循环；浏览器里 clearTimeout 会随请求结束执行。
  if (typeof timer === 'object' && timer !== null && 'unref' in timer) {
    (timer as { unref: () => void }).unref();
  }
  return controller.signal;
}

/**
 * 从 `document.cookie` 按名称读取 cookie。
 *
 * 非浏览器环境（Node、无 DOM 的 worker）或 cookie 不存在时返回 `null`，
 * 此时直接省略 CSRF 头而非抛错。做环境判断是因为本模块也会被 SDK 的 Node 调用方加载。
 */
export function readCookie(name: string): string | null {
  if (typeof document === 'undefined' || typeof document.cookie !== 'string') {
    return null;
  }

  for (const part of document.cookie.split(';')) {
    const separator = part.indexOf('=');
    if (separator < 0) continue;
    if (part.slice(0, separator).trim() !== name) continue;
    return part.slice(separator + 1).trim();
  }

  return null;
}

/** 读取服务端向本源放开可读的 CSRF token，没有则返回 null。 */
export function readCsrfToken(): string | null {
  const value = readCookie(CSRF_COOKIE_NAME);
  return value ? value : null;
}

/**
 * 底层 HTTP 客户端：负责拼 URL、带 CSRF 头、设置超时，并把所有非 2xx 响应
 * 以及网络/解析失败统一归一化成 `BlogApiError`。鉴权完全由浏览器托管的
 * session cookie 承担，本类不持有任何 token。
 */
export class HttpClient {
  private readonly baseUrl: string;
  private readonly fetchFn: typeof fetch;

  constructor(baseUrl: string, options: { fetch?: typeof fetch } = {}) {
    if (!/^https?:\/\//i.test(baseUrl)) {
      throw new Error('baseUrl must be an absolute http(s) URL');
    }
    this.baseUrl = baseUrl.replace(/\/+$/, '');
    this.fetchFn = options.fetch ?? fetch;
  }

  getBaseUrl(): string {
    return this.baseUrl;
  }

  // 便捷方法：发请求并把 2xx 响应体解析成 T（内部已经过 createErrorResponse 归一化错误）。
  async request<T>(options: HttpRequestOptions): Promise<T> {
    const response = await this.requestRaw(options);
    return this.parseResponse<T>(response, options.method ?? 'GET', options.path);
  }

  // 发出请求并返回原始 Response（不解析 body）。需要读取响应头或二进制流时（如媒体下载）
  // 直接用它；非 2xx 仍会在此处抛出 BlogApiError。
  async requestRaw(options: HttpRequestOptions): Promise<Response> {
    const method = options.method ?? 'GET';
    const url = this.buildUrl(options.path, options.query);
    const headers = new Headers();

    // 鉴权由 httpOnly 的 session cookie 承担，浏览器会自动带上；
    // 脚本唯一额外补充的是「非安全方法」所需的 CSRF token。没有 session 时服务端会直接忽略它。
    if (isUnsafeMethod(method)) {
      const csrfToken = readCsrfToken();
      if (csrfToken) {
        headers.set(CSRF_HEADER_NAME, csrfToken);
      }
    }

    let body: BodyInit | undefined;
    if (options.formData) {
      body = options.formData;
    } else if (options.json !== undefined) {
      headers.set('Content-Type', 'application/json');
      body = JSON.stringify(options.json);
    }

    // 上传用更宽但仍然有限的超时，不再让 Promise 有机会永不 settle。
    const timeoutMs = options.timeoutMs ?? (options.formData ? DEFAULT_UPLOAD_TIMEOUT_MS : DEFAULT_REQUEST_TIMEOUT_MS);
    const signal = buildTimeoutSignal(timeoutMs);

    let response: Response;
    try {
      response = await this.fetchFn.call(globalThis, url, {
        method,
        headers,
        body,
        signal,
        // 接口与 SPA 同源部署，因此每次请求都会带上 session cookie，无需开启跨站 credentials。
        credentials: 'same-origin',
      });
    // 超时通过 AbortSignal.timeout 触发，会抛 AbortError；这里把它转成带路径信息的 BlogApiError。
    } catch (cause) {
      if (isAbortError(cause) && timeoutMs > 0) {
        throw new BlogApiError(
          `Request to ${method} ${options.path} timed out after ${timeoutMs}ms`,
          { method, path: options.path, cause },
        );
      }
      const detail = cause instanceof Error ? cause.message : String(cause);
      throw new BlogApiError(`Network error while calling ${method} ${options.path}: ${detail}`, {
        method,
        path: options.path,
        cause,
      });
    }

    if (!response.ok) {
      throw await this.createErrorResponse(response, method, options.path);
    }

    return response;
  }

  // 把 2xx 响应体解析成 T。204/205 以及空正文都视为「无内容」，返回 undefined。
  private async parseResponse<T>(response: Response, method: string, path: string): Promise<T> {
    if (response.status === 204 || response.status === 205) {
      return undefined as T;
    }

    const text = await response.text();
    if (!text) {
      return undefined as T;
    }

    try {
      return JSON.parse(text) as T;
    } catch {
      // 早期实现会把原始文本当 T 返回，从而把错误藏起来：调用方会对字符串取属性
      // （如 `result.items`），在很远处才抛出令人困惑的 TypeError。成功的响应却不是
      // JSON，说明应答的不是本接口（多半是代理或静态页），必须显式报错。
      throw new BlogApiError(
        `Expected JSON from ${response.url || 'the API'} but received ${
          response.headers.get('content-type') ?? 'an unknown content type'
        }`,
        {
          status: response.status,
          method,
          path,
          body: text.slice(0, 500),
        },
      );
    }
  }

  // 非 2xx 时调用：尽量从错误体里取出可读的 message（优先 message 字段，其次 error 字段），
  // 并保留原始 body，方便上层按状态码分支处理。
  //
  // **5xx 例外**：5xx 的错误体不是「给用户看的文案」，而是「服务端没能处理这个请求」。
  // 它的内容不受本站控制——反向代理、网关、panic 处理器都可能往里写东西（堆栈、
  // SQL 片段、内部路径）。此前这类正文会被原样提升成 message，再由前端的
  // getErrorMessage 显示给用户（第三轮渗透测试 R3-16a）。
  // 这里只把它降级成一句固定文案，原文仍完整保留在 body 里供排查与上报。
  private async createErrorResponse(response: Response, method: string, path: string): Promise<BlogApiError> {
    const text = await response.text().catch(() => '');
    let body: unknown = text;
    let message = text || response.statusText || 'Request failed';

    try {
      body = JSON.parse(text);
      if (isPlainObject(body) && typeof body.message === 'string') {
        message = body.message;
      } else if (isPlainObject(body) && typeof body.error === 'string') {
        message = body.error;
      }
    } catch {
      // 非 JSON 的错误体：保留原始文本作为错误信息。
    }

    if (response.status >= 500) {
      message = `The server failed to handle ${method} ${path} (HTTP ${response.status})`;
    }

    return new BlogApiError(message, {
      status: response.status,
      method,
      path,
      body,
    });
  }

  // 用 baseUrl + path 构造绝对 URL，并把查询参数编码进 search。null/undefined 直接跳过，
  // 因此「未提供」与「传空串」不同：前者不出现于查询串，后者会生成 key=。
  private buildUrl(path: string, query?: QueryParams): string {
    const url = new URL(this.baseUrl + path);
    if (query) {
      for (const [key, value] of Object.entries(query)) {
        if (value === undefined || value === null) {
          continue;
        }
        url.searchParams.set(key, String(value));
      }
    }
    return url.toString();
  }
}
