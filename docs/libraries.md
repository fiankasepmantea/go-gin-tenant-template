# Libraries and Go version

## Go version policy

- `go.mod` uses **`go 1.26.0`**: golang.org/x/net v0.60.0 (HTTP/2 security fixes) requires Go 1.26. The other dependencies (gin v1.12.0, gorm postgres driver v1.6.3, pgx v5.11.0) only need 1.25.0.
  According to the [toolchain documentation](https://go.dev/doc/toolchain) the `go` line is the minimum version.
- Development and CI use the latest patch release of the 1.26 series (`1.26.x`) for security fixes.
- Do not raise the minimum Go version only for convenience; raise it for a security fix (as done for x/net v0.60.0) or when a needed library requires it, and record the reason here and in the ADR.

## Currently used (in go.mod)

| Library | Version | Notes |
|---|---|---|
| `github.com/gin-gonic/gin` | v1.12.0 | HTTP framework (48% of Go users, JetBrains 2025). Also used in `simple-bank` |
| `gorm.io/gorm` + `gorm.io/driver/postgres` | v1.31.2 + v1.6.3 | ORM; the driver uses pgx |
| `aidanwoods.dev/go-paseto` | v1.6.0 | PASETO **v4**. Import path `aidanwoods.dev`, GitHub repo `aidantwoods` |
| `github.com/google/uuid` | v1.6.0 | `NewV7()` for sequential primary keys |
| `github.com/joho/godotenv` | v1.5.1 | Reads `.env` (used in all previous Go repos) |
| `github.com/spf13/cobra` | v1.10.2 | CLI |
| `golang.org/x/crypto` | v0.48.0 | `argon2` for password hashing (argon2id). Already a transitive dependency of go-paseto |
| `github.com/jackc/pgx/v5` | v5.10.0 | Used directly only for `pgconn.PgError` (unique violation 23505 in the CLI); otherwise via the gorm driver |
| `log/slog` | stdlib | Structured logging |

## Pins for upcoming phases (versions checked against Go 1.25; all still work on 1.26)

| Library | Use | Latest (not adopted yet) |
|---|---|---|
| `google.golang.org/grpc` | v1.84.0 | v1.85.0 |
| `google.golang.org/protobuf` | v1.36.12 | |
| `github.com/redis/go-redis/v9` | v9.22.0 | v9.23.0 |
| `github.com/go-playground/validator/v10` | v10.30.4 | v10.30.5 |
| `github.com/golang-migrate/migrate/v4` | v4.19.1 | latest version needs go 1.25.11 |
| `github.com/prometheus/client_golang` | v1.23.2 | v1.25.0 |
| `github.com/hibiken/asynq` | v0.26.0 | |
| `github.com/rabbitmq/amqp091-go` | v1.16.0 | |
| `github.com/segmentio/kafka-go` | v0.4.51 | more active alternative: `twmb/franz-go` v1.21.7 |
| `go.uber.org/mock` | v0.6.0 | `golang/mock` is archived |
| `github.com/stretchr/testify` | v1.12.1 | |

## Consistent with previous repos

`simple-bank` (gin, validator, grpc, paseto, golang-migrate, asynq, prometheus, amqp091, kafka-go), `go-micro`
(chi, grpc, amqp091), `go-ambassador` (fiber, gorm, jwt), `sociomile-support-app` (gin, gorm, go-redis).
Intentional differences: PASETO v4 replaces `o1egl/paseto` v1.0.0 (last commit 2022), `zerolog` is replaced by `slog`,
`golang/mock` is replaced by `go.uber.org/mock`.

## How to update

1. `go list -m -u all` to see new versions.
2. Check the `go` line in that library's `go.mod`; if > 1.26.0, hold back.
3. `go get <modul>@<versi> && go mod tidy && make test && make vuln`.
