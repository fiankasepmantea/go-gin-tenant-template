# Skill: working with multi-tenancy

Model: one schema, a `tenant_id` column. Details: `docs/multi-tenancy.md`.

## Checklist for every table/query

- [ ] The table has `tenant_id UUID NOT NULL` and indexes that start with `tenant_id`.
- [ ] Unique constraints include `tenant_id` and `WHERE deleted_at IS NULL`.
- [ ] The repository filters `tenant_id` from `tenant.IDFrom(ctx)`; no tenant in context = error, not "all data".
- [ ] RLS is enabled (`ENABLE` + `FORCE ROW LEVEL SECURITY`) and the policy uses `current_setting('app.current_tenant')`.
- [ ] A test proves that tenant A cannot read/modify tenant B's data.

## Tenant origin

- At login: subdomain, then the `X-Tenant-Code` header, then the `tenant_code` field (the order of `tenant.Chain`).
- After login: only from the `tid` claim in the PASETO token. Never from client input.

## Two admin levels

- `platform`: manages tenants, cross-tenant, does not read tenant business data without a recorded reason.
- `tenant`: only within its own tenant.
