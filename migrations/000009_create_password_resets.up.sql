-- Password reset requests. Only the hash (SHA-256) of the token is stored, never the token itself.
-- A token is single use (used_at) and short lived (expires_at). Platform admins have tenant_id NULL.
CREATE TABLE password_resets (
    password_reset_id     uuid        PRIMARY KEY,
    password_reset_unique bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    tenant_id             uuid        REFERENCES tenants (tenant_id),
    user_id               uuid        NOT NULL REFERENCES users (user_id),
    token_hash            bytea       NOT NULL,
    ip_address            text        NOT NULL DEFAULT '',
    expires_at            timestamptz NOT NULL,
    used_at               timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    deleted_at            timestamptz
);
CREATE UNIQUE INDEX password_resets_hash_uq ON password_resets (token_hash);
CREATE INDEX password_resets_user_ix ON password_resets (tenant_id, user_id);
CREATE TRIGGER password_resets_updated_at BEFORE UPDATE ON password_resets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE password_resets ENABLE ROW LEVEL SECURITY;
ALTER TABLE password_resets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON password_resets
    USING (tenant_id = app_tenant() OR app_level() = 'platform')
    WITH CHECK (tenant_id = app_tenant() OR app_level() = 'platform');
