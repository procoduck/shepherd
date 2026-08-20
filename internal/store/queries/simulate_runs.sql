-- name: CreateSimulateRun :one
INSERT INTO simulate_runs (org_id, graph, requested_duration_seconds, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetSimulateRunByID :one
-- No org_id filter, matching GetPipelineByID: the caller (loadRun in
-- rpc_simulate.go) does the org-ownership check in Go and returns NotFound
-- on mismatch, mirroring loadPipeline's tenant-isolation shape exactly.
SELECT * FROM simulate_runs WHERE id = $1;

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
-- — see internal/simulate.RunWorker.
UPDATE simulate_runs
SET status = 'running', started_at = now()
WHERE id = (
    SELECT id FROM simulate_runs
    WHERE status = 'queued'
    ORDER BY created_at
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

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
RETURNING *;

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
