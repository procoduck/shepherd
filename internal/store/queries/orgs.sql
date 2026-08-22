-- name: CreateOrg :one
INSERT INTO orgs (name, display_name, admin_group_id, reader_group_id, tenant_id)
VALUES ($1, $2, $3, $4, sqlc.narg('tenant_id'))
RETURNING *;

-- name: GetOrgByID :one
SELECT * FROM orgs WHERE id = $1;

-- name: ListOrgs :many
SELECT * FROM orgs ORDER BY name;

-- name: UpdateOrg :one
UPDATE orgs
SET display_name    = $2,
    admin_group_id  = $3,
    reader_group_id = $4,
    updated_at      = now()
WHERE id = $1
RETURNING *;

-- name: SetOrgTenantID :one
-- Set-once, enforced in SQL rather than only in Go: the WHERE clause updates
-- nothing when tenant_id is already set, so a caller trying to CHANGE an
-- org's tenant identity gets no row back instead of a silent rewrite.
-- Changing it after routes exist would leave every existing HTTPRoute
-- injecting a tenant the org no longer claims — the routes keep working and
-- keep being wrong, which is the worst shape of all.
UPDATE orgs
SET tenant_id  = $2,
    updated_at = now()
WHERE id = $1
  AND tenant_id IS NULL
RETURNING *;

-- name: DeleteOrg :exec
DELETE FROM orgs WHERE id = $1;
