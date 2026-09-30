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

-- name: ListTenantRoutesForReconcile :many
-- Every route, in every status: the reconciler decides per row whether its
-- HTTPRoute should exist, so revoked rows are needed too (to delete theirs).
SELECT * FROM tenant_routes ORDER BY created_at;

-- name: SetTenantRouteApplyStatus :one
-- Records one reconcile outcome. applied_at moves only on a verified apply,
-- so it keeps meaning "last time attachment was confirmed".
UPDATE tenant_routes
SET apply_status  = sqlc.arg(apply_status),
    apply_message = sqlc.arg(apply_message),
    applied_at    = CASE WHEN sqlc.arg(apply_status) = 'applied' THEN now() ELSE applied_at END
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: RevokeExpiredTenantRoutes :many
-- The rotation overlap has ended: a deprecated route past valid_until is
-- revoked (the janitor sweep the gateway plan listed as unbuilt).
UPDATE tenant_routes
SET status = 'revoked', revoked_at = now(), updated_at = now()
WHERE status = 'deprecated' AND valid_until IS NOT NULL AND valid_until < now()
RETURNING *;
