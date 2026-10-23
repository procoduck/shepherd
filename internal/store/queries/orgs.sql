-- name: CreateOrg :one
INSERT INTO orgs (name, display_name, admin_group_id, viewer_group_id, editor_group_id)
VALUES ($1, $2, $3, $4, sqlc.narg('editor_group_id'))
RETURNING *;

-- name: GetOrgByID :one
SELECT * FROM orgs WHERE id = $1;

-- name: CountOrgContent :one
-- Backs DeleteOrg's not-empty check: an org with any cluster or pipeline
-- still attached must refuse deletion rather than orphan them. Derived
-- tables (rather than two scalar subqueries sharing one placeholder) sidestep
-- sqlc's analyzer treating the repeated org_id column name as ambiguous.
SELECT c.cluster_count::int AS cluster_count, p.pipeline_count::int AS pipeline_count
FROM (SELECT count(*) AS cluster_count FROM clusters WHERE clusters.org_id = sqlc.arg('target_org_id')) c,
     (SELECT count(*) AS pipeline_count FROM pipelines WHERE pipelines.org_id = sqlc.arg('target_org_id')) p;

-- name: ListOrgs :many
SELECT * FROM orgs ORDER BY name;

-- name: UpdateOrg :one
UPDATE orgs
SET display_name    = $2,
    admin_group_id  = $3,
    viewer_group_id = $4,
    editor_group_id = sqlc.narg('editor_group_id'),
    allow_experimental_components = sqlc.arg('allow_experimental_components'),
    allow_label_matching = sqlc.arg('allow_label_matching'),
    allow_local_attribute_matching = sqlc.arg('allow_local_attribute_matching'),
    updated_at      = now()
WHERE id = $1
RETURNING *;

-- name: DeleteOrg :exec
DELETE FROM orgs WHERE id = $1;

-- name: GetOrgByName :one
-- Resolve an org by its unique slug (orgs.name), the external identifier an
-- operator uses when creating an agent-identity binding.
SELECT * FROM orgs WHERE name = $1;

-- name: SetOrgStrictTenants :one
-- Strict tenant mode (0040_org_strict_tenants), changed only by an app admin
-- (AdminService.SetOrgStrictTenants) once its preflight passes.
UPDATE orgs
SET strict_tenants = sqlc.arg('strict_tenants'),
    updated_at     = now()
WHERE id = sqlc.arg('id')
RETURNING *;
