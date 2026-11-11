# Skill: creating a new module

**Start with the generator** (details: `docs/generator.md`):

```
make new-module name=product fields="name:string! price:bigint! active:bool" unique=name
```

It writes the migration, model, repository, CRUD handler, unit test and integration test, and registers the module in
`internal/app/app.go`. Then apply the migration, `make sync-endpoints`, grant the endpoints to a group, and add the
business rules (service, custom queries). The steps below describe what the generator produces and what to do by hand
when a module does not fit the CRUD template.

A module = one complete feature in `internal/modules/<name>/`.

## Steps

1. Migration first: `make migrate-new name=create_<table>`. Standard columns: `<t>_id` (UUID, PK), `<t>_unique`, `tenant_id`, `created_at`, `updated_at`, `deleted_at`. Indexes: `tenant_id` as the first column; partial unique `WHERE deleted_at IS NULL`.
2. Module files (package name = folder name, lowercase, singular):
   - `model.go` GORM struct with column tags, `gorm.DeletedAt` for soft delete, and the methods `TableName`, `PrimaryKey`, `GetID`, `SetID`, plus `SetTenantID` if tenant-scoped. Example: `internal/modules/group/model.go`.
   - `repository.go` embeds `*core.Repository[T, *T]` (CRUD, pagination, automatic tenant filter) and adds custom queries via `repo.Conn(ctx)`. Examples: `internal/modules/group/repository.go`, `internal/modules/user/repository.go`.
   - `service.go` business logic; receives the repository interface via the constructor.
   - `handler.go` thin Gin handler: bind, validate, call the service, write the response.
   - `routes.go` function `Register(r gin.IRouter, h *Handler)`.
   - `*_test.go` per file.
3. Register the handler in `app.Modules` (`internal/app/app.go`), run `make sync-endpoints` and grant the endpoints to a group (default deny, see `docs/rbac.md`).
4. Test tenant isolation in `test/integration` (copy the pattern from `repository_test.go`), then `make fmt vet test-race test-integration`.

## Patterns

- Handlers do not call GORM. Services do not know about `gin.Context`.
- Interfaces are defined on the consumer side (the service), not on the implementation side.
- Do not edit the generated files to remove tenant isolation (`tenant_id`, RLS policy, the `chain...` middleware); add to them.
