-- The serve format a cached served config was computed in (#296/#323
-- follow-up). Shepherd stamps every row it computes with its own format
-- constant (internal/serve.Format) and refuses to serve a row whose format is
-- lower: such a row is recomputed first, exactly as a dirty one is. A change
-- to what the served bytes contain bumps the constant instead of shipping a
-- "mark everything dirty" migration — which a rolling update defeats: 0032
-- marked every row dirty in the pre-upgrade Job, but old pods kept serving
-- during the rollout, recomputed those rows in the OLD format and cleared the
-- flag, and the new pods then trusted them.
--
-- Rows that exist now, and placeholder rows a dirty mark inserts, are format
-- 0: never computed by a binary that knows about formats.
ALTER TABLE serve_cache ADD COLUMN format SMALLINT NOT NULL DEFAULT 0;

-- Older binaries name their columns, so their recompute (dirty = false over
-- new content) leaves format at whatever it was. If a newer binary had
-- already stamped the row, that old write would inherit a format it did not
-- compute. Every recompute is preceded by a dirty mark, and every mark — from
-- any binary — bumps dirty_seq (0020) while no recompute write does; so a
-- mark resets the row to format 0 here, and only a recompute by a binary that
-- sets format can raise it again. Old-binary content therefore always reads
-- as format 0, whichever binary marked the row.
CREATE FUNCTION serve_cache_mark_resets_format() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.dirty_seq IS DISTINCT FROM OLD.dirty_seq THEN
        NEW.format := 0;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER serve_cache_mark_resets_format
    BEFORE UPDATE OF dirty_seq ON serve_cache
    FOR EACH ROW EXECUTE FUNCTION serve_cache_mark_resets_format();
