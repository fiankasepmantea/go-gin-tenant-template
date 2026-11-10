# HTTP API conventions

How every endpoint of the template answers, and how a module gets standard CRUD endpoints with
`crud.Handler` (the "BaseHandler"). Code: `internal/transport/rest/{response,request,crud,middleware}`.

## Response envelope

Success:

```json
{"success": true, "code": 200, "data": {...}, "message": "optional"}
```

Error:

```json
{"success": false, "code": 422,
 "error": {"code": "validation_failed", "message": "One or more fields are not valid.",
           "details": [{"field": "Name", "rule": "required"}]}}
```

- `code` at the top level is the HTTP status. `error.code` is the **stable** machine-readable code: clients
  branch on it, never on the message text. `details` appears only for `validation_failed`.
- Messages are English. The unexpected-error message is always generic; the real cause is only in the server
  log (the access log records it for 5xx responses, with the request id).
- Unknown routes (404), wrong methods (405) and panics (500) use the same envelope.
- Exception: the `/auth/login`, `/auth/platform/login` and `/auth/refresh` success bodies keep the OAuth 2
  token shape (`access_token`, `token_type`, ...), see [authentication.md](authentication.md). Their errors use
  the envelope above.

### Error codes

| `error.code` | HTTP | Meaning |
|---|---|---|
| `invalid_request` | 400 | Malformed body, unknown or invalid query parameter, nothing to update |
| `unauthorized` | 401 | Missing or invalid access token |
| `invalid_credentials` | 401 | Wrong tenant, username or password (never says which) |
| `invalid_refresh_token` | 401 | Refresh token unknown, expired or already used |
| `token_expired` | 401 | Access token expired; refresh it |
| `forbidden` | 403 | Authenticated but not allowed (also: a platform token on a tenant resource) |
| `not_found` | 404 | No such record or route. A record of another tenant is also `not_found` |
| `method_not_allowed` | 405 | |
| `conflict` | 409 | The record already exists (unique constraint) |
| `payload_too_large` | 413 | Body bigger than the limit (default 1 MiB) |
| `validation_failed` | 422 | Well-formed, but a field is not acceptable; also a reference to a missing record |
| `rate_limited` | 429 | Reserved for login rate limiting |
| `internal_error` | 500 | Unexpected |
| `service_unavailable` | 503 | |

`token_expired`, `rate_limited` and `service_unavailable` are reserved: they are in the catalog but nothing emits
them yet (an expired access token is currently `unauthorized`; login rate limiting is a later phase).

The catalog is `response/codes.go`; a test keeps the status and message tables complete. New codes may be
added; existing codes and statuses never change.

`response.Fail(c, err)` is the single place that turns an error into a response (`core.ErrNotFound` ->
404, `core.ErrConflict` -> 409, a validator error -> 422, ...). Services can return `response.NewError(code,
message)` (or wrap it with `%w`) to choose the response themselves.

### Paginated lists

```json
{"success": true, "code": 200, "data": {
  "total_data": 45, "total_pages": 3, "curr_page": 2, "next_page": 3, "prev_page": 1,
  "data_per_page": 20, "data": [...]}}
```

The field names match the earlier CI4 template. `prev_page` is absent on the first page; on the last page
`next_page` stays on the last page.

## Standard endpoints (`crud.Handler`)

| Method and path | Purpose |
|---|---|
| `GET /things` | List: pagination, filters, sort, `created_at` range |
| `GET /things/:id` | One record (`:id` must be a UUID, otherwise 404) |
| `GET /things/show_by/:column/:value` | First record where `column = value` |
| `GET /things/all_by/:column/:value` | List where `column = value` (same query options) |
| `POST /things` | Create, `201` with the new record |
| `PATCH /things/:id` | Update the fields that were sent, `200` with the record |
| `DELETE /things/:id` | Soft delete, `200` with `message` |

Differences from the CI4 template, on purpose: standard HTTP verbs (`PATCH`/`DELETE` instead of
`POST /update/:id` and `DELETE /delete/:id`); the list is always paginated (never "all rows");
`per_page` instead of `limit` (maximum 100); filters are query parameters.

### List query

| Parameter | Example | Notes |
|---|---|---|
| `page`, `per_page` | `?page=2&per_page=50` | Positive integers. Default 1 and 20, `per_page` is capped at 100 |
| `sort` | `?sort=-created_at,name` | `-` = descending. The primary key is always the last tie-breaker, so paging is stable |
| `from`, `to` | `?from=2026-01-01&to=2026-01-31` | Range on `created_at`, `to` inclusive. A date or an RFC 3339 timestamp, UTC |
| `<column>` | `?name=admin` | Equality filter; only columns the resource lists as filterable |

Anything else is rejected with `400 invalid_request`. This is deliberate: a typo such as `?nmae=x` must
not silently return unfiltered data. A parameter given twice, and a query string with `;`, are rejected for the
same reason.

## Adding a resource

```go
type groupCreate struct {
	Name        string `json:"name" binding:"required,max=100"`
	Description string `json:"description" binding:"max=500"`
}
type groupUpdate struct { // pointers: nil = "not sent"
	Name        *string `json:"name" binding:"omitempty,min=1,max=100"`
	Description *string `json:"description" binding:"omitempty,max=500"`
}

h := crud.New(crud.Config[group.Group, *group.Group, groupCreate, groupUpdate]{
	Store:      group.NewRepository(db),         // anything with Create/GetByID/List/Update/Delete
	Filterable: []string{"name"},                // columns a client may filter on
	Sortable:   []string{"name", "created_at"},  // columns a client may sort on
	Build: func(in groupCreate) *group.Group { return &group.Group{Name: in.Name, Description: in.Description} },
	Changes: func(in groupUpdate) map[string]any { /* only the fields that were sent */ },
})

// in RegisterRoutes(rg):
h.Register(rg.Group("/groups", middleware.Authenticate(mgr), middleware.TenantTx(db)))
```

`Build == nil` removes `POST`, `Changes == nil` removes `PATCH` (a read-only resource).

### Safety rules (do not bypass)

1. **Input types, not models.** The body is decoded into `groupCreate`/`groupUpdate`, never into the model, so
   a client cannot set `id`, `tenant_id` or timestamps (mass assignment). `core.Repository` sets the ID and
   `tenant_id` itself.
2. **Whitelists.** Only `Filterable`/`Sortable` columns are usable. Column names are also checked to be plain
   identifiers and are always quoted by the repository (defense in depth); values are always bound parameters.
3. **Tenant from the token only.** `middleware.Authenticate` puts the tenant of the verified token in the
   context; the repository filters on it, and PostgreSQL RLS is the second layer.
4. **`middleware.TenantTx` is required** on every route that touches tenant data. It opens one transaction per
   request with the RLS session variables; without it RLS fails closed and every query sees no rows. It commits
   when the request ends below 400 and rolls back otherwise.
5. **Authorization is not in `crud`.** The handler authenticates nothing by itself; mount it on a group that
   has the middleware. Per-group endpoint permissions are `middleware.Authorize`, the last middleware of the
   group, after `TenantTx` (see [rbac.md](rbac.md)). `internal/app` builds that chain for every module.

Database errors are mapped in one place (`core`): unique violation -> `ErrConflict` (409), foreign key ->
`ErrReference` (422), wrong type / NOT NULL / CHECK -> `ErrInvalidInput` (400). A unique-violation error still
wraps the driver error, so code that needs it (the CLI) can use `errors.As`.
