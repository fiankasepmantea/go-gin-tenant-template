-- Client application versions (global, no tenant_id). The version-check endpoint is public.
-- If white-label apps are added later: add a nullable tenant_id column (NULL = global default).
CREATE TABLE app_versions (
    app_version_id        uuid        PRIMARY KEY,
    app_version_unique    bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    platform              text        NOT NULL,
    version               text        NOT NULL,
    min_supported_version text        NOT NULL,
    is_force_update       boolean     NOT NULL DEFAULT false,
    release_notes         text        NOT NULL DEFAULT '',
    store_url             text        NOT NULL DEFAULT '',
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    deleted_at            timestamptz,
    CONSTRAINT app_versions_platform_valid CHECK (platform IN ('android', 'ios', 'web'))
);
CREATE UNIQUE INDEX app_versions_platform_version_uq ON app_versions (platform, version)
    WHERE deleted_at IS NULL;
CREATE TRIGGER app_versions_updated_at BEFORE UPDATE ON app_versions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
