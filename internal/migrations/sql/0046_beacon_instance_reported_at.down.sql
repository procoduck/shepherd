-- 0046_beacon_instance_reported_at.down.sql
-- Reverts 0046_beacon_instance_reported_at.up.sql.
ALTER TABLE beacon_inventory DROP COLUMN instance_reported_at;
