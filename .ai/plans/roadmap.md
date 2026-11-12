# Roadmap

Check items off when done. Order = order of work.

## Phase 1 - Foundation (done)
- [x] Project layout, per-mode config (development/production/testing), logger, database, PASETO v4, tenant resolver
- [x] Basic router, `/healthz`, `/readyz`, CLI `tenantctl keygen|version`
- [x] Docker, Compose, Kubernetes (kustomize), CI, VS Code, AI folder

## Phase 2 - Data and access
- [x] Migrations: tenants, users, groups, endpoints, group_endpoints, user_tokens, app_versions
- [x] Decision: platform admin in `users` with tenant_id NULL (ADR 0002)
- [x] Generic tenant-aware `core.Repository[T]` (hooks added when needed)
- [x] Login/refresh/logout, argon2id, `middleware.Authenticate`, CLI to create the first tenant/user/admin
- [x] HTTP foundation: error-code catalog, response envelope, `crud.Handler`, list filters/sort/pagination, `middleware.TenantTx`
- [x] RBAC middleware with endpoint whitelist, endpoint sync from the router, permissions API, CLI
- [x] Login rate limiting (Redis), password change, forgot/reset password, SMTP mailer
- [x] gRPC `AuthService` (Validate/Authorize), docs/grpc.md

## Phase 3 - Generator
- [x] `make:module`, `make:migration`, automatic registration in `app.Modules` (docs/generator.md)
- [x] Auto-CRUD routes per module (generated modules use `crud.Handler`)
- [ ] `tenantctl init` (rename the project / module path for a new client) - optional, only when needed

## Phase 3b - Database per tenant (hybrid, done)
- [x] `database.Router`, `tenants.db_ref`, `TENANT_DB_URL_<REF>`, `tenantctl migrate`, `tenant create --db-ref`, endpoint mirror, 503/Unavailable (docs/database-per-tenant.md)
- [ ] Tool to move an existing tenant into a dedicated database (not in the pilot)

## Phase 4 - Advanced infrastructure
- [ ] Redis cache, Asynq worker (optional module, only if a project needs background jobs)
- [ ] RabbitMQ and Kafka (`internal/platform/messaging`) (optional module, only if a project needs it)
- [ ] Observability: Prometheus metrics, OpenTelemetry tracing
- [ ] OpenAPI/Swagger generated from code
- [ ] Kubernetes hardening (NetworkPolicy, PDB, secret manager)
