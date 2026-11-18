# Operator Runbook

Bahasa Indonesia: [operator.id.md](operator.id.md). This English file is the source of truth; update both together.

Other guides: [Operator](operator.md) · [Platform Admin](platform-admin.md) · [Client Admin](client-admin.md) · [API Integration](api-integration.md) · [Developer](developer.md)

This guide is for operators / IT staff who run the application on a server (staging or production). Commands use Docker Compose as the main example; Kubernetes manifests live in `deployments/k8s` (`base`, `overlays/dev`, `overlays/prod`). Hosting is not decided yet, so the steps are kept neutral.

## 1. System overview

| Component | Purpose | Required? |
|---|---|---|
| API server (`/server`, port 8080) | REST API `/api/v1`, `/healthz`, `/readyz`, `/metrics`, `/docs` | Yes |
| PostgreSQL 17 | Data of all tenants (single `public` schema, `tenant_id` column, Row-Level Security) | Yes |
| Redis 8 | Login rate limiting (required with more than one replica) | Production: yes |
| SMTP | Forgot/reset password e-mail | If password reset is used |
| File volume (`STORAGE_DIR`) | Uploaded files and APKs | Yes, must be persistent |
| gRPC AuthService (`/auth-grpc`, port 9090) | Token verification for other services | Optional |
| Dedicated tenant database (`TENANT_DB_URL_<REF>`) | Tenants that need a separate database | Optional |

The runtime image is distroless, runs as non-root, and contains only `/server` and `/auth-grpc`. The `tenantctl` CLI is **not** in the image; run it from a build machine / CI job / bastion that can reach the database (`go run ./cmd/cli ...` or `go build -o tenantctl ./cmd/cli`).

## 2. Prerequisites

- Docker + Docker Compose (or a Kubernetes cluster), Go 1.26+ to run `tenantctl`.
- PostgreSQL with an application role that is **not a superuser and has no BYPASSRLS** (superusers and table owners bypass RLS, which breaks tenant isolation).
- A domain + TLS certificate in front of the server (reverse proxy / load balancer). The API itself speaks plain HTTP.
- An SMTP server if password reset is used.

## 3. Configuration and secrets

Template: `.env.production.example` (contains no secret values). On servers/Kubernetes supply values through the process environment, a ConfigMap + Secret, or a secret manager. **Never** commit real env files.

| Variable | Production notes |
|---|---|
| `APP_ENV=production` | Enables the strict rules below |
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SCHEMA` | `DB_PASSWORD` is **required**; the app refuses to start if empty |
| `DB_SSLMODE` | `require` or `verify-full`; `disable` is rejected |
| `PASETO_KEY` | **Required**, different from development. Generate with `make keygen` (`tenantctl keygen`) |
| `ACCESS_TOKEN_TTL` / `REFRESH_TOKEN_TTL` | Production defaults `15m` / `720h` |
| `REDIS_ADDR`, `REDIS_PASSWORD` | Required with more than one replica |
| `RATE_LIMIT_ATTEMPTS` / `RATE_LIMIT_WINDOW` | `5` attempts per `15m` |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`, `SMTP_SECURITY` | `starttls` or `tls`; `none` is rejected |
| `PASSWORD_RESET_URL`, `PASSWORD_RESET_TTL` | URL must be `https`, placeholders `{token}` and `{tenant}`; TTL defaults to `30m` |
| `TENANT_HEADER`, `TENANT_BASE_DOMAIN` | Default `X-Tenant-Code`; e.g. `app.example.com` so `tokoa.app.example.com` is recognized as tenant `tokoa` |
| `STORAGE_DRIVER=local`, `STORAGE_DIR`, `UPLOAD_MAX_MB`, `UPLOAD_APK_MAX_MB` | `STORAGE_DIR` on a persistent volume; defaults 10 MB and 200 MB |
| `DOCS_ENABLED` | Off by default; if enabled, protect the route at the proxy |
| `METRICS_ENABLED`, `METRICS_TOKEN` | Token at least 16 characters in production |
| `GRPC_*` | TLS is required; `GRPC_ALLOW_PLAINTEXT=true` only behind a service mesh that already does mTLS |
| `LOG_LEVEL` | `info`; logs are JSON in production |

