# Skill: creating a new module

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
3. Register the route in `internal/transport/rest` and its endpoint in the whitelist (the `endpoint` module).
4. Test tenant isolation in `test/integration` (copy the pattern from `repository_test.go`), then `make fmt vet test-race test-integration`.

## Patterns

- Handlers do not call GORM. Services do not know about `gin.Context`.
- Interfaces are defined on the consumer side (the service), not on the implementation side.
- A code template will be available via `tenantctl make:module <name>` (generator phase); until then, copy the pattern from the first completed module.
