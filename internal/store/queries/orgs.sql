-- name: CreateOrg :one
INSERT INTO orgs (name, display_name, admin_group_id, reader_group_id)
VALUES ($1, $2, $3, $4)
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

-- name: DeleteOrg :exec
DELETE FROM orgs WHERE id = $1;
