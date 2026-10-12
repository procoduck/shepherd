DROP TRIGGER IF EXISTS serve_cache_mark_resets_format ON serve_cache;
DROP FUNCTION IF EXISTS serve_cache_mark_resets_format();
ALTER TABLE serve_cache DROP COLUMN IF EXISTS format;
