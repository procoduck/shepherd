-- name: UpsertCollector :one
INSERT INTO collectors (cluster_id, role)
VALUES ($1, $2)
ON CONFLICT (cluster_id, role) DO UPDATE SET updated_at = now()
RETURNING *;

-- name: GetCollectorByID :one
SELECT * FROM collectors WHERE id = $1;

-- name: GetCollectorByClusterAndRole :one
SELECT c.* FROM collectors c
JOIN clusters cl ON c.cluster_id = cl.id
WHERE cl.name = $1 AND c.role = $2;

-- name: ListCollectorsByOrg :many
SELECT c.* FROM collectors c
JOIN clusters cl ON c.cluster_id = cl.id
WHERE cl.org_id = $1
ORDER BY cl.name, c.role;

-- name: ListCollectorsWithClusterByOrg :many
SELECT c.id, c.cluster_id, c.role, c.created_at, c.updated_at, c.labels, cl.name AS cluster_name
FROM collectors c
JOIN clusters cl ON c.cluster_id = cl.id
WHERE cl.org_id = $1
ORDER BY cl.name, c.role;

-- name: GetCollectorOrgID :one
SELECT cl.org_id FROM collectors c
JOIN clusters cl ON c.cluster_id = cl.id
WHERE c.id = $1;

-- name: GetCollectorOwner :one
-- The org that claims a collector's cluster (NULL when unclaimed) and the
-- cluster's name: what the beacon checks a writer's org and cluster
-- allowlist against.
SELECT cl.org_id, cl.name AS cluster_name FROM collectors c
JOIN clusters cl ON c.cluster_id = cl.id
WHERE c.id = $1;

-- name: SetCollectorLabel :one
UPDATE collectors
SET labels = labels || jsonb_build_object(sqlc.arg(label_key)::text, sqlc.arg(label_value)::text),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND (labels ? sqlc.arg(label_key)::text OR jsonb_array_length(jsonb_path_query_array(labels, '$.keyvalue()')) < 64)
RETURNING labels;

-- name: DeleteCollectorLabel :one
UPDATE collectors
SET labels = labels - sqlc.arg(label_key)::text, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING labels;

-- name: RecordCollectorRefusal :exec
-- The agent API calls this only when the row's recorded refusal differs or
-- is older than its refusal-log window (agentapi.recordCollectorRefusal).
UPDATE collectors
SET last_refused_at = now(), last_refused_reason = sqlc.arg(reason)::text
WHERE id = sqlc.arg(id);

-- name: SetCollectorVersionWarning :one
-- #375 (0044): a compare-and-swap on version_warning, so concurrent polls
-- that reach the same finding write — and audit — it once. No row means the
-- stored warning already says this.
UPDATE collectors
SET version_warning    = sqlc.narg(warning)::text,
    version_warning_at = CASE WHEN sqlc.narg(warning)::text IS NULL THEN NULL ELSE now() END,
    version_mismatch   = sqlc.arg(mismatch)::bool
WHERE id = sqlc.arg(id)
  AND version_warning IS DISTINCT FROM sqlc.narg(warning)::text
RETURNING id;

-- name: RecordCollectorVersionCheck :exec
-- The fingerprint of the version check just run (0044): what lets the next
-- poll skip it. Writes only when the fingerprint changed or the stored one
-- is older than stale_after (which refreshes version_checked_at for the
-- agent API's re-check windows), so a check that found what the last one
-- did costs no UPDATE.
UPDATE collectors
SET version_checked_hash     = sqlc.arg(hash)::text,
    version_checked_versions = sqlc.arg(versions)::text[],
    version_checked_until    = sqlc.narg(until)::timestamptz,
    version_checked_at       = now()
WHERE id = sqlc.arg(id)
  AND (version_checked_hash IS DISTINCT FROM sqlc.arg(hash)::text
       OR version_checked_versions IS DISTINCT FROM sqlc.arg(versions)::text[]
       -- until moves with every poll of a live instance; it only needs
       -- rewriting when it appears, disappears or has passed.
       OR (version_checked_until IS NULL) <> (sqlc.narg(until)::timestamptz IS NULL)
       OR version_checked_until < now()
       OR version_checked_at IS NULL
       OR version_checked_at < now() - make_interval(secs => sqlc.arg(stale_after_secs)::float8));

-- name: ClearCollectorRefusal :exec
UPDATE collectors
SET last_refused_at = NULL, last_refused_reason = NULL
WHERE id = $1 AND last_refused_at IS NOT NULL;
