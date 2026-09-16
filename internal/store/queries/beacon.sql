-- name: UpsertBeaconComponent :one
-- principal + instance_label + component_name is this table's identity (see
-- 0010_beacon_inventory.up.sql and 0022_beacon_principal.up.sql). principal is
-- the credential that reported: an agent token's UUID, or "oidc:<issuer>|<sub>"
-- for an OIDC collector. Re-reporting the same component just refreshes
-- healthy/last_seen -- a collector polling every scrape interval does not grow
-- this table, it only keeps rows alive.
INSERT INTO beacon_inventory (principal, instance_label, component_name, healthy, last_seen)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (principal, instance_label, component_name) DO UPDATE SET
    healthy   = EXCLUDED.healthy,
    last_seen = now()
RETURNING *;

-- name: ListBeaconInventoryByPrincipal :many
SELECT * FROM beacon_inventory WHERE principal = $1 ORDER BY instance_label, component_name;

-- name: DeleteExpiredBeaconInventory :execrows
-- Called from agentapi's existing sweeper (Sweeper.sweep), same shape as
-- MarkStaleInstancesInactive/DeleteOldInstances for collector_instances: a
-- collector that stops reporting ages out instead of lingering as a
-- permanently-healthy ghost (plan §4, W5).
DELETE FROM beacon_inventory WHERE last_seen < $1;
