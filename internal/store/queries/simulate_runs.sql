-- name: CreateSimulateRun :one
-- effective_duration_seconds is NULL on a row a v0.20 server inserted during
-- a rolling upgrade or after a rollback (0051); such a run runs for what it
-- requested, so every reader selects the coalesced value, never the column.
-- effective_duration_seconds is never below what was requested (#424): an
-- unset (0) or shorter effective duration stores the requested one.
INSERT INTO simulate_runs (org_id, graph, requested_duration_seconds, created_by, effective_duration_seconds, duration_note)
VALUES (
    sqlc.arg(org_id), sqlc.arg(graph), sqlc.arg(requested_duration_seconds), sqlc.arg(created_by),
    GREATEST(sqlc.arg(effective_duration_seconds)::int, sqlc.arg(requested_duration_seconds)),
    sqlc.arg(duration_note)
)
RETURNING id, org_id, status, graph, requested_duration_seconds, created_by,
    created_at, started_at, finished_at, rewrites, captured_series,
    captured_log_lines, component_health, gate_diagnostics, stderr_tail,
    error_code, error_message,
    COALESCE(effective_duration_seconds, requested_duration_seconds)::int AS effective_duration_seconds,
    duration_note;

-- name: GetSimulateRunByID :one
-- No org_id filter, matching GetPipelineByID: the caller (loadRun in
-- rpc_simulate.go) does the org-ownership check in Go and returns NotFound
-- on mismatch, mirroring loadPipeline's tenant-isolation shape exactly.
-- Coalesces effective_duration_seconds (see CreateSimulateRun).
SELECT id, org_id, status, graph, requested_duration_seconds, created_by,
    created_at, started_at, finished_at, rewrites, captured_series,
    captured_log_lines, component_health, gate_diagnostics, stderr_tail,
    error_code, error_message,
    COALESCE(effective_duration_seconds, requested_duration_seconds)::int AS effective_duration_seconds,
    duration_note
FROM simulate_runs WHERE id = $1;

-- name: CountNonTerminalSimulateRunsByOrg :one
SELECT count(*) FROM simulate_runs
WHERE org_id = $1 AND status IN ('queued', 'running');

-- name: CountQueuedSimulateRunsBefore :one
-- Best-effort queue_position for GetRun: how many queued runs sort ahead of
-- this one. Not transactionally consistent with the claim loop — see the
-- run-API spec's Risks (cosmetic UI progress text only).
SELECT count(*) FROM simulate_runs AS sr
WHERE sr.status = 'queued'
  AND sr.created_at < (SELECT created_at FROM simulate_runs AS target WHERE target.id = $1);

-- name: ClaimQueuedSimulateRun :one
-- Claims the single oldest queued run for the caller's connection. Callers
-- must hold a cluster-wide advisory lock (pg_try_advisory_lock) before
-- calling this so MaxConcurrentRuns is enforced regardless of replica count
-- — see internal/simulate/worker.RunWorker. Coalesces
-- effective_duration_seconds: the worker sends it to the simulator.
UPDATE simulate_runs
SET status = 'running', started_at = now()
WHERE id = (
    SELECT id FROM simulate_runs
    WHERE status = 'queued'
    ORDER BY created_at
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING id, org_id, status, graph, requested_duration_seconds, created_by,
    created_at, started_at, finished_at, rewrites, captured_series,
    captured_log_lines, component_health, gate_diagnostics, stderr_tail,
    error_code, error_message,
    COALESCE(effective_duration_seconds, requested_duration_seconds)::int AS effective_duration_seconds,
    duration_note;

-- name: CompleteSimulateRun :one
UPDATE simulate_runs
SET status              = $2,
    finished_at         = now(),
    rewrites            = $3,
    captured_series     = $4,
    captured_log_lines  = $5,
    component_health    = $6,
    gate_diagnostics    = $7,
    stderr_tail         = $8,
    error_code          = $9,
    error_message       = $10
WHERE id = $1
RETURNING id, org_id;

-- name: ExpireStaleSimulateRuns :many
-- Force-transitions any run still queued/running past its TTL to expired.
-- This is also the orphan-reclaim safety net for a worker that dies
-- mid-run: the advisory lock releases automatically when its connection
-- drops, and the abandoned "running" row simply ages out here.
UPDATE simulate_runs
SET status = 'expired', finished_at = now()
WHERE status IN ('queued', 'running')
  AND created_at < now() - make_interval(secs => sqlc.arg(ttl_seconds)::int)
RETURNING id, org_id;

-- name: DeleteOldSimulateRuns :exec
DELETE FROM simulate_runs
WHERE status IN ('completed', 'failed', 'expired')
  AND finished_at < now() - make_interval(secs => sqlc.arg(retention_seconds)::int);
