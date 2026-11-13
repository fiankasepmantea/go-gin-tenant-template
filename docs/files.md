# Files (`/api/v1/files`)

Upload, list, download, and delete the files of a tenant (avatars, images, PDF documents). The metadata is stored in
the `files` table (with RLS); the content is stored in a storage backend.

| Method | Path | Purpose |
|---|---|---|
| POST | `/files` | upload, `multipart/form-data`: field `file` (required), `purpose` (optional, e.g. `avatar`) |
| GET | `/files` | list (filters `purpose`, `content_type`, `uploaded_by`, `name`; sort `created_at`, `name`, `size`) |
| GET | `/files/:id` | metadata of one file |
| GET | `/files/:id/content` | the content (`?download=1` forces a download instead of showing it in the browser) |
| DELETE | `/files/:id` | delete (the row and the content) |

```bash
curl -H "Authorization: Bearer $TOKEN" -F "file=@photo.png" -F "purpose=avatar" http://localhost:8080/api/v1/files
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/files/<file_id>/content -o photo.png
```

In Postman: Body -> form-data, and change the type of the `file` field to **File**.

## Rules

- **File type** is detected from the content of the file, not from the name or from headers sent by the client.
  Allowed: PNG, JPEG, GIF, WebP, PDF. An HTML file named `.png` is refused (422). SVG is left out on purpose,
  because it can carry scripts. The list is `file.DefaultTypes`.
- **Size** is limited by `UPLOAD_MAX_MB` (default 10 MiB): larger is 413. An empty file is refused (422).
- **File name** from the client is cleaned (no directories, no control characters). The content is stored under a
  key made by the server, `<tenant_id>/<file_id>`, so a name from a client never becomes a path on disk.
- **Tenant isolation**: the metadata is protected by the tenant filter and RLS, the content lives under the folder
  of the tenant. Files of other tenants cannot be opened (404).
- **Downloads** are sent with `X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`, so a
  browser never runs an uploaded file as a page.
- Access is decided by RBAC like every other endpoint. After deploying: `endpoint sync`, then `group grant`.
- The `content` endpoint needs the `Authorization` header. An `<img src>` tag cannot send it: fetch the file with
  `fetch` and show it as a blob, or let your own backend request it.

## Configuration

```dotenv
STORAGE_DRIVER=local          # only local is built in
STORAGE_DIR=./storage         # folder for the content (already in .gitignore). In production: a persistent volume
UPLOAD_MAX_MB=10              # 1 to 100
```

`STORAGE_DIR` must be backed up together with the database, and shared (a shared volume) when more than one server
runs. Otherwise use an S3 backend (below).

## Switching to S3 (or MinIO, GCS)

The handler only knows `storage.Store` (`internal/platform/storage/storage.go`): `Put`, `Open`, `Delete`.
Write a new type that satisfies it, for example `storage.S3`, and choose it in `cmd/server/main.go` by
`STORAGE_DRIVER`. Nothing else changes. Keys are already safe paths (`<tenant_id>/<file_id>`) and work as object keys.

## Good to know

- Uploads and downloads run inside the database transaction of the request, so one connection is in use while the
  file is transferred. The size limit keeps this small. For large files use signed URLs from S3.
- If the database commit fails after the content was stored, the content can be orphaned (no row). This is rare and
  harmless: the file name is random and cannot be reached through the API.
- There is no `Range` support (partial downloads). Add it when streaming video is needed.

To attach many files to a record (photos of an activity, documents of a case), see `docs/attachments.md`.
