-- No-op by design: the up migration only marked cached served configs stale.
-- There is nothing to restore — a stale mark is always safe (the next poll
-- recomputes from the pipelines), and un-marking would risk keeping content
-- the running code no longer produces. After a downgrade the older code
-- recomputes on its own next write or poll of a dirty row.
SELECT 1;
