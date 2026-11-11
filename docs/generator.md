# Generator: `make:module` and `make:migration`

The generator writes the boring 90% of a new module so that it is correct from the first run: tenant isolation (row
level security), pagination, filters, sorting, soft delete and RBAC come with it. You add the business rules.

Run everything from the project root (the directory with `go.mod`).

## Create a module

```bash
make new-module name=product fields="name:string! price:bigint! active:bool note:text" unique=name
# the same without make:
go run ./cmd/cli make:module product --field 'name:string!' --field 'price:bigint!' --field active:bool --unique name
```

Check first what would be created, nothing is written: add `--dry-run`.

### Fields

`--field name:type` or `--field name:type!` (the `!` makes the value required).

| type | Go | PostgreSQL | notes |
|---|---|---|---|
| `string` | `string` | `text` | up to 255 characters, trimmed; filter and sort |
| `text` | `string` | `text` | up to 5000 characters, trimmed; no filter or sort |
| `int` | `int32` | `integer` | filter and sort |
| `bigint` | `int64` | `bigint` | filter and sort (use it for money in the smallest unit) |
| `float` | `float64` | `double precision` | sort |
| `bool` | `bool` | `boolean` | filter |
| `time` | `time.Time` | `timestamptz` | RFC 3339, sort |
| `uuid` | `string` | `uuid` | validated; filter |

- Required (`!`): `NOT NULL`, no default, and the create request is rejected with `422 validation_failed` when it is missing.
  Required numbers and booleans are pointers in the request, so `0` and `false` are valid values.
- Optional: text, numbers and booleans are `NOT NULL` with the zero value as default (`''`, `0`, `false`); optional `time` and `uuid`
  are `NULL` when not sent.
- `--unique name` makes a required field unique per tenant among the records that are not deleted (case-insensitive for
  `string`). A duplicate is answered with `409 conflict`. Repeat the flag for more fields (each one is unique on its own).
- `--table categories` overrides the table name. The default is the plural of the module name (`product` becomes `products`,
  `category` becomes `categories`); only the regular English rules are built in.

The module name is singular, lowercase letters and digits (`product`, `invoice`). Names that would clash with a template
package, a Go keyword or a table of the template (`user`, `group`, `session`, `tenant`, ...) are refused, and so are field names
that are SQL keywords or that the template creates itself (`id`, `tenant_id`, `created_at`, `updated_at`, `deleted_at`, `<name>_id`, `<name>_unique`).

### What it writes

| file | content |
|---|---|
| `migrations/NNNNNN_create_<table>.up.sql` / `.down.sql` | table with `<name>_id` (uuid), `<name>_unique` (identity), `tenant_id` (FK, NOT NULL), your columns, timestamps, `deleted_at`, an index on `tenant_id`, the unique indexes, the `updated_at` trigger, `ENABLE` + `FORCE ROW LEVEL SECURITY` and the `tenant_isolation` policy |
| `internal/modules/<name>/model.go` | GORM model with the methods `crud` needs |
| `internal/modules/<name>/repository.go` | `core.Repository` (automatic tenant filter); add your queries here |
| `internal/modules/<name>/handler.go` | `crud.Handler` with input DTOs, filter and sort whitelists, `Build` and `Changes` |
| `internal/modules/<name>/handler_test.go` | the 7 routes exist; validation of the inputs |
| `test/integration/<name>_test.go` | on PostgreSQL: default deny, grant, create, duplicate, list, get, update, validation, tenant B cannot see or change tenant A's record, soft delete |
| `internal/app/app.go` | the import and `<name>.NewHandler(d.DB, chain...)` in `app.Modules` (above the `// tenantctl:modules` comment: keep that comment) |

The migration number is the highest number in `migrations/` plus one, so migrations made with `make migrate-new` or by hand and
migrations of the generator share one sequence.

The generator never overwrites: if a file of the module or a migration that creates the table exists, nothing is written at all.
The same holds if `app.go` cannot be patched (for example the marker comment was removed): then add the module by hand as
described in `.ai/skills/create-module.md`.

### After generating

1. Read the migration. Change it if a column needs a different type; the model and the DTOs are yours to edit as well.
2. Apply it: `make migrate-up` (a dev database that exists already: `psql ... < migrations/NNNNNN_create_<table>.up.sql`).
3. `make sync-endpoints`: the new routes are registered in the whitelist. Until a group is granted they answer `403` (default deny).
4. Grant them: `tenantctl group grant --tenant <code> --group <group> --all` or `--endpoint "GET /api/v1/<table>"` (see [rbac.md](rbac.md)).
5. `make test lint`, and `make test-integration` with the database running.

### Generated endpoints

`GET /api/v1/<table>` (list, `?page`, `?per_page`, `?sort=name` or `-name`, `?<field>=value`, `?from=&to=` on `created_at`),
`GET /<table>/:id`, `GET /<table>/show_by/:column/:value`, `GET /<table>/all_by/:column/:value`, `POST /<table>`,
`PATCH /<table>/:id`, `DELETE /<table>/:id` (soft delete). Details: [http-api.md](http-api.md).

### Known limits

- An optional `time` or `uuid` field can be set but not cleared with PATCH (`null` means "not sent"). Edit `Changes` if you need it.
- Relations between modules are written by hand: a foreign key should be composite, `(tenant_id, other_id)` referencing the
  `UNIQUE (tenant_id, <name>_id)` constraint that every generated table has, so a record cannot point to another tenant's record.
- At most 30 fields; a wider table is written by hand.

## Create an empty migration

```bash
make new-migration name=add_price_to_products
# or: go run ./cmd/cli make:migration add_price_to_products
```

Creates `migrations/NNNNNN_add_price_to_products.up.sql` and `.down.sql` with the next number. Name: lowercase letters, digits and underscores.

## Tests of the generator

`internal/generator` has unit tests (validation, naming, numbering, registration, the SQL it writes) and
`TestGeneratedModuleCompiles`, which generates two modules into a copy of the repository and runs `go build`, `go vet`
(also with `-tags integration`) and the generated tests. It runs in CI (`GENERATOR_E2E=1`), so a change to the templates that
produces code that does not compile fails the build.
