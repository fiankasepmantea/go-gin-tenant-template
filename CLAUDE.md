# Rules for AI assistants and contributors

The full guide is in the `.ai/` folder (start from `.ai/README.md`: rules, skills, plans).

Read first: `docs/architecture.md`, `docs/multi-tenancy.md`, `docs/libraries.md`.

## Required

- Follow the official Go guidelines (https://go.dev/doc/) and `gofmt`/`goimports`. Run `make test lint` before finishing.
- Layout follows `docs/architecture.md`. Organize by feature; no `pkg/`, `utils`, `common`.
- Every query on a tenant-scoped table **must** use `tenant_id` from the context (`tenant.IDFrom`). Never take it from the body or headers.
- Primary keys are created with `idgen.New()` (UUID v7). Refresh tokens use `idgen.NewOpaqueToken()` (random, not sequential).
- Wrap errors with `%w`; compare with `errors.Is/As`. `context.Context` is always the first argument.
- Log with `slog` (key-value); never log passwords, tokens, or PASETO_KEY.
- New dependency: check the `go` line in its go.mod (must be ≤ 1.25.0) and record it in `docs/libraries.md`.
- Table-driven tests; add new tests for every new behavior.

## Forbidden

- Reading, creating, or modifying real env files (`.env`, `.env.development`, `.env.production`, etc.). Only modify `*.example`, and never put secret values in them.
- `git add -f` on ignored files, or bypassing hooks (`--no-verify`).
- Adding a dependency that needs Go > 1.25.0 without approval.
- `log.Fatal`/`panic` outside `main`; `init()` with side effects.
- Committing `.env`, `go.work`, or secrets.
