-- name: UpsertCollectorInstance :one
-- On conflict, an incoming name that is empty or equal to the wire id (both
-- signal "caller has no real display name to report, e.g. a bare poll with
-- no collector.name attribute") never clobbers a previously-known display
-- name. A successful upsert also counts as liveness recovery: it clears a
-- stale 'inactive' status marker left by the lifecycle sweeper so a
-- reconnecting instance shows live again.
INSERT INTO collector_instances (id, collector_id, name, local_attributes, alloy_version, os, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (id) DO UPDATE SET
    collector_id = EXCLUDED.collector_id,
    name         = CASE
                        WHEN EXCLUDED.name = '' OR EXCLUDED.name = collector_instances.id THEN collector_instances.name
                        ELSE EXCLUDED.name
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
UPDATE collector_instances
SET remote_config_status = $2,
    remote_config_error  = $3,
    updated_at           = now()
WHERE id = $1;

-- name: ClearStaleFailedStatus :exec
-- A poll that carries no RemoteConfigStatus payload but whose reported hash
-- matches what GetConfig actually served means the agent is healthy on its
-- current config (see B1) — clear a stale FAILED marker back to APPLIED.
-- Scoped to rows currently FAILED: a genuine FAILED the agent keeps
-- re-reporting is persisted by UpdateInstanceStatus earlier in the same
-- request and must win, so this call is a no-op whenever that happened.
UPDATE collector_instances
SET remote_config_status = 'APPLIED',
    remote_config_error  = NULL,
    updated_at           = now()
WHERE id = $1
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
SELECT remote_config_status, last_seen, alloy_version FROM collector_instances
WHERE collector_id = $1 AND unregistered_at IS NULL
ORDER BY last_seen DESC
LIMIT 1;

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
