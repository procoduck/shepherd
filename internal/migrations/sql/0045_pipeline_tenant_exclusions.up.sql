-- 0045_pipeline_tenant_exclusions.up.sql
-- Strict tenant mode's serve-time check.
--
-- Every save of a strict org's pipeline runs the tenant check, but a
-- pipeline saved before a check was added (or before the org turned strict
-- mode on without re-saving it) is served as stored. So every recompute of a
-- strict org's served configs (internal/serve.LoadPipelines, on both serve
-- paths) runs the same check on each enabled pipeline as served, and leaves
-- one it refuses out of every collector's merged config, listed in the
-- served header under "Excluded - strict tenant check". The pipeline stays
-- stored and enabled; saving a fix includes it again.
--
-- This table records which pipelines that check excludes now, and why, so
-- the management API can show it (the pipeline page, the collector's
-- reconciliation) and so it is audited once per change, never per
-- recompute: a row is written, changed or removed with a compare-and-swap on
-- reason, and only the write that lands adds an audit_log row
-- (pipeline.tenant_check_excluded / pipeline.tenant_check_cleared).
--
--   reason       what the check found, as the served header prints it
--   excluded_at  when reason last changed
CREATE TABLE pipeline_tenant_exclusions (
    pipeline_id uuid PRIMARY KEY REFERENCES pipelines (id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    reason text NOT NULL CHECK (reason <> ''),
    excluded_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pipeline_tenant_exclusions_org_idx ON pipeline_tenant_exclusions (org_id);
