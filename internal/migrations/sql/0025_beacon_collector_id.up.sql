-- 0025_beacon_collector_id.up.sql
--
-- #110 reconciliation: attribute a beacon inventory row to the collector whose
-- served config produced its baseline pipeline. The baseline now stamps
-- shepherd_collector_id onto every beacon series (internal/beacon), and the
-- handler stores it here. Nullable: a row written by a collector still running a
-- pre-#110 baseline carries no id and simply does not appear as "observed" for
-- any collector until that collector re-reports under the new baseline. No FK to
-- collectors(id) on purpose — the value is a self-reported label, verified only
-- as the credential's own claim, exactly like principal (see 0022).
ALTER TABLE beacon_inventory ADD COLUMN collector_id uuid;

CREATE INDEX idx_beacon_inventory_collector ON beacon_inventory (collector_id);
