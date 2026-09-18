-- 0025_beacon_collector_id.down.sql
DROP INDEX IF EXISTS idx_beacon_inventory_collector;
ALTER TABLE beacon_inventory DROP COLUMN collector_id;
