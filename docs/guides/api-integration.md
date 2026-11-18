# API Integration Guide

Bahasa Indonesia: [api-integration.id.md](api-integration.id.md). This English file is the source of truth; update both together.

Other guides: [Operator](operator.md) · [Platform Admin](platform-admin.md) · [Client Admin](client-admin.md) · [API Integration](api-integration.md) · [Developer](developer.md)

This guide is for **client developers** connecting an application (web, mobile or another service) to the API. The full, try-it-out specification is at `GET /docs` (Swagger UI) and `GET /openapi.json` when the operator has enabled it (`DOCS_ENABLED`). This guide explains the conventions shared by every endpoint.

## 1. Basics

- Base URL: `https://<host>/api/v1`. All requests and responses are JSON (`Content-Type: application/json`), except uploads (`multipart/form-data`) and file downloads.
- The tenant is decided **only at login**; afterwards it comes from the token.
- Body size is limited to 1 MiB (auth endpoints: 16 KiB; uploads have their own limits, see section 7). Larger = 413.
- Every response carries an `X-Request-ID` header. Keep it in your application logs and include it when reporting problems. You may send your own `X-Request-ID`.

## 2. Authentication and token lifecycle

| Request | Body | Result |
|---|---|---|
| `POST /auth/login` | `{"tenant_code"?, "username", "password"}` | tokens |
| `POST /auth/platform/login` | `{"username", "password"}` | tokens (platform admins only) |
| `POST /auth/refresh` | `{"refresh_token"}` | new tokens; the old refresh token is revoked |
| `POST /auth/logout` | `{"refresh_token"}` | 204, idempotent |
| `GET /auth/me` | bearer header | `user_id`, `level`, `tenant_id` |
| `POST /auth/password/forgot` | `{"tenant_code"?, "identifier"}` | 202, always the same |
| `POST /auth/password/reset` | `{"token", "new_password"}` | 204 |
| `POST /auth/password/change` | `{"current_password", "new_password"}` + bearer | 204, all sessions end |

**Finding the tenant at login**, in order: subdomain (`tokoa.app.com`), the `X-Tenant-Code` header, then `tenant_code` in the body. Mobile/API clients usually use the header.

Token response (OAuth 2 shape, no envelope):

```json
{ "access_token": "v4.local...", "token_type": "Bearer", "expires_in": 900, "refresh_token": "<tenant_id>.xxxxxxxx" }
```

Send `Authorization: Bearer <access_token>` on every request that needs a login. Token responses use `Cache-Control: no-store`.

**Client strategy**

1. Keep the access token **in memory**; keep the refresh token in secure storage (Keychain/Keystore on mobile; an `HttpOnly` cookie set by your backend for web). Avoid `localStorage` where possible.
2. Schedule a refresh before `expires_in` runs out, or refresh once on a 401 and retry that request one time.
3. A refresh token is **single-use**. Always store the new pair from the response. If two tabs/processes refresh at once with the same token, only one wins; serialize refreshes in a single queue.
4. **Reuse detection**: presenting an already used refresh token revokes **all** of that user's sessions. Treat 401 `invalid_refresh_token` as "session ended": go to the login screen, do not retry.
5. Access tokens are not checked against the database on every request, so after logout one stays valid until it expires (default 15 minutes in production). Do not keep it longer than needed.
6. Failed logins are counted (default 5 per 15 minutes per tenant+username+IP): the response is 429 `rate_limited` with a `Retry-After` header (seconds). Honor it.

Every login failure gives one identical answer, `invalid_credentials` (wrong tenant, wrong user, wrong password, inactive user, suspended tenant), so do not display any difference.

## 3. Response envelope

Success:

```json
{ "success": true, "code": 200, "data": { }, "message": "optional" }
```

Error:

```json
{ "success": false, "code": 422,
  "error": { "code": "validation_failed", "message": "One or more fields are not valid.",
             "details": [{ "field": "name", "rule": "required" }] } }
```

- The top-level `code` is the HTTP status. **Branch your logic on ****`error.code`**, not on the `message` text (English and may change). `details` appears only for `validation_failed`.
- 404, 405 and 500 use the same envelope.
- Exception to the success shape: `/auth/login`, `/auth/platform/login`, `/auth/refresh` (the OAuth shape above).

