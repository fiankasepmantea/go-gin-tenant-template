# Getting started: how to use this template

> Bahasa Indonesia: [getting-started.id.md](getting-started.id.md)

This guide takes you from an empty machine to a running API, explains the two kinds of accounts, and shows the
everyday workflow (add a module, upload files, attach them to records, publish app versions, import users).
Every command was run against this code. Deeper details live in the other documents of `docs/`, linked at the end.

## 1. What you get

A **multi-tenant** backend: one application serves many customers (tenants). Analogy: the application is an apartment
building and each tenant is a resident.

| Layer | What it is |
| --- | --- |
| Handler (`handler.go`) | HTTP only: reads the request, writes the response. No business rules. |
| Service (`service.go`) | The business rules. |
| Repository (`repository.go`) | The database queries, always limited to the tenant of the request. |
| PostgreSQL RLS | A second wall: even a mistake in the code cannot read another tenant's rows. |
| RBAC | Default deny: a user can call an endpoint only when their group was granted it. |

Stack: Go, Gin, GORM, PostgreSQL (row level security), PASETO v4 tokens, Redis, SMTP, gRPC, Cobra CLI (`tenantctl`).

## 2. The two kinds of account (read this first)

| | Platform admin | Tenant user |
| --- | --- | --- |
| Belongs to | the whole product (no tenant) | one tenant |
| Created with | `tenantctl admin create` | `tenantctl user create --tenant <code>` (or `POST /users` by a tenant admin) |
| Logs in at | `POST /api/v1/auth/platform/login` (no tenant code) | `POST /api/v1/auth/login` with the tenant code |
| May call | only endpoints with scope `platform` (for example **App Versions** admin routes). No grants needed. | only endpoints with scope `tenant`, and only the ones granted to the user's group |
| Typical use | the owner of the product | staff of a customer |

A token of one kind can never be used for the other kind: a tenant token on a platform route is `403`, and the other
way round as well.

In Postman these are two separate sets of variables: `username` / `password` (a tenant user, for example `admin2`
of tenant `acme`) and `platform_username` / `platform_password` (the platform admin, for example `root`). See section 11.

## 3. Prerequisites

Go (the version in `go.mod`), Docker, `make`. Developed on Linux and WSL.

## 4. First run, from zero

```bash
git clone https://github.com/fiankasepmantea/go-gin-tenant-template.git
cd go-gin-tenant-template

make hooks          # once per clone: refuses commits of env files and keys
make env-init       # creates .env.development with a fresh PASETO_KEY (never overwrites)
make docker-up      # PostgreSQL + Redis
go mod tidy         # first time only
make migrate-dev    # creates the tables
make sync-endpoints # writes the routes of the API into the endpoints table (the RBAC whitelist)
make run            # http://localhost:8080
```

Check it: `curl localhost:8080/healthz` answers `{"status":"ok"}`. The API describes itself at
`http://localhost:8080/docs` (OpenAPI document: `/openapi.json`).

`.env.*` files are never committed and never read by AI assistants. Edit `.env.development` yourself to change a
port or a password. Variables are explained in [environments.md](environments.md); the example file is
`.env.development.example`.

## 5. Create the first accounts

There is no public sign-up. Use the CLI (`go run ./cmd/cli ...` or `bin/tenantctl` after `make build`):

```bash
# a tenant (a customer)
go run ./cmd/cli tenant create --code acme --name "Acme Inc"

# a group (a role) and its permissions. A new group starts with NO permission
go run ./cmd/cli group create --tenant acme --name admin --description "Full access"
go run ./cmd/cli group grant  --tenant acme --group admin --all

# a tenant user in that group. The password is read from stdin (not from a flag, so it stays out of the shell history)
read -rsp "Password: " PW; echo
printf '%s\n' "$PW" | go run ./cmd/cli user create --tenant acme --username admin --email admin@acme.test \
    --name "Acme Admin" --group admin --password-stdin

# a platform admin (needed only for platform routes such as App Versions)
printf '%s\n' "$PW" | go run ./cmd/cli admin create --username root --email root@example.test \
    --name "Platform Admin" --password-stdin
unset PW
```

Passwords need at least 10 characters. After you add routes (a new module, a new version of the template) run
`make sync-endpoints` again and grant the new routes (see section 7).

> **Which is `platform_username` / `platform_password`?** They are exactly the `--username` and the password you typed
> for `tenantctl admin create` (in the example: `root` and your password). If you have not created a platform admin yet,
> run that last command once.

## 6. Log in and call the API

```bash
# tenant user
curl -s localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' -H 'X-Tenant-Code: acme' \
     -d '{"username":"admin","password":"<password>"}'

# the answer: {"access_token":"v4.local....","token_type":"Bearer","expires_in":3600,"refresh_token":"..."}
curl -s localhost:8080/api/v1/groups -H "Authorization: Bearer <access_token>"
```

