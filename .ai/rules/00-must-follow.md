# Mandatory rules

- Follow the official Go guidelines (https://go.dev/doc/), `gofmt`, `go vet`. Layout: `docs/architecture.md` (go.dev/doc/modules/layout, server project: code in `internal/`, binaries in `cmd/`).
- Organize by feature in `internal/modules/<name>`. Do not create `pkg/`, `utils`, `common`, `helpers`.
- Dependency direction is one-way: `transport -> modules (handler -> service -> repository) -> platform/core`. `platform` must not import `modules`.
- Queries on tenant-scoped tables **must** use `tenant_id` from the context (`tenant.IDFrom(ctx)`). Never from the body, query, or headers after login.
- Primary key: `idgen.New()` (UUID v7, sequential). Refresh token: `idgen.NewOpaqueToken()`.
- `context.Context` is the first argument. Errors are wrapped with `%w` and compared with `errors.Is/As`.
- Log with `slog` key-value. Do not log passwords, tokens, or `PASETO_KEY`.
- New dependency: the `go` line in its go.mod must be <= 1.25.0, then record it in `docs/libraries.md`.
- Table-driven tests. New behavior = new tests. `make test-race` must be green.
- No `log.Fatal`/`panic` outside `main`; no `init()` with side effects.

- Behavior or structure changes must update their documentation in the same PR: check off items in `.ai/plans/`, update skills in `.ai/skills/` if the pattern changes, add an ADR in `docs/adr/` for architectural decisions.
- Tenant-scoped tables: the model implements `SetTenantID` and uses `core.Repository`; do not write `db.Where("tenant_id = ?")` yourself. Custom queries go through `repo.Conn(ctx)`.
- Request flows that touch tenant-scoped tables are wrapped in `database.InTenantTx` (sets the RLS variables from the token). An error inside the transaction rolls it back.
- Tests that need PostgreSQL: `test/integration` with the `integration` build tag (`make test-integration`).

## Prohibited

- Adding a dependency that requires Go > 1.25.0 without the repo owner's approval.
- Committing `go.work`, build outputs, or secret files.
- Modifying a migration that has already been applied. Create a new migration.
