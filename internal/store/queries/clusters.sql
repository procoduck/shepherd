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

-- name: ClaimClusterForOrg :one
-- Auto-claim for an OIDC-authenticated collector (agent OIDC, resolution
-- mode 1): bind the cluster to org $2 if it is unclaimed, and succeed
-- (idempotently) if it is already ours. A cluster claimed by a DIFFERENT org
-- matches no row and returns pgx.ErrNoRows, which the caller turns into a
-- cross-org PermissionDenied — the token can never silently reassign another
-- org's cluster.
UPDATE clusters SET org_id = $2, updated_at = now()
WHERE id = $1 AND (org_id IS NULL OR org_id = $2)
RETURNING org_id;
