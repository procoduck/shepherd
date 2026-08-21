-- name: CreateTenantRoute :one
INSERT INTO tenant_routes (org_id, tenant_id, kind, segment, gateway_mode, gateway_name, gateway_namespace, rotated_from_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetTenantRouteByID :one
SELECT * FROM tenant_routes WHERE id = $1;

-- name: GetActiveTenantRoute :one
SELECT * FROM tenant_routes
WHERE org_id = $1 AND tenant_id = $2 AND kind = $3 AND status = 'active';

-- name: ListTenantRoutesByOrg :many
SELECT * FROM tenant_routes WHERE org_id = $1 ORDER BY tenant_id, kind, created_at DESC;

-- name: DeprecateTenantRoute :one
UPDATE tenant_routes
SET status = 'deprecated', valid_until = $2, updated_at = now()
WHERE id = $1 AND status = 'active'
RETURNING *;

-- name: RevokeTenantRoute :one
UPDATE tenant_routes
SET status = 'revoked', revoked_at = now(), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: TenantRouteSegmentExists :one
SELECT EXISTS(SELECT 1 FROM tenant_routes WHERE kind = $1 AND segment = $2) AS exists;
