-- LOSSY: once two collectors hold a row for the same wire id, the old
-- single-column key cannot hold both. Keep each id's most recently seen row
-- (ties broken by updated_at) and delete the others before restoring it.
DROP INDEX IF EXISTS idx_collector_instances_id;

DELETE FROM collector_instances ci
USING collector_instances newer
WHERE newer.id = ci.id
  AND newer.collector_id <> ci.collector_id
  AND (COALESCE(newer.last_seen, '-infinity'), newer.updated_at, newer.collector_id::text)
      > (COALESCE(ci.last_seen, '-infinity'), ci.updated_at, ci.collector_id::text);

ALTER TABLE collector_instances DROP CONSTRAINT collector_instances_pkey;
ALTER TABLE collector_instances ADD CONSTRAINT collector_instances_pkey PRIMARY KEY (id);
