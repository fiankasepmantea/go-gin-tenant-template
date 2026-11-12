# Current phase: Phase 2 - Data and access

## Goal
Users can log in to a tenant, receive a PASETO token, and access only the endpoints allowed for their group.

## Work order
1. ~~Login service and refresh token~~ (done, see docs/authentication.md).
2. ~~HTTP foundation: error-code catalog, envelope, `crud.Handler`, `middleware.TenantTx`~~ (done, see docs/http-api.md).
3. ~~RBAC middleware with the endpoint whitelist, endpoint sync, permissions API, CLI~~ (done, see docs/rbac.md).
4. ~~Rate limiting for login (Redis), password change, forgot/reset password with SMTP~~ (done, see docs/authentication.md).
5. ~~Generator: `make:module`, `make:migration`~~ (done, see docs/generator.md).
6. ~~gRPC AuthService using the same RBAC service~~ (done, see docs/grpc.md).
7. ~~Database per tenant, hybrid, `tenantctl migrate`~~ (done, see docs/database-per-tenant.md).

## Decisions (final)
- Platform admins live in `users` with `tenant_id` NULL + `level='platform'` (ADR 0002).
- `app_versions` is global; a nullable `tenant_id` is added only if a white-label app exists.

## Done
- [x] Database per tenant (hybrid): `database.Router`, migration 000010, `internal/platform/migrate`, `tenantctl migrate`, `tenant create --db-ref`, endpoint mirror
- [x] gRPC `AuthService`: `internal/transport/grpc`, `cmd/grpc`, generated code in `gen/`, TLS/mTLS, tests incl. agreement with REST
- [x] Generator `internal/generator` + `tenantctl make:module|make:migration` (generated module is built and tested in CI)
- [x] Migrations 000001-000009 (tested on PostgreSQL 16)
- [x] `ratelimit` (Redis + memory store, fail open), login rate limit, `mailer` (SMTP), `session.Account` (change/forgot/reset password), migration 000009
- [x] RBAC: `middleware.Authorize`, `rbac.Service`, `endpoint` sync, `/groups/:id/permissions`, CLI `endpoint sync|group create|group grant|user set-group`
- [x] `response` (codes, envelope, `Fail`), `request` (`BindJSON`, `ParseList`), `crud.Handler`, `middleware.TenantTx`, JSON 404/405/500
- [x] Login, refresh-token rotation with reuse detection, logout, `/auth/me`, argon2id, CLI `tenant|user|admin create`
- [x] Tenant-aware `core.Repository[T]`, `database.InTenantTx`, tenants/groups/users repositories, RLS integration tests

## Out of scope for this phase (do not work on these)
Kafka, RabbitMQ, observability.
