# Current phase: Phase 2 - Data and access

## Goal
Users can log in to a tenant, receive a PASETO token, and access only the endpoints allowed for their group.

## Work order
1. ~~Login service and refresh token~~ (done, see docs/authentication.md).
2. RBAC middleware (check the `endpoint` whitelist) on top of `middleware.Authenticate`.
3. gRPC AuthService using the same service.
4. Rate limiting for login (Redis).

## Decisions (final)
- Platform admins live in `users` with `tenant_id` NULL + `level='platform'` (ADR 0002).
- `app_versions` is global; a nullable `tenant_id` is added only if a white-label app exists.

## Done
- [x] Migrations 000001-000007 (tested on PostgreSQL 16)
- [x] Login, refresh-token rotation with reuse detection, logout, `/auth/me`, argon2id, CLI `tenant|user|admin create`
- [x] Tenant-aware `core.Repository[T]`, `database.InTenantTx`, tenants/groups/users repositories, RLS integration tests

## Out of scope for this phase (do not work on these)
Kafka, RabbitMQ, CLI generator, observability.
