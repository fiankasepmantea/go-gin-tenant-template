# Skill: database migrations

- Tool: golang-migrate. Create: `make migrate-new name=create_xxx`. Apply: `make migrate-up DATABASE_URL=...`.
- One migration = one logical change; always provide `up` and `down`.
- Do not edit a migration that has been applied in any environment; create a new one.
- New tenant-scoped table: copy the standard column pattern from `.ai/skills/create-module.md`, enable RLS.
- Risky changes (drop column, change type): split across several releases (add, migrate data, then drop).
- Do not put secrets (passwords, keys) in migration files.
