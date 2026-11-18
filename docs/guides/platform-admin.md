# Platform Admin Guide

Bahasa Indonesia: [platform-admin.id.md](platform-admin.id.md). This English file is the source of truth; update both together.

Other guides: [Operator](operator.md) · [Platform Admin](platform-admin.md) · [Client Admin](client-admin.md) · [API Integration](api-integration.md) · [Developer](developer.md)

This guide is for the **platform admin / product owner**: the person who manages the whole platform, registers tenants (clients) and publishes mobile app versions. There is no web interface yet, so everything is done through the `tenantctl` CLI (on the server) and the API (curl or Postman).

## 1. Two kinds of accounts

|  | Platform admin | Tenant admin/user |
|---|---|---|
| Belongs to | The whole platform, no tenant | One tenant (client) |
| Created with | `tenantctl admin create` | `tenantctl user create --tenant ...` or the `/users` API |
| Login | `POST /api/v1/auth/platform/login` | `POST /api/v1/auth/login` + tenant code |
| Can call | Only **platform**-scope routes (e.g. App Versions) | **Tenant**-scope routes allowed by its group |
| Sees tenant data? | **No**. A platform token on a tenant route gets 403 | Only its own tenant's data |

Platform admins deliberately cannot open client data through the API. Do not try to "log in as a client"; if help is needed on client data, ask the client admin or create a dedicated account inside that tenant with the client's permission.

## 2. Creating a platform admin

There is no public sign-up. Run on a machine connected to the database (see the Operator Runbook). The password is read from stdin so it never lands in shell history; at least 10 characters.

```bash
read -rsp "Password: " PW; echo
printf '%s\n' "$PW" | tenantctl admin create \
  --username root --email root@example.com --name "Platform Admin" --password-stdin
unset PW
```

Note: `tenantctl` = `go run ./cmd/cli` (or the result of `go build -o tenantctl ./cmd/cli`).

Login:

```bash
curl -s $BASE/api/v1/auth/platform/login -H 'Content-Type: application/json' \
  -d '{"username":"root","password":"<password>"}'
```

The response has `access_token` (valid for `ACCESS_TOKEN_TTL`), `refresh_token` and `expires_in`. Send it as `Authorization: Bearer <access_token>`. In Postman, fill the environment variables `platform_username` and `platform_password`, then run the platform Login request in the collection.

Good practice: one account per person (no shared accounts), keep the number of platform admins small, and store passwords in a password manager.

Forgot a platform admin password: `POST /api/v1/auth/platform/password/forgot` with `{"identifier": "<username or email>"}` (requires SMTP), then `POST /api/v1/auth/password/reset`.

## 3. Onboarding a new tenant (client)

Run every step below once per client.

1. **(Optional) Dedicated database**. New tenants only. Set `TENANT_DB_URL_<REF>` in the server environment, run `tenantctl migrate up --ref <REF>`, and use `--db-ref <REF>` in step 2. An existing tenant can **not** be moved to a dedicated database.
2. **Create the tenant**:
  ```bash
  tenantctl tenant create --code acme --name "Acme Inc"
  ```
  `--code` is what the client types at login (lower case, short, stable; do not rename it because it also becomes the subdomain when `TENANT_BASE_DOMAIN` is used).
3. **Sync endpoints** after a new release: `tenantctl endpoint sync`.
4. **Create an admin group** and grant permissions:
  ```bash
  tenantctl group create --tenant acme --name admin --description "Full access"
  tenantctl group grant  --tenant acme --group admin --all
  ```
  `--all` grants every **tenant-scope** endpoint. For a limited role create another group and grant specific endpoints, e.g. `--endpoint "GET /api/v1/users"` (repeatable).
5. **Create the client's first admin** and put them in the group:
  ```bash
  read -rsp "Password: " PW; echo
  printf '%s\n' "$PW" | tenantctl user create --tenant acme --username admin \
    --email admin@acme.com --name "Acme Admin" --group admin --password-stdin
  unset PW
  ```
6. **Test the login** as the client admin:
  ```bash
  curl -s $BASE/api/v1/auth/login -H 'Content-Type: application/json' -H 'X-Tenant-Code: acme' \
    -d '{"username":"admin","password":"<password>"}'
  ```
7. **Hand over to the client** (over a secure channel, not plain e-mail): API URL, tenant code, username, temporary password, plus the Client Admin Guide and the API Integration Guide. Ask the client to change the password right away (`POST /api/v1/auth/password/change`).

After that the client admin creates the other users through the API (see the Client Admin Guide). With `tenantctl user set-group --tenant acme --username <name> --group <group>` you can move an existing user into a group.

