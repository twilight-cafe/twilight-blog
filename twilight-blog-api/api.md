# Blog Engine API Documentation

This document describes the HTTP endpoints exposed by the blog engine server and the executable's test-email command.

## Base URL

The server listens on `:8080` by default:

```
http://localhost:8080
```

The listen host and port can be configured in `settings.json` using `server_name` and `port`. If omitted, the server uses all interfaces and port `8080`.

Logs are written to `log.txt` by default, including web request logs. Logging verbosity can be configured with `log_level`. Supported values are `dev`, `normal`, `warn`, and `error`. The default is `normal`.

The maximum upload size can be configured with `max_upload_size` in megabytes. If omitted, the default is `50`.

Email is sent through the Resend HTTP API. Resend authenticates the request with an API key and relays the message to the recipient.

Mail settings are configured under `mail` in `settings.json`:

```json
{
  "mail": {
    "from": "onboarding@resend.dev",
    "api_key": "re_..."
  }
}
```

- `from` — sender address. It must be an address allowed by your Resend account, usually on a domain you verified in Resend.
- `api_key` — Resend API key. If it is empty, email sending is disabled.

Other `settings.json` keys referenced by this document:

| Key | Meaning |
|---|---|
| `admin_username` | Administrator **login name**. Defaults to `admin`. Changed with `PUT /api/admin/profile`. |
| `admin_nickname` | Administrator **display name** shown to readers and used as the comment signature. Changed with `PUT /api/admin/profile`. |
| `site_description` | Site copy shown on the home page, in the footer and in page metadata. Changed with `PUT /api/admin/site`. |
| `trusted_proxies` | CIDR list of reverse proxies whose forwarding headers are believed. See [Rate limiting](#rate-limiting). |
| `rate_limit` | Per-IP allowances. See [Rate limiting](#rate-limiting). |

Administrator identity used to live in a single `admin_name` key. On load, a
non-empty `admin_name` is migrated into `admin_username` and `admin_nickname` and
the old key is cleared, so an upgraded installation keeps signing in the same way.

## Test Email Command

To send a test email through Resend without starting the HTTP server, run:

```text
<executable> email test -target recipient@example.com
```

Example:

```text
blog-engine.exe email test -target abc@def.com
```

This sends a test email through Resend and stores the email in the database after a successful send.

All request and response bodies are JSON unless stated otherwise. The server enforces a request body limit of `50M` by default.

## Authentication

Every authenticated request is carried by a **cookie session**. No endpoint
returns a token in its body, and a client never stores one: the browser holds the
session and attaches it automatically.

### 1. Roles

Two roles share one session mechanism.

| Role | How to obtain a session | What it may do |
|---|---|---|
| `admin` | `POST /api/auth/login` with the configured administrator **username** and password | Everything under `/api/admin/*`, plus commenting (signed with the configured administrator **nickname**) |
| `user` | `POST /api/auth/login` with a visitor username and password (an email is accepted as a fallback), or `POST /api/auth/register` | Comment, like/unlike, and edit its own profile |

#### One sign-in endpoint, two roles

`POST /api/auth/login` is the only sign-in path, and it takes exactly one shape: a
username and a password. The role is decided by the **username** alone — when it is
the configured administrator login name the caller becomes the administrator,
otherwise it is looked up among visitor accounts. There is no request field in
which a caller declares an identity, because a caller cannot know better than the
server here: both identities use the same kind of credentials, and a wrong guess
would produce the same `401` a wrong password produces.

That is why usernames form a **single namespace**. An administrator login name and
a visitor username may never collide, and every write path refuses a collision:

| Endpoint | Refuses |
|---|---|
| `POST /api/auth/register` | the administrator's login name |
| `PATCH /api/me` (visitor self-service) | the administrator's login name |
| `PUT /api/admin/users/:id` | the administrator's login name |
| `PUT /api/admin/profile` (administrator) | any name a visitor account already owns |

All comparisons are case-insensitive, matching the `COLLATE NOCASE` used by the
`users` table. A collision would make sign-in ambiguous and leave one of the two
identities unable to sign in, so it is rejected at every entry point rather than
resolved at sign-in time.

`POST /api/auth/user/login` is a **deprecated alias** of `POST /api/auth/login`: same
body, same handler, same rate-limit bucket. Use `POST /api/auth/login`.

#### Username and nickname are two different fields

Every identity has both, and they are never interchangeable:

| Field | Purpose | Where it is set |
|---|---|---|
| `username` | **Sign-in identifier.** Unique across the whole site — the administrator's login name included — 3–24 characters, must start with a letter and may otherwise contain letters, digits, `_` and `-`. Compared case-insensitively. | `POST /api/auth/register`, `PATCH /api/me`, `PUT /api/admin/users/:id` (visitors); installer default `admin`, `PUT /api/admin/profile` (administrator) |
| `nickname` | **Public display name.** What every other reader sees as the comment signature. Never used to authenticate. | Same endpoints as `username` |

For the administrator these are `admin_username` and `admin_nickname` in
`settings.json`; for visitor accounts they are the `username` and `nickname`
columns of the `users` table. Changing a nickname never changes how anyone signs
in, and changing a username never changes what anyone is called on the site.
`GET /api/me` therefore reports both values separately.

`GET /api/me` describes the current session; it is how a client learns which role
it holds and whether it is signed in at all (a signed-out caller gets `401`).

```json
{
  "role": "admin",
  "name": "Twilight",
  "username": "admin"
}
```

```json
{
  "role": "user",
  "user": {
    "id": 3,
    "username": "nightwalker",
    "nickname": "夜行者",
    "email": "user@example.com",
    "has_password": true,
    "created_at": 1700000000,
    "updated_at": 1700000000
  }
}
```

A visitor account edits itself through `PATCH /api/me` (`username` and/or
`nickname`) and `PUT /api/me/password` (password). The email is a verified contact
address — it is **not** used for sign-in and can only be corrected by an
administrator.

### 2. Cookies

Sign-in, registration and password change all set the same pair of cookies:

| Cookie | httpOnly | Purpose |
|---|---|---|
| `tw_session` | yes | The session. Only its SHA-256 is stored server-side; scripts cannot read it. |
| `tw_csrf` | no | Double-submit CSRF token. Readable on purpose, so the SPA can echo it in a header. |

Both are `Path=/` and `SameSite=Lax`. `Secure` is added automatically when the
request arrived over HTTPS — `X-Forwarded-Proto` is honoured only when the peer is
a trusted proxy — and `cookie_secure` in `settings.json` forces it to `always` or
`never`.

Administrator sessions live 24 hours and visitor sessions 30 days. Expired rows
are pruned periodically. `POST /api/auth/logout` revokes the session server-side
and clears both cookies; deleting them in the browser alone does not revoke it.

### 3. CSRF

Every unsafe request (`POST`, `PUT`, `PATCH`, `DELETE`) that carries a session must
echo the `tw_csrf` cookie in a header:

```
X-CSRF-Token: <value of the tw_csrf cookie>
```

Requests without a session are exempt: there is no ambient authority for an
attacker to ride on, and requiring a token before sign-in would break sign-in
itself. A missing or mismatched token is `403 Forbidden`.

The header is the **only** accepted source. The cookie itself is never accepted as
a fallback, because browsers attach it automatically and that would compare the
cookie with itself — a check that always passes.

### 4. Author identity and privacy

A comment is always signed with the **account that posted it** — there is no
author field in the create request to override it:

- A visitor comment is signed with that account's `nickname`.
- An administrator comment is signed with the configured administrator
  `admin_nickname`.

Responses distinguish the two by shape: visitor authors carry `nickname`,
administrator authors carry `name`.

**Admin-authenticated** responses additionally include the visitor author's
`email`. Anonymous responses and other visitors never do, so visitor addresses
cannot be harvested from the public endpoints.

## Rate limiting

The following endpoints are throttled per client IP. Exceeding an allowance
returns `429 Too Many Requests` with a `Retry-After` header.

| Endpoint | Default allowance |
|---|---|
| `POST /api/auth/login` (and its alias `POST /api/auth/user/login`) | 3 / minute, shared bucket |
| `POST /api/auth/email-code` | 3 / minute and 10 / hour, **plus 10 / hour per recipient address, with a 60-second minimum interval between two sends to the same address** |
| `POST /api/auth/register`, `POST /api/auth/reset-password` | 10 / minute |
| `POST /api/comments`, `POST`/`DELETE /api/comments/:id/like` | 5 / minute |
| `POST /install` | 5 / minute |

Both sign-in URLs share one per-IP bucket, so switching URL does not buy extra
attempts.

`POST /api/auth/email-code` is the only unauthenticated endpoint that makes the
server send mail somewhere, so it carries a second, independent limit on the
**recipient address** (10 codes per hour). A per-IP limit alone is defeated by
switching source IP, whereas a victim has only one inbox. Counting is by codes
issued, not codes still valid, so letting an old code expire does not restore the
allowance.

On top of that hourly allowance, the same address may not be sent a second code
within **60 seconds**. The hourly cap alone still permits a burst — ten codes in
one second is within the allowance but is both inbox harassment and a way to
compress the quota into a single spike. The interval is enforced on the server,
so calling the endpoint directly (bypassing the client-side countdown) does not
get around it. A request refused by this interval is a `429` (`Retry-After` set)
and, importantly, **does not invalidate the code the recipient already holds**;
the same applies to the address that sent case-varied (`A@x.com` vs `a@x.com`),
which counts as one inbox.

Allowances are configurable in `settings.json` under `rate_limit`. Neither the
per-address hourly limit nor the 60-second interval is configurable; they are
fixed guards against using the blog as a mail cannon. Throttling is **on** unless
`rate_limit.enabled` is explicitly set to `false`.

### How the client IP is determined

The client IP is taken from the network peer address. Forwarding headers are only
consulted when that peer is listed in `trusted_proxies`, which defaults to
`127.0.0.1/32` and `::1/128` — correct for the usual nginx-on-the-same-host
deployment. Behind a proxy on another host, add its CIDR range; otherwise the
forwarded header is ignored and every visitor shares the proxy's address.

When a peer is trusted, the address is resolved **from the right**: the last entry
of `X-Forwarded-For` is skipped while it belongs to a trusted proxy, and the first
entry that is not trusted is the client. Everything to the left of it is
client-supplied and is never used. `X-Real-IP` is not consulted at all.

This matters because the common nginx recipe appends rather than replaces:

```nginx
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
```

With that directive a request arrives carrying `<attacker-supplied>, <real peer>`,
and only the right-hand rule produces the real peer. If the blog engine ever
resolved the leftmost value instead, a single forged header would hand out a fresh
rate-limit bucket per request, disabling every limit in the table above.

## Common Error Format

Errors are returned as JSON:

```json
{
  "message": "error description"
}
```

Common HTTP status codes used by this API:

| Status | Meaning |
|---|---|
| `200 OK` | Successful read/update/login/install |
| `201 Created` | Resource successfully created |
| `204 No Content` | Successful delete/restore/batch operation without response body |
| `400 Bad Request` | Invalid request body, path parameter, or query parameter |
| `401 Unauthorized` | No session, or an expired/revoked one |
| `403 Forbidden` | Signed in with the wrong role, a missing or mismatched CSRF token, or a state-changing request whose `Origin` is not this site |
| `404 Not Found` | Resource does not exist |
| `409 Conflict` | Conflict with current state, e.g. already installed, duplicate tag name, or an email address another account already uses |
| `429 Too Many Requests` | Per-IP rate limit exceeded; see `Retry-After` |
| `500 Internal Server Error` | Server-side failure |
| `503 Service Unavailable` | Email sending is disabled because `mail.api_key` or `mail.from` is not configured |

## Data Types and Conventions

- IDs are 64-bit integers (`int64`).
- Timestamps are Unix seconds (`int64`).
- `deleted_at` is omitted when the resource is not soft-deleted.
- `parent_id` is omitted when it is `null`.
- List responses are always paginated JSON objects. Every list endpoint requires `page` and `page_size` query parameters, and `page_size` must not exceed `12`.

---

## Installation Endpoints

### `GET /install_status`

Returns whether the server is installed.

**Authentication:** none

**Response `200 OK`**

```json
{
  "installed": false
}
```

When installed:

```json
{
  "installed": true
}
```

### `POST /install`

Sets the admin password on first run.

This endpoint is only available while the server is not installed. The password is hashed using bcrypt and stored in `settings.json` as `password_hash`; the plaintext password is never stored.

**Password policy (enforced server-side):**

- at least 8 characters,
- at most 72 bytes (bcrypt ignores anything beyond 72 bytes),
- at least one letter and one digit.

**Authentication:** none

**Request body**

```json
{
  "password": "your-strong-password"
}
```

**Response `200 OK`**

```json
{
  "installed": true
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Password is missing, too short, too long, or lacks a letter/digit |
| `409 Conflict` | Server is already installed |

---

## Sessions and Accounts

### `POST /api/auth/login`

Signs **either** identity in. The role is determined by `username`: when it equals
the administrator's login name (`admin_username`) the request authenticates the
administrator, otherwise it is resolved against visitor accounts. There is no field
in which the caller states a role.

The administrator's login name is compared in constant time, so a wrong username
and a wrong password are indistinguishable; visitor lookups answer an unknown
account and a wrong password with the same `401` in the same amount of time, so the
endpoint cannot be used to discover which accounts exist. The username compared for
the administrator is the **login name**, never the display name.

If the administrator's login name is somehow also held by a visitor account — data
predating the rule below — the administrator wins, and that visitor account signs in
with its registered email address instead.

**Authentication:** none

**Request body**

```json
{
  "username": "admin",
  "password": "your-password"
}
```

`username` defaults to `admin` for the administrator and is changed with
`PUT /api/admin/profile`.

- `email` (optional) is a **deprecated compatibility field**: it is only consulted
  when `username` is absent, and it exists because accounts created before the
  `username` column was introduced were given a derived username the owner may not
  know. A login name can never contain `@`, so a value sent here always resolves
  against visitor accounts.
- Sending neither `username` nor `email` returns `400`.

**Response `200 OK`** — sets `tw_session` and `tw_csrf`, and returns the identity.
The administrator:

```json
{
  "role": "admin",
  "name": "Twilight",
  "username": "admin"
}
```

A visitor account:

```json
{
  "role": "user",
  "user": {
    "id": 3,
    "username": "nightwalker",
    "nickname": "夜行者",
    "email": "user@example.com",
    "has_password": true,
    "created_at": 1700000000,
    "updated_at": 1700000000
  }
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Missing username, or missing password |
| `401 Unauthorized` | Unknown account, wrong password, or (for the administrator) wrong login name |
| `409 Conflict` | Server is not installed yet **and** the username is the administrator's login name |
| `429 Too Many Requests` | Per-IP sign-in rate limit exceeded |

### `POST /api/auth/user/login`

**Deprecated.** An alias of [`POST /api/auth/login`](#post-apiauthlogin): the same
request body, the same handler, the same session cookies and the same rate-limit
bucket. It no longer has a rule of its own — it used to accept visitor credentials
only, which is exactly the asymmetry that made the split sign-in confusing. Kept so
that cached front-end bundles and scripts written against the older document keep
working; new clients should call `POST /api/auth/login`.

### `POST /api/auth/email-code`

Emails a six-digit verification code. One code serves both registration and
password reset, because in either case the recipient must already control the
address.

**Authentication:** none

**Request body**

```json
{
  "email": "user@example.com"
}
```

**Response `200 OK`**

```json
{
  "message": "verification code sent"
}
```

A code is valid for 10 minutes and is stored only as a SHA-256 hash. Issuing a
new code retires any code still outstanding for the same address, so only the most
recent one is accepted, and a code is consumed by its first successful use.

Successive sends to the same address must be at least **60 seconds apart**; a
faster retry is refused with `429` and leaves the outstanding code untouched.
This is what stops a scripted client from turning the hourly allowance into one
burst — the countdown the client shows is only a convenience and is not trusted.

A code also stops being accepted after **5 failed verification attempts**: trying
wrong codes burns it, and the recipient has to request a new one. A six-digit code
has a million possibilities, so without that cap a distributed attacker (whose IPs
each get their own allowance) could enumerate the space inside the ten-minute
window.

Only the recipient address and a hash of the code are stored. The message body is
recorded for delivery auditing with the code replaced by `[redacted]` — see
[`GET /api/admin/emails/:id`](#get-apiadminemailsid).

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Missing or malformed email |
| `429 Too Many Requests` | Per-IP email rate limit exceeded; fewer than 60 seconds since the last send to this address; or this address has already been sent 10 codes in the last hour |
| `503 Service Unavailable` | `mail.api_key` or `mail.from` is not configured |

### `POST /api/auth/register`

Consumes a code, creates the account and signs it in immediately.

**Authentication:** none

**Request body**

```json
{
  "email": "user@example.com",
  "code": "123456",
  "username": "nightwalker",
  "nickname": "夜行者",
  "password": "your-password"
}
```

- `username` is the sign-in identifier: 3–24 characters, must start with a letter,
  and may otherwise contain letters, digits, `_` and `-`. It is unique
  case-insensitively against every visitor account **and against the administrator's
  login name**, which cannot be registered; the spelling you choose is what gets
  displayed.
- `nickname` is the public display name, limited to 24 characters.
- `email` is only used to receive the code. It is unique as well.
- The password follows the same policy as the installer: at least 8 characters, at
  most 72 bytes, at least one letter and one digit.

If the address already belongs to a commenter who never set a password, the code
attaches the password to that existing account instead of creating a duplicate, so
the comment history is preserved.

A registration that is rejected for any other reason (a malformed username, a weak
password) does **not** consume the code, so the user only has to correct that field
rather than wait for a new email. A *wrong code*, however, counts against the
5-attempt allowance described under
[`POST /api/auth/email-code`](#post-apiauthemail-code), and that allowance is
shared with `POST /api/auth/reset-password`.

**Response `201 Created`** — sets the cookies and returns the `role: "user"` body.

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid body, malformed username, weak password, or invalid/expired/consumed code |
| `409 Conflict` | The username is already taken — by another visitor account or by the administrator's login name — or the address already has an account with a password |
| `429 Too Many Requests` | Per-IP verification rate limit exceeded |

### `POST /api/auth/reset-password`

Sets a new password from a code and revokes **every** session of that account, so
anyone holding a stolen session is evicted.

**Authentication:** none

**Request body**

```json
{
  "email": "user@example.com",
  "code": "123456",
  "password": "your-new-password"
}
```

A wrong code counts against the same 5-attempt allowance as
[`POST /api/auth/register`](#post-apiauthregister).

**Response `200 OK`**

```json
{
  "message": "password updated"
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid body, weak password, or invalid/expired code |
| `404 Not Found` | No account for that address |

### `POST /api/auth/logout`

Revokes the session used to make the request and clears both cookies. The session
stops working immediately; a later request with it returns `401`. Calling it while
signed out is a successful no-op.

**Authentication:** session (any role), CSRF token required

**Request body:** none

**Response `204 No Content`**

### `GET /api/me`

Returns the identity behind the current session; see
[Authentication](#1-roles) for both response shapes.

**Authentication:** session (any role)

**Errors**

| Status | Condition |
|---|---|
| `401 Unauthorized` | No session, or the account it refers to was removed |

### `PATCH /api/me`

Edits the signed-in visitor account's own profile. Both fields are optional, but
at least one must be present.

**Authentication:** session with role `user`, CSRF token required

**Request body**

```json
{
  "username": "nightwalker2",
  "nickname": "新的昵称"
}
```

Each field is applied independently:

- `nickname` affects display only; the sign-in identifier is untouched.
- `username` changes the sign-in identifier. Sessions already held by this account
  stay valid (a session records the account id, not the name), and the next
  sign-in must use the new username. The administrator's login name is refused here
  too — see [One sign-in endpoint, two roles](#one-sign-in-endpoint-two-roles).
- The email is deliberately **not** editable here; only an administrator can
  correct it.

Sending an empty `username` or `nickname` is `400` — omit the key to leave that
field alone. Sending neither is `400` as well.

**Response `200 OK`** — a `UserResponse` object reflecting the final state.

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Empty/over-long/malformed username or nickname, or no field supplied |
| `403 Forbidden` | The session belongs to the administrator, who has no account row |
| `409 Conflict` | Another account already uses that username |

### `PUT /api/me/password`

Changes the password of the **current account** — the administrator and a visitor
use this same endpoint. Every session of the account is revoked and this device is
signed back in with a fresh one, so other devices must sign in again. The
re-issued session keeps the caller's role: an administrator stays `admin`, and its
TTL stays `AdminTTL` (24h).

**Authentication:** any signed-in session (`admin` or `user`), CSRF token required

**Request body**

```json
{
  "current_password": "your-password",
  "new_password": "your-new-password"
}
```

**Response `204 No Content`** — sets fresh `tw_session` and `tw_csrf` cookies.

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Missing field or weak new password |
| `401 Unauthorized` | Current password is incorrect |
| `403 Forbidden` | No session |

> The administrator cannot reset this password through
> `POST /api/auth/reset-password`: that flow needs a reachable e-mail address, and
> the administrator account deliberately has none. Recover it locally with
> `main.exe admin set-password` (see the deployment notes).

---

## Public Endpoints

### Articles

#### `GET /api/articles`

Lists all non-deleted articles, ordered by newest first.

**Authentication:** none

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

```json
{
  "items": [
    {
      "id": 1,
      "title": "Hello World",
      "content": "Welcome to my blog.",
      "created_at": 1700000000,
      "updated_at": 1700000000,
      "tags": ["hello", "world"]
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

#### `GET /api/articles/:id`

Gets a single non-deleted article.

**Authentication:** none

**Path parameters**

| Name | Type | Description |
|---|---|---|
| `id` | int64 | Article ID |

**Response `200 OK`**

```json
{
  "id": 1,
  "title": "Hello World",
  "content": "Welcome to my blog.",
  "created_at": 1700000000,
  "updated_at": 1700000000,
  "tags": ["hello", "world"]
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid article ID |
| `404 Not Found` | Article does not exist or is deleted |

### Tags

#### `GET /api/tags`

Lists all tags with article counts.

**Authentication:** none

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

```json
{
  "items": [
    {
      "id": 1,
      "name": "hello",
      "article_count": 3
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

#### `GET /api/tags/:id/articles`

Lists non-deleted articles that have the given tag.

**Authentication:** none

**Path parameters**

| Name | Type | Description |
|---|---|---|
| `id` | int64 | Tag ID |

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

```json
{
  "items": [
    {
      "id": 2,
      "title": "Another Post",
      "content": "...",
      "created_at": 1700000100,
      "updated_at": 1700000100,
      "tags": ["hello"]
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid tag ID |
| `404 Not Found` | Tag does not exist |

#### `GET /api/tags/:id/count`

Returns the number of non-deleted articles for a tag.

**Authentication:** none

**Response `200 OK`**

```json
{
  "count": 3
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid tag ID |
| `404 Not Found` | Tag does not exist |

### Moments

#### `GET /api/moments`

Lists all non-deleted moments, ordered by newest first.

**Authentication:** none

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

```json
{
  "items": [
    {
      "id": 1,
      "content": "Today was a good day.",
      "media_items": [],
      "created_at": 1700000000,
      "updated_at": 1700000000
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

#### `GET /api/moments/:id`

Gets a single non-deleted moment.

**Authentication:** none

**Path parameters**

| Name | Type | Description |
|---|---|---|
| `id` | int64 | Moment ID |

**Response `200 OK`**

```json
{
  "id": 1,
  "content": "Today was a good day.",
  "media_items": [],
  "created_at": 1700000000,
  "updated_at": 1700000000
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid moment ID |
| `404 Not Found` | Moment does not exist or is deleted |

#### Moment image URL format

Each moment response includes a `media_items` array. Every element uses the same shape as `MediaItem` returned by the media upload and media list endpoints, and its `url` is:

```text
/api/media/{id}
```

Example moment response with an image:

```json
{
  "id": 1,
  "content": "Today was a good day.",
  "media_items": [
    {
      "id": 1,
      "url": "/api/media/1",
      "original_name": "photo.jpg",
      "media_type": "image",
      "mime_type": "image/webp",
      "size": 12345,
      "converted": true,
      "created_at": 1700000000
    }
  ],
  "created_at": 1700000000,
  "updated_at": 1700000000
}
```

Clients can render each image thumbnail by using `media_items[].url` directly as the `src`. `GET /api/media/:id` is public and requires no authentication. There is no separate thumbnail endpoint; clients that need a thumbnail can use the same URL and scale it in the UI.


### Pages

#### `GET /api/pages`

Lists all non-deleted pages.

**Authentication:** none

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

```json
{
  "items": [
    {
      "id": 1,
      "title": "About",
      "content": "This is the about page.",
      "created_at": 1700000000,
      "updated_at": 1700000000
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

#### `GET /api/pages/:id`

Gets a single non-deleted page.

**Authentication:** none

**Path parameters**

| Name | Type | Description |
|---|---|---|
| `id` | int64 | Page ID |

**Response `200 OK`**

```json
{
  "id": 1,
  "title": "About",
  "content": "This is the about page.",
  "created_at": 1700000000,
  "updated_at": 1700000000
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid page ID |
| `404 Not Found` | Page does not exist or is deleted |


### Search

#### `GET /api/search`

Searches articles and pages by title and content. Search is served by Meilisearch:
the server manages the binary itself (it uses a local copy if present and downloads
one otherwise) and keeps the index in sync. If the index cannot be built at startup
the server keeps running and search returns incomplete results until an article or
page is saved.

**Authentication:** none

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `q` | string | yes | Search query |
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |
| `lang` | string | no | `zh` or `en`; defaults to `zh`. Only the titles and bodies of that language are matched. |

**Response `200 OK`**

Article hits carry the full set of required `Article` fields, including both
language variants and the two independent AI declarations:

```json
{
  "items": [
    {
      "type": "article",
      "id": 1,
      "title": "Hello World",
      "content": "Welcome to my blog.",
      "title_en": "Hello World",
      "content_en": "Welcome to my blog.",
      "ai_generated": false,
      "ai_generated_en": true,
      "created_at": 1700000000,
      "updated_at": 1700000000,
      "cover_media_id": null,
      "tags": ["hello"],
      "tags_en": ["hello"]
    },
    {
      "type": "page",
      "id": 2,
      "title": "About",
      "content": "This is the about page.",
      "title_en": "About",
      "content_en": "This is the about page.",
      "ai_generated": false,
      "ai_generated_en": false,
      "created_at": 1700000000,
      "updated_at": 1700000000,
      "cover_media_id": null
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 2,
  "total_pages": 1
}
```

Page hits do not carry `tags` / `tags_en`; the two AI flags are always present and
are `false` for pages.

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Missing `q`, or invalid `page`/`page_size` |
| `500 Internal Server Error` | Meilisearch search failed |

### Comments

#### `GET /api/comments`

Lists comments for a target resource.

**Authentication:** none; when the request carries a session, `liked_by_me` is populated for that account.

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `target_type` | string | yes | One of `article`, `moment`, `page` |
| `target_id` | int64 | yes | ID of the target resource |
| `page` | int | yes | Page number, starting at 1; pages are based on top-level comments |
| `page_size` | int | yes | Number of top-level comments per page; max 12 |

**Response `200 OK`**

Comments are sorted by `like_count` within the same level. Top-level comments are sorted among top-level comments; children are sorted only among their siblings. A page contains the selected top-level comments and all of their descendant replies, in depth-first order (each reply immediately follows its parent). `total` and `total_pages` count **top-level** comments only.

Paging is performed in SQL against top-level comments, so the cost of one request does not grow with the total number of comments on the target.

```json
{
  "items": [
    {
      "id": 1,
      "author": {
        "type": "user",
        "id": 3,
        "nickname": "夜行者"
      },
      "target_type": "article",
      "target_id": 1,
      "content": "Nice post!",
      "like_count": 5,
      "liked_by_me": true,
      "created_at": 1700000000,
      "updated_at": 1700000000
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

Visitor authors expose `nickname` only. An `email` field is added to the author object **only** when the request carries an administrator session; anonymous callers and other visitors never receive visitor addresses. `name` is used by admin authors instead of `nickname`, so the two author kinds are distinguishable by field.

Admin comments have an author object like this:

```json
{
  "author": {
    "type": "admin",
    "name": "Admin"
  }
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid `target_id` or unsupported `target_type` |

#### `POST /api/comments`

Creates a comment as the administrator or as a signed-in visitor account.

**Authentication:** session required (any role), CSRF token required

**Request body**

```json
{
  "target_type": "article",
  "target_id": 1,
  "parent_id": null,
  "content": "Nice post!"
}
```

`parent_id` may be omitted or `null` for a top-level comment.

There is **no author field**: the signature is taken from the account that posts
the comment — a visitor's `nickname`, or the administrator's `admin_nickname`. A
client cannot post under a name that is not its own, and posting does not modify
the account. `content` is limited to 2000 characters. Article and page bodies are
deliberately **not** length-capped because only an authenticated administrator can
write them.

**Response `201 Created`**

```json
{
  "id": 2,
  "author": {
    "type": "user",
    "id": 3,
    "nickname": "夜行者"
  },
  "target_type": "article",
  "target_id": 1,
  "content": "Nice post!",
  "like_count": 0,
  "liked_by_me": false,
  "created_at": 1700000000,
  "updated_at": 1700000000
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid body, invalid target, empty/over-long content, or parent comment mismatch |
| `401 Unauthorized` | No session |
| `429 Too Many Requests` | Per-IP comment rate limit exceeded |

#### `POST /api/comments/:id/like`

Likes a comment as the authenticated comment user. A user may like their own comment. Liking an already-liked comment is a successful no-op.

**Authentication:** session with role `user`, CSRF token required; an administrator session is rejected

**Path parameters**

| Name | Type | Description |
|---|---|---|
| `id` | int64 | Comment ID |

**Response `200 OK`**

Returns the updated `CommentResponse`.

```json
{
  "id": 2,
  "author": {
    "type": "user",
    "id": 3,
    "nickname": "夜行者"
  },
  "target_type": "article",
  "target_id": 1,
  "content": "Nice post!",
  "like_count": 1,
  "liked_by_me": true,
  "created_at": 1700000000,
  "updated_at": 1700000000
}
```

Likes are made by visitor accounts, so the returned author never includes an `email`.

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid comment ID |
| `401 Unauthorized` | No session |
| `403 Forbidden` | Signed in as the administrator instead of a visitor account |
| `404 Not Found` | Comment does not exist or is deleted |

#### `DELETE /api/comments/:id/like`

Removes the authenticated comment user's like from a comment. Unliking a comment that is not liked is a successful no-op.

**Authentication:** session with role `user`, CSRF token required; an administrator session is rejected

**Path parameters**

| Name | Type | Description |
|---|---|---|
| `id` | int64 | Comment ID |

**Response `200 OK`**

Returns the updated `CommentResponse`.

```json
{
  "id": 2,
  "author": {
    "type": "user",
    "id": 3,
    "nickname": "夜行者"
  },
  "target_type": "article",
  "target_id": 1,
  "content": "Nice post!",
  "like_count": 0,
  "liked_by_me": false,
  "created_at": 1700000000,
  "updated_at": 1700000000
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid comment ID |
| `401 Unauthorized` | No session |
| `403 Forbidden` | Signed in as the administrator instead of a visitor account |
| `404 Not Found` | Comment does not exist or is deleted |


### Comment Authentication

Commenters no longer authenticate with a separate email-verified token. A visitor
account signs in once and that session covers commenting, replying and liking. See
[Sessions and Accounts](#sessions-and-accounts) for `POST /api/auth/email-code`,
`POST /api/auth/register` and `POST /api/auth/login`.

### Site

#### `GET /api/site`

Returns the site-level copy configured by the administrator. Reading is public
because the value is already displayed to every reader — it is the slogan on the
home page, the introduction in the footer, and the page metadata.

**Authentication:** none

**Response `200 OK`**

```json
{
  "description": "以代码为笔，以时间为墨。",
  "avatar_url": "",
  "code_block_max_lines": 20,
  "home_page_size": 12,
  "border_radius": 4
}
```

`description` is an empty string when the administrator has not set one; the
client falls back to its own built-in copy. The field is limited to 200
characters, and line breaks and runs of whitespace are collapsed to single spaces
when it is saved.

`avatar_url` is the administrator's avatar as a site-relative path
(`/api/media/{id}`), or an empty string when none is set.

`code_block_max_lines` caps how tall a fenced code block is rendered before it
scrolls internally. Default `20`, allowed range `3`–`500`.

`home_page_size` is how many articles the home page lists per page; the rest are
paginated. Default `12`, allowed range `1`–`12`. The upper bound is a **hard
limit** rather than a taste call: it equals the `page_size` ceiling every list
endpoint enforces, so a larger value would make the home page's very first
request fail with `400`.

`border_radius` is the unified corner radius, in pixels, applied to card-like
components (cards, buttons, inputs, the pagination control). Default `4`, allowed
range `0`–`24`. **`0` means square corners and is a legitimate value, not "unset"** —
omitting the key is what leaves the value unchanged. Shapes whose corner is part
of their meaning do not follow it: avatars stay circular, pill-shaped tags stay
fully rounded, and code blocks keep their own radius.

All five keys are always present in the response — none is omitted when empty or
zero. A client can therefore tell "the server sent no value" apart from "the value
is 0", which matters most for `border_radius` and `home_page_size`: treating a
missing key as a default would silently replace a deliberate setting.

No other key from `settings.json` appears here. Password hashes, the mail API key,
rate-limit allowances and `trusted_proxies` are configuration, not public copy, and
exposing them would be a disclosure.

The write side is `PUT /api/admin/site`.

---

## Admin Endpoints

All admin endpoints require an **administrator session** (`role: "admin"`), and
every unsafe method must also carry the CSRF header described in
[Authentication](#3-csrf).

If the session is missing, invalid or expired, the API returns `401 Unauthorized`.
If it belongs to a visitor account, the API returns `403 Forbidden`.

### Administrator Profile

#### `PUT /api/admin/profile`

Updates the administrator's **display name** and, optionally, the **login name**.
Both are persisted to `settings.json` as `admin_nickname` and `admin_username`.
The two are independent: changing the display name does not change how the
administrator signs in, and changing the login name does not change what readers
see.

**Authentication:** administrator session, CSRF token required

**Request body**

```json
{
  "name": "Twilight",
  "username": "twilight"
}
```

- `name` (required) is the display name: sidebar, footer and comment signature.
  Maximum 24 characters.
- `username` (optional) is the login name. **Omit the key to leave it unchanged** —
  removing the key is not the same as sending an empty string, which is rejected.
  It must satisfy the same username rules as visitor accounts, and it must be free
  in the shared namespace: a username a visitor account already owns is refused, so
  that sign-in never has to choose between two identities with the same name.

**Response `200 OK`** — the `role: "admin"` body carrying both new values.

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Empty or over-long name; empty or malformed username |
| `409 Conflict` | A visitor account already uses that username |

### Site settings

#### `PUT /api/admin/site`

Sets the site-level rendering settings described under
[`GET /api/site`](#get-apisite): the public description, the code block line cap,
the home page size, and the unified border radius.

The four settings are sent in a **single** request. Every field is **optional and
tri-state**: omit the key to leave that setting unchanged, or send a value to
replace it. Omitting is not the same as sending a zero — a request that only
carries `description` (as older clients do) must not reset the other three.

Clients must spread all four keys into the request body explicitly rather than
forwarding their input object wholesale. A missing key is the *only* way to say
"leave it alone", so a field dropped in the middle of the chain is not a lost
update but a silent command to reset — which is exactly what happened to
`home_page_size` before it was added to the request body: the settings form
appeared to save a new page size, the response echoed the old one, and nothing
reported an error.

**Authentication:** administrator session, CSRF token required

**Request body**

```json
{
  "description": "一个人的博客：记录技术、设计与生活。",
  "code_block_max_lines": 20,
  "home_page_size": 12,
  "border_radius": 4,
  "post_banner_height": 400,
  "moments_enabled": true,
  "comment_notify_to": "owner@example.com"
}
```

`description` is trimmed, line breaks and runs of whitespace are collapsed into
single spaces, and control characters are dropped. An empty string is accepted and
clears the description, which makes the client fall back to its built-in copy.

`code_block_max_lines` must be an integer in `3`–`500`.
`home_page_size` must be an integer in `1`–`12`.
`border_radius` must be an integer in `0`–`24`; **`0` is accepted and stores
square corners**.
`post_banner_height` must be an integer in `120`–`800`: the maximum height of a
post cover image, in pixels. Tall images are cropped instead of pushing the
title off the first screen.
`moments_enabled` toggles the whole Moments feature. It is **not** a
"soft" switch: when it is off the public Moments API answers `404`, the public
page says the feature is disabled and sends no request, and the navigation,
footer, sidebar and console menu entries disappear. Published moments are **not**
deleted — turning the switch back on restores them.
`comment_notify_to` is the address that receives a plain-text notice when a
**visitor** comments. An empty string disables the notice; any other value must
be a bare address (no display name). The owner's own comments never trigger it.

Out-of-range values are rejected with `400` rather than clamped — silently
changing a submitted value would make the saved result disagree with what the
settings form displays.

**Response `200 OK`** — every setting as stored, so a client can show the final
values without a second request:

```json
{
  "description": "一个人的博客：记录技术、设计与生活。",
  "avatar_url": "",
  "code_block_max_lines": 20,
  "home_page_size": 12,
  "border_radius": 4,
  "post_banner_height": 400,
  "moments_enabled": true
}
```

The response never contains `comment_notify_to`: it is the administrator's
private contact address. Read it back with
[`GET /api/admin/site`](#get-apiadminsite).

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid request body; the description exceeds 200 characters; or one of the numeric settings is out of range |
| `500 Internal Server Error` | The change could not be persisted to `settings.json` (the in-memory value is rolled back) |

#### `GET /api/admin/site`

The administrator's view of the site settings. It is the public snapshot
([`GET /api/site`](#get-apisite)) plus one extra field.

**Authentication:** administrator session

**Response `200 OK`**

```json
{
  "description": "一个人的博客：记录技术、设计与生活。",
  "avatar_url": "",
  "code_block_max_lines": 20,
  "home_page_size": 12,
  "border_radius": 4,
  "post_banner_height": 400,
  "moments_enabled": true,
  "comment_notify_to": "owner@example.com"
}
```

`comment_notify_to` is the administrator's private contact address and is
therefore **only** in this response — the public snapshot never carries it.
An empty string means the notice is turned off.

### Visitor Accounts

#### `GET /api/admin/users`

Lists registered visitor accounts, newest first. Paginated like every other list
endpoint: `page` and `page_size` are required and `page_size` must not exceed 12.

**Authentication:** administrator session

**Response `200 OK`**

```json
{
  "items": [
    {
      "id": 3,
      "username": "nightwalker",
      "nickname": "夜行者",
      "email": "user@example.com",
      "has_password": true,
      "created_at": 1700000000,
      "updated_at": 1700000000
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

`username` is the sign-in identifier and `nickname` the public display name; they
are separate fields here for the same reason they are separate everywhere else.

`has_password` is `false` for an account left behind by a commenter who never set
a password; such an account can claim itself through registration. The
administrator is not listed, because it is not a `users` row.

#### `PUT /api/admin/users/:id`

Corrects another account's username, nickname and email.

**Authentication:** administrator session, CSRF token required

**Path parameters**

| Name | Type | Description |
|---|---|---|
| `id` | int64 | Account ID |

**Request body**

```json
{
  "username": "nightwalker",
  "nickname": "夜行者",
  "email": "new-address@example.com"
}
```

All three fields are required and replace the stored values. `email` is a contact
address only — it is not used for sign-in, so changing it does not move the account
to a new login identity; changing `username` does.

The password is deliberately not settable here: it belongs to the account owner,
who can reset it from the sign-in page.

**Response `200 OK`** — the updated `UserResponse`.

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Missing/over-long nickname, malformed username or email |
| `404 Not Found` | No account with that ID |
| `409 Conflict` | Another account already uses that username, or the username is the administrator's login name, or the email is taken |

### Articles

#### `POST /api/admin/articles`

Creates an article.

**Request body**

```json
{
  "title": "Hello World",
  "content": "Welcome to my blog.",
  "title_en": "Hello World",
  "content_en": "Welcome to my blog.",
  "ai_generated": false,
  "ai_generated_en": false,
  "tags": ["hello", "world"]
}
```

`title` and `content` are the Chinese versions and are always required.
`title_en` and `content_en` are the optional English versions: an empty string
(or an omitted key) means "no English version", and the front end then falls back
to the Chinese text field by field. `ai_generated` and `ai_generated_en` are the
owner's explicit AI marks for the Chinese and English versions respectively. They
are independent: marking the English text as AI-generated never marks the Chinese
text, and vice versa. An omitted key is treated as `false` on create.

**Response `201 Created`**

```json
{
  "id": 1,
  "title": "Hello World",
  "content": "Welcome to my blog.",
  "title_en": "",
  "content_en": "",
  "ai_generated": false,
  "ai_generated_en": false,
  "created_at": 1700000000,
  "updated_at": 1700000000,
  "tags": ["hello", "world"],
  "tags_en": [""]
}
```

`tags_en` is parallel to `tags`: same length, same order, and an empty entry
means "this tag has no English name".

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Title is required or request body is invalid |

#### `PUT /api/admin/articles/:id`

Updates an existing non-deleted article.

**Path parameters**

| Name | Type |
|---|---|
| `id` | int64 |

**Request body**

```json
{
  "title": "Hello World Updated",
  "content": "Updated content.",
  "tags": ["hello"]
}
```

`title` and `content` are always applied. `tags` is optional and its absence is meaningful: **omitting it leaves the existing tags untouched**, while sending `[]` clears them. This is why an update that merely edits the body does not need to restate the tags.

`title_en`, `content_en`, `ai_generated` and `ai_generated_en` follow the same
three-state rule as `content`: omit the key to leave it alone, send a value
(including an empty string or `false`) to replace it. The two AI flags are
independent — updating `ai_generated_en` does not change `ai_generated`.

**Response `200 OK`**

Returns the updated `ArticleResponse`.

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid article ID or invalid body |
| `404 Not Found` | Article does not exist or is deleted |

#### `DELETE /api/admin/articles/:id`

Soft-deletes an article.

**Path parameters**

| Name | Type |
|---|---|
| `id` | int64 |

**Response `204 No Content`**

#### `DELETE /api/admin/articles`

Batch soft-deletes articles.

**Request body**

```json
{
  "ids": [1, 2, 3]
}
```

**Response `204 No Content`**

#### `GET /api/admin/trash/articles`

Lists soft-deleted articles.

**Authentication:** administrator session

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

```json
{
  "items": [
    {
      "id": 1,
      "title": "Hello World",
      "content": "Welcome to my blog.",
      "created_at": 1700000000,
      "updated_at": 1700000000,
      "deleted_at": 1700001000,
      "tags": ["hello"]
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

#### `POST /api/admin/articles/:id/restore`

Restores one soft-deleted article.

**Path parameters**

| Name | Type |
|---|---|
| `id` | int64 |

**Response `204 No Content`**

#### `POST /api/admin/articles/batch-restore`

Restores multiple soft-deleted articles.

**Request body**

```json
{
  "ids": [1, 2]
}
```

**Response `204 No Content`**

#### `DELETE /api/admin/articles/:id/permanent`

Permanently deletes one soft-deleted article.

**Path parameters**

| Name | Type |
|---|---|
| `id` | int64 |

**Response `204 No Content`**

#### `DELETE /api/admin/articles/batch-permanent`

Permanently deletes multiple soft-deleted articles.

**Request body**

```json
{
  "ids": [1, 2]
}
```

**Response `204 No Content`**

### Tags

#### `PUT /api/admin/tags/:id`

Renames a tag.

**Path parameters**

| Name | Type |
|---|---|
| `id` | int64 |

**Request body**

```json
{
  "name": "new-tag-name",
  "name_en": "new tag name"
}
```

`name` is the Chinese name and is required. `name_en` is the optional English
name: an empty string (or an omitted key) means "no English name", and the front
end falls back to `name`. It does **not** take part in the uniqueness
constraint — two Chinese tags may share an English name.

**Response `200 OK`**

```json
{
  "id": 1,
  "name": "new-tag-name",
  "name_en": "new tag name",
  "article_count": 3
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid tag ID or name is required |
| `404 Not Found` | Tag does not exist |
| `409 Conflict` | Another tag with the same name already exists |

### Moments

A moment may optionally reference uploaded media by sending `media_ids` in the create/update request. The returned `MomentResponse` contains the full media objects in `media_items`.

#### `POST /api/admin/moments`

Creates a moment.

**Request body**

```json
{
  "content": "Today was a good day.",
  "media_ids": []
}
```

**Response `201 Created`**

```json
{
  "id": 1,
  "content": "Today was a good day.",
  "media_items": [],
  "created_at": 1700000000,
  "updated_at": 1700000000
}
```

#### `PUT /api/admin/moments/:id`

Updates a moment.

**Path parameters**

| Name | Type |
|---|---|
| `id` | int64 |

**Request body**

```json
{
  "content": "Updated moment content.",
  "media_ids": [4, 5]
}
```

`media_ids` is optional and its absence is meaningful: **omitting it leaves the attached media untouched**, while sending `[]` detaches everything.

**Response `200 OK`**

Returns the updated `MomentResponse`.

#### `DELETE /api/admin/moments/:id`

Soft-deletes a moment.

**Response `204 No Content`**

#### `DELETE /api/admin/moments`

Batch soft-deletes moments.

**Request body**

```json
{
  "ids": [1, 2]
}
```

**Response `204 No Content`**

#### `GET /api/admin/trash/moments`

Lists soft-deleted moments.

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

```json
{
  "items": [
    {
      "id": 1,
      "content": "Today was a good day.",
      "media_items": [],
      "created_at": 1700000000,
      "updated_at": 1700000000,
      "deleted_at": 1700001000
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

#### `POST /api/admin/moments/:id/restore`

Restores one soft-deleted moment.

**Response `204 No Content`**

#### `POST /api/admin/moments/batch-restore`

Restores multiple soft-deleted moments.

**Request body**

```json
{
  "ids": [1, 2]
}
```

**Response `204 No Content`**

#### `DELETE /api/admin/moments/:id/permanent`

Permanently deletes one soft-deleted moment.

**Response `204 No Content`**

#### `DELETE /api/admin/moments/batch-permanent`

Permanently deletes multiple soft-deleted moments.

**Request body**

```json
{
  "ids": [1, 2]
}
```

**Response `204 No Content`**

### Pages

#### `POST /api/admin/pages`

Creates a page.

**Request body**

```json
{
  "title": "About",
  "content": "This is the about page."
}
```

**Response `201 Created`**

```json
{
  "id": 1,
  "title": "About",
  "content": "This is the about page.",
  "created_at": 1700000000,
  "updated_at": 1700000000
}
```

#### `PUT /api/admin/pages/:id`

Updates a page.

**Path parameters**

| Name | Type |
|---|---|
| `id` | int64 |

**Request body**

```json
{
  "title": "About Updated",
  "content": "Updated about page."
}
```

**Response `200 OK`**

Returns the updated `PageResponse`.

#### `DELETE /api/admin/pages/:id`

Soft-deletes a page.

**Response `204 No Content`**

#### `DELETE /api/admin/pages`

Batch soft-deletes pages.

**Request body**

```json
{
  "ids": [1, 2]
}
```

**Response `204 No Content`**

#### `GET /api/admin/trash/pages`

Lists soft-deleted pages.

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

```json
{
  "items": [
    {
      "id": 1,
      "title": "About",
      "content": "This is the about page.",
      "created_at": 1700000000,
      "updated_at": 1700000000,
      "deleted_at": 1700001000
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

#### `POST /api/admin/pages/:id/restore`

Restores one soft-deleted page.

**Response `204 No Content`**

#### `POST /api/admin/pages/batch-restore`

Restores multiple soft-deleted pages.

**Request body**

```json
{
  "ids": [1, 2]
}
```

**Response `204 No Content`**

#### `DELETE /api/admin/pages/:id/permanent`

Permanently deletes one soft-deleted page.

**Response `204 No Content`**

#### `DELETE /api/admin/pages/batch-permanent`

Permanently deletes multiple soft-deleted pages.

**Request body**

```json
{
  "ids": [1, 2]
}
```

**Response `204 No Content`**

### Comments

#### `GET /api/admin/comments`

Lists all non-deleted comments, including comments on all targets and from all authors. Soft-deleted comments appear only under `GET /api/admin/trash/comments`.

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

Because this endpoint is admin-authenticated, the author object includes the commenter's `email` alongside the public `nickname`:

```json
{
  "items": [
    {
      "id": 1,
      "author": {
        "type": "user",
        "id": 3,
        "nickname": "夜行者",
        "email": "user@example.com"
      },
      "target_type": "article",
      "target_id": 1,
      "content": "Nice post!",
      "created_at": 1700000000,
      "updated_at": 1700000000
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

#### `GET /api/admin/trash/comments`

Lists soft-deleted comments.

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

Returns a paginated list of `CommentResponse` objects with `deleted_at` set.

#### `DELETE /api/admin/comments/:id`

Soft-deletes one comment.

**Response `204 No Content`**

#### `DELETE /api/admin/comments`

Batch soft-deletes comments.

**Request body**

```json
{
  "ids": [1, 2]
}
```

**Response `204 No Content`**

#### `POST /api/admin/comments/:id/restore`

Restores one soft-deleted comment.

**Response `204 No Content`**

#### `POST /api/admin/comments/batch-restore`

Restores multiple soft-deleted comments.

**Request body**

```json
{
  "ids": [1, 2]
}
```

**Response `204 No Content`**

#### `DELETE /api/admin/comments/:id/permanent`

Permanently deletes one soft-deleted comment.

**Response `204 No Content`**

#### `DELETE /api/admin/comments/batch-permanent`

Permanently deletes multiple soft-deleted comments.

**Request body**

```json
{
  "ids": [1, 2]
}
```

**Response `204 No Content`**

### Emails

#### `GET /api/admin/emails`

Lists successfully sent emails, newest first.

**Authentication:** administrator session

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

```json
{
  "items": [
    {
      "id": 1,
      "from_email": "blog@example.com",
      "to_email": "user@example.com",
      "subject": "Your verification code",
      "body": "Your verification code is [redacted]",
      "sent_at": 1700000000,
      "created_at": 1700000000
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

`body` is the message as delivered, except that a verification code is replaced by
`[redacted]` before it is stored. Storing it verbatim would put the last ingredient
of an account takeover — the code, not the hash — behind a single read of this
log. What the log is for is proving that a message went out, which the recipient,
subject and timestamp already do.

#### `GET /api/admin/emails/:id`

Returns one stored sent email. The same redaction applies.

**Authentication:** administrator session

**Path parameters**

| Name | Type | Description |
|---|---|---|
| `id` | int | Email ID |

**Response `200 OK`**

```json
{
  "id": 1,
  "from_email": "blog@example.com",
  "to_email": "user@example.com",
  "subject": "Your verification code",
  "body": "Your verification code is [redacted]",
  "sent_at": 1700000000,
  "created_at": 1700000000
}
```

**Errors**

| Status | Condition |
|---|---|
| `400 Bad Request` | Invalid email ID |
| `401 Unauthorized` | No session, or a session that is not the administrator |
| `404 Not Found` | Email does not exist |

---

## Media

Media assets are stored on disk in the directory specified by `media_dir` in `settings.json`. The server also supports optional conversion tools:

- `webp_tool` — path to `cwebp` or `ffmpeg`, used to convert images to WebP.
- `webm_tool` — path to `ffmpeg`/`avconv`, used to convert audio to WebM.

If a conversion tool is missing, the server logs a warning at startup and stores the affected uploads in their original format. WebP and WebM conversion are independent.

### `POST /api/admin/media`

Uploads one or more media files.

**Authentication:** administrator session, CSRF token required

**Request:** `multipart/form-data`

| Field | Type | Description |
|---|---|---|
| `files` | file | One or more files. The field `file` is also accepted as a single-file alias. |

Supported uploads:

- Images: JPEG, PNG, BMP, TIFF, GIF, WebP
- Audio: MP3, WAV, OGG, FLAC, M4A, AAC
- Video: MP4/M4V, MOV, AVI, MKV, WebM, OGV, WMV, FLV, MPEG/MPG/M2V, TS/MTS/M2TS, 3GP/3G2

Non-WebP images are converted to WebP before storage. If WebP conversion is unavailable, non-WebP image uploads fail with a `400 Bad Request`. Audio is converted to WebM/Opus when the tool is available; otherwise the original audio file is stored unchanged. Videos are always stored unchanged and are never converted or transcoded.

**Response `201 Created`**

```json
[
  {
    "id": 1,
    "url": "/api/media/1",
    "original_name": "photo.jpg",
    "media_type": "image",
    "mime_type": "image/webp",
    "size": 12345,
    "converted": true,
    "created_at": 1700000000
  }
]
```

### `GET /api/media/:id`

Returns the stored media file.

**Authentication:** none

**Response `200 OK`**

The response body is the file content. `Content-Type` is the stored MIME type, e.g. `image/webp`, `audio/webm`, or `video/mp4`.

### `GET /api/admin/media`

Lists uploaded media, newest first.

**Authentication:** administrator session

**Query parameters**

| Name | Type | Required | Description |
|---|---|---|---|
| `page` | int | yes | Page number, starting at 1 |
| `page_size` | int | yes | Number of items per page; max 12 |

**Response `200 OK`**

```json
{
  "items": [
    {
      "id": 2,
      "url": "/api/media/2",
      "original_name": "song.wav",
      "media_type": "audio",
      "mime_type": "audio/webm",
      "size": 22492,
      "converted": true,
      "created_at": 1700000000
    }
  ],
  "page": 1,
  "page_size": 12,
  "total": 1,
  "total_pages": 1
}
```

### `PATCH /api/admin/media/:id`

Renames one media item's **display name** (`original_name`).

**Authentication:** administrator session, CSRF token required

**Request**

```json
{
  "original_name": "cat-on-the-keyboard"
}
```

Only the display name changes. The on-disk file name (`storage_name`, a random
hex name assigned at upload time) and the `/api/media/{id}` URL stay exactly the
same, so **no reference has to be rewritten**: article covers
(`articles.cover_media_id`), moment attachments (`moment_media.media_id`) and the
site owner's avatar (`users.avatar_media_id`) all point at the media **id**. This
is also why the endpoint never touches the stored file, its MIME type or its
size, and why `storage_name` cannot be supplied by the client.

Rules applied to the new name:

- Leading/trailing whitespace is stripped; an empty name is rejected with `400`.
- Control characters and path separators (`/`, `\`) are rejected with `400`.
  The name never takes part in path construction; this only keeps logs, the
  admin list and screen readers intact.
- The **extension follows the current display name**: whatever extension the
  request carries is dropped and replaced with the one already stored in
  `original_name`, so `cat`, `cat.png` and `cat.jpg` all end up as
  `cat.<existing extension>` for that record. A rename changes the base name
  only — an item uploaded as `sunset.png` stays `.png` even though the file on
  disk is WebP (the real format lives in `mime_type`; clients never infer a type
  from the file name).
- The result (extension included) must be at most 200 **characters**.
- Duplicate names are allowed: `original_name` is a label, not an identifier.

**Response `200 OK`**

```json
{
  "id": 2,
  "url": "/api/media/2",
  "original_name": "cat-on-the-keyboard.webp",
  "media_type": "image",
  "mime_type": "image/webp",
  "size": 22492,
  "converted": true,
  "created_at": 1700000000
}
```

**Errors**

| Status | Meaning |
|---|---|
| `400` | Invalid id, malformed body, or a name that breaks one of the rules above |
| `401` | No administrator session |
| `403` | Missing or invalid CSRF token |
| `404` | No media with that id |

### `DELETE /api/admin/media/:id`

Permanently deletes one media metadata row and its stored file.

**Authentication:** administrator session, CSRF token required

**Response `204 No Content`**

---

## Example Flow: First-Run Installation

Every call below keeps its cookies in `cookies.txt`, which is what makes the
session work, and reads the CSRF value back out of that file for unsafe requests.

1. Start the server without `settings.json`.
2. Query installation status:

```bash
curl http://localhost:8080/install_status
```

Response:

```json
{
  "installed": false
}
```

3. Install the administrator password. The **login name** defaults to `admin` and
   the **display name** to `Twilight`; both are changed later with
   `PUT /api/admin/profile`.

```bash
curl -X POST http://localhost:8080/install \
  -H "Content-Type: application/json" \
  -d '{"password":"my-secure-password"}'
```

Response:

```json
{
  "installed": true
}
```

4. Sign in. The response sets `tw_session` and `tw_csrf`; nothing else has to be
   stored anywhere. The same request signs a visitor account in — only the username
   differs, and it must not be the administrator's login name.

```bash
curl -c cookies.txt -X POST http://localhost:8080/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"my-secure-password"}'
```

Response — `name` is what readers see, `username` is what signs you in:

```json
{
  "role": "admin",
  "name": "Twilight",
  "username": "admin"
}
```

5. Use the cookie session for administrator calls:

```bash
curl -b cookies.txt "http://localhost:8080/api/admin/trash/articles?page=1&page_size=12"
```

6. State-changing calls also echo the CSRF cookie in a header. Here the display
   name is changed while the login name stays `admin` — omitting `username` is how
   you leave it alone:

```bash
curl -b cookies.txt -X PUT http://localhost:8080/api/admin/profile \
  -H "Content-Type: application/json" \
  -H "X-CSRF-Token: $(awk '$6 == "tw_csrf" { print $7 }' cookies.txt)" \
  -d '{"name":"Twilight"}'
```

7. A visitor account registers with a code obtained from
   `POST /api/auth/email-code`, and registration signs it in the same way.
   `username` is the sign-in identifier, `nickname` the public display name:

```bash
curl -c visitor.txt -X POST http://localhost:8080/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"email":"user@example.com","code":"123456","username":"nightwalker","nickname":"夜行者","password":"my-secure-password"}'
```

8. Sign out. The session is revoked server-side, so a copy of the cookies stops
   working immediately:

```bash
curl -b cookies.txt -X POST http://localhost:8080/api/auth/logout \
  -H "X-CSRF-Token: $(awk '$6 == "tw_csrf" { print $7 }' cookies.txt)"
```

