# Database per tenant (hybrid)

By default every tenant lives in the **shared** database (one schema, `tenant_id`, RLS: see
[multi-tenancy.md](multi-tenancy.md)). A big or regulated tenant can instead get a **dedicated database**. Both
kinds run side by side, with the same code and the same migrations. Nothing changes until you configure a
dedicated database and create a tenant with `--db-ref`.

## What lives where

| Database | Holds |
|---|---|
| Shared ("control") | `tenants` (with `db_ref`), platform admins, `endpoints`, `app_versions`, and every tenant that has no `db_ref` |
| Dedicated (one per `db_ref`) | everything the tenant owns: users, groups, grants, sessions, its business tables. Also a copy of its own `tenants` row (the foreign keys need it) and a mirror of `endpoints` with the same ids |

RLS stays on in both, so the code does not know which kind it talks to. `database.Router` picks the database:
the `db_ref` of a tenant is read from the control database (cached 30 s), pools to dedicated databases are
opened on first use.

## Set up a dedicated tenant

```bash
# 1. a database and a role for it (the role must not be a superuser, it must not have BYPASSRLS)
# 2. its URL, in the environment of the server, grpc and the CLI (never in the database):
export TENANT_DB_URL_ACME='postgres://app:secret@db-acme:5432/acme?sslmode=require'
# 3. migrate it (as the owner of the database; a role that may CREATE tables)
go run ./cmd/cli migrate up --ref acme
# 4. the application role reads the version, so give it SELECT on schema_migrations
#    GRANT SELECT ON schema_migrations TO app;   (plus the usual grants on the tables)
# 5. create the tenant
go run ./cmd/cli tenant create --code acme --name "Acme" --db-ref acme
```

`tenant create --db-ref` refuses when the reference has no URL, when the database is not migrated to the latest
version, or when the copy in the dedicated database fails (then the row in the shared database is removed again).
After that `user create`, `group create`, `group grant` etc. work as before: they go to the right database.

Settings: `TENANT_DB_URL_<REF>` (`<REF>` is the reference in capitals; in production `sslmode` must be `require`,
`verify-ca` or `verify-full`), `TENANT_DB_MAX_OPEN_CONNS` (default 10) and `TENANT_DB_MAX_IDLE_CONNS` (default 2),
both per dedicated database.

## Migrations: `tenantctl migrate`

| Command | Does |
|---|---|
| `migrate up [--ref R \| --all]` | applies pending `migrations/*.up.sql`, one transaction per file, under a lock |
| `migrate status [--ref R \| --all]` | shows the version of each database |
| `migrate baseline --version N [--ref R]` | records the version of a database whose migrations were applied by hand (psql) |

It uses the same `schema_migrations` table as golang-migrate, so `make migrate-up` keeps working on the shared
database. Run `migrate up --all` on every deploy: the shared database and every configured dedicated database.
`Up` refuses a database that has tables but no history: run `baseline` once.

## Endpoints

`tenantctl endpoint sync` writes the shared database first and then mirrors the rows (same ids) to every
dedicated database that has a tenant. If one mirror fails the command reports it after updating the others; run it
again. Run it, like before, before the new version takes traffic.

## When a dedicated database is down

Only its tenants are affected. REST answers **503 `service_unavailable`**, gRPC `Authorize` answers
**`Unavailable`**. The callers of gRPC must treat that as "no" (fail closed). A failed connect is not retried for
5 s, so a dead database does not make every request wait for a timeout. The other tenants are not touched.

## Limits (pilot)

- **No data-move tool.** Only a new tenant can get a `db_ref`; moving an existing tenant needs a plan of its own
  (dump, restore, switch `db_ref`, delete).
- The `tenants` row in a dedicated database is a **copy**. Status changes (suspend) are made in the shared database,
  which is what login reads.
- Platform admins live in the shared database, so platform-level calls see the shared tenants only.
- `/readyz` checks the shared database only; an unreachable dedicated database shows as 503 for its tenants.
- A changed `db_ref` is noticed by other processes after up to 30 s.
- Backups, restore and the cost of many pools are yours to plan per database.
