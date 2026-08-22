-- name: InsertAuditLog :exec
-- on_behalf_of is G13's second attribution half (docs/gateway-tier-plan.md):
-- the human a machine actor's write is performed for. NULL for every human
-- session's own action; a 'service_account' actor_type row always carries
-- one, because internal/mgmtapi.requireWriteAuthorized rejects a machine
-- write with no on-behalf-of before any InsertAuditLog call is reached — see
-- that function's doc comment for the reject-vs-record-as-unattributed
-- decision.
INSERT INTO audit_log (actor, actor_type, org_id, action, resource_type, resource_id, detail, on_behalf_of)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

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
