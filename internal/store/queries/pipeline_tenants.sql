-- name: DeletePipelineTenants :exec
-- Forget what one pipeline writes to (pipeline_tenants, 0040), before it is
-- recorded again or when it is disabled.
DELETE FROM pipeline_tenants WHERE pipeline_id = $1;

-- name: InsertPipelineTenant :exec
-- One tenant an enabled pipeline's text writes to (validate.TenantLiterals).
INSERT INTO pipeline_tenants (pipeline_id, org_id, tenant)
VALUES (sqlc.arg(pipeline_id), sqlc.arg(org_id), sqlc.arg(tenant))
ON CONFLICT DO NOTHING;

-- name: DeletePipelineTenantIndex :exec
DELETE FROM pipeline_tenant_index WHERE pipeline_id = $1;

-- name: UpsertPipelineTenantIndex :exec
-- What pipeline_tenants describes for one enabled pipeline (0040).
INSERT INTO pipeline_tenant_index (pipeline_id, org_id, contents_sha256, complete)
VALUES (sqlc.arg(pipeline_id), sqlc.arg(org_id), sqlc.arg(contents_sha256), sqlc.arg(complete))
ON CONFLICT (pipeline_id) DO UPDATE
SET org_id = EXCLUDED.org_id, contents_sha256 = EXCLUDED.contents_sha256, complete = EXCLUDED.complete;

-- name: ListStalePipelineTenantIndexOfOtherOrgs :many
-- Enabled pipelines of orgs other than org_id whose pipeline_tenants rows do
-- not describe their current contents: never recorded, or recorded for
-- other contents (a pod that predates the index wrote them). AddOrgTenant
-- re-records these before it checks.
SELECT p.* FROM pipelines p
LEFT JOIN pipeline_tenant_index i ON i.pipeline_id = p.id
WHERE p.org_id <> sqlc.arg(org_id) AND p.enabled = true
  AND (i.pipeline_id IS NULL OR i.contents_sha256 <> encode(sha256(convert_to(p.contents, 'UTF8')), 'hex'))
ORDER BY p.id;

-- name: ListIncompletePipelineTenantOrgs :many
-- Orgs other than org_id with enabled pipelines whose tenants the lint could
-- not all read, and how many: AddOrgTenant warns the app admin about them
-- (it does not refuse).
SELECT o.name AS org_name, count(*)::int AS pipelines
FROM pipeline_tenant_index i
JOIN pipelines p ON p.id = i.pipeline_id
JOIN orgs o ON o.id = i.org_id
WHERE i.org_id <> sqlc.arg(org_id) AND p.enabled = true AND NOT i.complete
GROUP BY o.name
ORDER BY o.name;

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
WHERE pt.org_id <> sqlc.arg(org_id) AND p.enabled = true
  AND (pt.tenant = sqlc.arg(tenant)::text
       OR (sqlc.arg(is_prefix)::boolean
           AND pt.tenant >= sqlc.arg(tenant)::text COLLATE "C"
           AND left(pt.tenant, length(sqlc.arg(tenant)::text)) = sqlc.arg(tenant)::text))
ORDER BY o.name, p.name, pt.tenant;

-- name: DeleteAllPipelineTenants :exec
-- The boot rebuild (tenancy.RebuildPipelineTenants) starts from nothing.
DELETE FROM pipeline_tenants;

-- name: DeleteAllPipelineTenantIndex :exec
DELETE FROM pipeline_tenant_index;

-- name: GetPipelineTenantIndex :one
SELECT * FROM pipeline_tenant_index WHERE pipeline_id = $1;
