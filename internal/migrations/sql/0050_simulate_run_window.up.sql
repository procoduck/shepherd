-- 0050_simulate_run_window.up.sql
-- Sandbox run window (#424): a run is extended to cover two of the longest
-- scrape or poll intervals in the pipeline, capped at the maximum run length.
-- requested_duration_seconds keeps what was asked for; the effective length
-- and the reason it differs are stored beside it. Existing rows ran for what
-- they requested.
ALTER TABLE simulate_runs ADD COLUMN effective_duration_seconds INT;
UPDATE simulate_runs SET effective_duration_seconds = requested_duration_seconds;
ALTER TABLE simulate_runs
    ALTER COLUMN effective_duration_seconds SET NOT NULL,
    ADD CONSTRAINT simulate_runs_effective_duration_seconds_check
        CHECK (effective_duration_seconds BETWEEN 1 AND 120);
ALTER TABLE simulate_runs ADD COLUMN duration_note TEXT NOT NULL DEFAULT '';