- The tenant is found from the subdomain, then the `X-Tenant-Code` header, then `tenant_code` in the body. After login the
  tenant comes only from the token.
- The access token lives `ACCESS_TOKEN_TTL` (1 hour in development, 15 minutes in production). Use
  `POST /auth/refresh` with the refresh token to get new ones; a refresh token works once.
- Successful answers are `{"success":true,"code":200,"data":...}`. Lists are paginated:
  `?page=1&per_page=20&sort=-created_at&name=...`. Errors are
  `{"success":false,"code":422,"error":{"code":"validation_failed","message":"...","details":[{"field":"...","rule":"..."}]}}`.

## 7. The everyday workflow: add a module

```bash
go run ./cmd/cli make:module product --field 'name:string!' --field 'price:bigint!' --unique name
# or: make new-module name=product fields="name:string! price:bigint!" unique=name
make migrate-dev        # apply the new migration
make sync-endpoints     # register the new routes
go run ./cmd/cli group grant --tenant acme --group admin --all
make test lint
```

This writes the migration (table with RLS), model, repository, service, handler, and tests, and registers the module.
You then add your business rules in `service.go`. Until a group is granted the routes, they answer `403`
(default deny). Details: [generator.md](generator.md), [rbac.md](rbac.md).

Generated endpoints for module `product`: `GET/POST /api/v1/products`, `GET/PATCH/DELETE /api/v1/products/:id`,
`GET /products/show_by/:column/:value`, `GET /products/all_by/:column/:value`.

## 8. Files, and attachments to a record

**Files** are uploaded once and referred to by id (`file_id`):

1. `POST /api/v1/files`: multipart form, field `file` (PNG, JPEG, GIF, WebP or PDF, checked by content), optional text field
   `purpose` (a short lowercase word). The answer contains `file_id`.
2. `GET /api/v1/files/:id` (metadata), `GET /api/v1/files/:id/content` (download), `GET /api/v1/files` (list), `DELETE /api/v1/files/:id`.

The default limit is 10 MiB (`UPLOAD_MAX_MB`). Files are stored under `STORAGE_DIR` (`STORAGE_DRIVER=local`); S3 can replace
it by implementing one small interface. Details: [files.md](files.md).

**Attachments** link files to records of your own modules, for example photos of an activity. The URL is

```
/api/v1/<parent>/<parent_id>/<name>        for example   /api/v1/activities/{{parent_id}}/documents
```

### What is `parent_id`?

`parent_id` is the **id of the record that owns the attachments**, taken from the module in the URL.

- In `/api/v1/activities/{{parent_id}}/documents`, `activities` is *your* module (the template does not ship one;
  create it with `make:module activity`). `parent_id` is the `activity_id` you get when you create an activity with
  `POST /api/v1/activities`.
- With your old tables `dokumentasi_kegiatan(kegiatan_id, ...)`: the parent is the `kegiatan`, so `parent_id` is a `kegiatan_id`.
- `documents` (`<name>`) is the name you chose in the `Config` when you registered the attachment handler.
- It is not created by the template and not set automatically in Postman: create the parent first, copy its id into the
  `parent_id` variable.

The flow:

```text
1. POST   /api/v1/activities                                  -> {"activity_id": "A1"}      (the parent)
2. POST   /api/v1/files            (multipart, file)           -> {"file_id": "F1"}
3. POST   /api/v1/activities/A1/documents   {"file_id":"F1"}   -> the attachment (attachment_id "D1")
4. GET    /api/v1/activities/A1/documents                      -> list, each with name, content_type, size, url
5. DELETE /api/v1/activities/A1/documents/D1                   -> removes the attachment, the file and its content
```

Wiring it to your table (migration with composite foreign keys, and one registration in `internal/app/app.go`) is shown
step by step in [attachments.md](attachments.md).

## 9. App versions (mobile app updates and APK download)

For apps that are distributed outside the stores. Global (not per tenant).

- **Admin** (platform admin token only): `/api/v1/app-versions` (create, list, get, update, delete) and
  `PUT /api/v1/app-versions/:id/package` (multipart `file`: an APK, for Android versions).
- **Public** (no token): `GET /api/v1/public/app-versions/latest?platform=android`,
  `GET .../check?platform=android&version=1.4.0` (answers `update_available` and `force_update`),
  `GET .../latest/download?platform=android`, `GET .../:id/download` (supports resume with `Range`).

Versions are compared by number (`1.10.0` is newer than `1.9.0`). Details: [app-versions.md](app-versions.md).

## 10. Import users from Excel

`GET /api/v1/users/import/template` downloads the `.xlsx` template. Fill it (columns `username`, `email`, `name`,
`password`; optional `group` (the name of an existing group) and `is_active`), then `POST /api/v1/users/import` with the file in
the form field `file`. Add `?dry_run=true` to only check. It is all or nothing: if one row is wrong, nothing is created and the answer
lists the rows (`row 3 password: min`). Limits: 5 MiB, 500 users per file. Details: [users-import.md](users-import.md).

