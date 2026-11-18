# Developer Handbook

Bahasa Indonesia: [developer.id.md](developer.id.md). This English file is the source of truth; update both together.

Other guides: [Operator](operator.md) · [Platform Admin](platform-admin.md) · [Client Admin](client-admin.md) · [API Integration](api-integration.md) · [Developer](developer.md)

This guide is for the **developer/maintainer** who receives this code: what each folder and file is for, how a request travels to the database, how to add a new module with the generator, and how to test and ship changes. Repository: `go-gin-tenant-template` (modular monolith: Gin, GORM/PostgreSQL with Row-Level Security, PASETO v4, Cobra CLI `tenantctl`, Redis, SMTP, gRPC). The layout follows the official Go guide [Organizing a Go module](https://go.dev/doc/modules/layout).

## 1. Project map

| Location | Purpose |
|---|---|
| `cmd/server/main.go` | The REST API binary. Thin: load config, wire dependencies, run. No business logic |
| `cmd/cli/main.go` | The `tenantctl` binary (Cobra CLI) |
| `cmd/grpc/` | gRPC AuthService binary for other services (token verification + RBAC) |
| `cmd/worker/` | Worker placeholder (README) |
| `internal/app/app.go` | **Module wiring**: creates every handler and the middleware chain. New modules are registered here (above the `// tenantctl:modules` comment) |
| `internal/config/` | Reads the environment, validates, production-only rules |
| `internal/core/` | Base contracts: the generic `Repository` (CRUD, pagination, automatic `tenant_id` filter), entities, domain errors (`ErrNotFound`, `ErrConflict`) |
| `internal/modules/<feature>/` | One folder per feature (see section 3): `user`, `group`, `session` (login, refresh, password reset), `tenant`, `rbac`, `endpoint`, `file`, `attachment`, `appversion` |
| `internal/platform/` | Technical libraries used by modules: `auth` (PASETO), `password` (argon2id), `database` (connection, tenant transactions, per-tenant DB router), `tenant` (tenant identity in the context), `migrate`, `ratelimit`, `cache`/`ttlcache`, `mailer` (SMTP), `storage` (file storage), `xlsx` (Excel read/write), `observability`, `logger`, `validator`, `idgen` (UUID v7), `messaging` |
| `internal/transport/rest/` | The HTTP layer: `router.go`, `middleware/` (request id, access log, authenticate, tenant tx, authorize), `response/` (envelope + error-code catalog), `request/` (bind, list parsing), `crud/` (generic CRUD handler), `openapi/` |
| `internal/transport/grpc/` | The gRPC server; generated code lives in `gen/` |
| `internal/generator/` | The `make:module` and `make:migration` engine |
| `internal/cli/` | `tenantctl` commands (provisioning, migrate, make) |
| `migrations/` | Numbered SQL migrations `NNNNNN_name.up.sql` / `.down.sql` (000001 to 000013 at present) |
| `api/openapi`, `api/proto` | API specification and protobuf definitions |
| `deployments/` | `docker/` (dev compose, Dockerfile) and `k8s/` (base + dev/prod overlays) |
| `test/integration/` | Tests against real PostgreSQL and Redis (tenant isolation, RBAC, auth, files, import, ...) |
| `docs/` | Per-topic documentation + `docs/adr/` (architecture decisions) + `docs/postman/` |
| `.ai/`, `CLAUDE.md`, `AGENTS.md` | Rules and skills for AI assistants and contributors |
| `scripts/` | `init-env.sh` (creates `.env.<mode>` + a fresh `PASETO_KEY`) and git hooks that block env/key files |
| `Makefile` | All daily commands (`make help`) |

Principle: modules are grouped by **feature**, not by kind (no `controllers/`, `models/`, `utils/`, `pkg/`). `internal/` cannot be imported by other projects, so internal APIs can be refactored freely. `platform/*` packages must not import `modules/*`; modules talk to each other through interfaces.

## 2. The journey of a request

Example: `GET /api/v1/users` by a tenant admin.

1. **Router** (`transport/rest/router.go`) receives the request. `RequestID` creates or reuses `X-Request-ID`; `AccessLog` writes one JSON line per request.
2. **`Authenticate`** verifies the PASETO token and puts the `tenant_id` and level **from the token** into the context (headers/subdomain are no longer trusted after login).
3. **`TenantTx`** opens one database transaction per request and sets the session variables `app.current_tenant` and `app.current_level` (`set_config(..., true)`, equal to `SET LOCAL`). It commits when the status is below 400 and rolls back otherwise. Without it RLS fails closed and queries see no rows.
4. **`Authorize`** matches `METHOD + Gin route pattern` (e.g. `GET /api/v1/users`, not the raw URL) against the permissions of the user's group. Default deny: no permission means 403. The denial reason goes to the server log.
5. **Handler** (`modules/user/handler.go`) only translates HTTP: bind body/query, call the service, map errors to the envelope. No business rules.
6. **Service** (`service.go`) holds the business rules (e.g. you cannot deactivate yourself) and depends on the repository **interface**.
7. **Repository** (`repository.go`) reaches the database through `core.Repository`, which always adds `WHERE tenant_id = ?`. PostgreSQL enforces it again through RLS (`app_tenant()`).
8. **Response** is wrapped in the envelope `{success, code, data}`; errors go through `response.Fail(c, err)` and become `{success:false, code, error:{code,message,details}}`.

Two tenant-isolation layers work together (application filter and RLS); `test/integration` proves that each works on its own. The application must connect as a **non-superuser database role without BYPASSRLS**.

## 3. Anatomy of a module

Example `internal/modules/user/`:

| File | Purpose |
|---|---|
| `model.go` | GORM struct and the methods `crud` needs (e.g. `SetTenantID`) |
| `repository.go` | Database queries; add custom queries here |
| `service.go` | Business rules; the handler calls the service, the service calls the repository |
| `handler.go` | HTTP routes, input DTOs, filter/sort whitelists; `RegisterRoutes(rg)` |
| `*_test.go` | Unit tests (no database) with table-driven cases |

Richer modules add their own files, e.g. `user/importer.go` + `sheet.go` (Excel import), `session/reset.go` (password reset).

The **crud.Handler** pattern gives 7 standard routes per resource: `GET /x`, `GET /x/:id`, `GET /x/show_by/:column/:value`, `GET /x/all_by/:column/:value`, `POST /x`, `PATCH /x/:id`, `DELETE /x/:id` (soft delete). Its safety rules: the body is decoded into an **input type** (not the model, which prevents mass assignment), only whitelisted columns can be filtered/sorted, `tenant_id` comes only from the token, and `TenantTx` is required on every tenant-data route.

## 4. Adding a module with the generator

The generator writes the boring 90% and gets it right from the first run: tenant isolation (RLS), pagination, filters, sorting, soft delete and RBAC. You add the business rules.

```bash
# see what would be created first (writes nothing)
go run ./cmd/cli make:module product --field 'name:string!' --field 'price:bigint!' --field active:bool --unique name --dry-run

# create for real (through make)
make new-module name=product fields="name:string! price:bigint! active:bool note:text" unique=name
```

**Field types**: `string` (text, max 255), `text` (max 5000, no filter/sort), `int`, `bigint` (use it for money in the smallest unit), `float`, `bool`, `time` (RFC 3339), `uuid`. `!` = required (`NOT NULL`; missing = 422). `--unique name` = unique per tenant among non-deleted records (duplicate = 409). `--table` overrides the table name (default: plural of the module name).

**What it writes**: the migration `migrations/NNNNNN_create_<table>.up/down.sql` (columns `<name>_id` uuid, `tenant_id`, timestamps, `deleted_at`, indexes, an `updated_at` trigger, `ENABLE`+`FORCE ROW LEVEL SECURITY`, the `tenant_isolation` policy), `model.go`, `repository.go`, `service.go`, `handler.go`, `handler_test.go`, `test/integration/<name>_test.go`, and it registers the module in `internal/app/app.go`. The generator **never overwrites**; if any file exists, nothing is written.

**After generating**

1. Read the migration; change it if a column needs another type.
2. Apply it: `make migrate-dev`.
3. `make sync-endpoints` so the new routes enter the whitelist (until granted, routes answer 403).
4. Grant access: `tenantctl group grant --tenant <code> --group <group> --all` or `--endpoint "GET /api/v1/<table>"`.
5. `make test lint`, then `make test-integration` with the database running.

Module names: singular, lower-case letters and digits (`product`, `invoice`); names that clash with template packages (`user`, `group`, `session`, `tenant`, ...) are refused. Manual migration: `make new-migration name=add_price_to_products`. Details: `docs/generator.md`. If `app.go` cannot be patched (the marker comment was removed), register by hand as in `.ai/skills/create-module.md`.

## 5. Tables, migrations and RLS

- Standard columns: `{table}_id` (UUID v7), `{table}_unique` (identity), `tenant_id`, `created_at`, `updated_at`, `deleted_at`. Global tables (`endpoints`, `app_versions`) have no `tenant_id`.
- A unique constraint = a *partial index* `WHERE deleted_at IS NULL` starting with `tenant_id`.
- Foreign keys to tenant tables use **pairs** `(tenant_id, column)` because PostgreSQL FK checks are not subject to RLS; without that, tenant B could point at tenant A's data.
- Migrations only move forward, each file in its own transaction: `make migrate-dev` (all databases), `make migrate-status`, or `tenantctl migrate up\|status\|baseline`.
- Never build tenant SQL by string concatenation; use bound parameters.

## 6. Daily commands

| Command | Purpose |
|---|---|
| `make env-init` | Create `.env.development` from the example + a fresh `PASETO_KEY` (never overwrites) |
| `make docker-up` / `docker-down` | Start/stop Postgres + Redis (`PROFILES="mail app"` for extras) |
| `make migrate-dev` | Apply migrations to every database |
| `make sync-endpoints` | Register routes in the `endpoints` table |
| `make run` / `run-grpc` | Run REST / gRPC |
| `make test`, `test-race`, `test-integration`, `cover` | Unit tests, with the race detector, against a real DB, coverage report |
| `make fmt`, `vet`, `lint`, `vuln` | Format, vet, golangci-lint v2, dependency vulnerability check |
| `make new-module`, `new-migration` | Generators |
| `make keygen` | A new `PASETO_KEY` value |
| `make build`, `docker-build` | Binaries into `./bin`, the application image |
| `make proto` | Generate gRPC code (needs buf and plugins) |

The `tenantctl` CLI (`go run ./cmd/cli ...`): `tenant create`, `user create\|set-group`, `admin create`, `group create\|grant`, `endpoint sync [--prune]`, `migrate up\|status\|baseline`, `make:module`, `make:migration`, `keygen`.

## 7. Testing

- **Unit** (`make test`): no database, table-driven; every new behavior needs a test.
- **Integration** (`make test-integration`): real PostgreSQL and Redis (`DB_*` and `REDIS_PORT` from `.env.<mode>`, or `TEST_DATABASE_URL` / `TEST_REDIS_ADDR`). Covers tenant isolation (tenant B cannot see or change A's data), default deny, auth, files, import, and REST/gRPC agreement.
- **Postman collection** (`docs/postman` and the latest collection file) is for manual end-to-end checks.
- Before a PR: `make fmt vet lint test`; CI runs lint, tests, govulncheck and gitleaks.

## 8. Mandatory conventions

Source: `CLAUDE.md` / `AGENTS.md`.

- Everything in the repository (code, comments, docs, error messages, commits, PRs) is in **English**; the exception is `docs/getting-started.id.md`.
- Follow the official Go guidelines and `gofmt`/`goimports`; `context.Context` is the first argument; wrap errors with `%w`.
- Every query on a tenant table uses `tenant_id` from the context (`tenant.IDFrom`), never from the body/headers.
- Primary keys: `idgen.New()` (UUID v7); refresh tokens: `idgen.NewOpaqueToken()`.
- Log with `slog` (key-value); never log passwords, tokens or `PASETO_KEY`.
- New dependency: check the `go` line in its go.mod (max 1.26.0) and record it in `docs/libraries.md`.
- Forbidden: reading/modifying real `.env` files, `git add -f`, bypassing hooks (`--no-verify`), `log.Fatal`/`panic` outside `main`.
- Workflow: feature branch, `make test lint`, commit, push, open a PR, merge once CI is green.

## 9. Extending the system

- **File storage on S3/MinIO/GCS**: write a new type satisfying `storage.Store` (`Put`, `Open`, `Delete`) and select it through `STORAGE_DRIVER` in `cmd/server/main.go`. Object keys are already safe.
- **Attachments for other modules**: the `attachment` pattern (`docs/attachments.md`): a `<parent>_documents` table with composite FKs, then register `attachment.NewHandler(...)` in `app.go`.
- **Public endpoints** (no token): place them under `/api/v1/public/`; these routes automatically stay out of the RBAC whitelist.
- **Platform-only routes**: give them scope `platform` (see `docs/rbac.md` and App Versions as an example).
- **Other services** (the next microservice template) verify tokens through the gRPC AuthService without holding `PASETO_KEY` (`docs/grpc.md`).
- The OpenAPI documentation is built from code (`/openapi.json`, `/docs`); register new operations in the handler with `openapi.Op`.

## 10. Reference documents

`docs/architecture.md`, `multi-tenancy.md`, `rbac.md`, `authentication.md`, `http-api.md`, `generator.md`, `files.md`, `attachments.md`, `app-versions.md`, `users-api.md`, `users-import.md`, `database-per-tenant.md`, `grpc.md`, `observability.md`, `openapi.md`, `environments.md`, `libraries.md`, and `docs/adr/` for the reasons behind big decisions (ADR 0001 initial decisions, ADR 0002 platform admin and app version).
