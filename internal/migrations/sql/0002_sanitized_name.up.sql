-- Add sanitized_name as a generated column for collision detection.
-- sanitize() mirrors internal/merge.SanitizeName: lowercase, non-[a-z0-9_] → '_', prepend 'p' if starts with digit.
CREATE OR REPLACE FUNCTION sanitize_pipeline_name(name text) RETURNS text
  LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS
$$
  SELECT CASE
    WHEN regexp_replace(lower(name), '[^a-z0-9_]', '_', 'g') ~ '^[0-9]'
      THEN 'p' || regexp_replace(lower(name), '[^a-z0-9_]', '_', 'g')
    ELSE regexp_replace(lower(name), '[^a-z0-9_]', '_', 'g')
  END
$$;

ALTER TABLE pipelines
  ADD COLUMN sanitized_name text GENERATED ALWAYS AS (sanitize_pipeline_name(name)) STORED;

CREATE UNIQUE INDEX pipelines_org_sanitized_name_key
  ON pipelines (org_id, sanitized_name);
