-- 0051_simulate_run_effective_nullable.up.sql
-- Rolling-upgrade and rollback compatibility for 0050: a v0.20 server's run
-- insert names only org_id, graph, requested_duration_seconds and created_by,
-- so a NOT NULL effective_duration_seconds refused it (SQLSTATE 23502) while
-- old and new pods ran side by side, or after a rollback to v0.20. NULL now
-- means "runs for what it requested": every reader selects
-- COALESCE(effective_duration_seconds, requested_duration_seconds). The range
-- CHECK from 0050 stays (it passes NULL).
ALTER TABLE simulate_runs ALTER COLUMN effective_duration_seconds DROP NOT NULL;
