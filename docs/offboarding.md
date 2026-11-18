# Offboarding a client (tenant)

Phase F. A client leaves in three steps, each one a separate command so that nothing is lost by accident:
**suspend**, **export**, **delete**. This page describes what exists today.

| Step | Command | Status |
| --- | --- | --- |
| Suspend / resume | `tenantctl tenant suspend --code acme` / `tenantctl tenant resume --code acme` | available |
| Export the data | `tenantctl tenant export` | planned |
| Delete the data | `tenantctl tenant delete` | planned |

## Suspend and resume

```bash
tenantctl tenant suspend --code acme
tenantctl tenant resume  --code acme
```

- A suspended tenant cannot log in or refresh a token (the answer is the same `invalid_credentials` as any other
  failed login, so nothing is revealed).
- Suspending **revokes every refresh token** of the tenant. Access tokens already issued keep working until they
  expire (`ACCESS_TOKEN_TTL`, 15 minutes in production).
- **No data is changed or deleted.** `resume` makes the tenant usable again; users have to log in again.
- It works for a tenant in the shared database and for one with a dedicated database (the status is updated in both
  places). Other tenants are not affected.
- The password-reset flow also refuses a suspended tenant.

Run it from a machine that can reach the database (see the Operator Runbook).
