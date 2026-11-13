# go-gin-tenant-template

Template backend **multi-tenant** backend template based on Go: Gin, GORM, PostgreSQL, PASETO v4, RBAC, gRPC, and a CLI generator.
Analogy: one application = one apartment building, each tenant = one resident (at least one).

## Status

| Phase | Contents | Status |
|---|---|---|
| 0–2 | Foundation: config, logger, UUID v7, PASETO v4, tenant resolver, DB connection, health check, basic CLI | **Done** |
| 3 | Core tables and migrations, two admin levels, RLS | **Done** (tested on PostgreSQL) |
| 4 | Generic tenant-aware `core.Repository`, tenant transactions | **Done** (integration tests) |
| 5a | Login, refresh-token rotation, logout, `/auth/me`, CLI to create tenants/users/admins ([docs](docs/authentication.md)) | **Done** (unit + integration tests) |
| 5b | HTTP foundation: error-code catalog, response envelope, `BaseHandler` (`crud.Handler`), filters/sort/pagination, `middleware.TenantTx` ([docs](docs/http-api.md)) | **Done** (unit + integration tests) |
| 5c | RBAC: endpoint whitelist, default deny, groups and permissions, `tenantctl endpoint sync` / `group` / `user set-group` ([docs](docs/rbac.md)) | **Done** (unit + integration tests) |
| 5d | Login rate limiting (Redis), password change, forgot/reset password with SMTP e-mail ([docs](docs/authentication.md)) | **Done** (unit + integration tests) |
| 5e | Generator: `tenantctl make:module` (migration, model, repository, CRUD handler, tests, registration) and `make:migration` ([docs](docs/generator.md)) | **Done** (unit + generated-module build test + integration test) |
| 5f | gRPC `AuthService` (`Validate`, `Authorize`, TLS/mTLS, same RBAC decision as REST) ([docs](docs/grpc.md)) | **Done** (unit + integration tests) |
| 5g | Database per tenant (hybrid): dedicated database for chosen tenants, `tenantctl migrate` ([docs](docs/database-per-tenant.md)) | **Done** (unit + two-database integration tests) |
| 5h | Observability (request log with user/tenant, Prometheus `/metrics`, `/readyz` with dedicated databases) ([docs](docs/observability.md)) and OpenAPI/Swagger generated from the code ([docs](docs/openapi.md)) | **Done** (unit + integration tests) |
| 6 | Optional: Asynq, Kafka/RabbitMQ, OpenTelemetry tracing, advanced Kubernetes; template 2 (microservice) | Not started (only when a project needs it) |

## Prerequisites

Go ≥ 1.26.0 (see [docs/libraries.md](docs/libraries.md)), Docker, `make`.
Developed on Linux/WSL.

## Quick start

```bash
make hooks                  # once per clone: reject commits of env files
make env-init               # create .env.development + a new PASETO_KEY
make docker-up              # PostgreSQL + Redis
go mod tidy                 # first time: downloads dependencies & creates go.sum
make test
make migrate-dev            # create the tables (Go runner; a database made by hand: see docs/database-per-tenant.md, "baseline")
make run                    # development mode, http://localhost:8080/healthz
```

Then: `go run ./cmd/cli endpoint sync`, create the first tenant and user, and log in: see
[docs/authentication.md](docs/authentication.md). The API describes itself at http://localhost:8080/docs
([docs/openapi.md](docs/openapi.md)), metrics are at `/metrics` ([docs/observability.md](docs/observability.md)).

## Structure

```
cmd/                 entry points (server, grpc, cli)
internal/
  config/            configuration from environment
  platform/          infrastructure: auth (PASETO), database, idgen, logger, tenant, ...
  core/              generic building blocks (phase 4)
  modules/           business modules per feature (tenant, user, group, ...)
  transport/         rest (Gin) and grpc
  cli/               Cobra commands
  version/           build info
api/                 contracts: OpenAPI and proto
migrations/          sequential SQL migrations
deployments/         docker/ and k8s/ (kustomize)
docs/                architecture, multi-tenancy, libraries, ADRs
.ai/                 rules, skills, and plans for AI assistants (see .ai/README.md)
test/                integration tests
```

The rationale for the layout is in [docs/architecture.md](docs/architecture.md).

## Commands

Development/production modes are described in [docs/environments.md](docs/environments.md). `make help` lists them all: `run`, `build`, `test`, `test-race`, `cover`, `lint`, `vuln`, `docker-up`, `proto`, `migrate-dev`, `migrate-status`, `new-module`.
