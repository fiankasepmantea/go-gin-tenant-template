# Users API (`/api/v1/users`)

A tenant administrator manages the users of their own tenant through the API. Before this, users could only be
created with the CLI (`tenantctl user create`).

| Method | Path | Purpose |
|---|---|---|
| GET | `/users` | list users (filters `username`, `email`, `name`, `is_active`, `group_id`; sort `username`, `email`, `name`, `created_at`; `page`, `per_page`, `from`, `to`) |
| GET | `/users/:id` | one user |
| POST | `/users` | create a user: `username`, `email`, `name`, `password` (at least 10 characters), `group_id` (optional), `is_active` (default true) |
| PATCH | `/users/:id` | change `email`, `name`, `group_id` (`""` removes the user from the group), `is_active` |
| DELETE | `/users/:id` | delete (soft delete) |
| POST | `/users/:id/password` | an administrator sets a new password: `{"password": "..."}` |

## Rules

- **Tenant isolation**: the tenant comes from the token, not from the request. The repository applies the tenant
  filter and PostgreSQL RLS enforces it again. Users of other tenants are invisible (404). Platform admins never
  show up.
- **Passwords** are never returned. A new password must pass the same policy as a password change (at least 10 and
  at most 1024 characters) and is hashed with argon2id.
- Deactivating (`is_active=false`), deleting, or setting a new password **ends all sessions** of that user
  (their refresh tokens are revoked).
- An administrator **cannot deactivate or delete their own account** (403), so a tenant cannot lose its last
  administrator by accident.
- `username` and `email` are unique per tenant (case-insensitive): a clash is 409. `group_id` must be a group of
  the same tenant: anything else is 422.
- Access is decided by RBAC like every other endpoint (default deny). After deploying:

```bash
go run ./cmd/cli endpoint sync
go run ./cmd/cli group grant --tenant <code> --group <name> --all     # or specific endpoints, e.g. "GET /api/v1/users"
```

Groups (`/api/v1/groups`) already have full CRUD; the permissions of a group are managed through
`/api/v1/groups/:id/endpoints` (see `docs/rbac.md`).