On Kubernetes, non-secret values live in `configmap.yaml` (per environment via overlays) and secrets (`DB_USER`, `DB_PASSWORD`, `PASETO_KEY`, `REDIS_PASSWORD`, `SMTP_*`) live in a Secret; `secret.example.yaml` is only an example, use External Secrets / Sealed Secrets or a cloud secret manager.

**`PASETO_KEY`**** is the most important secret.** Anyone holding it can mint valid tokens. If it leaks: generate a new key, redeploy (all sessions become invalid and users log in again), and remove the old key everywhere.

## 4. First-time setup

Order matters; repeat per environment.

1. **Prepare the database and application role** (no BYPASSRLS).
2. **Migrate**: `go run ./cmd/cli migrate up --all` (shared database + every dedicated tenant database). Verify with `migrate status`. Each migration file runs in its own transaction under a lock, so re-running is safe.
3. **Register endpoints with RBAC**: `go run ./cmd/cli endpoint sync` (`make sync-endpoints`). New routes are **refused** until granted to a group (default-deny).
4. **Create the first platform admin**: `tenantctl admin create ...` (see the Platform Admin Guide).
5. **Start the server**, then verify using section 6.
6. Onboard the first tenant following the Platform Admin Guide.

Docker Compose example: `deployments/docker/docker-compose.yml` (postgres, redis; optional profiles `mail` (Mailpit), `app`, `grpc`, `kafka`, `rabbitmq`). That file is for **development**; for production use a managed database or your own volumes and backups.

```bash
docker compose -f deployments/docker/docker-compose.yml --profile app up -d --build
```

## 5. Reverse proxy and network

- Terminate TLS at the proxy / load balancer and forward to port 8080.
- Restrict `/metrics` and `/docs` (internal network only, plus `METRICS_TOKEN`).
- `/api/v1/public/*` (app version check, APK download) is public by design: put rate limiting / a CDN in front, especially for APK downloads (Range and ETag are supported).
- Request bodies are limited to 1 MiB (auth endpoints 16 KiB) except upload endpoints; upload limits are `UPLOAD_MAX_MB` and `UPLOAD_APK_MAX_MB`, so match the proxy body limit to them.
- Forward `X-Request-ID` if the proxy creates one; otherwise the server generates it.
- Never expose PostgreSQL or Redis ports to the internet.

## 6. Post-deploy verification

| Check | Expected |
|---|---|
| `GET /healthz` | 200, process is alive (liveness) |
| `GET /readyz` | 200 when the database (and dedicated tenant databases, `tenant_databases`) are reachable (readiness) |
| `POST /api/v1/auth/platform/login` | 200 with a token |
| `GET /metrics` (with token) | Prometheus format |
| Logs | JSON, one line per request with `request_id` |

Kubernetes: liveness on `/healthz` and readiness on `/readyz` are already in `deployment.yaml` (2 replicas, HPA 2 to 10 at 70% CPU, `readOnlyRootFilesystem`; that is why `STORAGE_DIR` must be a mounted volume).

## 7. Upgrading

1. Read the release notes and new migrations (`migrations/*.up.sql`).
2. **Back up first** (section 8).
3. Run `migrate up --all` **before** the new version takes traffic.
4. Run `endpoint sync` before the new version takes traffic. New routes have no grants yet: grant them to the right groups (`tenantctl group grant ...`, or `--all` for a given group) or users get 403 on new features. Removed routes are reported; `--prune` soft-deletes their rows.
5. Deploy the new image (rolling update). Watch `/readyz` and error metrics.
6. Rollback: redeploy the old image. Migrations only move forward; rolling back a schema needs a restore from backup, so plan backward-compatible migrations.

## 8. Backup and restore

Two things must be backed up **together**: the database and the file folder.

