-- 0047_beacon_instance_reports.up.sql
--
-- When each beacon-reporting instance last reported, kept apart from its
-- beacon_inventory rows.
--
-- 0046 stamps the instance's last report on its beacon_inventory rows, so a
-- collector whose only pipeline was disabled reads "nothing running" while
-- the rows remain. But the sweeper deletes those rows by last_seen
-- (beaconInventoryExpireAfter, 5 minutes); once they are gone no row carries
-- the stamp, and reconciliation read the collector's observed state as
-- unknown although its instance was still reporting (v0.20.0 follow-up).
--
-- One row per (collector_id, instance_label), upserted by the beacon handler
-- on every accepted write (throttled in the statement, like the 0046 stamp),
-- and swept with the instance lifecycle (agent.delete_after), not with the
-- beacon rows. The 0046 column stays: older replicas read it during a rolling
-- upgrade, and this Shepherd still writes it.
--
-- No FK to collectors: an unscoped agent token may report a collector id that
-- names no collector (beacon_inventory.collector_id has no FK either), and the
-- write must not fail for it. The label bound mirrors beacon_inventory's.
CREATE TABLE beacon_instance_reports (
    collector_id   uuid        NOT NULL,
    instance_label text        NOT NULL CHECK (length(instance_label) BETWEEN 1 AND 256),
    reported_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collector_id, instance_label)
);

CREATE INDEX idx_beacon_instance_reports_reported_at ON beacon_instance_reports (reported_at);
