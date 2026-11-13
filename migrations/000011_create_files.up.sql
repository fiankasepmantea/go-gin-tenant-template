-- Metadata of uploaded files. The content itself is in the storage backend (docs/files.md), under storage_key.
CREATE TABLE files (
    file_id      uuid        PRIMARY KEY,
    file_unique  bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    tenant_id    uuid        NOT NULL REFERENCES tenants (tenant_id),
    uploaded_by  uuid        REFERENCES users (user_id),
    name         text        NOT NULL,
    content_type text        NOT NULL,
    size         bigint      NOT NULL CHECK (size > 0),
    purpose      text        NOT NULL DEFAULT '',
    storage_key  text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz
);
CREATE INDEX files_tenant_created_idx ON files (tenant_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX files_tenant_purpose_idx ON files (tenant_id, purpose) WHERE deleted_at IS NULL;

CREATE TRIGGER files_updated_at BEFORE UPDATE ON files
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE files ENABLE ROW LEVEL SECURITY;
ALTER TABLE files FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON files
    USING (tenant_id = app_tenant())
    WITH CHECK (tenant_id = app_tenant());
