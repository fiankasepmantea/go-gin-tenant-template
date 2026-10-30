# ADR 0002: Platform admin and app_version

Status: accepted

## Decision
1. Platform admins are stored in `users` with `level='platform'` and `tenant_id IS NULL`.
2. `app_versions` is global, without `tenant_id`.

## Rationale
1. A single login and token path for all users. The risk of `NULL` rows is closed in the database: level/tenant CHECK,
   two partial unique sets, and an RLS policy that exposes platform rows only to platform sessions.
2. One client app serves all tenants and the API is the same, so the minimum supported version is also the same.

## Consequences
- Splitting into a separate table will be reconsidered if platform admins need mandatory MFA or an IP allowlist.
- White-label apps: add a nullable `app_versions.tenant_id` (NULL = global default).
- The application must use a database role without BYPASSRLS; table owners and superusers bypass RLS.
