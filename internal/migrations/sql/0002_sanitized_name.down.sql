DROP INDEX IF EXISTS pipelines_org_sanitized_name_key;
ALTER TABLE pipelines DROP COLUMN IF EXISTS sanitized_name;
DROP FUNCTION IF EXISTS sanitize_pipeline_name(text);
