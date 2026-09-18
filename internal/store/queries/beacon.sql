-- name: UpsertBeaconComponent :one
-- principal + instance_label + component_name is this table's identity (see
-- 0010_beacon_inventory.up.sql and 0022_beacon_principal.up.sql). principal is
-- the credential that reported: an agent token's UUID, or "oidc:<issuer>|<sub>"
-- for an OIDC collector. Re-reporting the same component just refreshes
-- healthy/last_seen -- a collector polling every scrape interval does not grow
-- this table, it only keeps rows alive.
INSERT INTO beacon_inventory (principal, instance_label, component_name, healthy, collector_id, last_seen)
VALUES ($1, $2, $3, $4, sqlc.narg('collector_id'), now())
ON CONFLICT (principal, instance_label, component_name) DO UPDATE SET
    healthy      = EXCLUDED.healthy,
    -- Upgrade a previously-unattributed row (NULL from a pre-#110 baseline) the
    -- moment the collector re-reports under the new baseline; never overwrite a
    -- known id back to NULL if a stray write omits the label.
    collector_id = COALESCE(EXCLUDED.collector_id, beacon_inventory.collector_id),
    last_seen    = now()
RETURNING *;

-- name: ListBeaconInventoryByPrincipal :many
SELECT * FROM beacon_inventory WHERE principal = $1 ORDER BY instance_label, component_name;

-- name: ListBeaconInventoryByCollector :many
-- Backs the reconciliation surface's "observed" input (#110): every component
-- the fleet has reported running under this collector's baseline. Keyed on the
-- collector id the baseline stamps (shepherd_collector_id), so it is exact per
-- collector rather than per shared credential.
SELECT * FROM beacon_inventory WHERE collector_id = $1 ORDER BY instance_label, component_name;

-- name: DeleteExpiredBeaconInventory :execrows
-- Called from agentapi's existing sweeper (Sweeper.sweep), same shape as
-- MarkStaleInstancesInactive/DeleteOldInstances for collector_instances: a
-- collector that stops reporting ages out instead of lingering as a
-- permanently-healthy ghost (plan §4, W5).
DELETE FROM beacon_inventory WHERE last_seen < $1;