Onboarding checklist: tenant created, admin group + grants, first admin in the group, login works, credentials handed over, password changed, client knows how to reach support.

## 4. Mobile app versions (App Versions)

This feature belongs to the platform admin; versions are global (not per tenant). Client devices check versions through public routes without a token.

| Action | Route | Who |
|---|---|---|
| List, create, read, update, delete a version | `/api/v1/app-versions` | Platform admin token only |
| Upload the APK | `PUT /api/v1/app-versions/:id/package` | Platform admin token only |
| Latest version, update check, APK download | `/api/v1/public/app-versions/...` | Anyone, no token |

**Publishing a new version**

1. Create the version record:
  ```bash
  curl -s -X POST $BASE/api/v1/app-versions -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
    "platform": "android", "version": "1.10.0", "min_supported_version": "1.5.0",
    "is_force_update": false, "release_notes": "Bug fixes", "store_url": "https://play.google.com/..." }'
  ```
  - Version: 1 to 4 numbers separated by dots (`1.4`, `2.0.1`), no letters or `v` prefix; compared numerically (`1.10.0` is newer than `1.9.0`); `1.10` and `1.10.0` are the same version (duplicate = 409).
  - `platform` (`android`, `ios`, `web`) and `version` cannot change after creation.
  - `min_supported_version` (default `0.0.0`) must not be above `version`; `store_url` must be http/https.
2. **Upload the APK** (Android only), multipart field `file`:
  ```bash
  curl -s -X PUT $BASE/api/v1/app-versions/<id>/package -H "Authorization: Bearer $TOKEN" -F file=@app-release.apk
  ```
  The file must be a zip archive containing `AndroidManifest.xml`; max size `UPLOAD_APK_MAX_MB` (default 200, larger = 413). The server records size and SHA-256. Uploading again replaces the old package. The server does **not** verify the APK signature (Android does that on install).
3. **Test as a device** (no token):
  ```bash
  curl -s "$BASE/api/v1/public/app-versions/check?platform=android&version=1.9.0"
  ```
  Result: `update_available`, `force_update`, `download_url`.

**Forced update**: `force_update` is true when the installed version is below the latest release's `min_supported_version`, or the latest release has `is_force_update=true` and the device is behind it. A device already on the latest version is never forced. The mobile app is responsible for showing the update screen.

Downloads use `Content-Type: application/vnd.android.package-archive` and `ETag` (SHA-256), and support `Range` (resume) and `If-None-Match` (304). The public routes have no rate limit in the application: put a CDN/proxy in front and cache downloads. Downloads are public; never upload an APK that must stay private. Deleting a version deletes its package.

## 5. Day-to-day operations

- **New backend release**: after deploy, run `endpoint sync` then `group grant` for the groups entitled to the new features, in every tenant (otherwise users get 403).
- **Deactivating a client's user**: done by the client admin (`PATCH /users/:id` with `is_active=false`), which also ends their sessions. If no client admin is available, contact the operator.
- **Resetting a client admin's password**: another client admin can use `POST /users/:id/password`; if none exists, the user follows the forgot-password flow (`/auth/password/forgot`), or the operator creates a new admin with `tenantctl user create`.
- **Client offboarding**: use the `tenantctl` commands tenant suspend (stops access, data is kept), tenant export (a zip of the data and files for the client), and, after the retention period in the contract, tenant delete with --confirm repeating the tenant code (permanent, only for a suspended tenant). A dedicated database is not dropped and backups must be cleaned up separately. Details: docs/offboarding.md.
- **Leaked credentials**: change the password of the affected account (sessions end); if `PASETO_KEY` leaked, follow the rotation steps in the Operator Runbook.

## 6. Troubleshooting

| Symptom | Cause | Action |
|---|---|---|
| 401 `invalid_credentials` on platform login | Wrong username/password, or a tenant account used on the platform route | Use `/auth/platform/login` with a platform account |
| 403 on `/app-versions` | A tenant token was used | Use a platform admin token |
| 403 on a tenant route with a platform token | Not allowed by design | Use a tenant account |
| 429 `rate_limited` | Too many failed logins | Wait per `Retry-After` |
| 409 creating a version | The same version already exists | Bump the version number |
| 413 uploading an APK | Over `UPLOAD_APK_MAX_MB` or the proxy limit | Raise the limit on the server and proxy |
| 422 uploading an APK | Not a zip with `AndroidManifest.xml` | Upload a real APK |
| Client user gets 403 on a new feature | Route not granted yet | `endpoint sync` + `group grant` |
