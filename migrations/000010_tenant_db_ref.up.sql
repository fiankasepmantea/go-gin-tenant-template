-- Database per tenant (optional). NULL = the tenant lives in the shared database (the default).
-- A value is the reference of a dedicated database: its URL comes from the environment (TENANT_DB_URL_<REF>),
-- never from this table. See docs/database-per-tenant.md.
ALTER TABLE tenants ADD COLUMN db_ref text;
ALTER TABLE tenants ADD CONSTRAINT tenants_db_ref_format CHECK (db_ref IS NULL OR db_ref ~ '^[a-z][a-z0-9_]{0,30}$');
