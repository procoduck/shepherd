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
-- An org-scoped query ($1 set) returns exactly that org's rows, for every
-- caller. A NULL $1 is the app admin's global view: every row, including the
-- platform-level events that belong to no org (NULL org_id — local user
-- management, single sign-on configuration). The interceptor admits a call
-- with no org only for an app admin (internal/auth.Authorize).
--
-- Until #253 an include_global parameter folded those platform rows into
-- whichever org an app admin was viewing, so an org's audit view showed user
-- and SSO events that are not that org's. The platform trail stays readable
-- in the product through the global view, which the Audit page offers app
-- admins as a scope.
SELECT * FROM audit_log
WHERE ($1::uuid IS NULL OR org_id = $1)
  AND (NULLIF($2::text, '') IS NULL OR actor ILIKE '%' || $2 || '%')
  AND (NULLIF($3::text, '') IS NULL OR action = $3)
ORDER BY at DESC
LIMIT $4 OFFSET $5;

-- name: CountAuditLog :one
-- Predicate kept identical to ListAuditLog: a total that counted a different
-- set than the page it labels is a paginator that lies.
SELECT COUNT(*)::int AS total FROM audit_log
WHERE ($1::uuid IS NULL OR org_id = $1)
  AND (NULLIF($2::text, '') IS NULL OR actor ILIKE '%' || $2 || '%')
  AND (NULLIF($3::text, '') IS NULL OR action = $3);
