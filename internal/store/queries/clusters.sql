-- name: UpsertCluster :one
INSERT INTO clusters (name)
VALUES ($1)
ON CONFLICT (name) DO UPDATE SET updated_at = now()
RETURNING *;

-- name: GetClusterByID :one
SELECT * FROM clusters WHERE id = $1;

-- name: GetClusterByName :one
SELECT * FROM clusters WHERE name = $1;

-- name: ListUnclaimedClusters :many
SELECT * FROM clusters WHERE org_id IS NULL ORDER BY created_at;

-- name: ListAllClusters :many
SELECT * FROM clusters ORDER BY created_at;

-- name: ClaimCluster :exec
UPDATE clusters SET org_id = $2, updated_at = now() WHERE id = $1;

-- name: UnclaimCluster :exec
UPDATE clusters SET org_id = NULL, updated_at = now() WHERE id = $1;

-- name: ListClustersByOrg :many
SELECT * FROM clusters WHERE org_id = $1 ORDER BY name;
