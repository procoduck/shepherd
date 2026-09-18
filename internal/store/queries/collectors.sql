-- name: UpsertCollector :one
INSERT INTO collectors (cluster_id, role)
VALUES ($1, $2)
ON CONFLICT (cluster_id, role) DO UPDATE SET updated_at = now()
RETURNING *;

-- name: GetCollectorByID :one
SELECT * FROM collectors WHERE id = $1;

-- name: GetCollectorByClusterAndRole :one
SELECT c.* FROM collectors c
JOIN clusters cl ON c.cluster_id = cl.id
WHERE cl.name = $1 AND c.role = $2;

-- name: ListCollectorsByOrg :many
SELECT c.* FROM collectors c
JOIN clusters cl ON c.cluster_id = cl.id
WHERE cl.org_id = $1
ORDER BY cl.name, c.role;

-- name: ListCollectorsWithClusterByOrg :many
SELECT c.id, c.cluster_id, c.role, c.created_at, c.updated_at, c.labels, cl.name AS cluster_name
FROM collectors c
JOIN clusters cl ON c.cluster_id = cl.id
WHERE cl.org_id = $1
ORDER BY cl.name, c.role;

-- name: GetCollectorOrgID :one
SELECT cl.org_id FROM collectors c
JOIN clusters cl ON c.cluster_id = cl.id
WHERE c.id = $1;

-- name: SetCollectorLabel :one
UPDATE collectors
SET labels = labels || jsonb_build_object(sqlc.arg(label_key)::text, sqlc.arg(label_value)::text),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND (labels ? sqlc.arg(label_key)::text OR jsonb_array_length(jsonb_path_query_array(labels, '$.keyvalue()')) < 64)
RETURNING labels;

-- name: DeleteCollectorLabel :one
UPDATE collectors
SET labels = labels - sqlc.arg(label_key)::text, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING labels;
