-- Who may use an endpoint at all. 'tenant': users of a tenant (needs a group grant unless is_public).
-- 'platform': platform admins only (no grants needed). A token of the wrong level is always refused.
ALTER TABLE endpoints
    ADD COLUMN scope text NOT NULL DEFAULT 'tenant',
    ADD CONSTRAINT endpoints_scope_valid CHECK (scope IN ('tenant', 'platform'));

COMMENT ON COLUMN endpoints.is_public IS 'any authenticated user of the right scope may call it, no group grant needed';