## 11. Postman

Import `GO-GIN-TENANT-TEMPLATE.postman_collection.json` and the environment file, select the environment, and run
**AUTH → Login** first (it saves the token).

| Variable | Meaning |
| --- | --- |
| `base_url` | `http://localhost:8080/api/v1` **without a trailing slash** (a trailing slash gives `//` and a 404) |
| `root_url` | `http://localhost:8080` (health, metrics, openapi) |
| `tenant_code` | the tenant to log in to, for example `acme` |
| `username`, `password` | a **tenant user** of that tenant (section 5: `admin`; the password you chose) |
| `platform_username`, `platform_password` | the **platform admin** (section 5: `root`; the password you chose). Only for App Versions admin requests |
| `access_token`, `refresh_token` | saved by Login |
| `platform_token` | saved by Login (platform admin) |
| `group_id`, `user_id`, `file_id`, `version_id` | saved by the Create requests |
| `parent_id`, `attachment_id` | for the attachment example: set by hand (see section 8) |
| `metrics_token` | only if the server has `METRICS_TOKEN` set |

Secret variables (`password`, tokens) must be filled in the environment (the lock icon). For file requests, in the Body
tab choose form-data and give the key `file` the type **File**.

## 12. Development and production

`APP_ENV=development` (default) or `production`. Production needs `DB_PASSWORD`, refuses `DB_SSLMODE=disable`, logs JSON, and
uses a 15-minute access token. In production use the process environment (secret manager) instead of an env file.
Build and run: `make build`, `make docker-build`, manifests in `deployments/`. Details: [environments.md](environments.md).

A big or regulated tenant can get its own database (hybrid): [database-per-tenant.md](database-per-tenant.md).
When a customer leaves, suspend, export and delete its data with `tenantctl tenant suspend|export|delete`: [offboarding.md](offboarding.md).

## 13. Quality checks

```bash
make test               # unit tests
make test-race          # with the race detector
make test-integration   # against a real PostgreSQL and Redis
make lint               # golangci-lint
make vuln               # govulncheck
```

CI (GitHub Actions) runs lint, test, integration, vulnerability scan, secret scan, env-file guard, and the Docker build on every
pull request. Merge only when all are green.

## 14. Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `connect ECONNREFUSED 127.0.0.1:8080` | The server is not running: `make run`. Check `curl localhost:8080/healthz`. |
| `404` on every Postman request | `base_url` ends with `/`. Remove the trailing slash. |
| `401 invalid_credentials` at login | Wrong tenant code, user or password (all look the same on purpose), the user is inactive, or the tenant is suspended. |
| `401` on other requests | No or expired access token: run Login (or Refresh) again, and set the Bearer token. |
| `403 forbidden` on a normal route | The group of the user was not granted the endpoint. `make sync-endpoints`, then `group grant` (or the `/groups/:id/permissions` API). Reasons are in the server log (`access_denied`: `not_granted`, `no_group`, `wrong_level`). |
| `403` on App Versions admin routes | You used a tenant token. Log in with **Login (platform admin)**. |
| `422 validation_failed` | Read `error.details`: field and rule. Passwords need at least 10 characters. |
| `409 conflict` | The record already exists (for example a duplicate username or version). |
| `413` | The file is larger than `UPLOAD_MAX_MB` (APK: `UPLOAD_APK_MAX_MB`). |
| `429 rate_limited` | Too many failed logins; wait for `Retry-After`. |
| `tenant does not exist` in the CLI | The CLI uses the database of the active `.env.<mode>`; create the tenant there with `tenant create`. |

## 15. Where to read more

| Topic | Document |
| --- | --- |
| Guides by audience (operator, platform admin, client admin, API integration, developer) | [guides/](guides/developer.md) |
| Architecture and layout | [architecture.md](architecture.md) |
| Authentication, tokens, rate limiting | [authentication.md](authentication.md) |
| RBAC | [rbac.md](rbac.md) |
| HTTP conventions (envelope, filters, errors) | [http-api.md](http-api.md) |
| Multi-tenancy and RLS | [multi-tenancy.md](multi-tenancy.md) |
| Generator | [generator.md](generator.md) |
| Users API and import | [users-api.md](users-api.md), [users-import.md](users-import.md) |
| Files and attachments | [files.md](files.md), [attachments.md](attachments.md) |
| App versions | [app-versions.md](app-versions.md) |
| gRPC, observability, OpenAPI | [grpc.md](grpc.md), [observability.md](observability.md), [openapi.md](openapi.md) |
| Database per tenant | [database-per-tenant.md](database-per-tenant.md) |
| Offboarding a client | [offboarding.md](offboarding.md) |
