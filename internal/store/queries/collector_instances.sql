-- name: UpsertCollectorInstance :one
-- On conflict, an incoming name that is empty or equal to the wire id (both
-- signal "caller has no real display name to report, e.g. a bare poll with
-- no collector.name attribute") never clobbers a previously-known display
-- name — but only when a real display name is actually stored. Otherwise
-- (first insert, or a row still carrying an empty name from before this
-- fallback existed) fall back to the wire id rather than persist an empty
-- name. remote_config_status is not touched: it is the agent's last reported
-- outcome, and liveness is last_seen alone — "inactive" is derived from it at
-- read time (#237), so a reconnecting instance shows live again simply by
-- bumping last_seen, with its outcome intact.
INSERT INTO collector_instances (id, collector_id, name, local_attributes, alloy_version, os, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (id) DO UPDATE SET
    collector_id = EXCLUDED.collector_id,
    name         = CASE
                        WHEN (EXCLUDED.name = '' OR EXCLUDED.name = collector_instances.id)
                             AND collector_instances.name <> '' THEN collector_instances.name
                        ELSE COALESCE(NULLIF(EXCLUDED.name, ''), collector_instances.id)
                    END,
    local_attributes = EXCLUDED.local_attributes,
    alloy_version = EXCLUDED.alloy_version,
    os           = EXCLUDED.os,
    last_seen    = now(),
    updated_at   = now()
RETURNING *;

-- name: UpdateInstanceStatus :exec
-- remote_config_status_hash records which config the status is about: the
-- hash the agent sent alongside it (see 0028).
UPDATE collector_instances
SET remote_config_status      = $2,
    remote_config_error       = $3,
    remote_config_status_hash = sqlc.narg(status_hash),
    updated_at                = now()
WHERE id = $1;

-- name: ApplySilentPoll :exec
-- A poll with no RemoteConfigStatus, whose hash equals what GetConfig served.
-- What that silence means is Alloy's rule (remotecfg's
-- getRemoteConfigStatusForRequest, v1.20.1): it re-sends a status only when
-- the (status, error message) pair CHANGES — not when the config hash does.
-- Evidence and captured request sequences: docs/proofs/applied-status.md.
--
--   * loaded = the poll carries effective_config: Alloy sets it only after a
--     successful load, and sends it whenever the loaded config changes, so
--     this is a verified load of the polled hash → APPLIED.
--   * no status ever reported (NULL or '') → APPLIED, so a healthy collector that applied before its row was reset
--     does not sit at UNKNOWN forever.
--   * anything else → the status stands: Alloy said nothing because its
--     outcome is unchanged. A FAILED stays FAILED — a new config that fails
--     with the same error is not re-reported (#115, F1) — and its
--     status_hash moves to the polled hash, which is now the config it is
--     failing on.
-- Writes only when something changes, so a steady fleet does not rewrite
-- its rows on every poll.
UPDATE collector_instances
SET remote_config_status = CASE
        WHEN sqlc.arg(loaded)::bool
          OR remote_config_status IS NULL
          OR remote_config_status = '' THEN 'APPLIED'
        ELSE remote_config_status
    END,
    remote_config_error = CASE
        WHEN sqlc.arg(loaded)::bool
          OR remote_config_status IS NULL
          OR remote_config_status = '' THEN NULL
        ELSE remote_config_error
    END,
    remote_config_status_hash = sqlc.arg(polled_hash),
    updated_at                = now()
WHERE id = sqlc.arg(id)
  AND (sqlc.arg(loaded)::bool
       OR remote_config_status IS NULL
       OR remote_config_status = ''
       OR remote_config_status_hash IS DISTINCT FROM sqlc.arg(polled_hash));

-- name: UnregisterInstance :exec
UPDATE collector_instances
SET unregistered_at = now(), updated_at = now()
WHERE id = $1;

-- name: DeleteOldInstances :exec
DELETE FROM collector_instances WHERE last_seen < $1;

-- name: GetCollectorInstanceByID :one
SELECT * FROM collector_instances WHERE id = $1;

-- name: GetLatestCollectorInstanceSummary :one
-- Status, last-seen, and version of the most recently reporting live
-- instance for a collector, in a single round trip (used by the collector
-- list endpoint instead of N per-row status-only lookups). status_hash and
-- served_hash let the reader tell an outcome about the config being served
-- from one about an earlier config (docs/proofs/applied-status.md).
SELECT ci.remote_config_status, ci.last_seen, ci.alloy_version, ci.local_attributes,
       ci.remote_config_status_hash AS status_hash, sc.hash AS served_hash
FROM collector_instances ci
LEFT JOIN serve_cache sc ON sc.collector_id = ci.collector_id
WHERE ci.collector_id = $1 AND ci.unregistered_at IS NULL
ORDER BY ci.last_seen DESC
LIMIT 1;

-- name: ListLatestLocalAttributesByOrg :many
-- The bulk read for Phase 2 org-wide matching (LABEL-MATCHING-PLAN.md §6
-- Phase 2 step 1): one query per org-wide Assemble loop (stage3Check,
-- previewMatchedCollectors, recomputeOrgCaches), not one per collector --
-- the plan's explicitly called-out N+1 risk at ~1000-collector scale. The
-- hot path (agentapi's GetConfig) does NOT use this: it already has the
-- current request's local_attributes in hand and must use that, not a
-- re-query that would reflect the previous heartbeat instead of this one.
--
-- DISTINCT ON (collector_id) + last_seen DESC picks each collector's most
-- recently reporting live instance, the same "last-seen wins" semantics
-- GetLatestCollectorInstanceSummary already uses for one collector, applied
-- here to every collector in the org in a single round trip.
--
-- Deliberate simplification, decided 2026-09-18 (LABEL-MATCHING-PLAN.md §5):
-- this returns one INSTANCE's whole attribute blob, not a true per-key union
-- across every live instance a collector has -- docs/spec.md:353 describes
-- the latter ("last-seen instance wins per key"). They're the same result
-- unless a collector genuinely has more than one concurrently-live instance
-- (an HA pair, or briefly during a rolling upgrade) reporting different
-- keys, in which case this can make served config flap on whichever key
-- differs, depending on poll timing. Accepted for now rather than block on
-- it; revisiting means dropping DISTINCT ON to return every live instance
-- per collector and folding them per-key in Go -- still one query, more
-- rows, not the N+1-by-query-count pattern this query exists to avoid.
SELECT DISTINCT ON (ci.collector_id) ci.collector_id, ci.local_attributes
FROM collector_instances ci
JOIN collectors c ON c.id = ci.collector_id
JOIN clusters cl ON cl.id = c.cluster_id
WHERE cl.org_id = $1
  AND ci.unregistered_at IS NULL
ORDER BY ci.collector_id, ci.last_seen DESC NULLS LAST;

-- name: ListCollectorInstancesByCollector :many
-- All live (still-registered) instances reporting under a collector,
-- newest last_seen first, for the collector detail view. status_hash and
-- served_hash: see GetLatestCollectorInstanceSummary.
SELECT ci.name, ci.alloy_version, ci.os, ci.last_seen, ci.remote_config_status, ci.remote_config_error,
       ci.local_attributes, ci.remote_config_status_hash AS status_hash, sc.hash AS served_hash
FROM collector_instances ci
LEFT JOIN serve_cache sc ON sc.collector_id = ci.collector_id
WHERE ci.collector_id = $1 AND ci.unregistered_at IS NULL
ORDER BY ci.last_seen DESC NULLS LAST;

-- name: CountActiveInstances :one
-- Feeds the shepherd_active_collectors gauge: registered instances that are
-- not inactive. Inactive is derived at read time (#237), never stored: an
-- instance whose last_seen is older than inactive_before (now minus
-- agent.inactive_after) — the same rule mgmtapi's FleetService presents as
-- status "inactive". A NULL inactive_before (agent.inactive_after unset)
-- counts every registered instance. Written as a query rather than derived
-- from a list so the sweeper does not pull every instance row once a tick
-- just to count them.
SELECT COUNT(*)::bigint AS total
FROM collector_instances
WHERE unregistered_at IS NULL
  AND (sqlc.narg(inactive_before)::timestamptz IS NULL
       OR last_seen IS NULL
       OR last_seen >= sqlc.narg(inactive_before)::timestamptz);
