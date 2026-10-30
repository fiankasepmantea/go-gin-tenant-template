-- Refresh tokens. Only the hash (SHA-256) is stored, never the raw token.
CREATE TABLE user_tokens (
    user_token_id     uuid        PRIMARY KEY,
    user_token_unique bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    tenant_id         uuid        REFERENCES tenants (tenant_id),
    user_id           uuid        NOT NULL REFERENCES users (user_id),
    token_hash        bytea       NOT NULL,
    user_agent        text        NOT NULL DEFAULT '',
    ip_address        text        NOT NULL DEFAULT '',
    expires_at        timestamptz NOT NULL,
    revoked_at        timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);
CREATE UNIQUE INDEX user_tokens_hash_uq ON user_tokens (token_hash);
CREATE INDEX user_tokens_user_ix ON user_tokens (tenant_id, user_id);
CREATE TRIGGER user_tokens_updated_at BEFORE UPDATE ON user_tokens
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE user_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON user_tokens
    USING (tenant_id = app_tenant() OR app_level() = 'platform')
    WITH CHECK (tenant_id = app_tenant() OR app_level() = 'platform');
