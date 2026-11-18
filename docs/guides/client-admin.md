# Client Admin Guide

Bahasa Indonesia: [client-admin.id.md](client-admin.id.md). This English file is the source of truth; update both together.

Other guides: [Operator](operator.md) · [Platform Admin](platform-admin.md) · [Client Admin](client-admin.md) · [API Integration](api-integration.md) · [Developer](developer.md)

This guide is for the **client admin** (tenant admin): the person at the client company who manages users, roles and files in their tenant. There is no web interface yet, so everything is done through the API using Postman or curl. You do not need to write code.

You only see and change **your own tenant's** data. Other clients' data is never visible.

## 1. What you receive from the service provider

- The API URL, e.g. `https://api.example.com` (called `BASE` below)
- Your tenant code, e.g. `acme`
- The first admin's username and a temporary password
- The Postman collection and environment (optional but recommended)

## 2. Setting up Postman

1. Import the collection `GO-GIN-TENANT-TEMPLATE.postman_collection.json` and the environment `GO-GIN-TENANT-TEMPALTE-ENV.postman_environment.json`, then select that environment (top right).
2. Fill the environment variables:

| Variable | Value |
|---|---|
| `base_url` | `https://api.example.com/api/v1` (**no trailing slash**; with one the URL becomes `//` and you get a 404) |
| `tenant_code` | your tenant code |
| `username`, `password` | your account |

1. Run the **Login** request in the Auth folder. The token is stored automatically and used by the other requests. `ECONNREFUSED` means the server is not running or `base_url` is wrong.

Without Postman (curl): replace `BASE` and `TOKEN` in the examples below.

## 3. Logging in

```bash
curl -s $BASE/api/v1/auth/login -H 'Content-Type: application/json' -H 'X-Tenant-Code: acme' \
  -d '{"username":"admin","password":"<password>"}'
```

The response has `access_token`, `refresh_token` and `expires_in` (seconds). Send `Authorization: Bearer <access_token>` on every following request.

- The access token is short-lived (e.g. 15 minutes). When it expires, get a new one with `POST /api/v1/auth/refresh` body `{"refresh_token":"..."}`. Each refresh token can be used **once**; keep the new one from the response.
- Log out: `POST /api/v1/auth/logout` body `{"refresh_token":"..."}`.
- Check who you are: `GET /api/v1/auth/me`.
- **Change password** (do this at first login): `POST /api/v1/auth/password/change` body `{"current_password":"...","new_password":"..."}`. The new password needs at least 10 characters. All your sessions end; log in again.
- **Forgot password**: `POST /api/v1/auth/password/forgot` body `{"tenant_code":"acme","identifier":"<username or e-mail>"}`. The answer is always 202 (so outsiders cannot guess accounts). If the account exists, an e-mail with a link valid for about 30 minutes is sent; the token from the link goes into `POST /api/v1/auth/password/reset` body `{"token":"...","new_password":"..."}`.
- 5 failed logins within 15 minutes cause a temporary block (429, see the `Retry-After` header). Wait instead of retrying.

## 4. Managing users

Access to these routes depends on your group; if you get 403, ask the service provider to grant the permission.

| Action | Request |
|---|---|
| List users | `GET /api/v1/users?page=1&per_page=20&name=budi&is_active=true` (filters: `username`, `email`, `name`, `is_active`, `group_id`; ordering: `sort`; date range `from`, `to`) |
| View one | `GET /api/v1/users/:id` |
| Create | `POST /api/v1/users` |
| Update | `PATCH /api/v1/users/:id` |
| Deactivate/activate | `PATCH /api/v1/users/:id` body `{"is_active": false}` |
| Delete | `DELETE /api/v1/users/:id` |
| Set a user's password | `POST /api/v1/users/:id/password` body `{"password":"..."}` |

Example of creating a user:

```json
{ "username": "budi", "email": "budi@acme.com", "name": "Budi", "password": "at-least-10-chars", "group_id": "<group id>", "is_active": true }
```

Important rules:

- `username` and `email` are unique per tenant, case-insensitive. A duplicate gives 409.
- `group_id` must be a group of your tenant, otherwise 422. To remove a user from a group send `"group_id": ""` in the PATCH.
- Deactivating, deleting, or setting the password of a user **ends all their sessions** (they must log in again).
- You **cannot deactivate or delete your own account** (403), so a tenant never loses its last admin.
- A user without a group has no permissions at all.
- The system never shows passwords. Hand out initial passwords over a secure channel and ask users to change them.

## 5. Importing users from Excel

To create many users at once.

1. Download the template: `GET /api/v1/users/import/template` (file `users-template.xlsx`, with a Help sheet).
2. Fill the first sheet. Row 1 holds the column names:

| Column | Required | Rule |
|---|---|---|
| `username` | yes | 3 to 64 characters, unique in the tenant |
| `email` | yes | a plain e-mail address, unique in the tenant |
| `name` | yes | up to 100 characters |
| `password` | yes | at least 10 characters |
| `group` | no | the **name** of an existing group; empty = no group |
| `is_active` | no | `yes`/`no` (also `true`/`false`, `1`/`0`); empty = yes |

