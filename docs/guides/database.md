# Database and CLI guide

> Indonesian: [database.id.md](database.id.md). The English file is the source of truth.

Audience: maintainers and operators. It explains what each table is for, what its columns mean, how the
`tenantctl` CLI and the server work together, which routes need a login, and how several tenants share (or do not
share) a database. Sources: `migrations/*.up.sql`, `internal/cli`, `internal/modules/endpoint`,
`docs/multi-tenancy.md`, `docs/rbac.md`, `docs/database-per-tenant.md`.

## 1. Columns every table shares

| Column | Meaning |
|---|---|
| `<table>_id` | Primary key, UUID v7 (`idgen.New()`). This is the id the API shows. |
| `<table>_unique` | Identity number. Gives a stable absolute order. Never exposed as an id. |
| `tenant_id` | The owner of the row. Present on every tenant table, always the first column of an index. |
| `created_at`, `updated_at` | Timestamps. `updated_at` is set by the trigger `set_updated_at()`. |
| `deleted_at` | Soft delete. A row with a value is treated as deleted. Unique indexes are partial: `WHERE deleted_at IS NULL`. |

## 2. Tables

| Table | Purpose | Tenant scoped (RLS) |
|---|---|---|
| `tenants` | A customer organization | No (platform owned) |
| `groups` | A role inside one tenant | Yes |
| `users` | Every account, tenant users and platform admins | Yes (platform rows visible to platform sessions only) |
| `endpoints` | Whitelist of API routes | No (global) |
| `group_endpoints` | Which group may call which endpoint | Yes |
| `user_tokens` | Refresh tokens | Yes |
| `password_resets` | Password reset requests | Yes |
| `files` | Metadata of uploaded files | Yes |
| `app_versions` | Versions of the client app | No (global) |

### `tenants`
| Column | Meaning |
|---|---|
| `code` | Short name used at login (`acme`). Lowercase letters, digits and `-`, unique among active tenants. |
| `name` | Display name. |
| `status` | `active` or `suspended`. A suspended tenant cannot log in. |
| `db_ref` | `NULL`: the tenant lives in the shared database. A value (`acme`): a dedicated database, whose URL is in the environment variable `TENANT_DB_URL_ACME` (never stored in the table). |

### `groups`
`name` (unique per tenant, case-insensitive) and `description`. A group has no permissions until you grant some.

### `users`
| Column | Meaning |
|---|---|
| `level` | `tenant` or `platform`. A check keeps `level` and `tenant_id` consistent: platform admins have no tenant and no group. |
| `group_id` | The role of the user. A composite foreign key `(tenant_id, group_id)` makes it impossible to point to a group of another tenant. |
| `username`, `email` | Unique per tenant, case-insensitive (two separate index sets for tenant users and platform admins, because `NULL`s are distinct in a plain unique index). |
| `name` | Display name. |
| `password_hash` | argon2id hash in PHC format. Never returned by the API. |
| `is_active` | `false` blocks login and ends all sessions. |

### `endpoints` and `group_endpoints`
`endpoints` has one row per API route: `method` + `path` (the Gin pattern, for example `GET /api/v1/users/:id`),
`name`, `scope` (`tenant` or `platform`) and `is_public` (see section 5). It is global and is filled by
`tenantctl endpoint sync` from the routes in the code. `group_endpoints` links `(tenant_id, group_id)` to an
`endpoint_id`: it is the permission list of a group. Details: [../rbac.md](../rbac.md).

### `user_tokens`
One row per refresh token: `user_id`, `token_hash` (SHA-256, the token itself is never stored), `user_agent`,
`ip_address`, `expires_at`, `revoked_at`. A token works once; presenting a used one revokes all sessions of the user.

### `password_resets`
`user_id`, `token_hash` (SHA-256), `ip_address`, `expires_at`, `used_at`. Single use and short lived.

### `files`
Metadata only: `name`, `content_type`, `size`, `purpose` (a short label such as `avatar`), `uploaded_by`, and
`storage_key`, the key of the content in the storage backend (local folder by default). `(tenant_id, file_id)` is
unique so other tables can reference a file with a composite foreign key and never point to another tenant's file.

### `app_versions`
Global list of client app versions: `platform` (`android`, `ios`, `web`), `version`, `min_supported_version`,
`is_force_update`, `release_notes`, `store_url`, and the optional APK (`package_key`, `package_size`,
`package_sha256`, set together or not at all). The version check endpoints are public.

## 3. How tenants are separated

Two layers, so a bug in one is caught by the other:

