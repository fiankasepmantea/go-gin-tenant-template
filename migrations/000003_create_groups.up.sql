-- Group = a role (admin, finance, ...) within a single tenant.
CREATE TABLE groups (
    group_id     uuid        PRIMARY KEY,
    group_unique bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    tenant_id    uuid        NOT NULL REFERENCES tenants (tenant_id),
    name         text        NOT NULL,
    description  text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz,
    -- used by composite FKs so other tables cannot reference a group that belongs to another tenant
    CONSTRAINT groups_tenant_group_uq UNIQUE (tenant_id, group_id)
);
CREATE UNIQUE INDEX groups_tenant_name_uq ON groups (tenant_id, lower(name)) WHERE deleted_at IS NULL;

CREATE TRIGGER groups_updated_at BEFORE UPDATE ON groups
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE groups FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON groups
    USING (tenant_id = app_tenant())
    WITH CHECK (tenant_id = app_tenant());
