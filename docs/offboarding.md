# Offboarding a client (tenant)

A client leaves in three steps, each one its own command so that nothing is lost by accident:
**suspend**, **export**, **delete**. Run them from a machine that can reach the database and the file storage
(see the Operator Runbook); they use the active `.env.<mode>`.

| Step | Command | Reversible? |
| --- | --- | --- |
| Suspend / resume | `tenantctl tenant suspend --code acme` / `resume` | yes |
| Export the data | `tenantctl tenant export --code acme --out acme.zip` | n/a (read only) |
| Delete everything | `tenantctl tenant delete --code acme --confirm acme` | **no** |

## 1. Suspend and resume

- A suspended tenant cannot log in, refresh a token or reset a password. The answer is the same
  `invalid_credentials` as for any failed login, so nothing is revealed.
- Suspending **revokes every refresh token**. Access tokens already issued keep working until they expire
  (`ACCESS_TOKEN_TTL`, 15 minutes in production).
- No data is changed. `resume` makes the tenant usable again; users log in again.
- Works for the shared database and for a dedicated database. Other tenants are not affected.

## 2. Export

```bash
tenantctl tenant export --code acme --out /secure/acme-2026-11-18.zip
```

The archive contains:

| Entry | Content |
| --- | --- |
| `data/<table>.jsonl` | One JSON object per line, for **every table that has a `tenant_id`** (found from the schema, so tables made by the generator are included), including soft-deleted rows |
| `files/<file_id>` | The uploaded files (names and types are in `data/files.jsonl`) |
| `manifest.json` | Export time, tenant, row count per table, number of files, `missing_files` (listed in the database but absent from storage) |

Left out on purpose: password hashes, token hashes (`user_tokens`, `password_resets`) and the internal storage key.
The archive is created with mode `0600`, an existing file is never overwritten, and a failed export leaves no
partial file. It can be run on an active tenant, but export after suspending so the data stops changing.
Hand it to the client over a secure channel and keep your own copy as long as the contract requires.

## 3. Delete

```bash
tenantctl tenant suspend --code acme     # required first
tenantctl tenant delete  --code acme --confirm acme
```

**This cannot be undone.** It refuses to run unless the tenant is suspended and `--confirm` repeats the code.

- Deletes every row of every table that has a `tenant_id` (children before parents, in one transaction: either
  everything goes or nothing), then the tenant itself, so the code can be used again.
- Deletes the uploaded files from storage after the database commit. A file that cannot be deleted is reported as a
  warning and does not undo the rest.
- A **dedicated database** is not dropped: drop it and remove `TENANT_DB_URL_<REF>` yourself.
- **Backups still contain the data.** Remove or let expire the database and file backups that hold the tenant,
  according to the contract. The tool cannot do that.
- Global data (app versions, the endpoint list, platform admins) is never touched.

## Checklist

1. Agree the date and what is delivered (contract).
2. `suspend` on the agreed date.
3. `export`, verify the archive (`unzip -l`, `manifest.json`), hand it over, record the hand-over.
4. Wait the agreed retention period.
5. `delete`, then drop a dedicated database if there was one, then expire the backups.
