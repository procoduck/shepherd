-- 0007_simulate_runs.up.sql
-- S3 sandbox runs (VB-1 design doc §6.4/§13 M7): storage for CreateRun/GetRun.
--
-- A new table, not an in-memory registry: the Helm chart's default replica
-- count is 2 (deploy/helm/shepherd/values.yaml), and an in-process map keyed
-- per-pod would 404 a poll that lands on a different pod than the POST that
-- created the run. Every other piece of Shepherd state that must survive a
-- request boundary (pipelines, sessions, agent tokens, audit log) already
-- lives in Postgres for exactly this reason.
--
-- status/error_code stay plain TEXT with a CHECK, matching the repo's
-- documented "enum-like fields stay strings" contract rule
-- (docs/archive/api-contract-design.md) — not a Postgres ENUM type, so an
-- allowed value can be added by an ordinary migration instead of an
-- ENUM ADD VALUE dance.
CREATE TABLE simulate_runs (
    id                         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    status                     TEXT NOT NULL DEFAULT 'queued'
                                   CHECK (status IN ('queued', 'running', 'completed', 'failed', 'expired')),
    graph                      JSONB NOT NULL,
    requested_duration_seconds INT NOT NULL DEFAULT 30
                                   CHECK (requested_duration_seconds BETWEEN 1 AND 120),
    created_by                 TEXT NOT NULL,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at                 TIMESTAMPTZ,
    finished_at                TIMESTAMPTZ,
    rewrites                   JSONB NOT NULL DEFAULT '[]',
    captured_series            JSONB NOT NULL DEFAULT '[]',
    captured_log_lines         JSONB NOT NULL DEFAULT '[]',
    component_health           JSONB NOT NULL DEFAULT '[]',
    gate_diagnostics           JSONB NOT NULL DEFAULT '[]',
    stderr_tail                TEXT NOT NULL DEFAULT '',
    error_code                 TEXT NOT NULL DEFAULT '',
    error_message              TEXT NOT NULL DEFAULT ''
);

-- Backs both the janitor's TTL/retention sweeps (status + created_at /
-- finished_at) and ClaimQueuedSimulateRun's oldest-queued lookup.
CREATE INDEX simulate_runs_status_created_idx ON simulate_runs (status, created_at);