| `error.code` | HTTP | Meaning | Client action |
|---|---|---|---|
| `invalid_request` | 400 | Broken body, unknown/invalid query parameter | Fix the request, do not retry |
| `unauthorized` | 401 | Token missing/invalid/expired | Refresh, then retry once |
| `invalid_credentials` | 401 | Login failed | Show a generic message |
| `invalid_refresh_token` | 401 | Refresh token unknown/expired/already used | Go to login |
| `forbidden` | 403 | No permission (or a platform token on a tenant route, or the reverse) | Show "not allowed" |
| `not_found` | 404 | Does not exist (including another tenant's record) | Show not found |
| `method_not_allowed` | 405 | Wrong method | Fix the code |
| `conflict` | 409 | Already exists (unique) | Ask for another value |
| `payload_too_large` | 413 | Too large | Make it smaller |
| `validation_failed` | 422 | Invalid field; also a reference to a missing record | Mark the fields in `details` |
| `rate_limited` | 429 | Too many attempts | Wait for `Retry-After` |
| `internal_error` | 500 | Unexpected | Retry with backoff; report the `X-Request-ID` |
| `service_unavailable` | 503 | A tenant's dedicated database is unreachable | Retry with backoff |

New codes may be added; existing codes and statuses never change. Treat an unknown code according to its HTTP status.

## 4. Lists: pagination, filtering, sorting

`GET` on a collection is always paginated:

```json
{ "success": true, "code": 200, "data": {
  "total_data": 45, "total_pages": 3, "curr_page": 2, "next_page": 3, "prev_page": 1,
  "data_per_page": 20, "data": [ ] } }
```

`prev_page` is absent on the first page; on the last page `next_page` stays on the last page (compare `curr_page` with `total_pages` to stop).

| Parameter | Example | Notes |
|---|---|---|
| `page`, `per_page` | `?page=2&per_page=50` | Defaults 1 and 20; `per_page` max 100 |
| `sort` | `?sort=-created_at,name` | `-` = descending; only columns the resource allows |
| `from`, `to` | `?from=2026-01-01&to=2026-01-31` | Range on `created_at`, `to` inclusive; a date or RFC 3339 (UTC) |
| `<column>` | `?name=admin` | Equality filter; only columns the resource allows |

Unknown parameters, repeated parameters and `;` in the query string are rejected with 400 (so a typo never silently returns unfiltered data). CRUD resources also have `GET /x/show_by/:column/:value` and `GET /x/all_by/:column/:value`.

## 5. Verbs and semantics

- `POST` creates (201 with the new record), `PATCH` changes **only the fields sent** (200), `DELETE` = soft delete (200). `:id` must be a UUID, otherwise 404.
- Do not send `id`, `tenant_id` or timestamps; they cannot be set by the client.
- There is no built-in idempotency key. A `POST` repeated after a timeout can create a duplicate unless a unique rule (username, email, name) answers 409. Design clients not to auto-retry `POST` without checking first.
- Times are UTC, RFC 3339.

## 6. Permissions (RBAC)

Every endpoint is checked against the permissions of the user's group (default deny): 403 when not granted. An endpoint is identified by method + route pattern (`GET /api/v1/users/:id`). Your app should handle 403 by hiding the feature or showing a message, and the admin grants access through `PUT /groups/:id/permissions`. Routes under `/auth/*` and `/public/*` need no group permission.

## 7. Files and attachments

- Upload: `POST /files`, `multipart/form-data`, field `file` (required) and `purpose` (optional). The type is detected from the content: PNG, JPEG, GIF, WebP, PDF; anything else is 422. Max `UPLOAD_MAX_MB` (default 10 MB), larger = 413. The response contains the file `id`.
- Download: `GET /files/:id/content` (`?download=1` forces a download). This endpoint **needs the Authorization header**, so an `<img src>` tag cannot use it directly: fetch it with `fetch` and show it as a blob, or let your backend request it.
- Attachments on other records (pattern): `POST /<parent>/:id/documents` with `{"file_id":"..."}`, `GET` to list, `DELETE /<parent>/:id/documents/:attachment_id` (deletes the attachment and the file). `:id` = the parent record's id, not the file id.
- Excel user import: `GET /users/import/template`, `POST /users/import?dry_run=true`, then without `dry_run` (all or nothing; per-row errors are in `details`, e.g. `row 3 password` rule `min`). Limits: 5 MiB, 500 rows.

## 8. Public endpoints for mobile apps (version check)

No token, under `/api/v1/public/app-versions`:

| Request | Result |
|---|---|
| `GET /latest?platform=android` | Latest version, release notes, `store_url`, package size and hash |
| `GET /check?platform=android&version=1.9.0` | `update_available`, `force_update`, `download_url` |
| `GET /latest/download?platform=android` | The latest APK |
| `GET /:id/download` | The APK of one version |

Suggested flow when the app opens: call `check` with the installed version. If `force_update` is true, show a blocking screen until the user updates (`store_url` or `download_url`); if only `update_available`, offer the update. Downloads support `Range` (resume) and `ETag`/`If-None-Match` (304). Compare versions numerically (`1.10.0` > `1.9.0`), never as text. The public endpoints are not rate limited by the application; cache results on the client.

## 9. Complete curl examples

```bash
BASE=https://api.example.com/api/v1

# 1. log in
RESP=$(curl -s $BASE/auth/login -H 'Content-Type: application/json' -H 'X-Tenant-Code: acme' \
  -d '{"username":"admin","password":"<password>"}')
TOKEN=$(echo "$RESP" | jq -r .access_token); REFRESH=$(echo "$RESP" | jq -r .refresh_token)

# 2. list users, page 1, 20 rows
curl -s "$BASE/users?page=1&per_page=20&sort=-created_at" -H "Authorization: Bearer $TOKEN"

# 3. create a user
curl -s -X POST $BASE/users -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"username":"budi","email":"budi@acme.com","name":"Budi","password":"at-least-10-chars"}'

# 4. upload a file
curl -s -X POST $BASE/files -H "Authorization: Bearer $TOKEN" -F file=@photo.png -F purpose=avatar

# 5. refresh the token (store the new pair!)
curl -s $BASE/auth/refresh -H 'Content-Type: application/json' -d "{\"refresh_token\":\"$REFRESH\"}"

# 6. log out
curl -s -X POST $BASE/auth/logout -H 'Content-Type: application/json' -d "{\"refresh_token\":\"$REFRESH\"}"
```

## 10. Integration checklist

- Logic branches on `error.code`, not `message`
- Refresh token stored securely, refreshes serialized, the new pair always stored
- A 401 triggers one refresh; a failed refresh = login screen
- 429 honors `Retry-After`
- 403 handled (feature hidden or a clear message)
- Lists use `per_page` <= 100 and stop at `curr_page == total_pages`
- `X-Request-ID` is logged on the client
- No `POST` is auto-retried without checking
- The mobile app calls the version `check` on launch
- No tokens or passwords in logs, repositories or bug trackers
