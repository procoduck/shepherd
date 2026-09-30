-- name: UpsertCollectorInstance :one
-- On conflict, an incoming name that is empty or equal to the wire id (both
-- signal "caller has no real display name to report, e.g. a bare poll with
-- no collector.name attribute") never clobbers a previously-known display
-- name — but only when a real display name is actually stored. Otherwise
-- (first insert, or a row still carrying an empty name from before this
-- fallback existed) fall back to the wire id rather than persist an empty
-- name. A successful upsert also counts as liveness recovery: it clears a
-- stale 'inactive' status marker left by the lifecycle sweeper so a
-- reconnecting instance shows live again.
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
    remote_config_status = CASE
                                WHEN collector_instances.remote_config_status = 'inactive' THEN NULL
                                ELSE collector_instances.remote_config_status
                            END,
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

-- name: ClearStaleFailedStatus :exec
-- A poll that carries no RemoteConfigStatus payload but whose reported hash
-- matches what GetConfig actually served means the agent is healthy on its
-- current config (see B1) — clear a stale FAILED marker back to APPLIED.
-- Scoped to rows currently FAILED: a genuine FAILED the agent keeps
-- re-reporting is persisted by UpdateInstanceStatus earlier in the same
-- request and must win, so this call is a no-op whenever that happened.
--
-- EXCEPT a FAILED reported for the very hash now being polled (#115): Alloy
-- reports a rejected config's FAILED once, then keeps polling with the
-- rejected hash and no status while it runs its previous config. That silence
-- is not recovery; the row stays FAILED until the agent reports a status for
-- another config.
UPDATE collector_instances
SET remote_config_status      = 'APPLIED',
    remote_config_error       = NULL,
    remote_config_status_hash = sqlc.arg(polled_hash),
    updated_at                = now()
WHERE id = sqlc.arg(id)
  AND NOT (COALESCE(remote_config_status, '') = 'FAILED'
           AND remote_config_status_hash IS NOT DISTINCT FROM sqlc.arg(polled_hash))
  -- Also covers a status that was never reported (NULL) or was cleared by the
  -- sweeper's inactive marker: agents report a status only when they apply a
  -- CHANGE, so a healthy collector that applied once and then polled steadily
  -- would otherwise sit at UNKNOWN in the UI forever. A status the agent is
  -- actively re-reporting is written by UpdateInstanceStatus earlier in the
  -- same request and still wins, because this runs only when the poll carried
  -- no status payload at all.
  AND (remote_config_status IS NULL
       OR remote_config_status IN ('FAILED', 'inactive', ''));

-- name: UnregisterInstance :exec
UPDATE collector_instances
SET unregistered_at = now(), updated_at = now()
WHERE id = $1;

-- name: MarkStaleInstancesInactive :exec
UPDATE collector_instances
SET remote_config_status = 'inactive', updated_at = now()
WHERE last_seen < $1
  AND unregistered_at IS NULL
  AND (remote_config_status IS NULL OR remote_config_status != 'inactive');

-- name: DeleteOldInstances :exec
DELETE FROM collector_instances WHERE last_seen < $1;

-- name: GetCollectorInstanceByID :one
SELECT * FROM collector_instances WHERE id = $1;

-- name: GetLatestCollectorInstanceSummary :one
-- Status, last-seen, and version of the most recently reporting live
-- instance for a collector, in a single round trip (used by the collector
-- list endpoint instead of N per-row status-only lookups).
SELECT remote_config_status, last_seen, alloy_version, local_attributes FROM collector_instances
WHERE collector_id = $1 AND unregistered_at IS NULL
ORDER BY last_seen DESC
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
-- newest last_seen first, for the collector detail view.
SELECT name, alloy_version, os, last_seen, remote_config_status, remote_config_error, local_attributes
FROM collector_instances
WHERE collector_id = $1 AND unregistered_at IS NULL
ORDER BY last_seen DESC NULLS LAST;

-- name: CountActiveInstances :one
-- Feeds the shepherd_active_collectors gauge. "Active" is the definition the
-- gauge's help text already claimed: registered, not swept to 'inactive'.
-- Written as a query rather than derived from a list so the sweeper does not
-- pull every instance row once a tick just to count them.
SELECT COUNT(*)::bigint AS total
FROM collector_instances
WHERE unregistered_at IS NULL
  AND (remote_config_status IS NULL OR remote_config_status != 'inactive');
