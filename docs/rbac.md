# Authorization (RBAC)

Authentication answers "who is this?" (see [authentication.md](authentication.md)). Authorization answers "may
this caller use this endpoint?". It is **default deny**: a request is refused unless a rule says yes.

## The model

| Table | Meaning |
|---|---|
| `endpoints` | The whitelist: one row per API route (`method` + route pattern). Global, not per tenant. |
| `groups` | A role of one tenant (for example `admin`, `cashier`). |
| `group_endpoints` | Which endpoints a group may use. One set per tenant group. |
| `users.group_id` | The group of a user. |

An endpoint is identified by the HTTP method and the **Gin route pattern** (`GET /api/v1/groups/:id`), never the
raw URL, so `/groups/1` and `/groups/2` are the same endpoint and a client cannot change the match by editing the
path (`/groups/../x`, encoding, trailing slash).

Two columns of `endpoints` change the rule:

- `scope`: `tenant` (used by users of a tenant) or `platform` (used by platform admins). A caller of the other
  level is always refused.
- `is_public`: any authenticated caller of the right level may use it, no group grant needed. It is still behind
  a valid login. Declare it in `overrides`; it cannot be granted because there is nothing to grant.

## The decision, in order

1. No verified token: 401.
2. The route is not in `endpoints` (or was deleted): 403 (`unregistered_endpoint`).
3. The level of the token does not match `scope`: 403 (`wrong_level`).
4. Platform admin on a `platform` endpoint: allowed.
5. Tenant user: the user must exist in the tenant, not deleted, and `is_active` (otherwise 401); a `public`
   endpoint is then allowed; otherwise the user needs a group (403 `no_group`) that was granted the endpoint
   (403 `not_granted`).
6. Any error while deciding (database down): the request is refused with 5xx. It never falls open.

The reason of every refusal is written to the server log (`access_denied`, with user, tenant and route) and
is **not** sent to the client, who only sees `forbidden`.

The group of the user is read from the database, not from the token, so moving a user to another group or
deactivating them applies without waiting for the access token to expire.

## Middleware order

`internal/app` builds one chain for all modules:

```
Authenticate  ->  TenantTx  ->  Authorize  ->  handler
```

`Authorize` runs inside the tenant transaction, so its queries are under row level security like everything
else. Routes under `/api/v1/auth/` are public by design (they authenticate themselves) and are not part of the
whitelist.

## Workflow when you add or change routes

```bash
make migrate-up            # schema changes, if any (this phase added 000008: endpoints.scope)
make sync-endpoints        # = tenantctl endpoint sync: writes the routes of the code into `endpoints`
tenantctl group grant --tenant acme --group admin --all
```

Run `endpoint sync` **before** the new version takes traffic. New routes start without any grant, so they are
refused until you grant them. Routes that no longer exist are reported and kept; `--prune` soft-deletes them.
A route that needs a non-default rule (`platform` scope, or public) is declared once in `overrides` in
`internal/modules/endpoint/endpoint.go`; the sync applies it. Routes without an override keep whatever `scope` and
`is_public` they have in the database, so a hand edit is not overwritten, but it is also not reproducible on
another environment: prefer the override.

The list is read from the same code that serves the API (`app.Routes(app.Modules(...))`), so the whitelist cannot
drift from the real routes. A new module only has to be added to `app.Modules`.

## First tenant, end to end

```bash
tenantctl tenant create --code acme --name "Acme Inc"
tenantctl endpoint sync
tenantctl group create --tenant acme --name admin --description "Full access"
tenantctl group grant  --tenant acme --group admin --all
printf '%s\n' "$PW" | tenantctl user create --tenant acme --username admin --email admin@acme.test \
     --name "Acme Admin" --group admin --password-stdin
```

`--all` grants every `tenant` scope, non-public endpoint that exists at that moment. After a deploy with new
routes, run `endpoint sync` and `group grant` again (grants are only added, never removed, by the CLI).
To grant one endpoint: `--endpoint "GET /api/v1/groups"` (repeatable; the route pattern, not a URL).

## Managing permissions through the API

These endpoints are protected by RBAC like everything else (the caller needs the permission to manage permissions):

| Request | Purpose |
|---|---|
| `GET /api/v1/rbac/endpoints` | The endpoints that can be granted (tenant scope, not public). |
| `GET /api/v1/groups/:id/permissions` | The endpoints granted to a group. |
| `PUT /api/v1/groups/:id/permissions` | Replace the set: `{"endpoint_ids": ["<uuid>", ...]}`. `[]` revokes everything. |

An unknown or non-grantable id answers 422 and changes nothing; a group of another tenant answers 404.

## Caching and its limits

Endpoints (60 s), users and grants (30 s) are cached in the process (`internal/platform/ttlcache`, bounded).
A change made through the API of the same process applies at once. A change made by the CLI or by another
replica applies after at most the TTL. If you need instant effect across replicas, replace the cache with Redis
(phase 5d); the `rbac.Service` is the only place that needs to change.

## Known limits

- A **suspended tenant** is stopped at login and refresh, but an access token that is already issued keeps working
  until it expires (15 minutes), because `Authorize` does not look at the tenant row.
- An expired access token is answered with `unauthorized`, the same as an invalid one.
- Deleting the group of a user leaves the user without permissions (not deleted).
