# gRPC AuthService

Other services (the microservice template, a worker, a gateway) must not hold the `PASETO_KEY` and must not read the
tenant database. They ask the AuthService instead:

| RPC | question | answer |
|---|---|---|
| `Validate(access_token)` | who owns this token? | `user_id`, `tenant_id` (empty for a platform admin), `level` |
| `Authorize(access_token, method, path)` | may this user call this endpoint? | `allowed` true or false |

The contract is `api/proto/tenantapp/auth/v1/auth.proto`; the generated code is committed in
`internal/transport/grpc/gen/` (regenerate with `make proto`, which needs `buf`, `protoc-gen-go` and `protoc-gen-go-grpc`).

## One decision maker

`Authorize` calls the same `rbac.Service` as the REST middleware (default deny, endpoint = method + route pattern, groups,
grants, platform / tenant scope, inactive users). REST and gRPC cannot disagree; `TestGRPC_AgreesWithREST` asks both the
same questions on a real PostgreSQL.

- `path` is the **route pattern**, exactly as in the endpoints table (`/api/v1/groups/:id`), not the URL of the request
  (`/api/v1/groups/0194...`). A caller that sends the raw URL gets `allowed=false`.
- The endpoint must exist in the `endpoints` table (`tenantctl endpoint sync`) and be granted to the user's group.
  Another service registers its own routes the same way.
- The tenant always comes from the verified token. The request cannot name a tenant.

## Answers

| result | meaning | what the caller does |
|---|---|---|
| `allowed=true` | granted (or public, or a platform admin on a platform endpoint) | let the request through |
| `allowed=false` | refused: not granted, wrong level, endpoint unknown | answer 403 |
| `Unauthenticated` | token forged or expired, or the user no longer exists or is inactive | answer 401 |
| `InvalidArgument` | no token, unknown method, path that does not start with `/` | fix the call |
| `Internal` | the decision could not be made (database down, a panic) | **treat as refused** (fail closed), maybe retry |
| `Unavailable` | the dedicated database of this tenant cannot be reached ([docs](database-per-tenant.md)) | **treat as refused**, retry later |

The reason for a refusal goes to the server log (`access_denied`, `transport=grpc`), never to the caller. Tokens are never logged.

`Validate` only verifies the token (like the REST `Authenticate` middleware): an access token stays valid until it expires,
even after logout. Keep `ACCESS_TOKEN_TTL` short, and call `Authorize` when the user's state matters.

## Run it

```bash
make run-grpc                 # APP_ENV=development go run ./cmd/grpc, listens on GRPC_ADDR (default :9090)
grpcurl -plaintext localhost:9090 list                         # development only: reflection is on
grpcurl -plaintext -d '{"access_token":"v4.local...."}' localhost:9090 tenantapp.auth.v1.AuthService/Validate
grpcurl -plaintext -d '{"access_token":"v4.local....","method":"GET","path":"/api/v1/groups"}' \
        localhost:9090 tenantapp.auth.v1.AuthService/Authorize
```

`cmd/grpc` needs the database and `PASETO_KEY` (the same values as the REST server), nothing else: no Redis, no SMTP.
Docker: the image holds both binaries; `docker compose ... --profile grpc up` starts it (entrypoint `/auth-grpc`, port `GRPC_PORT`, default 9090).

| variable | default | |
|---|---|---|
| `GRPC_ADDR` | `:9090` | listen address |
| `GRPC_TLS_CERT_FILE`, `GRPC_TLS_KEY_FILE` | empty | server certificate and key (PEM); set both or neither |
| `GRPC_TLS_CLIENT_CA_FILE` | empty | when set, every client must present a certificate signed by this CA (mutual TLS) |
| `GRPC_ALLOW_PLAINTEXT` | `false` | production only: serve without TLS because a service mesh encrypts (cannot be combined with a certificate) |

## Security

- The AuthService is an **internal** service. Do not expose it to the internet; put it on the private network only.
- In production `cmd/grpc` refuses to start without TLS (or `GRPC_ALLOW_PLAINTEXT=true`). Use mutual TLS
  (`GRPC_TLS_CLIENT_CA_FILE`) so that only your own services can call it. Minimum TLS version is 1.2.
- Reflection is on outside production only.
- Limits: 16 KiB per request, 5 s per call, 1000 concurrent streams per connection. A token longer than 4096 bytes is refused.
- The decision caches (endpoints 60 s, users and grants 30 s) are per process. A change made through the REST server or
  the CLI reaches `cmd/grpc` after at most one TTL. A user who is deactivated can therefore still be allowed for up to 30 s.
- Run the database role without `BYPASSRLS` (not a superuser): RLS is what keeps one tenant's data away from another.

## Calling it from Go (another service)

```go
conn, err := grpc.NewClient("auth.internal:9090", grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
client := authv1.NewAuthServiceClient(conn)
res, err := client.Authorize(ctx, &authv1.AuthorizeRequest{AccessToken: token, Method: "GET", Path: "/api/v1/groups/:id"})
if status.Code(err) == codes.Unauthenticated { /* 401 */ } else if err != nil || !res.GetAllowed() { /* 403 */ }
```

Set a deadline on every call. On `Internal`, `Unavailable` or a timeout, refuse the request.

## Tests

`internal/transport/grpc`: verdicts, fail closed (error, transaction failure, panic), input limits, tokens never logged, health,
TLS and mutual TLS on a real TCP port. `test/integration/grpc_test.go`: real `rbac.Service`, real transactions under RLS, tenant
isolation, platform admins, inactive users, and agreement with the REST API.
