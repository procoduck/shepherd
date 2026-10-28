-- 0051_simulate_run_effective_nullable.down.sql
-- Reverts 0051_simulate_run_effective_nullable.up.sql: rows written without an
-- effective duration ran for what they requested.
UPDATE simulate_runs SET effective_duration_seconds = requested_duration_seconds
WHERE effective_duration_seconds IS NULL;
ALTER TABLE simulate_runs ALTER COLUMN effective_duration_seconds SET NOT NULL;
