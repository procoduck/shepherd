-- name: InsertAuditLog :exec
INSERT INTO audit_log (actor, actor_type, org_id, action, resource_type, resource_id, detail)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListAuditLog :many
SELECT * FROM audit_log
WHERE ($1::uuid IS NULL OR org_id = $1)
  AND (NULLIF($2::text, '') IS NULL OR actor ILIKE '%' || $2 || '%')
  AND (NULLIF($3::text, '') IS NULL OR action = $3)
ORDER BY at DESC
LIMIT $4 OFFSET $5;

-- name: CountAuditLog :one
SELECT COUNT(*)::int AS total FROM audit_log
WHERE ($1::uuid IS NULL OR org_id = $1)
  AND (NULLIF($2::text, '') IS NULL OR actor ILIKE '%' || $2 || '%')
  AND (NULLIF($3::text, '') IS NULL OR action = $3);
