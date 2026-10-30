# Skill: dependency injection

Go does not need a container. Wire manually in `cmd/*/main.go` (the composition root).

```go
repo := user.NewRepository(db)
svc  := user.NewService(repo, tokens)
h    := user.NewHandler(svc)
user.Register(api, h)
```

## Rules

- Constructors `NewX(deps...) *X` accept small interfaces and return concrete structs.
- No global variables, no service locator, no `init()`.
- `context.Context` and `*slog.Logger` are passed explicitly, never stored globally.
- Create an interface only when there is more than one implementation or it needs to be mocked in tests.
- Mocks: `go.uber.org/mock`; or a handwritten fake if there are only 1-2 methods.
- If the wiring in `main` grows too large, split it into functions like `wireUser(db) ...`, not a DI framework.
