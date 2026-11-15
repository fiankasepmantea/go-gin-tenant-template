-- The installable package (APK) of a version. All three columns are set together, or none of them.
-- The content is in the storage backend under package_key (docs/app-versions.md).
ALTER TABLE app_versions
    ADD COLUMN package_key    text,
    ADD COLUMN package_size   bigint,
    ADD COLUMN package_sha256 text,
    ADD CONSTRAINT app_versions_package_all_or_none CHECK (
        (package_key IS NULL AND package_size IS NULL AND package_sha256 IS NULL)
        OR (package_key IS NOT NULL AND package_size > 0 AND package_sha256 IS NOT NULL)
    );
