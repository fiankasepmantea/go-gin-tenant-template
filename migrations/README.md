# migrations

Sequential SQL migrations using [golang-migrate](https://github.com/golang-migrate/migrate).

```bash
make migrate-new name=create_tenants     # creates 000001_create_tenants.{up,down}.sql
make migrate-up   DATABASE_URL="postgres://user:pass@localhost:5432/tenantdb?sslmode=disable"
make migrate-down DATABASE_URL="..."
```

Table rules (see `docs/multi-tenancy.md`): `{table}_id uuid` (UUID v7), `{table}_unique` identity,
`tenant_id`, `created_at`, `updated_at`, `deleted_at`; unique constraints are *partial indexes*
`WHERE deleted_at IS NULL`; `tenant_id` is always the first column in an index.

## Current contents

| # | Table | Notes |
|---|---|---|
| 1 | functions `app_tenant()`, `app_level()`, `set_updated_at()` | used by RLS and triggers |
| 2 | `tenants` | platform-owned, no RLS |
| 3 | `groups` | per tenant, RLS |
| 4 | `users` | `tenant_id` is NULL for platform admins, RLS |
| 5 | `endpoints`, `group_endpoints` | global whitelist + per-group permissions (RLS) |
| 6 | `user_tokens` | refresh tokens (hashed), RLS |
| 7 | `app_versions` | global, no RLS |

Tested on PostgreSQL 16 as a role without BYPASSRLS: fail-closed without a tenant, read/write isolation between tenants,
platform admins invisible to tenant sessions, level/tenant CHECK, NULL-aware unique, cross-tenant FKs rejected, `down` then `up` again.
Migrations are run by the owner role; the application uses a separate role without BYPASSRLS and without DDL privileges.
