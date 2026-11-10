ALTER TABLE endpoints
    DROP CONSTRAINT IF EXISTS endpoints_scope_valid,
    DROP COLUMN IF EXISTS scope;
