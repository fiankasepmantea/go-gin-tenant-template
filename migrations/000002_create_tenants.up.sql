-- Tenant = a customer organization. This table is owned by the platform and is not tenant-scoped (no RLS).
CREATE TABLE tenants (
    tenant_id     uuid        PRIMARY KEY,
    tenant_unique bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    code          text        NOT NULL,
    name          text        NOT NULL,
    status        text        NOT NULL DEFAULT 'active',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    CONSTRAINT tenants_code_format CHECK (code ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    CONSTRAINT tenants_status_valid CHECK (status IN ('active', 'suspended'))
);
CREATE UNIQUE INDEX tenants_code_uq ON tenants (code) WHERE deleted_at IS NULL;

CREATE TRIGGER tenants_updated_at BEFORE UPDATE ON tenants
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