1. **Check first** without creating anything: `POST /api/v1/users/import?dry_run=true`, form-data body, field `file` of type File (in Postman change the field type to **File**).
2. If clean, send it again without `dry_run`. Result: `{"rows":120,"created":120,"dry_run":false}`.

Rule: **all or nothing**. If any row has a problem the response is 422 and **no** user is created. The response lists the problems per row, e.g. `row 3 password` rule `min`, `row 5 group` rule `unknown`. The row number is the spreadsheet row (header = row 1). Rules mean: `required` (empty), `min`/`max` (too short/long), `format` (wrong shape), `duplicate` (twin row in the file), `taken` (user already exists), `unknown` (group does not exist). Fix everything and send again.

Limits: 5 MiB file, 500 users per file (split if more), `.xlsx` only (first sheet), formulas are not evaluated. Passwords are in the file as plain text: **delete the file after importing**.

## 6. Groups and permissions

A group is a role (e.g. `admin`, `cashier`). Permissions are given to a **group**, not to a user. The base rule is **denied unless allowed**.

| Action | Request |
|---|---|
| List/create/update/delete groups | `/api/v1/groups` (fields `name`, `description`) |
| See grantable permissions | `GET /api/v1/rbac/endpoints` |
| See a group's permissions | `GET /api/v1/groups/:id/permissions` |
| Replace a group's permissions | `PUT /api/v1/groups/:id/permissions` body `{"endpoint_ids":["<uuid>",...]}` |

Notes:

- `PUT` **replaces the whole list**. Send the complete list you want; `[]` revokes everything.
- An unknown id gives 422 and nothing changes.
- Changes take effect within seconds (caches of up to 30 seconds in some cases).
- Deleting a group does not delete its users, but they lose all permissions.
- Be careful with your own group: do not revoke the permission to manage permissions from the group you use.
- New features from later releases are not granted to groups automatically; the service provider will tell you, or you grant them with the `PUT` above.

## 7. Files and attachments

Upload images or PDFs and attach them to other records.

| Action | Request |
|---|---|
| Upload | `POST /api/v1/files` (form-data: `file`, optional `purpose`, e.g. `avatar`) |
| List | `GET /api/v1/files` (filters `purpose`, `content_type`, `uploaded_by`, `name`; ordering `created_at`, `name`, `size`) |
| Metadata | `GET /api/v1/files/:id` |
| File content | `GET /api/v1/files/:id/content` (`?download=1` to download) |
| Delete | `DELETE /api/v1/files/:id` |

```bash
curl -H "Authorization: Bearer $TOKEN" -F "file=@photo.png" -F "purpose=avatar" $BASE/api/v1/files
```

- Accepted types: PNG, JPEG, GIF, WebP, PDF. The type is checked from the file **content**, not the name; an HTML file named `.png` is refused (422). SVG is not accepted.
- Default maximum size 10 MB (413 above that). Empty files are refused.
- Other clients' files cannot be opened (404).

**Attachments** (modules that support them, e.g. `activities`): (1) upload the file and note the `id` from the response; (2) `POST /api/v1/activities/<activity id>/documents` body `{"file_id":"<file id>"}`; (3) `GET .../documents` to list; (4) `DELETE .../documents/<attachment id>` deletes the attachment **and** its file. `<activity id>` (`parent_id` in Postman) is the id of the parent record you want to attach to, not the file id.

## 8. Common errors

| HTTP / `error.code` | Meaning | What to do |
|---|---|---|
| 400 `invalid_request` | Malformed body/parameter | Check the JSON and parameter names |
| 401 `invalid_credentials` | Wrong tenant, username or password (deliberately not specified) | Check all three |
| 401 `unauthorized` | Token missing, wrong or expired | Log in or refresh again |
| 403 `forbidden` | Your group lacks the permission | Ask an admin/the service provider |
| 404 `not_found` | No such record (or it belongs to another tenant) | Check the id |
| 409 `conflict` | Record already exists (username/email) | Use another value |
| 413 `payload_too_large` | Too large | Make the file/body smaller |
| 422 `validation_failed` | A field is invalid; see `details` (`field` and `rule`) | Fix that field |
| 429 `rate_limited` | Too many login attempts | Wait per `Retry-After` |
| 5xx | Server problem | Note the `X-Request-ID` response header and send it to the service provider |

Example error response:

```json
{"success": false, "code": 422, "error": {"code": "validation_failed", "message": "One or more fields are not valid.", "details": [{"field": "name", "rule": "required"}]}}
```

## 9. Security practices for client admins

- One account per person; never share passwords.
- Change the initial password immediately; use a password manager.
- Deactivate the account of anyone leaving the company the same day.
- Grant only the permissions needed (least privilege); do not make everyone an admin.
- Never keep tokens or passwords in chat, e-mail or shared spreadsheets.
- Report suspicious activity to the service provider with the time and `X-Request-ID`.
