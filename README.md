# go-gin-tenant-template

Template backend **multi-tenant** backend template based on Go: Gin, GORM, PostgreSQL, PASETO v4, RBAC, gRPC, and a CLI generator.
Analogy: one application = one apartment building, each tenant = one resident (at least one).

## Status

| Phase | Contents | Status |
|---|---|---|
| 0–2 | Foundation: config, logger, UUID v7, PASETO v4, tenant resolver, DB connection, health check, basic CLI | **Done** |
| 3 | Core tables and migrations, two admin levels, RLS | **Done** (tested on PostgreSQL) |
| 4 | Generic tenant-aware `core.Repository`, tenant transactions | **Done** (integration tests) |
| 5 | Login and refresh token, RBAC, gRPC `AuthService`, `BaseHandler` | Next |
| 2b | `make:module`, `make:migration` generators | Not yet |
| 5–7 | Kafka/RabbitMQ, advanced Kubernetes, observability, Swagger | Base files exist, logic not yet |

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
make run                    # development mode, http://localhost:8080/healthz
```

## Structure

```
cmd/                 entry points (server, cli; grpc & worker to follow)
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

Development/production modes are described in [docs/environments.md](docs/environments.md). `make help` lists them all: `run`, `build`, `test`, `test-race`, `cover`, `lint`, `vuln`, `docker-up`, `proto`, `migrate-*`.
