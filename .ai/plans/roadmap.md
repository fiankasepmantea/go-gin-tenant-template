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
- [ ] Login/refresh/logout, RBAC middleware with endpoint whitelist
- [ ] gRPC `AuthService` (Validate/Authorize)

## Phase 3 - Generator
- [ ] `tenantctl init`, `make:module`, `make:migration`
- [ ] Auto-CRUD routes per module

## Phase 4 - Advanced infrastructure
- [ ] Redis cache, Asynq worker
- [ ] RabbitMQ and Kafka (`internal/platform/messaging`)
- [ ] Observability: Prometheus metrics, OpenTelemetry tracing
- [ ] OpenAPI/Swagger generated from code
- [ ] Kubernetes hardening (NetworkPolicy, PDB, secret manager)
