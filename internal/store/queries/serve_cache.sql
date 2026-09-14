-- name: GetServeCache :one
SELECT * FROM serve_cache WHERE collector_id = $1;

-- name: MarkServeCacheDirtyByOrg :exec
-- A mark must never be lost: a collector with no cache row yet (never polled,
-- or its first recompute still in flight) gets a dirty placeholder row so the
-- generation bump lands and a recompute that read "no row" (generation 0)
-- cannot insert over it. Every mark bumps dirty_seq: see
-- UpsertServeCacheConditional.
INSERT INTO serve_cache (collector_id, content, hash, dirty, dirty_seq)
SELECT c.id, '', '', true, 1
FROM collectors c
JOIN clusters cl ON c.cluster_id = cl.id
WHERE cl.org_id = $1
ON CONFLICT (collector_id) DO UPDATE SET
    dirty = true,
    dirty_seq = serve_cache.dirty_seq + 1;

-- name: MarkServeCacheDirtyByCluster :exec
INSERT INTO serve_cache (collector_id, content, hash, dirty, dirty_seq)
SELECT c.id, '', '', true, 1
FROM collectors c
WHERE c.cluster_id = $1
ON CONFLICT (collector_id) DO UPDATE SET
    dirty = true,
    dirty_seq = serve_cache.dirty_seq + 1;

-- name: MarkServeCacheDirty :exec
-- KEEP: single-collector dirty marking; used in agentapi tests and available for
-- targeted per-collector invalidation. Do not remove.
INSERT INTO serve_cache (collector_id, content, hash, dirty, dirty_seq)
VALUES ($1, '', '', true, 1)
ON CONFLICT (collector_id) DO UPDATE SET
    dirty = true,
    dirty_seq = serve_cache.dirty_seq + 1;

-- name: UpsertServeCacheConditional :one
-- Compare-and-swap on the dirty generation. The caller passes the dirty_seq it
-- read BEFORE loading pipelines (0 when no row existed yet); the write lands
-- only if the row is still dirty AND no newer mark has bumped the generation
-- since. A recompute that raced a newer mark therefore writes nothing — the
-- recompute that observed the newer generation writes instead — so stale
-- content can never consume a newer dirty flag. Returns no row when skipped;
-- callers treat that as "superseded", not as a failure.
INSERT INTO serve_cache (collector_id, content, hash, computed_at, dirty, dirty_seq)
VALUES ($1, $2, $3, now(), false, 0)
ON CONFLICT (collector_id) DO UPDATE SET
    content     = EXCLUDED.content,
    hash        = EXCLUDED.hash,
    computed_at = now(),
    dirty       = false
WHERE serve_cache.dirty = true AND serve_cache.dirty_seq = $4
RETURNING *;

-- name: ListServeCacheSeqByOrg :many
-- The generation of every collector's cache row in an org, read by the eager
-- recompute BEFORE it loads pipelines. Collectors with no row yet are absent
-- (their expected generation is 0).
SELECT sc.collector_id, sc.dirty_seq
FROM serve_cache sc
JOIN collectors c ON sc.collector_id = c.id
JOIN clusters cl ON c.cluster_id = cl.id
WHERE cl.org_id = $1;
