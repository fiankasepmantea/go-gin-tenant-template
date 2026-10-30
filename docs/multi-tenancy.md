# Multi-tenancy

Decision: **a single schema with a `tenant_id` column** (option A), with a `TenantResolver` so that
schema-per-tenant or database-per-tenant can be swapped in without changing modules.

## Tenant identity

1. At **login** the tenant is identified in order from: subdomain (web) -> `X-Tenant-Code` header (mobile/API) -> `tenant_code` field in the body.
2. After login, `tenant_id` is taken only from the **PASETO token** (`tid`). Headers/subdomain afterwards are ignored.
3. Platform admins log in without a tenant code (level `platform`).

The code is in `internal/platform/tenant` and `internal/platform/auth`.

## Standard table columns

`{table}_id` (UUID v7), `{table}_unique` (identity, absolute ordering), `tenant_id`, `created_at`, `updated_at`, `deleted_at`.

- Unique constraint = *partial index* `WHERE deleted_at IS NULL`, starting with `tenant_id`, e.g. `(tenant_id, username)`.
- `tenant_id` is the first column in every index.
- Global tables (e.g. `endpoints`, `app_versions`) have no `tenant_id`.

## Two admin levels

Decision (ADR 0002): platform admins are stored in the `users` table with `level='platform'` and `tenant_id IS NULL`.
The safeguards live in the database, not only in code:

- CHECK `users_level_tenant`: `platform` must have no `tenant_id` and no group; `tenant` must have a `tenant_id`.
- Unique constraints are made in two sets (tenant rows and platform rows) because `NULL` is considered distinct in a regular unique.
- RLS policy: a tenant session sees only its own tenant; platform rows are visible only to sessions with `app.current_level='platform'`.
- The composite FK `(tenant_id, group_id)` prevents a user from pointing to a group of another tenant.

`app.current_level` is set by the application from the token claim (`lvl`), never from client input. The application must connect as a
non-superuser role without BYPASSRLS; superusers/table owners bypass RLS.

`app_version` is global (no `tenant_id`, no RLS), managed by platform admins, and its version-check endpoint is public.

## Layered isolation (defense in depth)

1. **Application**: `BaseRepository` adds `WHERE tenant_id = ?` and fills in `tenant_id` on insert (phase 4).
2. **Database (Row-Level Security)**: the policy reads `current_setting('app.current_tenant', true)`; if empty, the result is zero rows (fail closed).

RLS pitfalls to avoid (from research, tested with our driver):

- Set the tenant with **`set_config('app.current_tenant', $1, true)`** inside a transaction (equivalent to `SET LOCAL`), not a plain `SET`:
  `SET` persists on the pooled connection and can leak into another tenant's request.
- Enable `FORCE ROW LEVEL SECURITY`, and connect the application as a **non-superuser role without `BYPASSRLS`**.
- Do not build tenant SQL with string concatenation.
