-- endpoint: route allowlist (global, managed by the platform). group_endpoint: per-group permissions.
CREATE TABLE endpoints (
    endpoint_id     uuid        PRIMARY KEY,
    endpoint_unique bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    method          text        NOT NULL,
    path            text        NOT NULL,
    name            text        NOT NULL DEFAULT '',
    is_public       boolean     NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz,
    CONSTRAINT endpoints_method_valid CHECK (method IN ('GET', 'POST', 'PUT', 'PATCH', 'DELETE'))
);
CREATE UNIQUE INDEX endpoints_method_path_uq ON endpoints (method, path) WHERE deleted_at IS NULL;
CREATE TRIGGER endpoints_updated_at BEFORE UPDATE ON endpoints
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE group_endpoints (
    group_endpoint_id     uuid        PRIMARY KEY,
    group_endpoint_unique bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    tenant_id             uuid        NOT NULL,
    group_id              uuid        NOT NULL,
    endpoint_id           uuid        NOT NULL REFERENCES endpoints (endpoint_id),
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    deleted_at            timestamptz,
    FOREIGN KEY (tenant_id, group_id) REFERENCES groups (tenant_id, group_id)
);
CREATE UNIQUE INDEX group_endpoints_uq ON group_endpoints (tenant_id, group_id, endpoint_id)
    WHERE deleted_at IS NULL;
CREATE TRIGGER group_endpoints_updated_at BEFORE UPDATE ON group_endpoints
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE group_endpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE group_endpoints FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON group_endpoints
    USING (tenant_id = app_tenant())
    WITH CHECK (tenant_id = app_tenant());
