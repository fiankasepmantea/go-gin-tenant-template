# Skill: writing tests

- Table-driven, subtests with `t.Run`. Case names in the same language as the surrounding code.
- Use `t.Setenv`, `t.TempDir`, `t.Helper`. Do not depend on test order or the system clock (inject `func() time.Time`).
- Unit tests use no network and no database. Tests that need Postgres go in `test/integration/` with the `integration` build tag, run via docker compose.
- Handlers: `httptest.NewRecorder` + a router from `rest.NewRouter`.
- Configuration: see `internal/config/config_test.go` for the env isolation pattern.
- Always: `make test-race`. Coverage: `make cover`.
- Tests for bugs: write the failing test first, fix the bug, and keep the test.
