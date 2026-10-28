-- 0050_simulate_run_window.down.sql
-- Reverts 0050_simulate_run_window.up.sql.
ALTER TABLE simulate_runs DROP COLUMN IF EXISTS duration_note;
ALTER TABLE simulate_runs DROP COLUMN IF EXISTS effective_duration_seconds;
