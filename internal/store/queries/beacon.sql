-- name: UpsertBeaconComponent :one
-- token_id + instance_label + component_name is this table's identity (see
-- 0010_beacon_inventory.up.sql). Re-reporting the same component just
-- refreshes healthy/last_seen -- a collector polling every scrape interval
-- does not grow this table, it only keeps rows alive.
INSERT INTO beacon_inventory (token_id, instance_label, component_name, healthy, last_seen)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (token_id, instance_label, component_name) DO UPDATE SET
    healthy   = EXCLUDED.healthy,
    last_seen = now()
RETURNING *;

-- name: ListBeaconInventoryByToken :many
SELECT * FROM beacon_inventory WHERE token_id = $1 ORDER BY instance_label, component_name;

-- name: DeleteExpiredBeaconInventory :execrows
-- Called from agentapi's existing sweeper (Sweeper.sweep), same shape as
-- MarkStaleInstancesInactive/DeleteOldInstances for collector_instances: a
-- collector that stops reporting ages out instead of lingering as a
-- permanently-healthy ghost (plan §4, W5).
DELETE FROM beacon_inventory WHERE last_seen < $1;