- **PostgreSQL**: scheduled `pg_dump -Fc` (daily at minimum), or managed-database backups / PITR. Include every dedicated tenant database (`TENANT_DB_URL_*`).
- **Uploaded files** (`STORAGE_DIR`): volume snapshots or scheduled rsync. Tables hold metadata only; the physical files exist only here, so a database without its files (or the reverse) yields broken attachments.
- **Secrets** (`PASETO_KEY`, DB password, SMTP) live in the secret manager, not in database backups.
- **Test restores** regularly into a separate environment: restore the dump, `migrate status`, `GET /readyz`, log in, download one file.

## 9. Monitoring and logs

- **Logs**: JSON, every request has a `request_id` (also returned in the `X-Request-ID` response header). When a user reports an error, ask for the `X-Request-ID` from the response and search the logs.
- **RBAC denials** are logged with a reason: `not_granted` (group lacks the permission), `no_group` (user has no group), `wrong_level` (tenant calling a platform route or the reverse), `unregistered_endpoint` (`endpoint sync` was forgotten).
- **Metrics**: `/metrics` (Prometheus). Suggested alerts: 5xx ratio, p95 latency, `/readyz` failing, SMTP failures, `STORAGE_DIR` disk space, database connections. There is no tracing.
- See `docs/observability.md` for metric names.

## 10. Operating tenants with a dedicated database

A large tenant can use its own database: set `TENANT_DB_URL_<REF>` (sslmode `require`, `verify-ca` or `verify-full`), create the tenant with `tenantctl tenant create --db-ref <REF>`, migrate with `migrate up --ref <REF>` (or `--all`). A database whose migrations were applied by hand can be recorded with `migrate baseline`. Details: `docs/database-per-tenant.md`.

## 11. Production security checklist

- `APP_ENV=production`, `DB_SSLMODE=require/verify-full`, `DB_PASSWORD` set
- `PASETO_KEY` unique per environment, kept in a secret manager
- Database role is not a superuser and has no BYPASSRLS
- Redis enabled when replicas > 1
- TLS at the proxy; `/metrics` and `/docs` not exposed publicly
- `METRICS_TOKEN` is at least 16 characters
- SMTP uses `starttls`/`tls`; `PASSWORD_RESET_URL` is https
- gRPC uses mTLS (or a service mesh)
- Database + file backups exist and a restore has been tested
- No `.env`/key files in the repository; code is handed to clients through the client's own repository (see `SECURITY.md`)
- Any secret that ever leaked has been rotated

## 12. Troubleshooting

| Symptom | Common cause | Action |
|---|---|---|
| Server will not start in production | Empty `DB_PASSWORD`, `DB_SSLMODE=disable`, `SMTP_SECURITY=none`, short `METRICS_TOKEN` | Read the error in the log, fix the variable |
| `/readyz` fails | Database / tenant database unreachable | Check network, credentials, `TENANT_DB_URL_*` |
| 403 on a new feature after upgrade | Route not synced or not granted | `endpoint sync`, then `group grant`; check the reason in the log |
| 429 `rate_limited` on login | 5 failed attempts per 15 minutes | Wait per the `Retry-After` header; without Redis each replica counts separately |
| Reset e-mail never arrives | `SMTP_HOST` empty (the endpoint still answers 202) | Configure SMTP; check logs |
| Upload rejected | Over `UPLOAD_MAX_MB`/`UPLOAD_APK_MAX_MB` or the proxy limit; type not allowed | Align limits; allowed types are PNG, JPEG, GIF, WebP, PDF |
| Files vanish after redeploy | `STORAGE_DIR` is not a persistent volume | Mount a volume, restore from backup |
| All users suddenly logged out | `PASETO_KEY` changed | By design; log in again |
| Tenant data not visible | Wrong token/tenant, or the DB role bypasses RLS | Check the tenant at login and the database role |

For every incident record the time, `request_id`, tenant and actions taken; if a secret may have leaked, rotate first and investigate afterwards.
