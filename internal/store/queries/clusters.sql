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
-- Every statement that changes a cluster's org also removes, in the same
-- statement (so in the same transaction as the change), the group
-- assignments on the cluster's collectors that the new org did not make
-- (0053). A grant is bound to the org that made it; one left behind would
-- keep its group's members' viewer access inside the new org.
WITH claimed AS (
    UPDATE clusters SET org_id = sqlc.arg('org_id'), updated_at = now() WHERE clusters.id = sqlc.arg('id')
    RETURNING clusters.id, clusters.org_id
), revoked AS (
    DELETE FROM group_assignments ga
    USING collectors c, claimed
    WHERE c.id = ga.collector_id
      AND c.cluster_id = claimed.id
      AND ga.org_id IS DISTINCT FROM claimed.org_id
)
SELECT count(*) FROM claimed;

-- name: UnclaimCluster :exec
-- Unclaiming removes every group assignment on the cluster's collectors: an
-- unclaimed cluster belongs to no org, so no grant on it can be the owner's.
WITH unclaimed AS (
    UPDATE clusters SET org_id = NULL, updated_at = now() WHERE clusters.id = sqlc.arg('id')
    RETURNING clusters.id
), revoked AS (
    DELETE FROM group_assignments ga
    USING collectors c, unclaimed
    WHERE c.id = ga.collector_id
      AND c.cluster_id = unclaimed.id
)
SELECT count(*) FROM unclaimed;

-- name: ClaimClusterForOrg :one
-- Auto-claim for an OIDC-authenticated collector (agent OIDC, resolution
-- mode 1): bind the cluster to org $2 if it is unclaimed, and succeed
-- (idempotently) if it is already ours. A cluster claimed by a DIFFERENT org
-- matches no row and returns pgx.ErrNoRows, which the caller turns into a
-- cross-org PermissionDenied — the token can never silently reassign another
-- org's cluster.
--
-- Like ClaimCluster, a successful claim removes the cluster's group
-- assignments the claiming org did not make; a refused claim (no row) removes
-- nothing, because the delete joins on the UPDATE's RETURNING.
WITH claimed AS (
    UPDATE clusters SET org_id = sqlc.arg('org_id'), updated_at = now()
    WHERE clusters.id = sqlc.arg('id') AND (clusters.org_id IS NULL OR clusters.org_id = sqlc.arg('org_id'))
    RETURNING clusters.id, clusters.org_id
), revoked AS (
    DELETE FROM group_assignments ga
    USING collectors c, claimed
    WHERE c.id = ga.collector_id
      AND c.cluster_id = claimed.id
      AND ga.org_id IS DISTINCT FROM claimed.org_id
)
SELECT claimed.org_id FROM claimed;
