-- All users live here. Platform admins: level='platform' and tenant_id NULL.
CREATE TABLE users (
    user_id       uuid        PRIMARY KEY,
    user_unique   bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    tenant_id     uuid        REFERENCES tenants (tenant_id),
    level         text        NOT NULL DEFAULT 'tenant',
    group_id      uuid,
    username      text        NOT NULL,
    email         text        NOT NULL,
    name          text        NOT NULL,
    password_hash text        NOT NULL,
    is_active     boolean     NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    CONSTRAINT users_level_valid CHECK (level IN ('platform', 'tenant')),
    -- level and tenant_id must be consistent
    CONSTRAINT users_level_tenant CHECK (
        (level = 'platform' AND tenant_id IS NULL AND group_id IS NULL) OR
        (level = 'tenant'   AND tenant_id IS NOT NULL)
    ),
    -- the group must belong to the same tenant (skipped when group_id is NULL)
    CONSTRAINT users_group_same_tenant FOREIGN KEY (tenant_id, group_id)
        REFERENCES groups (tenant_id, group_id)
);
-- NULLs are treated as distinct in a plain unique index, so two sets of indexes are created.
CREATE UNIQUE INDEX users_tenant_username_uq ON users (tenant_id, lower(username))
    WHERE deleted_at IS NULL AND tenant_id IS NOT NULL;
CREATE UNIQUE INDEX users_tenant_email_uq ON users (tenant_id, lower(email))
    WHERE deleted_at IS NULL AND tenant_id IS NOT NULL;
CREATE UNIQUE INDEX users_platform_username_uq ON users (lower(username))
    WHERE deleted_at IS NULL AND tenant_id IS NULL;
CREATE UNIQUE INDEX users_platform_email_uq ON users (lower(email))
    WHERE deleted_at IS NULL AND tenant_id IS NULL;
CREATE INDEX users_tenant_group_ix ON users (tenant_id, group_id);

CREATE TRIGGER users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
-- A tenant session only sees its own tenant. Platform rows (tenant_id NULL) are for platform sessions only.
CREATE POLICY tenant_isolation ON users
    USING (tenant_id = app_tenant() OR app_level() = 'platform')
    WITH CHECK (tenant_id = app_tenant() OR app_level() = 'platform');
