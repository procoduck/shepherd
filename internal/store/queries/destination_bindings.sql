-- name: CreateDestinationBinding :one
INSERT INTO destination_bindings (destination_id, org_id, name, tenant_id)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetDestinationBindingByID :one
SELECT * FROM destination_bindings WHERE id = $1;

-- name: ListDestinationBindingsByOrg :many
SELECT * FROM destination_bindings WHERE org_id = $1 ORDER BY name;

-- name: ListDestinationBindingsByOrgAndDestination :many
SELECT * FROM destination_bindings WHERE org_id = $1 AND destination_id = $2 ORDER BY name;

-- name: UpdateDestinationBinding :one
-- Only name and tenant_id are settable -- see 0008_destination_bindings.up.sql
-- and rpc_destination.go:rejectCredentialOverride for why the credential
-- fields are not, and cannot be, part of this statement.
UPDATE destination_bindings
SET name       = $2,
    tenant_id  = $3,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteDestinationBinding :exec
DELETE FROM destination_bindings WHERE id = $1;

-- name: GetResolvedDestinationBinding :one
-- The one query a serving-time consumer should use: the binding's tenant_id
-- merged with its template's url/type/secret_name/secret_namespace/auth_mode/
-- extra. Never assemble this by hand from a separate GetDestinationBindingByID
-- + GetDestinationByID pair -- that seam is exactly where a half-resolved row
-- (tenant overridden, credential stale/missing) could leak to a consumer.
SELECT
    b.id                AS binding_id,
    b.name              AS binding_name,
    b.tenant_id         AS tenant_id,
    b.org_id            AS org_id,
    d.id                AS destination_id,
    d.name              AS destination_name,
    d.type              AS type,
    d.url               AS url,
    d.secret_name       AS secret_name,
    d.secret_namespace  AS secret_namespace,
    d.auth_mode         AS auth_mode,
    d.extra             AS extra
FROM destination_bindings b
JOIN destinations d ON d.id = b.destination_id
WHERE b.id = $1;
