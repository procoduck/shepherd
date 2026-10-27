-- name: ListOrgStrictComponents :many
-- The components an app admin allowed for one org in strict tenant mode
-- (0047), by name.
SELECT * FROM org_strict_components WHERE org_id = $1 ORDER BY component;

-- name: AddOrgStrictComponent :one
INSERT INTO org_strict_components (org_id, component, created_by)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetOrgStrictComponent :one
SELECT * FROM org_strict_components WHERE id = $1;

-- name: DeleteOrgStrictComponent :execrows
DELETE FROM org_strict_components WHERE id = $1 AND org_id = $2;
