# Observability

## Request log

One structured JSON line per request (`http_request`): `request_id`, `method`, `route` (the route pattern, never
the raw URL), `status`, `latency`, `client_ip`, and after login `user_id` and `tenant_id`. The level follows the
status (5xx error, 4xx warn). The text of an unexpected error is written for 5xx only, never sent to the client.
The same `request_id` is in the `X-Request-ID` response header, so a user report can be found in the log.

## Metrics: `GET /metrics`

Prometheus text format, no extra dependency.

| Metric | Type | Labels |
|---|---|---|
| `http_requests_total` | counter | `method`, `route`, `status` |
| `http_request_duration_seconds` | histogram | `method`, `route` |
| `http_requests_in_flight` | gauge | |
| `db_pool_open_connections`, `_in_use_`, `_idle_`, `db_pool_wait_count_total`, `db_pool_wait_seconds_total` | gauge | `db` (`shared` or the reference of a dedicated database) |
| `app_build_info` | gauge | `version` |

`route` is the pattern (`/api/v1/groups/:id`), `unmatched` for a 404, so the number of series stays small; it is
also capped at 2000 series (`http_metrics_dropped_total` counts what was dropped).

| Variable | Default | Meaning |
|---|---|---|
| `METRICS_ENABLED` | `true` in development, `false` in production | register the route |
| `METRICS_TOKEN` | empty | scrapers send `Authorization: Bearer <token>`; without it the route answers 404. Production with metrics on needs at least 16 characters |

Prometheus scrape config: `authorization: { type: Bearer, credentials: <token> }`.

## Health

- `GET /healthz`: the process is alive (restart decision).
- `GET /readyz`: the shared database answers (traffic decision). It returns `version`, and `tenant_databases`
  with `ok` or `unavailable` per dedicated database. A dedicated database that is down does **not** make the
  response 503: the other tenants are fine, and 503s for that tenant are already answered per request.

Tracing (OpenTelemetry) is not part of the template; add it per project.
