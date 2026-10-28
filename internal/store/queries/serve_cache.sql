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
--
-- format is the caller's serve format (internal/serve.Format). A clean row in
-- an older format is as stale as a dirty one (0033), so the write also lands
-- on it — under the same generation check.
--
-- provenance (0049) is how content was built, written in the same row write so
-- the two never disagree; internal/serve.DecodeProvenance also checks the hash
-- it records against the row's, for a row a pre-0049 binary rewrote.
INSERT INTO serve_cache (collector_id, content, hash, computed_at, dirty, dirty_seq, format, provenance)
VALUES (sqlc.arg(collector_id), sqlc.arg(content), sqlc.arg(hash), now(), false, 0, sqlc.arg(format), sqlc.narg(provenance)::jsonb)
ON CONFLICT (collector_id) DO UPDATE SET
    content     = EXCLUDED.content,
    hash        = EXCLUDED.hash,
    computed_at = now(),
    dirty       = false,
    format      = EXCLUDED.format,
    provenance  = EXCLUDED.provenance
WHERE (serve_cache.dirty = true OR serve_cache.format < EXCLUDED.format)
  AND serve_cache.dirty_seq = sqlc.arg(dirty_seq)
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

-- name: MarkServeCacheFailed :one
-- Records that the recompute of generation $2 (the dirty_seq the caller read
-- before it loaded pipelines) failed, at most once per generation: it advances
-- last_failed_seq only while $2 is newer than it, and returns the row only when
-- it did. No row back means this generation's failure was already recorded (or
-- a newer one was), so the caller writes no second audit row. A collector with
-- no cache row yet gets a dirty placeholder at generation $2 — the same
-- placeholder a dirty mark inserts, so the next recompute still writes over it.
-- Never touches dirty_seq, so the 0033 format trigger does not fire.
INSERT INTO serve_cache (collector_id, content, hash, dirty, dirty_seq, last_failed_seq)
VALUES ($1, '', '', true, sqlc.arg(failed_seq)::bigint, sqlc.arg(failed_seq)::bigint)
ON CONFLICT (collector_id) DO UPDATE SET
    last_failed_seq = EXCLUDED.last_failed_seq
WHERE serve_cache.last_failed_seq < EXCLUDED.last_failed_seq
RETURNING collector_id;

-- name: MarkServeCacheDirtyAll :exec
-- Every collector's served config, for a change every org's strict tenant
-- check reads (the installation's first live tenant route, or its last).
INSERT INTO serve_cache (collector_id, content, hash, dirty, dirty_seq)
SELECT c.id, '', '', true, 1
FROM collectors c
ON CONFLICT (collector_id) DO UPDATE SET
    dirty = true,
    dirty_seq = serve_cache.dirty_seq + 1;
