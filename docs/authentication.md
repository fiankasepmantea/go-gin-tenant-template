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
| `POST /password/forgot` | `{"tenant_code"?, "identifier"}` (username or e-mail) | `202`, always the same answer |
| `POST /platform/password/forgot` | `{"identifier"}` | `202`, always the same answer |
| `POST /password/reset` | `{"token", "new_password"}` | `204`; the token is from the e-mail |
| `POST /password/change` | `{"current_password", "new_password"}` + bearer token | `204`; all sessions end |

Token response: `{"access_token", "token_type": "Bearer", "expires_in": <seconds>, "refresh_token"}` with
`Cache-Control: no-store`. Errors use the envelope of [http-api.md](http-api.md):
`{"success": false, "code": 401, "error": {"code", "message"}}`.

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
- **Rate limiting** (see below) answers `429 rate_limited` with a `Retry-After` header.
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

## Rate limiting

Failed logins are counted in fixed windows (`RATE_LIMIT_ATTEMPTS` per `RATE_LIMIT_WINDOW`, default 5 per 15 minutes):

| Counter | Limit | Stops |
|---|---|---|
| tenant + username + IP | N | guessing one account from one address |
| IP | 6 x N | one address trying many accounts |
| tenant + username | 10 x N | many addresses trying one account (highest: it is also a way to lock an account for a while) |

- Only failed logins count (an unknown tenant or user counts like a wrong password, so the limit tells nothing).
  A success clears the first counter and **never** the IP counter, so logging in to an account of your own does
  not reset the address limit.
- A blocked login is refused **before** the password is looked at, also when the password is right.
- Counters live in Redis (`REDIS_ADDR`), shared by all replicas. Without it they live in the process memory,
  which is correct only for one replica (a warning is logged in production).
- If Redis is down the limiter **fails open**: logins keep working and the error is logged. It is a second line
  of defense, not the lock on the door.
- `IP` is `c.ClientIP()`. Behind a proxy or load balancer configure Gin's trusted proxies, otherwise every
  client has the address of the proxy (and shares one counter) or a client can choose its own address with
  `X-Forwarded-For`.
- Refresh is not rate limited: its tokens are 256-bit random values.

## Password change and reset

**Forgot / reset.** `forgot` always answers `202` with the same message, whether or not the account exists, and
the e-mail is sent in the background, so neither the answer nor its timing tells who has an account. If the
account exists and is active, a token is created (30 minutes by default, `PASSWORD_RESET_TTL`), only its SHA-256
is stored, and a message with the link is sent. A new request cancels the older ones; the token works once; the
token carries its tenant (like refresh tokens), so it cannot be used under another tenant. A successful reset
ends **all** sessions of the user. Requests are rate limited per address and per account (every request counts,
because each may send an e-mail).

**Change.** For a signed-in user: the current password is checked (wrong guesses are rate limited per user, so a
stolen access token cannot be used to guess the password), the new one must pass the policy and differ from the
current one, and all sessions end, including the current one. The client logs in again. An access token that was
already issued keeps working until it expires, like after logout.

**E-mail.** `internal/platform/mailer` sends plain-text mail through SMTP (`net/smtp`): `starttls` (587), `tls` (465)
or `none` (a local test server only; refused in production, and refused with a password for any non-local host).
There is never a fallback to plain text. Configure `SMTP_*`, `APP_NAME` and `PASSWORD_RESET_URL` (the page of your
front end that takes the token; placeholders `{token}` and `{tenant}`; empty = the e-mail contains the token
itself). Without `SMTP_HOST` the message is written to the log, **with the reset link outside production only**.
For local work use Mailpit: `make docker-up PROFILES="mail"`, `SMTP_HOST=localhost SMTP_PORT=1025
SMTP_SECURITY=none`, and read the mail at http://localhost:8025.

To send mail through a provider API (SES, Resend, ...) or to deliver the token another way (SMS), implement
`mailer.Mailer` or `session.Notifier`; nothing else changes.

## Not built yet

- gRPC `AuthService` (Validate/Authorize) using the same RBAC service.
- A way to reset the password of a user from the CLI (use `forgot` for now).
