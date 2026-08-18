-- name: UpsertCollectorInstance :one
INSERT INTO collector_instances (id, collector_id, name, local_attributes, alloy_version, os, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (id) DO UPDATE SET
    collector_id = EXCLUDED.collector_id,
    name         = EXCLUDED.name,
    local_attributes = EXCLUDED.local_attributes,
    alloy_version = EXCLUDED.alloy_version,
    os           = EXCLUDED.os,
    last_seen    = now(),
    updated_at   = now()
RETURNING *;

-- name: UpdateInstanceLastSeen :exec
UPDATE collector_instances
SET last_seen = now(), updated_at = now()
WHERE id = $1;

-- name: UpdateInstanceStatus :exec
UPDATE collector_instances
SET remote_config_status = $2,
    remote_config_error  = $3,
    updated_at           = now()
WHERE id = $1;

-- name: UnregisterInstance :exec
UPDATE collector_instances
SET unregistered_at = now(), updated_at = now()
WHERE id = $1;

-- name: MarkStaleInstancesInactive :exec
UPDATE collector_instances
SET remote_config_status = 'inactive', updated_at = now()
WHERE last_seen < $1
  AND unregistered_at IS NULL
  AND (remote_config_status IS NULL OR remote_config_status != 'inactive');

-- name: DeleteOldInstances :exec
DELETE FROM collector_instances WHERE last_seen < $1;

-- name: GetCollectorInstanceByID :one
SELECT * FROM collector_instances WHERE id = $1;

-- name: GetLatestCollectorInstanceStatus :one
SELECT remote_config_status FROM collector_instances
WHERE collector_id = $1 AND unregistered_at IS NULL
ORDER BY last_seen DESC
LIMIT 1;
