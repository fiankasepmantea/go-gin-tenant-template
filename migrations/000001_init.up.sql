-- Helper functions for RLS and the updated_at column.
-- The application sets session variables inside a transaction:
--   SELECT set_config('app.current_tenant', '<uuid>', true);
--   SELECT set_config('app.current_level',  'tenant|platform', true);

CREATE FUNCTION app_tenant() RETURNS uuid
LANGUAGE sql STABLE AS
$$ SELECT nullif(current_setting('app.current_tenant', true), '')::uuid $$;

CREATE FUNCTION app_level() RETURNS text
LANGUAGE sql STABLE AS
$$ SELECT coalesce(nullif(current_setting('app.current_level', true), ''), '') $$;

CREATE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS
$$ BEGIN NEW.updated_at = now(); RETURN NEW; END $$;
