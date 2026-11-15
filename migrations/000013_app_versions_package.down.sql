ALTER TABLE app_versions
    DROP CONSTRAINT IF EXISTS app_versions_package_all_or_none,
    DROP COLUMN IF EXISTS package_key,
    DROP COLUMN IF EXISTS package_size,
    DROP COLUMN IF EXISTS package_sha256;
