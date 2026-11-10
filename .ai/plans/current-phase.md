# Current phase: Phase 2 - Data and access

## Goal
Users can log in to a tenant, receive a PASETO token, and access only the endpoints allowed for their group.

## Work order
1. ~~Login service and refresh token~~ (done, see docs/authentication.md).
2. ~~HTTP foundation: error-code catalog, envelope, `crud.Handler`, `middleware.TenantTx`~~ (done, see docs/http-api.md).
3. RBAC middleware (check the `endpoints`/`group_endpoints` whitelist; default deny; match on the Gin route pattern,
   not the raw URL) after `Authenticate` + `TenantTx`; sync the endpoint list from `engine.Routes()`.
4. Rate limiting for login (Redis), password change/reset.
5. gRPC AuthService using the same service.

## Decisions (final)
- Platform admins live in `users` with `tenant_id` NULL + `level='platform'` (ADR 0002).
- `app_versions` is global; a nullable `tenant_id` is added only if a white-label app exists.

## Done
- [x] Migrations 000001-000007 (tested on PostgreSQL 16)
- [x] `response` (codes, envelope, `Fail`), `request` (`BindJSON`, `ParseList`), `crud.Handler`, `middleware.TenantTx`, JSON 404/405/500
- [x] Login, refresh-token rotation with reuse detection, logout, `/auth/me`, argon2id, CLI `tenant|user|admin create`
- [x] Tenant-aware `core.Repository[T]`, `database.InTenantTx`, tenants/groups/users repositories, RLS integration tests

## Out of scope for this phase (do not work on these)
Kafka, RabbitMQ, CLI generator, observability.
