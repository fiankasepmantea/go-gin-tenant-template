# App versions

Publish mobile app releases, let devices ask whether they must update, and serve the Android package (APK).

## Who can do what

| Action | Route | Who |
| --- | --- | --- |
| List, create, read, update, delete a version | `/api/v1/app-versions` | **Platform admin** token only |
| Upload the APK of a version | `PUT /api/v1/app-versions/:id/package` | Platform admin token only |
| Latest version, update check, APK download | `/api/v1/public/app-versions/...` | **Nobody**: no token |

App versions are global (one table, no `tenant_id`, no RLS): a release belongs to the product, not to one tenant.
The admin routes have scope `platform` (see `docs/rbac.md`): a tenant token gets 403 and no grant can change that.
Everything under `/api/v1/public/` is skipped by `tenantctl endpoint sync`, so it never becomes a grantable endpoint.

## Versions

A version is 1 to 4 numbers separated by dots (`1.4`, `2.0.1`, `1.2.3.4`). No letters, no `v` prefix.
Versions are compared **numerically**, so `1.10.0` is newer than `1.9.0`. They are stored in canonical form
(`1.9` becomes `1.9.0`), so `1.10` and `1.10.0` are the same version and the second one is a 409.
`platform` is `android`, `ios` or `web` and cannot change after creation, nor can `version`.

```
POST /api/v1/app-versions
{ "platform": "android", "version": "1.10.0", "min_supported_version": "1.5.0",
  "is_force_update": false, "release_notes": "Bug fixes", "store_url": "https://play.google.com/..." }
```

`min_supported_version` defaults to `0.0.0` and must not be above `version`. `store_url` must be http or https.

## Package upload (Android only)

`PUT /api/v1/app-versions/:id/package` with multipart field `file`.

- The file must be a zip archive that contains `AndroidManifest.xml`; the extension and the declared content type are ignored.
- Size is limited by `UPLOAD_APK_MAX_MB` (default 200): larger is 413.
- The server stores it under its own key (`app-versions/<id>-<random>`) and records size and SHA-256.
- Uploading again replaces the package; the old file is removed after the new one is saved.
- Deleting the version removes the package.

The APK is **not** signature-checked: the server cannot know your signing key. Android verifies the signature on install.

## Public routes

| Route | Result |
| --- | --- |
| `GET /public/app-versions/latest?platform=android` | Latest version, notes, store URL, package size and hash |
| `GET /public/app-versions/check?platform=android&version=1.9.0` | `update_available`, `force_update`, `download_url` |
| `GET /public/app-versions/latest/download?platform=android` | The latest APK |
| `GET /public/app-versions/:id/download` | The APK of one version |

`force_update` is true when the installed version is below `min_supported_version` of the latest release, or when the
latest release has `is_force_update` and the device is behind it. A device on the latest version is never forced.

Downloads send `Content-Type: application/vnd.android.package-archive`, `ETag` (the SHA-256), `nosniff`, and support
`Range` (resume) and `If-None-Match` (304). The storage key is never shown in any response.

## Things to know

- Upload and download hold the request's database transaction for their duration (see `docs/multi-tenancy.md`).
- The public routes have no rate limit here: put a reverse proxy or CDN in front, and let it cache the downloads.
- Downloads are public by design. Do not publish an APK that must stay private.
