-- name: ListPipelineTenantExclusionsByOrg :many
-- 0045: the org's pipelines strict tenant mode's serve-time check excludes.
SELECT * FROM pipeline_tenant_exclusions
WHERE org_id = $1
ORDER BY pipeline_id;

-- name: GetPipelineTenantExclusion :one
SELECT * FROM pipeline_tenant_exclusions
WHERE pipeline_id = $1;

-- name: SetPipelineTenantExclusion :one
-- A compare-and-swap on reason: no row means the stored exclusion already
-- says this, so concurrent recomputes write (and audit) a change once.
INSERT INTO pipeline_tenant_exclusions (pipeline_id, org_id, reason)
VALUES (sqlc.arg(pipeline_id), sqlc.arg(org_id), sqlc.arg(reason))
ON CONFLICT (pipeline_id) DO UPDATE
SET reason = EXCLUDED.reason, excluded_at = now()
WHERE pipeline_tenant_exclusions.reason IS DISTINCT FROM EXCLUDED.reason
RETURNING pipeline_id;

-- name: ClearPipelineTenantExclusion :one
-- No row means there was nothing to clear (another recompute cleared it).
DELETE FROM pipeline_tenant_exclusions
WHERE pipeline_id = $1
RETURNING pipeline_id;

-- name: ListPipelineTenantExclusionOrgs :many
-- The orgs with a serve-time exclusion recorded, and whether each is in
-- strict tenant mode by its own flag (boot clears the rest).
SELECT DISTINCT e.org_id, o.strict_tenants
FROM pipeline_tenant_exclusions e
JOIN orgs o ON o.id = e.org_id
ORDER BY e.org_id;
