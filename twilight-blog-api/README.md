# twilight-blog-api

TypeScript/Node.js client for the [twilight-blog](../twilight-blog-api/api.md) HTTP API.

## Features

- Complete coverage of the documented twilight-blog API
- TypeScript types for requests and responses
- Single `createClient()` entrypoint
- Cookie-session aware: nothing to store, and the CSRF token is echoed automatically
- Descriptive `BlogApiError` for HTTP and network failures
- Supports both ESM and CommonJS consumers
- Uses native `fetch` (Node.js 18+)

## Install / Build

```bash
npm install
npm run build
```

The package can also be consumed directly from source in a TypeScript project, or from the built `dist/` output.

## Sessions

Authentication is a **cookie session**, so the client holds no token and there is
nothing to configure or persist:

- `blog.login(...)` and `blog.register(...)` make the
  server set `tw_session` (httpOnly) and `tw_csrf` (readable).
- Sign-in takes one shape — a username and a password — for both identities. The
  role comes back in the response (`role: 'admin' | 'user'`); the caller never
  declares it, because the server decides it from the username.
- On every unsafe request the client reads `tw_csrf` from `document.cookie` and
  echoes it in `X-CSRF-Token`, which is what the server's double-submit check
  expects.
- `credentials: 'same-origin'` is sent, so the API is expected to be served from
  the same origin as the page — the usual single-binary deployment.

In a browser all of this is automatic. In Node there is no cookie jar, so pass a
`fetch` that maintains cookies (for example `undici` with a cookie store) and
supply the CSRF header through it; the SDK's own cookie reader returns `null`
outside a DOM.

## Quick Start

```ts
import { createClient } from 'twilight-blog-api';

const blog = createClient({
  baseUrl: 'http://localhost:8080',
});

// Install on first run
const status = await blog.getInstallStatus();
if (!status.installed) {
  await blog.install({ password: 'my-secure-password' });
}

// Sign in; the response describes the session and its role
const me = await blog.login({ username: 'admin', password: 'my-secure-password' });

// Public API
const articles = await blog.listArticles({ page: 1, pageSize: 12 });
const article = await blog.getArticle(articles.items[0].id);

// Admin API
const created = await blog.createArticle({
  title: 'Hello',
  content: 'World',
  tags: ['hello'],
});

// Visitor accounts: email a code, then register (which signs the account in)
await blog.requestEmailCode({ email: 'user@example.com' });
await blog.register({
  email: 'user@example.com',
  code: '123456',
  password: 'my-secure-password',
  nickname: '夜行者',
});

await blog.createComment({
  targetType: 'article',
  targetId: created.id,
  content: 'Nice post!',
  nickname: '夜行者',
});

// Who am I?  `{ role: 'admin', name }` or `{ role: 'user', user }`
const identity = await blog.me();
```

## Error Handling

All non-2xx responses and network errors are thrown as `BlogApiError`.

```ts
import { BlogApiError, isBlogApiError } from 'twilight-blog-api';

try {
  await blog.getArticle(999999);
} catch (error) {
  if (isBlogApiError(error)) {
    console.error(error.status);   // HTTP status if available
    console.error(error.message);  // server error message
    console.error(error.method);
    console.error(error.path);
    console.error(error.body);     // parsed response body when JSON
  }
}
```

A `401` means the session is gone; call `blog.me()` again after the next sign-in
rather than caching the previous identity. A `403` means the session is valid but
the role is wrong, or the CSRF header was rejected.

## Media Upload

`uploadMedia` accepts file paths, `Buffer`, `Blob`, `File`, `ArrayBuffer`, or descriptor objects.

```ts
// Upload by path
await blog.uploadMedia('/path/to/photo.jpg');

// Upload multiple Buffers with explicit names
await blog.uploadMedia([
  { data: imageBuffer, name: 'cover.png', contentType: 'image/png' },
  { data: audioBuffer, name: 'audio.wav', contentType: 'audio/wav' },
]);
```

## API Coverage

The client exposes methods for:

- Installation: `getInstallStatus`, `install`
- Sessions: `login`, `requestEmailCode`, `register`, `resetPassword`, `logout`, `me`
- Own account: `updateNickname`, `changePassword`, `updateAdminProfile`
- Visitor accounts (administrator only): `listUsers`, `updateUser`
- Public articles, tags, moments, pages, search, comments, media
- Admin CRUD, batch delete/restore/permanent-delete, trash listing
- Admin email history: `listEmails`, `getEmail`
- Media upload/list/delete/download

See `src/types.ts` and the generated declarations for complete TypeScript types.
