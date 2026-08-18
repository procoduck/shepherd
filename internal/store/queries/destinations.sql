-- name: CreateDestination :one
INSERT INTO destinations (org_id, name, type, url, tenant_id, secret_name, secret_namespace, auth_mode, extra)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetDestinationByID :one
SELECT * FROM destinations WHERE id = $1;

-- name: ListDestinationsByOrg :many
SELECT * FROM destinations WHERE org_id = $1 ORDER BY name;

-- name: UpdateDestination :one
UPDATE destinations
SET name             = $2,
    type             = $3,
    url              = $4,
    tenant_id        = $5,
    secret_name      = $6,
    secret_namespace = $7,
    auth_mode        = $8,
    extra            = $9,
    updated_at       = now()
WHERE id = $1
RETURNING *;

-- name: DeleteDestination :exec
DELETE FROM destinations WHERE id = $1;
