# 0001 — Initial decisions

| Topic | Decision | Rationale |
|---|---|---|
| Tenant isolation | Single schema + `tenant_id`, swappable resolver | Lightest option; the upgrade path to per-tenant schema/DB stays open |
| Column name | `tenant_id` | Clearer than `domain_id` |
| Primary key | UUID v7 | Sequential (index-friendly), native `uuid` type; absolute ordering by `{table}_unique` |
| Standard columns | `{t}_id`, `{t}_unique`, `tenant_id`, `created_at`, `updated_at`, `deleted_at` | Continuation of the CI4 convention + soft delete |
| Token | PASETO v4.local | JWT replacement without a misusable algorithm choice |
| Admin | Two levels: platform and tenant | Building owner vs resident |
| Go | `go 1.26.0` minimum (raised from 1.25.0 for the x/net HTTP/2 security fixes) | See docs/libraries.md |
| Logging | `log/slog` | Stdlib, supported by OpenTelemetry |
| Migration | golang-migrate | Already used in `simple-bank` |
