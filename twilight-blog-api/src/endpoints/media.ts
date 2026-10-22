import type { HttpClient } from '../http';
import type {
  MediaContent,
  MediaInput,
  MediaInputDescriptor,
  MediaItem,
  MediaSource,
  Paginated,
  PaginationOptions,
  RenameMediaInput,
} from '../types';
import { resolvePagination } from './pagination';

// 媒体接口。上传/列表/删除/改名走 /api/admin（需登录）；getMedia 走公开 /api/media，
// 直接返回原始字节（不是 JSON），用于下载或预览。上传用 multipart/form-data，
// 字段名固定为 'files'。
export interface MediaApi {
  uploadMedia(files: MediaInput | MediaInput[]): Promise<MediaItem[]>;
  getMedia(id: number): Promise<MediaContent>;
  listMedia(options?: PaginationOptions): Promise<Paginated<MediaItem>>;
  deleteMedia(id: number): Promise<void>;
  /**
   * 改媒体的展示名（仅站长）。只影响界面/alt/灯箱里显示的名字，磁盘文件名与
   * `/api/media/{id}` 地址都不变，所有引用（头图、微语配图、头像）自动继续有效。
   */
  renameMedia(id: number, input: RenameMediaInput): Promise<MediaItem>;
}

/**
 * 把媒体 id 拼成可以直接放进 `<img src>` 的**站内相对**地址。
 *
 * 为什么是相对路径而不是绝对 URL：前端与 API 同源（生产环境 Go 直接内嵌 SPA，
 * 开发环境由 vite 代理 /api），相对路径在两种环境下都对。写死 origin 会在
 * 反向代理、多域名部署或 vite 代理下失效，而症状是「图片全裂」，很难指向这里。
 *
 * 存在的意义是消灭重复的魔法字符串：头图在首页卡片与文章详情页两处渲染，
 * 两处各写一遍 `/api/media/${id}` 就有漂移的可能，而漂移的表现是其中一处裂图。
 * 服务端拼同一个地址的地方是 viewmodel.NewMediaResponse。
 */
export function mediaUrl(id: number): string {
  return `/api/media/${id}`;
}

const mimeByExtension: Record<string, string> = {
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.png': 'image/png',
  '.gif': 'image/gif',
  '.bmp': 'image/bmp',
  '.tif': 'image/tiff',
  '.tiff': 'image/tiff',
  '.webp': 'image/webp',
  '.mp3': 'audio/mpeg',
  '.wav': 'audio/wav',
  '.ogg': 'audio/ogg',
  '.oga': 'audio/ogg',
  '.opus': 'audio/ogg',
  '.flac': 'audio/flac',
  '.m4a': 'audio/mp4',
  '.aac': 'audio/aac',
  '.mp4': 'video/mp4',
  '.m4v': 'video/mp4',
  '.mov': 'video/quicktime',
  '.qt': 'video/quicktime',
  '.avi': 'video/x-msvideo',
  '.mkv': 'video/x-matroska',
  '.webm': 'video/webm',
  '.ogv': 'video/ogg',
  '.wmv': 'video/x-ms-wmv',
  '.flv': 'video/x-flv',
  '.mpeg': 'video/mpeg',
  '.mpg': 'video/mpeg',
  '.m2v': 'video/mpeg',
  '.ts': 'video/mp2t',
  '.mts': 'video/mp2t',
  '.m2ts': 'video/mp2t',
  '.3gp': 'video/3gpp',
  '.3g2': 'video/3gpp2',
};

// 本地路径辅助函数。这里不直接 import node:path，否则浏览器打包也会被要求提供 shim；
// 浏览器用不到这些函数（不会传文件路径字符串），所以把所需的两个实现内联。
function basename(path: string): string {
  const normalized = path.replace(/[\\/]+$/, '');
  const index = Math.max(normalized.lastIndexOf('/'), normalized.lastIndexOf('\\'));
  return index >= 0 ? normalized.slice(index + 1) : normalized;
}

function extname(path: string): string {
  const base = basename(path);
  const index = base.lastIndexOf('.');
  return index > 0 ? base.slice(index) : '';
}

/**
 * 从磁盘读文件。只在 Node 下有意义，因此用动态 import 延迟加载 node:fs/promises：
 * 面向浏览器的打包产物不会真正引入它，除非调用方真的传了文件路径字符串。
 */
