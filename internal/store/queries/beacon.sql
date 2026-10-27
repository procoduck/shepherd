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

-- name: RecordBeaconInstanceReport :exec
-- Stamps "this instance reported now" on every row of one instance under one
-- collector (0046). The beacon handler runs it once per accepted write, after
-- that write's upserts, whether or not the write carried any pipeline series:
-- a row whose last_seen falls behind this is a pipeline the instance no
-- longer reports. Keyed on (collector_id, instance_label), not principal,
-- because reconciliation groups an instance's rows that way.
--
-- Throttled in the statement: a row stamped within the last 5 seconds is left
-- alone, so the several remote_write requests one scrape is split into update
-- the rows once rather than once each. 5s is well under
-- reconcile.LatestReportTolerance (15s), so the lag never makes a refreshed
-- row look dropped. A row this write just inserted has NULL and is stamped.
UPDATE beacon_inventory SET instance_reported_at = now()
WHERE collector_id = $1 AND instance_label = $2
  AND (instance_reported_at IS NULL OR instance_reported_at < now() - interval '5 seconds');

-- name: RecordBeaconInstanceLastReport :exec
-- Records "this instance reported now" in beacon_instance_reports (0047), the
-- record that outlives the instance's beacon_inventory rows: once the sweeper
-- has deleted the rows of a collector whose only pipeline was dropped, this
-- row is what still tells reconciliation the instance reports and runs none.
-- Run by the beacon handler on every accepted write, next to
-- RecordBeaconInstanceReport, with the same 5-second throttle in the
-- statement (the conditional DO UPDATE leaves a fresh row alone).
INSERT INTO beacon_instance_reports (collector_id, instance_label, reported_at)
VALUES ($1, $2, now())
ON CONFLICT (collector_id, instance_label) DO UPDATE SET reported_at = now()
WHERE beacon_instance_reports.reported_at < now() - interval '5 seconds';

-- name: ListBeaconInstanceReportsByCollector :many
-- Every instance's last report under one collector (0047): reconciliation's
-- evidence that an instance is reporting when it has no beacon_inventory row.
SELECT * FROM beacon_instance_reports WHERE collector_id = $1 ORDER BY instance_label;

-- name: DeleteExpiredBeaconInstanceReports :execrows
-- Called from agentapi's sweeper with the instance lifecycle's cutoff
-- (agent.delete_after, as DeleteOldInstances): a report record lives as long
-- as an instance that stopped polling would, not as long as its beacon rows.
DELETE FROM beacon_instance_reports WHERE reported_at < $1;
