# Authentication

Login, refresh-token rotation and logout live in `internal/modules/session`. Passwords are hashed in
`internal/platform/password`, access tokens in `internal/platform/auth` (PASETO v4.local).

## Endpoints (`/api/v1/auth`)

| Method and path | Body | Result |
|---|---|---|
| `POST /login` | `{"tenant_code"?, "username", "password"}` | tokens |
| `POST /platform/login` | `{"username", "password"}` | tokens (platform admins only) |
| `POST /refresh` | `{"refresh_token"}` | new tokens, the old refresh token is revoked |
| `POST /logout` | `{"refresh_token"}` | `204`, idempotent |
| `GET /me` | header `Authorization: Bearer <access token>` | `user_id`, `level`, `tenant_id` |

Token response: `{"access_token", "token_type": "Bearer", "expires_in": <seconds>, "refresh_token"}` with
`Cache-Control: no-store`. Errors always look like `{"error": {"code", "message"}}`.

## How the tenant is found at login

In order: subdomain (`tokoa.app.com`), then the `X-Tenant-Code` header, then `tenant_code` in the body.
After login the tenant comes **only** from the verified token (`tid`); headers and subdomain are ignored.
Platform admins belong to no tenant and use their own endpoint, so a missing tenant code can never turn into a platform login.

## Security decisions

- **argon2id** (64 MiB, 3 iterations, 2 threads) in PHC format. Parameters travel with the hash, so they can be raised:
  a successful login re-hashes an older hash with the current parameters. Lower `MemoryKiB` in
  `password.DefaultParams` if many logins can happen at once on a small machine.
- **One error for every failed login** (`invalid_credentials`): wrong tenant, wrong user, wrong password, inactive user and
  suspended tenant look the same. When the account does not exist a dummy hash is still computed, so timing does not reveal usernames.
- **Refresh tokens** are random (256 bit), only their SHA-256 is stored, and each can be used **once**. The token starts with
  its scope (`<tenant_id>.` or `platform.`), so the server knows which tenant to look in before RLS applies.
- **Reuse detection**: presenting an already used refresh token revokes all sessions of that user. The revocation is
  committed, not rolled back with the error. Two concurrent refreshes with one token: exactly one wins.
- **Access tokens** live `ACCESS_TOKEN_TTL` (default 15 min) and are not checked against the database on each request.
  After logout or revocation an access token stays valid until it expires. Keep the TTL short.
- Request bodies are limited to 16 KiB, passwords to 1024 bytes (bounds the work an attacker can force).

## Creating the first accounts

There is no public sign-up. Use the CLI against the database of the active `.env.<mode>`:

```bash
tenantctl tenant create --code acme --name "Acme Inc"

read -rsp "Password: " PW; echo
printf '%s\n' "$PW" | tenantctl user create  --tenant acme --username admin --email admin@acme.test --name "Acme Admin" --password-stdin
printf '%s\n' "$PW" | tenantctl admin create --username root --email root@example.test --name "Platform Admin" --password-stdin
unset PW
```

The password is read from stdin only (flags end up in shell history). New passwords need at least 10 characters.
Run with `go run ./cmd/cli ...` or `make build` first and use `bin/tenantctl`.

## Try it

```bash
make run
curl -s localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' -H 'X-Tenant-Code: acme' \
     -d '{"username":"admin","password":"<the password>"}'
curl -s localhost:8080/api/v1/auth/me -H "Authorization: Bearer <access_token>"
```

## Not built yet

- Rate limiting and lockout against password guessing (planned with Redis).
- Password change and reset.
- Authorization (RBAC with the endpoint whitelist) on top of `middleware.Authenticate`: the next phase.
