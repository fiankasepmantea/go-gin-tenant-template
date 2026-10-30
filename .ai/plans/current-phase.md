# Current phase: Phase 2 - Data and access

## Goal
Users can log in to a tenant, receive a PASETO token, and access only the endpoints allowed for their group.

## Work order
1. Login service (tenant resolver -> password verification -> token) and refresh token.
2. Auth + RBAC middleware (check the `endpoint` whitelist).
3. gRPC AuthService using the same service.

## Decisions (final)
- Platform admins live in `users` with `tenant_id` NULL + `level='platform'` (ADR 0002).
- `app_versions` is global; a nullable `tenant_id` is added only if a white-label app exists.

## Done
- [x] Migrations 000001-000007 (tested on PostgreSQL 16)
- [x] Tenant-aware `core.Repository[T]`, `database.InTenantTx`, tenants/groups/users repositories, RLS integration tests

## Out of scope for this phase (do not work on these)
Kafka, RabbitMQ, CLI generator, observability.
