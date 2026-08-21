-- name: GetServeCache :one
SELECT * FROM serve_cache WHERE collector_id = $1;

-- name: MarkServeCacheDirtyByOrg :exec
UPDATE serve_cache sc
SET dirty = true
FROM collectors c
JOIN clusters cl ON c.cluster_id = cl.id
WHERE sc.collector_id = c.id AND cl.org_id = $1;

-- name: MarkServeCacheDirty :exec
-- KEEP: single-collector dirty marking; used in agentapi tests and available for
-- targeted per-collector invalidation. Do not remove.
UPDATE serve_cache SET dirty = true WHERE collector_id = $1;

-- name: UpsertServeCacheConditional :one
-- Only clears the dirty flag if it is still true at write time (conditional update).
-- This prevents a newer dirty-mark from being overwritten by a stale recompute.
INSERT INTO serve_cache (collector_id, content, hash, computed_at, dirty)
VALUES ($1, $2, $3, now(), false)
ON CONFLICT (collector_id) DO UPDATE SET
    content     = EXCLUDED.content,
    hash        = EXCLUDED.hash,
    computed_at = now(),
    dirty       = false
WHERE serve_cache.dirty = true
RETURNING *;