async function readFileFromPath(path: string): Promise<Uint8Array> {
  const fs = await import('node:fs/promises');
  const contents = await fs.readFile(path);
  return new Uint8Array(contents);
}

function inferContentType(filename: string): string | undefined {
  return mimeByExtension[extname(filename).toLowerCase()];
}

function isDescriptor(value: unknown): value is MediaInputDescriptor {
  return typeof value === 'object' && value !== null && 'data' in value;
}

async function normalizeMediaInput(input: MediaInput, index: number): Promise<{ blob: Blob; filename: string }> {
  if (typeof input === 'string') {
    return toBlob(await readFileFromPath(input), basename(input), undefined, index);
  }

  if (input instanceof Blob) {
    return toBlob(input, undefined, undefined, index);
  }

  if (ArrayBuffer.isView(input) || input instanceof ArrayBuffer) {
    return toBlob(input, undefined, undefined, index);
  }

  if (isDescriptor(input)) {
    const name = input.name ?? (typeof input.data === 'string' ? basename(input.data) : undefined);
    return toBlob(input.data, name, input.contentType, index);
  }

  throw new TypeError(`Unsupported media input at index ${index}`);
}

async function toBlob(
  data: MediaSource,
  nameHint: string | undefined,
  contentType: string | undefined,
  index: number,
): Promise<{ blob: Blob; filename: string }> {
  if (typeof data === 'string') {
    const filename = nameHint ?? basename(data);
    const contents = await readFileFromPath(data);
    const type = contentType ?? inferContentType(filename);
    // TS 把 typed array 建模为对其底层 buffer 泛型化的类型，导致 Uint8Array 不能直接当 BlobPart；
    // 这里的值其实就是普通字节视图，做一次断言即可。
    return { blob: new Blob([contents as unknown as BlobPart], type ? { type } : undefined), filename };
  }

  if (data instanceof Blob) {
    const filename = nameHint ?? (data as { name?: string }).name ?? `file-${index}`;
    const type = contentType ?? (data.type || undefined);
    if (type && data.type !== type) {
      return { blob: new Blob([data], { type }), filename };
    }
    return { blob: data, filename };
  }

  const filename = nameHint ?? `file-${index}`;
  const type = contentType ?? inferContentType(filename);
  return { blob: new Blob([data as BlobPart], type ? { type } : undefined), filename };
}

export function createMediaApi(http: HttpClient): MediaApi {
  return {
    async uploadMedia(files: MediaInput | MediaInput[]) {
      const inputs = Array.isArray(files) ? files : [files];
      const formData = new FormData();

      for (let i = 0; i < inputs.length; i += 1) {
        const { blob, filename } = await normalizeMediaInput(inputs[i], i);
        // 字段名固定为 'files'，多个文件同名追加（服务端按数组接收）。
        formData.append('files', blob, filename);
      }

      return http.request<MediaItem[]>({
        method: 'POST',
        path: '/api/admin/media',
        formData,
      });
    },

    async getMedia(id: number) {
      // 用 requestRaw 而不是 request：响应体是二进制而非 JSON，request 会因无法解析 JSON 而报错。
      const path = `/api/media/${id}`;
      const response = await http.requestRaw({ path });
      const data = new Uint8Array(await response.arrayBuffer());
      const contentType = response.headers.get('content-type') ?? 'application/octet-stream';

      return {
        data,
        contentType,
        url: `${http.getBaseUrl()}${path}`,
      };
    },

    listMedia(options = {}) {
      const pagination = resolvePagination(options);
      return http.request<Paginated<MediaItem>>({
        path: '/api/admin/media',
        query: pagination,
      });
    },

    deleteMedia(id: number) {
      return http.request<void>({
        method: 'DELETE',
        path: `/api/admin/media/${id}`,
      });
    },

    renameMedia(id: number, input: RenameMediaInput) {
      return http.request<MediaItem>({
        // PATCH 而不是 PUT：只改展示名一个字段，媒体本体（文件、类型、大小）不动。
        method: 'PATCH',
        path: `/api/admin/media/${id}`,
        // 逐个列出字段，不写成 `{ ...input }`：契约里「调用方没提这件事」与
        // 「调用方明确要求这个值」是两件事，多透一个键就等于替调用方做了决定。
        json: { original_name: input.original_name },
      });
    },
  };
}
