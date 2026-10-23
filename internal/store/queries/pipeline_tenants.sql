-- name: DeletePipelineTenants :exec
-- Forget what one pipeline writes to (pipeline_tenants, 0040), before it is
-- recorded again or when it is disabled.
DELETE FROM pipeline_tenants WHERE pipeline_id = $1;

-- name: InsertPipelineTenant :exec
-- One tenant an enabled pipeline's text writes to (validate.TenantLiterals).
INSERT INTO pipeline_tenants (pipeline_id, org_id, tenant)
VALUES (sqlc.arg(pipeline_id), sqlc.arg(org_id), sqlc.arg(tenant))
ON CONFLICT DO NOTHING;

-- name: DeleteAllPipelineTenants :exec
-- The boot rebuild (tenancy.RebuildPipelineTenants) starts from nothing.
DELETE FROM pipeline_tenants;

-- name: ListAllEnabledPipelines :many
-- Every enabled pipeline, for the boot rebuild of pipeline_tenants.
SELECT * FROM pipelines WHERE enabled = true ORDER BY org_id, name;

-- name: ListPipelineTenantsCoveredByRule :many
-- Every tenant another org's enabled pipeline writes to that a rule would
-- cover: exactly tenant, or (is_prefix) starting with it. The range
-- condition uses pipeline_tenants_tenant_idx (COLLATE "C"); left() keeps it
-- exact. AddOrgTenant refuses while this returns anything.
SELECT pt.tenant, p.name AS pipeline_name, o.name AS org_name
FROM pipeline_tenants pt
JOIN pipelines p ON p.id = pt.pipeline_id
JOIN orgs o ON o.id = pt.org_id
WHERE pt.org_id <> sqlc.arg(org_id)
  AND (pt.tenant = sqlc.arg(tenant)::text
       OR (sqlc.arg(is_prefix)::boolean
           AND pt.tenant >= sqlc.arg(tenant)::text COLLATE "C"
           AND left(pt.tenant, length(sqlc.arg(tenant)::text)) = sqlc.arg(tenant)::text))
ORDER BY o.name, p.name, pt.tenant;