1. The repository adds `WHERE tenant_id = ?` from the context. The tenant always comes from the verified token,
   never from the body or a header (the header is only used at login).
2. PostgreSQL row level security: each tenant table has a `tenant_isolation` policy that compares `tenant_id` with
   `app_tenant()`. Without a tenant in the session the result is zero rows (fail closed). The application connects
   as a role without `BYPASSRLS`.

## 4. One schema, or a schema (database) per tenant?

| Model | What it means | Status |
|---|---|---|
| **Shared schema** (default) | One database, one set of tables. Rows of all tenants live together and are separated by `tenant_id` and RLS. | Built, default |
| **Database per tenant** | A tenant gets its own database. Same code, same migrations. Create the tenant with `--db-ref`. | Built (pilot), see [../database-per-tenant.md](../database-per-tenant.md) |
| **Schema per tenant** (Postgres schemas in one database) | Each tenant gets its own schema. | **Not built.** The tenant resolver could host it, but there is no code for it. |

With several tenants in the shared database nothing is duplicated: a new table or column is created once and is
available to every tenant. Only the data is separate. A dedicated database is for a large or regulated tenant that
must be physically apart; it costs more to run (pools, backups, migrations per database).

## 5. Which routes need a login

| Kind | Needs a token | Needs a group grant | How it is made |
|---|---|---|---|
| **Open** | No | No | Mount it under `/api/v1/public/...` (or `/auth/...`), outside the middleware chain. See `appversion/handler.go`, `RegisterRoutes`. Nothing to sync or grant. |
| **Login only** (`is_public`) | Yes | No | Add `"GET /api/v1/x": {IsPublic: true}` to `overrides` in `internal/modules/endpoint/endpoint.go`, then `endpoint sync`. |
| **Granted** (default) | Yes | Yes | Every route made by the generator or `crud.Handler`. Refused with 403 until the group is granted. |
| **Platform only** | Yes (platform admin) | No | `{Scope: ScopePlatform}` in `overrides`. |

Rule of thumb: a route is protected unless you deliberately put it under `/public/` or `/auth/`. The generator never
creates an open route.

## 6. Working with the CLI

Two terminals, one database (the one named in `.env.<mode>`):

| Terminal | Runs | Lifetime |
|---|---|---|
| 1 | `make run`: the REST server | stays running |
| 2 | `tenantctl ...` (`go run ./cmd/cli ...` or `bin/tenantctl`): provisioning, migrations, generator | one command, then exits |

The CLI talks to the database directly, so it works with the server stopped. The server does not notice new code or
new routes until you restart it, because `go run` compiled the old code.

### Create a table and its endpoints (a new module)

```bash
# terminal 2
make new-module name=product fields="name:string! price:bigint!" unique=name   # code, migration, tests
make migrate-dev                                                               # creates the table
make sync-endpoints                                                            # registers the 7 routes
go run ./cmd/cli group grant --tenant acme --group admin --all                 # allows the group
# terminal 1: stop the server (Ctrl+C) and run `make run` again
```

The generator writes the standard CRUD routes only. A custom route is written by hand in the module's `handler.go`
(`RegisterRoutes`); then run `make sync-endpoints` and grant it. No CLI command creates a custom route.
`make sync-endpoints` reads the routes from the code, so it must run after the code exists.

### Several tenants

The tool and the terminal are the same. The tenant is chosen with `--tenant` on each command:

```bash
tenantctl tenant create --code acme --name "Acme"
tenantctl tenant create --code budi --name "Budi"
tenantctl group create --tenant acme --name admin
tenantctl group grant  --tenant acme --group admin --all
tenantctl group create --tenant budi --name admin
tenantctl group grant  --tenant budi --group admin --all
printf '%s\n' "$PW" | tenantctl user create --tenant budi --username owner --email o@budi.test --name Owner --group admin --password-stdin
```

| Step | Per tenant or global |
|---|---|
| `make migrate-dev` | Global: once for every tenant in the shared database (and every dedicated one) |
| `make sync-endpoints` | Global |
| `group create`, `group grant`, `user create`, `user set-group` | Per tenant (`--tenant`) |
| `admin create` | Platform level, no `--tenant` |
| `tenant export`, `tenant delete` | Per tenant (`--code`) |

A group named `admin` in tenant `acme` and one in tenant `budi` are different rows with different permissions.

### Command overview

`tenant create|export|delete`, `group create|grant`, `user create|set-group`, `admin create`, `endpoint sync [--prune]`,
`migrate up|status|baseline [--ref R | --all]`, `make:module`, `make:migration`, `keygen`, `version`.
Passwords are read from stdin (`--password-stdin`), never from a flag.
